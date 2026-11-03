// Package nodedeploy deploys an inference service or an uploaded application onto
// nodes CB-Tumblebug already runs.
//
// For each node it reads the node and its SSH key from CB-Tumblebug, places the
// files on the stage volume shared with Semaphore, and runs install-docker and
// then compose-up or copy-and-run through Semaphore. A request is accepted at
// once and followed in the background; its record says how every node went.
//
// On the node, a deployment lands in the folder and compose project of its
// work key (see placement), not of its deployment ID, so that deploying the
// same inference runtime or application again replaces the previous one.
//
// Once the playbooks of a node succeed, the ports the deployment serves are
// opened inbound in every CB-Tumblebug security group of the node (see
// openPorts), unless Options.OpenPorts is off. A port that cannot be opened is
// noted on the node and does not fail it.
//
// A deployment is taken down from its nodes with compose-down (see Remove),
// which keeps the node folders and the named volumes, and leaves the ports
// open.
//
// With dry run on, only the look-up and the file placement are done, nothing
// is registered in Semaphore, and no port is opened.
package nodedeploy

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/appstore"
	"github.com/ish-hcc/ai-agent-proto/internal/archive"
	"github.com/ish-hcc/ai-agent-proto/internal/compose"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/modelcat"
	"github.com/ish-hcc/ai-agent-proto/internal/semaphore"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

// Playbooks run on a node, from the Semaphore playbook folder.
const (
	PlaybookInstallDocker = "install-docker.yml"
	PlaybookComposeUp     = "compose-up.yml"
	PlaybookCopyAndRun    = "copy-and-run.yml"
)

// Archive record kinds of this package.
const (
	archiveKindDeploy  = "node-deployment"
	archiveKindControl = "node-control"
)

// Control result statuses.
const (
	controlDone    = "done"
	controlFailed  = "failed"
	controlBlocked = "blocked"
)

// GPU decision sources recorded on a node.
const (
	gpuFromRequest = "request"
	gpuFromSpec    = "spec"
	gpuFromDefault = "default"
)

// DefaultMaxParallel bounds how many nodes of one deployment run at once.
const DefaultMaxParallel = 4

// ErrNotFound is returned when no deployment has an ID.
var ErrNotFound = errors.New("deployment not found")

// ErrNodeNotFound is returned by Create when CB-Tumblebug has no node for a
// target.
var ErrNodeNotFound = errors.New("node not found")

// ErrTumblebug is returned by Create when CB-Tumblebug could not tell whether
// a target node exists.
var ErrTumblebug = errors.New("CB-Tumblebug could not be read")

// Tumblebug is what this package reads from and sends to CB-Tumblebug.
// *tumblebug.Client implements it.
type Tumblebug interface {
	GetNode(ctx context.Context, nsID, infraID, nodeID string) (*tumblebug.Node, error)
	GetSSHKey(ctx context.Context, nsID, sshKeyID string) (*tumblebug.SSHKey, error)
	GetSpec(ctx context.Context, nsID, specID string) (*tumblebug.SpecInfo, error)
	ControlNode(ctx context.Context, nsID, infraID, nodeID, action string) (string, error)
	GetSecurityGroup(ctx context.Context, nsID, sgID string) (*tumblebug.SecurityGroupInfo, error)
	AddFirewallRules(ctx context.Context, nsID, sgID string, rules []tumblebug.FirewallRuleReq) (*tumblebug.SecurityGroupUpdateResp, error)
}

// Runner runs one playbook against one node. *semaphore.Client implements it.
type Runner interface {
	Run(ctx context.Context, spec semaphore.RunSpec) (*semaphore.RunResult, error)
}

// Options are the settings of a Service.
type Options struct {
	// StageRoot is the stage folder shared with Semaphore at the same path.
	StageRoot string
	// SpecNamespace holds the node spec catalog in CB-Tumblebug.
	SpecNamespace string
	// DryRun stops every deployment after its files are placed, and blocks
	// node control.
	DryRun bool
	// MaxParallel bounds the nodes of one deployment run at once. Zero or less
	// takes DefaultMaxParallel.
	MaxParallel int
	// OpenPorts opens the ports a deployment serves in the security groups
	// of each node whose playbooks succeeded. Off, the ports are only
	// recorded.
	OpenPorts bool
}

// Service deploys onto nodes and keeps the records.
type Service struct {
	tb      Tumblebug
	runner  Runner
	models  *modelcat.Store
	apps    *appstore.Store
	store   *Store
	archive *archive.Store
	opts    Options

	// ctx is cancelled by Shutdown and ends every background deployment.
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// locks keeps two deployments off the same placement of a node at once.
	locks placementLocks
}

// NewService builds a Service. archive may be nil.
func NewService(tb Tumblebug, runner Runner, models *modelcat.Store, apps *appstore.Store,
	store *Store, archiveStore *archive.Store, opts Options) *Service {
	if opts.MaxParallel <= 0 {
		opts.MaxParallel = DefaultMaxParallel
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		tb: tb, runner: runner, models: models, apps: apps, store: store, archive: archiveStore,
		opts: opts, ctx: ctx, cancel: cancel,
	}
}

// DryRun reports whether deployments stop after placing their files.
func (s *Service) DryRun() bool { return s.opts.DryRun }

// plan is what a request resolved to before any node is touched.
type plan struct {
	model    *model.Model
	artifact *model.Artifact
	runtime  *model.RuntimeProfile
	// appDir is the application version folder in the application store.
	appDir string
	// appPorts are the ports the application version was uploaded with.
	appPorts []int
	// place is where the deployment lands on every node.
	place placement
}

// Create accepts a deployment request.
//
// The request is checked and resolved against the model catalog or the
// application store first, so that an unknown model, an incompatible runtime
// or a missing application fails here. Every target node is then read from
// CB-Tumblebug, so that a missing node fails here too. With dry run on, the deployment is
// carried out before Create returns and the finished record is returned.
// Otherwise the record is returned pending and the deployment goes on in the
// background.
func (s *Service) Create(ctx context.Context, req model.NodeDeploymentReq) (*model.NodeDeployment, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	p, err := s.resolve(ctx, &req)
	if err != nil {
		return nil, err
	}
	if err := s.checkTargets(ctx, req.Targets); err != nil {
		return nil, err
	}
	id, err := newUUID()
	if err != nil {
		return nil, err
	}

	d := model.NodeDeployment{
		ID: id, Type: req.Type, Status: model.NodeDeployPending, DryRun: s.opts.DryRun,
		Request: req, CreatedAt: time.Now().UTC(),
		Nodes: make([]model.NodeDeployResult, len(req.Targets)),
	}
	for i, t := range req.Targets {
		d.Nodes[i] = model.NodeDeployResult{Target: t, Status: model.NodeDeployPending}
	}
	if err := s.store.Create(d); err != nil {
		return nil, err
	}
	log.Info().Str("deploymentId", id).Str("type", req.Type).Int("targets", len(req.Targets)).
		Bool("dryRun", s.opts.DryRun).Msg("Accepted node deployment")

	if s.opts.DryRun {
		s.execute(ctx, id, p)
		return s.Get(ctx, id)
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.execute(s.ctx, id, p)
	}()
	return s.Get(ctx, id)
}

// checkTargets reads every target node from CB-Tumblebug, one after another.
//
// The error names the target, and never carries the upstream text: that may
// hold the CB-Tumblebug address. The upstream error is logged instead.
func (s *Service) checkTargets(ctx context.Context, targets []model.NodeDeployTarget) error {
	for _, t := range targets {
		_, err := s.tb.GetNode(ctx, t.NsID, t.InfraID, t.NodeID)
		if err == nil {
			continue
		}
		name := t.NsID + "/" + t.InfraID + "/" + t.NodeID
		if errors.Is(err, tumblebug.ErrNodeNotFound) {
			return fmt.Errorf("%w: %s", ErrNodeNotFound, name)
		}
		log.Warn().Str("nsId", t.NsID).Str("infraId", t.InfraID).Str("nodeId", t.NodeID).
			Str("error", err.Error()).Msg("Failed to read a target node")
		if status := tumblebug.StatusCode(err); status != 0 {
			return fmt.Errorf("%w: node %s: CB-Tumblebug responded %d", ErrTumblebug, name, status)
		}
		return fmt.Errorf("%w: node %s: CB-Tumblebug did not answer", ErrTumblebug, name)
	}
	return nil
}

// resolve finds the model, artifact and runtime, or the application version,
// of req, and fills in the runtime and version it took.
func (s *Service) resolve(ctx context.Context, req *model.NodeDeploymentReq) (*plan, error) {
	if req.Type == model.NodeDeployTypeApp {
		version := req.App.Version
		if version == 0 {
			app, err := s.apps.Get(ctx, req.App.Name)
			if err != nil {
				return nil, err
			}
			version = app.Versions[len(app.Versions)-1].Version
		}
		v, err := s.apps.GetVersion(ctx, req.App.Name, version)
		if err != nil {
			return nil, err
		}
		dir, err := s.apps.Path(req.App.Name, version)
		if err != nil {
			return nil, err
		}
		app := *req.App
		app.Version = version
		req.App = &app
		place, err := placementFor(req)
		if err != nil {
			return nil, err
		}
		return &plan{appDir: dir, appPorts: v.Ports, place: place}, nil
	}

	inf := *req.Inference
	m, artifact, err := s.models.Artifact(ctx, inf.ModelID, inf.ArtifactID)
	if err != nil {
		return nil, err
	}
	if inf.RuntimeID == "" {
		if len(artifact.Runtimes) == 0 {
			return nil, fmt.Errorf("%w: artifact %s/%s lists no runtime", compose.ErrIncompatible, m.ID, artifact.ID)
		}
		inf.RuntimeID = artifact.Runtimes[0].RuntimeID
	}
	runtime, err := modelcat.Runtime(inf.RuntimeID)
	if err != nil {
		return nil, err
	}
	// A render up front refuses an incompatible pairing, env or args before the
	// request is accepted. The GPU block is decided per node later.
	if _, err := compose.Render(m, artifact, runtime, compose.Options{Env: inf.Env, Args: inf.Args}); err != nil {
		return nil, err
	}
	req.Inference = &inf
	place, err := placementFor(req)
	if err != nil {
		return nil, err
	}
	return &plan{model: m, artifact: artifact, runtime: runtime, place: place}, nil
}

// execute runs every node of a deployment and records the outcome.
func (s *Service) execute(ctx context.Context, id string, p *plan) {
	d, ok := s.store.Get(id)
	if !ok {
		return
	}
	if !s.opts.DryRun {
		s.update(id, func(d *model.NodeDeployment) { d.Status = model.NodeDeployRunning })
	}

	limit := make(chan struct{}, s.opts.MaxParallel)
	var wg sync.WaitGroup
	for i := range d.Nodes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			select {
			case limit <- struct{}{}:
			case <-ctx.Done():
				s.finishNode(id, i, model.NodeDeployInterrupted, "not started: "+ctx.Err().Error())
				return
			}
			defer func() { <-limit }()
			s.deployNode(ctx, id, i, &d.Request, p)
		}(i)
	}
	wg.Wait()

	// The archive line is written before the record says the deployment
	// ended, so that a caller who sees it ended also finds it archived.
	final, ok := s.store.Get(id)
	if !ok {
		return
	}
	now := time.Now().UTC()
	final.Status = overallStatus(final)
	final.FinishedAt = &now
	s.archiveDeployment(final)
	s.update(id, func(d *model.NodeDeployment) {
		d.Status = final.Status
		d.FinishedAt = &now
	})
	log.Info().Str("deploymentId", id).Str("status", final.Status).Msg("Node deployment ended")
}

// overallStatus derives the deployment status from its nodes.
func overallStatus(d *model.NodeDeployment) string {
	if d.DryRun {
		for _, n := range d.Nodes {
			if n.Status != model.NodeDeployDryRun {
				return model.NodeDeployFailed
			}
		}
		return model.NodeDeployDryRun
	}
	succeeded, failed := 0, 0
	for _, n := range d.Nodes {
		switch n.Status {
		case model.NodeDeploySucceeded:
			succeeded++
		case model.NodeDeployInterrupted:
			return model.NodeDeployInterrupted
		default:
			failed++
		}
	}
	switch {
	case failed == 0:
		return model.NodeDeploySucceeded
	case succeeded == 0:
		return model.NodeDeployFailed
	}
	return model.NodeDeployPartial
}

// deployNode carries out the deployment on one node.
func (s *Service) deployNode(ctx context.Context, id string, idx int, req *model.NodeDeploymentReq, p *plan) {
	target := req.Targets[idx]
	logger := log.With().Str("deploymentId", id).Str("nsId", target.NsID).Str("infraId", target.InfraID).
		Str("nodeId", target.NodeID).Logger()
	started := time.Now().UTC()
	s.updateNode(id, idx, func(n *model.NodeDeployResult) {
		n.Status = model.NodeDeployRunning
		n.StartedAt = &started
	})

	fail := func(err error) {
		status := model.NodeDeployFailed
		if ctx.Err() != nil {
			status = model.NodeDeployInterrupted
		}
		logger.Warn().Str("error", err.Error()).Msg("Node deployment failed")
		s.finishNode(id, idx, status, err.Error())
	}

	node, err := s.tb.GetNode(ctx, target.NsID, target.InfraID, target.NodeID)
	if err != nil {
		fail(err)
		return
	}
	if node.SSHKeyID == "" {
		fail(fmt.Errorf("node %q of infra %q has no ssh key", target.NodeID, target.InfraID))
		return
	}
	key, err := s.tb.GetSSHKey(ctx, target.NsID, node.SSHKeyID)
	if err != nil {
		fail(err)
		return
	}
	access, err := tumblebug.ResolveNodeAccess(node, key)
	if err != nil {
		fail(err)
		return
	}
	if err := semaphore.CheckPrivateKey(access.PrivateKey); err != nil {
		fail(fmt.Errorf("ssh key %q of node %q: %w", node.SSHKeyID, target.NodeID, err))
		return
	}
	dir, err := stageDir(s.opts.StageRoot, id, access.CSP, target.NodeID)
	if err != nil {
		fail(err)
		return
	}

	gpu, source, note := s.decideGPU(ctx, req, node)
	ports := servedPorts(req, p)
	s.updateNode(id, idx, func(n *model.NodeDeployResult) {
		n.Ports = ports
		n.Node = &model.NodeDeployNode{
			CSP: access.CSP, Region: access.Region, Zone: access.Zone,
			Host: access.Host, Port: access.Port, User: access.User, SpecID: node.SpecID,
		}
		n.GPU, n.GPUSource = gpu, source
		if note != "" {
			n.Notes = append(n.Notes, note)
		}
		n.StageDir = dir
		n.WorkKey, n.NodeDir = p.place.WorkKey, p.place.NodeDir
	})

	rendered, err := s.stage(id, dir, req, p, gpu)
	if err != nil {
		fail(err)
		return
	}
	if rendered != "" {
		serviceURL := fmt.Sprintf("http://%s:%d%s", access.Host, p.runtime.Port, p.runtime.APIBasePath)
		s.updateNode(id, idx, func(n *model.NodeDeployResult) {
			n.Compose = rendered
			n.ServiceURL = serviceURL
		})
	}
	logger.Info().Str("stageDir", dir).Str("workKey", p.place.WorkKey).Str("nodeDir", p.place.NodeDir).
		Bool("gpu", gpu).Msg("Placed deployment files")

	if s.opts.DryRun {
		s.finishNode(id, idx, model.NodeDeployDryRun, "")
		return
	}

	base := semaphore.RunSpec{
		Name: shortID(id) + "-" + access.CSP + "-" + target.NodeID,
		Host: access.Host, Port: access.Port, User: access.User, PrivateKey: access.PrivateKey,
	}
	steps := []semaphore.RunSpec{withPlaybook(base, PlaybookInstallDocker, map[string]any{"gpu": gpu})}
	common := map[string]any{
		"stage_dir": dir, "deployment_id": id,
		"work_key": p.place.WorkKey, "node_dir": p.place.NodeDir, "compose_project": p.place.Project,
	}
	if req.Type == model.NodeDeployTypeInference {
		vars := copyVars(common)
		vars["health_url"] = healthURL(p.runtime)
		if services := compose.CompletionServices(p.runtime.ID); len(services) > 0 {
			vars["wait_services"] = services
		}
		if gpu {
			addGPUCheckVars(vars, compose.GPUCheckFor(p.runtime.ID))
		}
		steps = append(steps, withPlaybook(base, PlaybookComposeUp, vars))
	} else {
		vars := copyVars(common)
		vars["script"] = ScriptFile
		steps = append(steps, withPlaybook(base, PlaybookCopyAndRun, vars))
	}

	unlock, err := s.locks.lock(ctx, target.Key()+" "+p.place.WorkKey)
	if err != nil {
		fail(fmt.Errorf("waiting for another deployment of %s on the node: %w", p.place.WorkKey, err))
		return
	}
	defer unlock()
	for _, spec := range steps {
		if err := s.runStep(ctx, id, idx, spec); err != nil {
			fail(err)
			return
		}
	}
	s.updateNode(id, idx, func(n *model.NodeDeployResult) { n.GPUCheck = gpuCheckEvidence(n.Steps) })
	s.openNodePorts(ctx, id, idx, target.NsID, node, ports)
	s.finishNode(id, idx, model.NodeDeploySucceeded, "")
	logger.Info().Msg("Node deployment succeeded")
}

// openNodePorts opens ports in the security groups of node and records the
// outcome on the node. Nothing that goes wrong here fails the node: its
// service runs, and the ports can be opened in CB-Tumblebug by hand.
func (s *Service) openNodePorts(ctx context.Context, id string, idx int, nsID string, node *tumblebug.Node, ports []int) {
	if len(ports) == 0 {
		return
	}
	list := joinPorts(ports)
	if !s.opts.OpenPorts {
		s.addNote(id, idx, fmt.Sprintf("port opening is off (AIAPP_DEPLOYMENT_OPEN_PORTS): ports %s were not opened", list))
		return
	}
	sgIDs := uniqueIDs(node.SecurityGroupIDs)
	if len(sgIDs) == 0 {
		s.addNote(id, idx, fmt.Sprintf("the node lists no security group: ports %s were not opened", list))
		return
	}
	opened, failures := s.openPorts(ctx, nsID, sgIDs, ports)
	s.updateNode(id, idx, func(n *model.NodeDeployResult) {
		n.OpenedPorts = opened
		if len(failures) > 0 {
			n.PortsError = strings.Join(failures, "; ")
			n.Notes = append(n.Notes, "some ports could not be opened; see portsError")
		}
	})
	log.Info().Str("deploymentId", id).Str("nodeId", node.ID).Strs("securityGroupIds", sgIDs).
		Str("ports", list).Int("failed", len(failures)).Msg("Opened serving ports")
}

func (s *Service) addNote(id string, idx int, note string) {
	s.updateNode(id, idx, func(n *model.NodeDeployResult) { n.Notes = append(n.Notes, note) })
}

// runStep runs one playbook, records it, and returns why the node cannot go on.
func (s *Service) runStep(ctx context.Context, id string, idx int, spec semaphore.RunSpec) error {
	step := model.NodeDeployStep{Playbook: spec.Playbook, StartedAt: time.Now().UTC()}
	stepIdx := -1
	s.updateNode(id, idx, func(n *model.NodeDeployResult) {
		n.Steps = append(n.Steps, step)
		stepIdx = len(n.Steps) - 1
	})

	result, err := s.runner.Run(ctx, spec)
	finished := time.Now().UTC()
	s.updateNode(id, idx, func(n *model.NodeDeployResult) {
		if stepIdx < 0 || stepIdx >= len(n.Steps) {
			return
		}
		st := &n.Steps[stepIdx]
		st.FinishedAt = &finished
		if result != nil {
			st.TaskID = result.TaskID
			st.TaskURL = result.TaskURL
			st.Status = result.Status
			st.Output = result.Output
			st.CleanupErrors = result.CleanupErrors
		}
		if err != nil {
			st.Error = err.Error()
		}
	})
	switch {
	case err != nil:
		return fmt.Errorf("%s: %w", spec.Playbook, err)
	case !result.Succeeded():
		return fmt.Errorf("%s ended with status %q; see the logs", spec.Playbook, result.Status)
	}
	return nil
}

// decideGPU takes the request's gpu when given, and otherwise asks the node
// spec whether its accelerator is a GPU. A spec that cannot be read means no
// GPU, and the returned note says why.
func (s *Service) decideGPU(ctx context.Context, req *model.NodeDeploymentReq, node *tumblebug.Node) (bool, string, string) {
	if req.Inference != nil && req.Inference.GPU != nil {
		return *req.Inference.GPU, gpuFromRequest, ""
	}
	if node.SpecID == "" {
		return false, gpuFromDefault, "gpu taken as false: the node has no specId"
	}
	spec, err := s.tb.GetSpec(ctx, s.opts.SpecNamespace, node.SpecID)
	if err != nil {
		return false, gpuFromDefault, fmt.Sprintf("gpu taken as false: spec %q could not be read: %s", node.SpecID, err)
	}
	gpu := strings.EqualFold(spec.AcceleratorType, "gpu")
	return gpu, gpuFromSpec, ""
}

// stage places the files of one node and returns the rendered compose file of
// an inference service.
func (s *Service) stage(id, dir string, req *model.NodeDeploymentReq, p *plan, gpu bool) (string, error) {
	if !filepath.IsAbs(s.opts.StageRoot) {
		return "", fmt.Errorf("stage root %q is not an absolute path", s.opts.StageRoot)
	}
	if _, err := os.Stat(dir); err == nil {
		return "", fmt.Errorf("stage folder %q already exists", dir)
	}
	if err := makeStageDir(s.opts.StageRoot, dir); err != nil {
		return "", err
	}

	if req.Type == model.NodeDeployTypeApp {
		if err := stageApp(p.appDir, dir); err != nil {
			return "", err
		}
		if err := writeStageFile(filepath.Join(dir, EnvFile), envFileContent(id, req.App.Env), stageFilePerm); err != nil {
			return "", err
		}
		return "", writeManifest(dir)
	}

	rendered, err := compose.Render(p.model, p.artifact, p.runtime, compose.Options{GPU: gpu, Env: req.Inference.Env, Args: req.Inference.Args})
	if err != nil {
		return "", err
	}
	if err := writeStageFile(filepath.Join(dir, ComposeFile), []byte(rendered), stageFilePerm); err != nil {
		return "", err
	}
	if err := writeStageFile(filepath.Join(dir, EnvFile), envFileContent(id, req.Inference.Env), stageFilePerm); err != nil {
		return "", err
	}
	return rendered, writeManifest(dir)
}

// healthURL is the runtime health endpoint as seen from the node itself.
func healthURL(runtime *model.RuntimeProfile) string {
	path := runtime.HealthPath
	if path == "" {
		path = "/"
	}
	return fmt.Sprintf("http://127.0.0.1:%d%s", runtime.Port, path)
}

// addGPUCheckVars sets the compose-up vars that make the playbook check that
// the runtime runs on the GPU.
func addGPUCheckVars(vars map[string]any, check *compose.GPUCheck) {
	if check == nil {
		return
	}
	vars["gpu_check"] = check.Method
	if check.Method == compose.GPUCheckLog {
		vars["gpu_log_service"] = check.Service
		vars["gpu_log_pattern"] = check.Pattern
		vars["gpu_log_evidence"] = check.Evidence
	}
}

// gpuCheckMarker starts the message compose-up prints once the GPU check
// passed: "aiapp-gpu-check: <method>: <evidence line>".
const gpuCheckMarker = "aiapp-gpu-check: "

// gpuCheckEvidence finds what the compose-up GPU check printed in the task
// output of steps, as "<method>: <evidence line>". The debug task prints it
// as `"msg": "aiapp-gpu-check: ..."`, a JSON string. Empty when no step
// printed it.
func gpuCheckEvidence(steps []model.NodeDeployStep) string {
	for i := len(steps) - 1; i >= 0; i-- {
		if steps[i].Playbook != PlaybookComposeUp {
			continue
		}
		for _, line := range steps[i].Output {
			_, quoted, ok := strings.Cut(line, `"msg": `)
			if !ok || !strings.Contains(quoted, gpuCheckMarker) {
				continue
			}
			var msg string
			if err := json.Unmarshal([]byte(strings.TrimSpace(quoted)), &msg); err != nil {
				continue
			}
			if evidence, ok := strings.CutPrefix(msg, gpuCheckMarker); ok {
				return evidence
			}
		}
	}
	return ""
}

func withPlaybook(base semaphore.RunSpec, playbook string, vars map[string]any) semaphore.RunSpec {
	base.Playbook = playbook
	base.ExtraVars = vars
	return base
}

func copyVars(vars map[string]any) map[string]any {
	out := make(map[string]any, len(vars)+1)
	for k, v := range vars {
		out[k] = v
	}
	return out
}

// shortID is the first group of a UUID, enough to tell runs apart in the
// Semaphore UI.
func shortID(id string) string {
	if i := strings.IndexByte(id, '-'); i > 0 {
		return id[:i]
	}
	return id
}

func (s *Service) finishNode(id string, idx int, status, errText string) {
	now := time.Now().UTC()
	s.updateNode(id, idx, func(n *model.NodeDeployResult) {
		n.Status = status
		n.Error = errText
		n.FinishedAt = &now
	})
}

func (s *Service) updateNode(id string, idx int, change func(*model.NodeDeployResult)) {
	s.update(id, func(d *model.NodeDeployment) {
		if idx >= 0 && idx < len(d.Nodes) {
			change(&d.Nodes[idx])
		}
	})
}

// update changes a record. A record that cannot be written to the store file
// is still changed in memory, and the failure is logged: the deployment goes
// on whether or not its record reaches the disk.
func (s *Service) update(id string, change func(*model.NodeDeployment)) {
	if err := s.store.Update(id, change); err != nil {
		log.Error().Err(err).Str("deploymentId", id).Msg("Failed to write the deployment record")
	}
}

// Get reads one deployment without task output.
func (s *Service) Get(ctx context.Context, id string) (*model.NodeDeployment, error) {
	d, ok := s.store.Get(id)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	withoutOutput(d)
	return d, nil
}

// ListFilter narrows a deployment listing. Every set field must match
// exactly; empty fields match anything.
type ListFilter struct {
	// Type is the deployment type, inference or app.
	Type string
	// NsID matches a deployment when any of its targets is in the namespace.
	NsID string
	// Status is the deployment status.
	Status string
}

func (f ListFilter) matches(d *model.NodeDeployment) bool {
	if f.Type != "" && d.Type != f.Type {
		return false
	}
	if f.Status != "" && d.Status != f.Status {
		return false
	}
	if f.NsID != "" && !slices.ContainsFunc(d.Request.Targets, func(t model.NodeDeployTarget) bool {
		return t.NsID == f.NsID
	}) {
		return false
	}
	return true
}

// List reads the deployments filter matches, newest first, without task output.
func (s *Service) List(ctx context.Context, filter ListFilter) []model.NodeDeployment {
	list := make([]model.NodeDeployment, 0)
	for _, d := range s.store.List() {
		if !filter.matches(&d) {
			continue
		}
		withoutOutput(&d)
		list = append(list, d)
	}
	return list
}

// Logs reads the task output of every node of a deployment.
func (s *Service) Logs(ctx context.Context, id string) (*model.NodeDeploymentLogs, error) {
	d, ok := s.store.Get(id)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	out := &model.NodeDeploymentLogs{ID: d.ID, Status: d.Status, Nodes: make([]model.NodeDeployNodeLogs, len(d.Nodes))}
	for i, n := range d.Nodes {
		steps := n.Steps
		if steps == nil {
			steps = []model.NodeDeployStep{}
		}
		out.Nodes[i] = model.NodeDeployNodeLogs{Target: n.Target, Status: n.Status, Steps: steps}
	}
	return out, nil
}

func withoutOutput(d *model.NodeDeployment) {
	for i := range d.Nodes {
		for j := range d.Nodes[i].Steps {
			d.Nodes[i].Steps[j].Output = nil
		}
	}
}

// Control applies a CB-Tumblebug lifecycle action to every node of a
// deployment. Nothing is sent when the service runs with dry run on or the
// deployment was a dry run; each node is then reported blocked.
func (s *Service) Control(ctx context.Context, id, action string) (*model.NodeControlResp, error) {
	switch action {
	case tumblebug.NodeActionSuspend, tumblebug.NodeActionResume, tumblebug.NodeActionReboot, tumblebug.NodeActionTerminate:
	case "":
		return nil, &model.ValidationError{Message: "action required"}
	default:
		return nil, &model.ValidationError{Message: "action must be suspend, resume, reboot or terminate"}
	}
	d, ok := s.store.Get(id)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}

	started := time.Now().UTC()
	blocked := ""
	switch {
	case s.opts.DryRun:
		blocked = "dry run is on: nothing was sent to CB-Tumblebug"
	case d.DryRun:
		blocked = "the deployment was a dry run: nothing was sent to CB-Tumblebug"
	}

	resp := &model.NodeControlResp{ID: id, Action: action, Nodes: make([]model.NodeControlResult, len(d.Nodes))}
	var failures []string
	for i, n := range d.Nodes {
		t := n.Target
		result := model.NodeControlResult{Target: t}
		if blocked != "" {
			result.Status, result.Message = controlBlocked, blocked
		} else if msg, err := s.tb.ControlNode(ctx, t.NsID, t.InfraID, t.NodeID, action); err != nil {
			result.Status, result.Message = controlFailed, err.Error()
			failures = append(failures, t.Key()+": "+err.Error())
		} else {
			result.Status, result.Message = controlDone, msg
		}
		resp.Nodes[i] = result
	}
	log.Info().Str("deploymentId", id).Str("action", action).Bool("blocked", blocked != "").
		Int("failed", len(failures)).Msg("Controlled deployment nodes")
	s.archiveControl(ctx, d, action, started, resp, blocked != "", failures)
	return resp, nil
}

// archiveDeployment leaves one archive line for a finished deployment.
func (s *Service) archiveDeployment(d *model.NodeDeployment) {
	if s.archive == nil {
		return
	}
	type nodeOutcome struct {
		Target      model.NodeDeployTarget `json:"target"`
		Status      string                 `json:"status"`
		Error       string                 `json:"error,omitempty"`
		OpenedPorts []model.NodeOpenedPort `json:"openedPorts,omitempty"`
		PortsError  string                 `json:"portsError,omitempty"`
	}
	outcomes := make([]nodeOutcome, len(d.Nodes))
	for i, n := range d.Nodes {
		outcomes[i] = nodeOutcome{Target: n.Target, Status: n.Status, Error: n.Error,
			OpenedPorts: n.OpenedPorts, PortsError: n.PortsError}
	}
	ended := time.Now().UTC()
	if d.FinishedAt != nil {
		ended = *d.FinishedAt
	}
	record := model.ArchiveRecord{
		RunID: d.ID, StartedAt: d.CreatedAt, EndedAt: ended, Namespace: firstNamespace(d), Kind: archiveKindDeploy,
		Steps: []model.ToolStep{{
			Index: 1, Tool: "create_deployment", Grade: "write", Input: d.Request, Executed: !d.DryRun,
			Output:   map[string]any{"id": d.ID, "status": d.Status, "nodes": outcomes},
			CalledAt: d.CreatedAt, Duration: ended.Sub(d.CreatedAt).Round(time.Millisecond).String(),
		}},
	}
	if d.Status != model.NodeDeploySucceeded && d.Status != model.NodeDeployDryRun {
		record.Error = "deployment " + d.Status
	}
	if err := s.archive.Append(context.Background(), record); err != nil {
		log.Error().Err(err).Str("deploymentId", d.ID).Msg("Failed to archive node deployment")
	}
}

func (s *Service) archiveControl(ctx context.Context, d *model.NodeDeployment, action string, started time.Time,
	resp *model.NodeControlResp, blocked bool, failures []string) {
	if s.archive == nil {
		return
	}
	record := model.ArchiveRecord{
		RunID: "control-" + d.ID + "-" + fmt.Sprint(started.UnixNano()), StartedAt: started, EndedAt: time.Now().UTC(),
		Namespace: firstNamespace(d), Kind: archiveKindControl,
		Steps: []model.ToolStep{{
			Index: 1, Tool: "control_node", Grade: "write",
			Input: map[string]any{"id": d.ID, "action": action}, Executed: !blocked, Output: resp,
			CalledAt: started, Duration: time.Since(started).Round(time.Millisecond).String(),
		}},
	}
	if len(failures) > 0 {
		record.Error = strings.Join(failures, "; ")
	}
	if err := s.archive.Append(ctx, record); err != nil {
		log.Error().Err(err).Str("deploymentId", d.ID).Msg("Failed to archive node control")
	}
}

func firstNamespace(d *model.NodeDeployment) string {
	if len(d.Request.Targets) > 0 {
		return d.Request.Targets[0].NsID
	}
	return ""
}

// Shutdown cancels every running deployment and waits for them to clean up
// what they registered in Semaphore, or for ctx to end. Deployments cut off
// are recorded interrupted.
func (s *Service) Shutdown(ctx context.Context) error {
	s.cancel()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("deployments still running at shutdown: %w", ctx.Err())
	}
}

// newUUID returns a random (version 4) UUID.
func newUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate a deployment id: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
