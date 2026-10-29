package nodedeploy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/appstore"
	"github.com/ish-hcc/ai-agent-proto/internal/archive"
	"github.com/ish-hcc/ai-agent-proto/internal/config"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/modelcat"
	"github.com/ish-hcc/ai-agent-proto/internal/semaphore"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

const (
	testKeyBody = "c2VjcmV0LWtleS1ib2R5LWZvci10ZXN0cw=="
	// The PEM label is split so that secret scanners do not take this fake
	// key for a real one.
	testKeyLabel = "OPENSSH " + "PRIVATE KEY"
	testKey      = "-----BEGIN " + testKeyLabel + "-----\n" + testKeyBody + "\n-----END " + testKeyLabel + "-----\n"
)

// fakeTumblebug answers the node, ssh key, spec and node control routes for
// nodes vm1 (aws, GPU spec), vm2 (azure, CPU spec) and vm3 (gcp, spec that
// cannot be read), all in namespace etri, infra travel.
type fakeTumblebug struct {
	mu       sync.Mutex
	controls []string
	// failControl makes control of this node answer 500.
	failControl string
}

func (f *fakeTumblebug) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	nodes := map[string]map[string]any{
		"vm1": {"id": "vm1", "publicIP": "203.0.113.11", "sshKeyId": "key1", "nodeUserName": "cb-user",
			"specId": "aws+gpu", "connectionConfig": map[string]any{"providerName": "AWS",
				"regionZoneInfo": map[string]any{"assignedRegion": "ap-northeast-2", "assignedZone": "ap-northeast-2a"}}},
		"vm2": {"id": "vm2", "privateIP": "10.0.0.12", "sshKeyId": "key1", "nodeUserName": "cb-user",
			"specId": "azure+cpu", "connectionConfig": map[string]any{"providerName": "Azure"}},
		"vm3": {"id": "vm3", "publicIP": "203.0.113.13", "sshKeyId": "key1", "nodeUserName": "cb-user",
			"specId": "gcp+missing", "connectionConfig": map[string]any{"providerName": "GCP"}},
		"evil": {"id": "evil", "publicIP": "203.0.113.14", "sshKeyId": "key1", "nodeUserName": "cb-user",
			"connectionConfig": map[string]any{"providerName": "../../../../tmp/evil"}},
		"badkey": {"id": "badkey", "publicIP": "203.0.113.15", "sshKeyId": "broken", "nodeUserName": "cb-user",
			"connectionConfig": map[string]any{"providerName": "AWS"}},
	}
	path := strings.TrimPrefix(r.URL.Path, "/tumblebug")
	if m := regexp.MustCompile(`^/ns/etri/infra/travel/node/([^/]+)$`).FindStringSubmatch(path); m != nil {
		if node, ok := nodes[m[1]]; ok {
			writeJSON(w, http.StatusOK, node)
			return
		}
	}
	if m := regexp.MustCompile(`^/ns/etri/control/infra/travel/node/([^/]+)$`).FindStringSubmatch(path); m != nil {
		f.mu.Lock()
		f.controls = append(f.controls, m[1]+":"+r.URL.Query().Get("action"))
		fail := f.failControl == m[1]
		f.mu.Unlock()
		if fail {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "cannot control"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"message": r.URL.Query().Get("action") + " " + m[1]})
		return
	}
	switch path {
	case "/ns/etri/resources/sshKey/key1":
		writeJSON(w, http.StatusOK, map[string]any{"id": "key1", "username": "cb-user", "privateKey": testKey})
		return
	case "/ns/etri/resources/sshKey/broken":
		writeJSON(w, http.StatusOK, map[string]any{"id": "broken", "username": "cb-user", "privateKey": "not-a-key"})
		return
	case "/ns/system/resources/spec/aws+gpu":
		writeJSON(w, http.StatusOK, map[string]any{"id": "aws+gpu", "acceleratorType": "gpu"})
		return
	case "/ns/system/resources/spec/azure+cpu":
		writeJSON(w, http.StatusOK, map[string]any{"id": "azure+cpu", "acceleratorType": ""})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "not found: " + path})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// fakeRunner stands in for Semaphore. It records every run and answers with
// the status set for the node host and playbook.
type fakeRunner struct {
	mu   sync.Mutex
	runs []semaphore.RunSpec
	// statuses maps "<host> <playbook>" to the task status; success otherwise.
	statuses map[string]string
	// block, when set, holds every run until it is closed or ctx ends.
	block chan struct{}
}

func (f *fakeRunner) Run(ctx context.Context, spec semaphore.RunSpec) (*semaphore.RunResult, error) {
	f.mu.Lock()
	f.runs = append(f.runs, spec)
	taskID := len(f.runs)
	status := f.statuses[spec.Host+" "+spec.Playbook]
	block := f.block
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return &semaphore.RunResult{TaskID: 7, Status: semaphore.StatusStopped, Output: []string{}}, ctx.Err()
		}
	}
	if status == "" {
		status = semaphore.StatusSuccess
	}
	return &semaphore.RunResult{
		TaskID: taskID, Status: status,
		Output: []string{"TASK [" + spec.Playbook + "]", "ok: [node] " + spec.Host},
	}, nil
}

func (f *fakeRunner) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.runs)
}

type fixture struct {
	svc     *Service
	tb      *fakeTumblebug
	runner  *fakeRunner
	stage   string
	archive *archive.Store
	store   string
}

func newFixture(t *testing.T, dryRun bool) *fixture {
	t.Helper()
	tb := &fakeTumblebug{}
	srv := httptest.NewServer(tb)
	t.Cleanup(srv.Close)
	tbClient := tumblebug.NewClient(config.TumblebugConfig{BaseURL: srv.URL + "/tumblebug", Timeout: 5 * time.Second})

	models, _, err := modelcat.Open("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := models.Register(context.Background(), model.Model{
		ID: "tiny", Name: "Tiny", Tasks: []string{"embedding"},
		Artifacts: []model.Artifact{{
			ID: "gguf", Source: "ollama", Ref: "all-minilm:22m", Format: model.FormatGGUF,
			Engines: []model.EngineCompat{{Engine: model.EngineOllama}},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	apps, err := appstore.Open(filepath.Join(t.TempDir(), "apps"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := apps.Register(context.Background(), "web", "echo v1", []appstore.File{
		{Name: "docker-compose.yml", Content: strings.NewReader("services: {}\n")},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := apps.Register(context.Background(), "web", "docker compose up -d", []appstore.File{
		{Name: "docker-compose.yml", Content: strings.NewReader("services:\n  web:\n    image: nginx:alpine\n")},
		{Name: "index.html", Content: strings.NewReader("hello")},
	}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	archiveStore, err := archive.NewStore(filepath.Join(dir, "archive"))
	if err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(dir, "deployments.json")
	store, err := OpenStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(dir, "stage")
	runner := &fakeRunner{statuses: map[string]string{}}
	svc := NewService(tbClient, runner, models, apps, store, archiveStore, Options{
		StageRoot: stage, SpecNamespace: "system", DryRun: dryRun, MaxParallel: 2,
	})
	t.Cleanup(func() { _ = svc.Shutdown(context.Background()) })
	return &fixture{svc: svc, tb: tb, runner: runner, stage: stage, archive: archiveStore, store: storePath}
}

func target(node string) model.NodeDeployTarget {
	return model.NodeDeployTarget{Kind: model.NodeTargetKindNode, NsID: "etri", InfraID: "travel", NodeID: node}
}

func modelReq(nodes ...string) model.NodeDeploymentReq {
	req := model.NodeDeploymentReq{
		Type:         model.NodeDeployTypeModelService,
		ModelService: &model.NodeModelServiceReq{ModelID: "tiny", ArtifactID: "gguf", Env: map[string]string{"OLLAMA_KEEP_ALIVE": "24h"}},
	}
	for _, n := range nodes {
		req.Targets = append(req.Targets, target(n))
	}
	return req
}

func appReq(nodes ...string) model.NodeDeploymentReq {
	req := model.NodeDeploymentReq{
		Type: model.NodeDeployTypeApp,
		App:  &model.NodeAppReq{Name: "web", Env: map[string]string{"MODEL_URL": "http://10.0.0.5:11434/v1", "GREETING": "a $HOME `x`"}},
	}
	for _, n := range nodes {
		req.Targets = append(req.Targets, target(n))
	}
	return req
}

// waitFinished polls a deployment until it leaves pending and running.
func waitFinished(t *testing.T, svc *Service, id string) *model.NodeDeployment {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		d, err := svc.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if d.Status != model.NodeDeployPending && d.Status != model.NodeDeployRunning {
			return d
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("deployment %s did not finish", id)
	return nil
}

func assertNoKey(t *testing.T, what string, data []byte) {
	t.Helper()
	if strings.Contains(string(data), testKeyBody) {
		t.Errorf("%s holds the private key", what)
	}
}

func TestModelServiceDeploymentSucceeds(t *testing.T) {
	f := newFixture(t, false)
	created, err := f.svc.Create(context.Background(), modelReq("vm1", "vm2"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Status != model.NodeDeployPending && created.Status != model.NodeDeployRunning {
		t.Errorf("created status = %q, want pending", created.Status)
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(created.ID) {
		t.Errorf("id = %q, want a version 4 UUID", created.ID)
	}
	if created.Request.ModelService.Engine != model.EngineOllama {
		t.Errorf("engine = %q, want the artifact's first engine filled in", created.Request.ModelService.Engine)
	}

	d := waitFinished(t, f.svc, created.ID)
	if d.Status != model.NodeDeploySucceeded {
		t.Fatalf("status = %q, nodes = %+v", d.Status, d.Nodes)
	}
	vm1, vm2 := d.Nodes[0], d.Nodes[1]
	if vm1.Node == nil || vm1.Node.CSP != "aws" || vm1.Node.Host != "203.0.113.11" || vm1.Node.Region != "ap-northeast-2" {
		t.Errorf("vm1 node = %+v", vm1.Node)
	}
	if vm2.Node == nil || vm2.Node.Host != "10.0.0.12" || vm2.Node.CSP != "azure" {
		t.Errorf("vm2 node = %+v, want the private IP when there is no public one", vm2.Node)
	}
	if !vm1.GPU || vm1.GPUSource != "spec" || vm2.GPU || vm2.GPUSource != "spec" {
		t.Errorf("gpu = %v/%s and %v/%s, want true and false from the spec", vm1.GPU, vm1.GPUSource, vm2.GPU, vm2.GPUSource)
	}
	if !strings.Contains(vm1.Compose, "driver: nvidia") || strings.Contains(vm2.Compose, "driver: nvidia") {
		t.Error("the GPU block follows the spec decision")
	}
	if vm1.ServiceURL != "http://203.0.113.11:11434/v1" {
		t.Errorf("service url = %q", vm1.ServiceURL)
	}
	if len(vm1.Steps) != 2 || vm1.Steps[0].Playbook != PlaybookInstallDocker || vm1.Steps[1].Playbook != PlaybookComposeUp ||
		vm1.Steps[1].Status != semaphore.StatusSuccess || vm1.Steps[1].FinishedAt == nil {
		t.Errorf("steps = %+v", vm1.Steps)
	}
	if vm1.Steps[0].Output != nil {
		t.Error("Get returns task output; it belongs to Logs")
	}

	wantDir := filepath.Join(f.stage, d.ID, "aws-vm1")
	if vm1.StageDir != wantDir {
		t.Errorf("stage dir = %q, want %q", vm1.StageDir, wantDir)
	}
	composeFile, err := os.ReadFile(filepath.Join(wantDir, ComposeFile))
	if err != nil || string(composeFile) != vm1.Compose {
		t.Errorf("staged compose = %q, %v", composeFile, err)
	}
	env, _ := os.ReadFile(filepath.Join(wantDir, EnvFile))
	if !strings.Contains(string(env), "OLLAMA_KEEP_ALIVE='24h'\n") {
		t.Errorf(".env = %q", env)
	}
	for _, p := range []string{wantDir, filepath.Join(wantDir, ComposeFile), filepath.Join(wantDir, EnvFile)} {
		info, err := os.Stat(p)
		if err != nil || info.Mode().Perm()&0o004 == 0 {
			t.Errorf("%s mode = %v, %v; Semaphore runs as another user and must read it", p, info.Mode(), err)
		}
	}

	f.runner.mu.Lock()
	runs := append([]semaphore.RunSpec(nil), f.runner.runs...)
	f.runner.mu.Unlock()
	if len(runs) != 4 {
		t.Fatalf("runs = %d, want 2 per node", len(runs))
	}
	for _, run := range runs {
		if run.PrivateKey != testKey || run.User != "cb-user" || run.Port != 22 {
			t.Errorf("run = %+v", run)
		}
		switch run.Playbook {
		case PlaybookInstallDocker:
			if gpu := run.ExtraVars["gpu"]; gpu != (run.Host == "203.0.113.11") {
				t.Errorf("install-docker gpu on %s = %v", run.Host, gpu)
			}
		case PlaybookComposeUp:
			if waits, _ := run.ExtraVars["wait_services"].([]string); len(waits) != 1 || waits[0] != "model-pull" {
				t.Errorf("compose-up wait_services = %v, want the ollama model-pull service", run.ExtraVars["wait_services"])
			}
			if run.ExtraVars["health_url"] != "http://127.0.0.1:11434/" || run.ExtraVars["deployment_id"] != d.ID ||
				!strings.HasPrefix(run.ExtraVars["stage_dir"].(string), filepath.Join(f.stage, d.ID)) {
				t.Errorf("compose-up vars = %v", run.ExtraVars)
			}
		default:
			t.Errorf("unexpected playbook %s", run.Playbook)
		}
	}

	logs, err := f.svc.Logs(context.Background(), d.ID)
	if err != nil || len(logs.Nodes) != 2 || len(logs.Nodes[0].Steps[1].Output) == 0 {
		t.Fatalf("logs = %+v, %v", logs, err)
	}

	stored, _ := os.ReadFile(f.store)
	assertNoKey(t, "the store file", stored)
	records, _ := f.archive.List(context.Background(), 0)
	if len(records) != 1 || records[0].RunID != d.ID || records[0].Kind != archiveKindDeploy || !records[0].Steps[0].Executed {
		t.Errorf("archive = %+v", records)
	}
	encoded, _ := json.Marshal(records)
	assertNoKey(t, "the archive", encoded)
}

func TestAppDeploymentStagesFilesScriptAndEnv(t *testing.T) {
	f := newFixture(t, false)
	created, err := f.svc.Create(context.Background(), appReq("vm1"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Request.App.Version != 2 {
		t.Errorf("version = %d, want the latest (2) filled in", created.Request.App.Version)
	}
	d := waitFinished(t, f.svc, created.ID)
	if d.Status != model.NodeDeploySucceeded {
		t.Fatalf("status = %q, nodes = %+v", d.Status, d.Nodes)
	}
	dir := d.Nodes[0].StageDir
	for name, want := range map[string]string{
		"docker-compose.yml": "services:\n  web:\n    image: nginx:alpine\n",
		"index.html":         "hello",
		ScriptFile:           "docker compose up -d",
	} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", name, got, err, want)
		}
	}
	env, _ := os.ReadFile(filepath.Join(dir, EnvFile))
	if !strings.Contains(string(env), "GREETING='a $HOME `x`'\nMODEL_URL='http://10.0.0.5:11434/v1'\n") {
		t.Errorf(".env = %q, want sorted single quoted values", env)
	}
	if d.Nodes[0].GPUSource != "spec" {
		t.Errorf("gpu source = %q", d.Nodes[0].GPUSource)
	}

	f.runner.mu.Lock()
	defer f.runner.mu.Unlock()
	last := f.runner.runs[len(f.runner.runs)-1]
	if last.Playbook != PlaybookCopyAndRun || last.ExtraVars["script"] != ScriptFile {
		t.Errorf("last run = %s %v", last.Playbook, last.ExtraVars)
	}
}

func TestDeploymentFailsAndPartiallyFails(t *testing.T) {
	f := newFixture(t, false)
	// vm1's compose-up fails; vm2 succeeds.
	f.runner.statuses["203.0.113.11 "+PlaybookComposeUp] = semaphore.StatusError
	created, err := f.svc.Create(context.Background(), modelReq("vm1", "vm2"))
	if err != nil {
		t.Fatal(err)
	}
	d := waitFinished(t, f.svc, created.ID)
	if d.Status != model.NodeDeployPartial {
		t.Fatalf("status = %q, want partial", d.Status)
	}
	if d.Nodes[0].Status != model.NodeDeployFailed || !strings.Contains(d.Nodes[0].Error, "compose-up.yml ended with status \"error\"") {
		t.Errorf("vm1 = %s %q", d.Nodes[0].Status, d.Nodes[0].Error)
	}
	if d.Nodes[1].Status != model.NodeDeploySucceeded {
		t.Errorf("vm2 = %s", d.Nodes[1].Status)
	}

	// install-docker failing stops the node before compose-up.
	f.runner.statuses["10.0.0.12 "+PlaybookInstallDocker] = semaphore.StatusError
	before := f.runner.count()
	created, err = f.svc.Create(context.Background(), modelReq("vm1", "vm2"))
	if err != nil {
		t.Fatal(err)
	}
	d = waitFinished(t, f.svc, created.ID)
	if d.Status != model.NodeDeployFailed {
		t.Fatalf("status = %q, want failed", d.Status)
	}
	if len(d.Nodes[1].Steps) != 1 || f.runner.count()-before != 3 {
		t.Errorf("vm2 steps = %+v, runs = %d; a failed install-docker must stop the node", d.Nodes[1].Steps, f.runner.count()-before)
	}
	records, _ := f.archive.List(context.Background(), 0)
	if len(records) != 2 || records[0].Error == "" {
		t.Errorf("archive = %+v", records)
	}
}

func TestUnreadableSpecTakesNoGPUAndSaysWhy(t *testing.T) {
	f := newFixture(t, false)
	created, err := f.svc.Create(context.Background(), modelReq("vm3"))
	if err != nil {
		t.Fatal(err)
	}
	d := waitFinished(t, f.svc, created.ID)
	n := d.Nodes[0]
	if n.GPU || n.GPUSource != "default" || len(n.Notes) != 1 || !strings.Contains(n.Notes[0], "gcp+missing") {
		t.Errorf("node = gpu %v source %q notes %v", n.GPU, n.GPUSource, n.Notes)
	}
	if d.Status != model.NodeDeploySucceeded {
		t.Errorf("status = %q; an unreadable spec must not fail the node", d.Status)
	}
}

func TestRequestedGPUOverridesTheSpec(t *testing.T) {
	f := newFixture(t, false)
	req := modelReq("vm1")
	no := false
	req.ModelService.GPU = &no
	created, err := f.svc.Create(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	d := waitFinished(t, f.svc, created.ID)
	if d.Nodes[0].GPU || d.Nodes[0].GPUSource != "request" || strings.Contains(d.Nodes[0].Compose, "nvidia") {
		t.Errorf("node = %+v", d.Nodes[0])
	}
}

func TestMissingNodeFailsOnlyThatNode(t *testing.T) {
	f := newFixture(t, false)
	created, err := f.svc.Create(context.Background(), appReq("vm1", "vm9"))
	if err != nil {
		t.Fatal(err)
	}
	d := waitFinished(t, f.svc, created.ID)
	if d.Status != model.NodeDeployPartial || d.Nodes[1].Status != model.NodeDeployFailed || d.Nodes[1].Node != nil {
		t.Errorf("status = %q, vm9 = %+v", d.Status, d.Nodes[1])
	}
}

func TestDryRunPlacesFilesAndNeverRuns(t *testing.T) {
	f := newFixture(t, true)
	d, err := f.svc.Create(context.Background(), modelReq("vm1", "vm2"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if d.Status != model.NodeDeployDryRun || !d.DryRun || d.FinishedAt == nil {
		t.Fatalf("dry run returned %q, want the finished dry-run record", d.Status)
	}
	if f.runner.count() != 0 {
		t.Errorf("%d Semaphore runs in dry run, want 0", f.runner.count())
	}
	for _, n := range d.Nodes {
		if n.Status != model.NodeDeployDryRun || n.Compose == "" || n.Node == nil || n.Node.Host == "" || len(n.Steps) != 0 {
			t.Errorf("node = %+v", n)
		}
		if _, err := os.Stat(filepath.Join(n.StageDir, ComposeFile)); err != nil {
			t.Errorf("compose not staged: %v", err)
		}
	}
	encoded, _ := json.Marshal(d)
	assertNoKey(t, "the dry-run record", encoded)
	records, _ := f.archive.List(context.Background(), 0)
	if len(records) != 1 || records[0].Steps[0].Executed {
		t.Errorf("archive = %+v, want one record not executed", records)
	}

	ctl, err := f.svc.Control(context.Background(), d.ID, "reboot")
	if err != nil || ctl.Nodes[0].Status != controlBlocked || len(f.tb.controls) != 0 {
		t.Errorf("control in dry run = %+v, %v, calls %v", ctl, err, f.tb.controls)
	}
}

func TestRequestsRefusedBeforeAnythingRuns(t *testing.T) {
	f := newFixture(t, false)
	k8s := modelReq("vm1")
	k8s.Targets[0].Kind = model.NodeTargetKindK8s
	if _, err := f.svc.Create(context.Background(), k8s); !errors.Is(err, model.ErrUnsupportedTarget) {
		t.Errorf("k8s target = %v, want ErrUnsupportedTarget", err)
	}

	cases := map[string]func(*model.NodeDeploymentReq){
		"lower case env name":  func(r *model.NodeDeploymentReq) { r.ModelService.Env = map[string]string{"bad": "x"} },
		"env value with \\n":   func(r *model.NodeDeploymentReq) { r.ModelService.Env = map[string]string{"A": "x\ny"} },
		"env value with quote": func(r *model.NodeDeploymentReq) { r.ModelService.Env = map[string]string{"A": "it's"} },
		"no targets":           func(r *model.NodeDeploymentReq) { r.Targets = nil },
		"duplicate target":     func(r *model.NodeDeploymentReq) { r.Targets = append(r.Targets, r.Targets[0]) },
		"path in node id":      func(r *model.NodeDeploymentReq) { r.Targets[0].NodeID = "../x" },
		"no model service":     func(r *model.NodeDeploymentReq) { r.ModelService = nil },
		"unknown type":         func(r *model.NodeDeploymentReq) { r.Type = "vm" },
	}
	for name, mutate := range cases {
		req := modelReq("vm1")
		mutate(&req)
		if _, err := f.svc.Create(context.Background(), req); !errors.Is(err, model.ErrValidation) {
			t.Errorf("%s: err = %v, want a validation error", name, err)
		}
	}

	unknown := modelReq("vm1")
	unknown.ModelService.ModelID = "nope"
	if _, err := f.svc.Create(context.Background(), unknown); !errors.Is(err, modelcat.ErrNotFound) {
		t.Errorf("unknown model = %v", err)
	}
	wrongEngine := modelReq("vm1")
	wrongEngine.ModelService.Engine = model.EngineVLLM
	if _, err := f.svc.Create(context.Background(), wrongEngine); err == nil {
		t.Error("an engine the artifact does not list was accepted")
	}
	missingApp := appReq("vm1")
	missingApp.App.Name = "nope"
	if _, err := f.svc.Create(context.Background(), missingApp); !errors.Is(err, appstore.ErrNotFound) {
		t.Errorf("missing app = %v", err)
	}
	missingVersion := appReq("vm1")
	missingVersion.App.Version = 9
	if _, err := f.svc.Create(context.Background(), missingVersion); !errors.Is(err, appstore.ErrVersionNotFound) {
		t.Errorf("missing version = %v", err)
	}

	if f.runner.count() != 0 || len(f.svc.List(context.Background())) != 0 {
		t.Error("a refused request left a run or a record")
	}
	if entries, _ := os.ReadDir(f.stage); len(entries) != 0 {
		t.Errorf("a refused request staged %d folders", len(entries))
	}
}

func TestControlSendsTheActionToEveryNode(t *testing.T) {
	f := newFixture(t, false)
	created, err := f.svc.Create(context.Background(), appReq("vm1", "vm2"))
	if err != nil {
		t.Fatal(err)
	}
	waitFinished(t, f.svc, created.ID)
	f.tb.failControl = "vm2"

	resp, err := f.svc.Control(context.Background(), created.ID, "suspend")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Nodes[0].Status != controlDone || resp.Nodes[0].Message != "suspend vm1" || resp.Nodes[1].Status != controlFailed {
		t.Errorf("control = %+v", resp)
	}
	if strings.Join(f.tb.controls, ",") != "vm1:suspend,vm2:suspend" {
		t.Errorf("calls = %v", f.tb.controls)
	}
	if _, err := f.svc.Control(context.Background(), created.ID, "explode"); !errors.Is(err, model.ErrValidation) {
		t.Errorf("unknown action = %v", err)
	}
	if _, err := f.svc.Control(context.Background(), "missing", "reboot"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown deployment = %v", err)
	}
	records, _ := f.archive.List(context.Background(), 0)
	if len(records) != 2 || records[0].Kind != archiveKindControl || records[0].Error == "" {
		t.Errorf("archive = %+v", records)
	}
}

func TestShutdownInterruptsARunningDeploymentAndReopenKeepsIt(t *testing.T) {
	f := newFixture(t, false)
	f.runner.block = make(chan struct{})
	created, err := f.svc.Create(context.Background(), appReq("vm1"))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for f.runner.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if err := f.svc.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	d, _ := f.svc.Get(context.Background(), created.ID)
	if d.Status != model.NodeDeployInterrupted || d.Nodes[0].Status != model.NodeDeployInterrupted {
		t.Errorf("after shutdown = %q / %q, want interrupted", d.Status, d.Nodes[0].Status)
	}

	reopened, err := OpenStore(f.store)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := reopened.Get(created.ID); !ok || got.Status != model.NodeDeployInterrupted {
		t.Errorf("reopened = %+v", got)
	}
}

func TestOpenStoreMarksRunningRecordsInterrupted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.json")
	body := `{"deployments":[
		{"id":"a","status":"running","createdAt":"2026-10-29T12:00:00Z","nodes":[{"status":"running"},{"status":"succeeded"}]},
		{"id":"b","status":"succeeded","createdAt":"2026-10-29T13:00:00Z","nodes":[{"status":"succeeded"}]}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := store.Get("a")
	b, _ := store.Get("b")
	if a.Status != model.NodeDeployInterrupted || a.Nodes[0].Status != model.NodeDeployInterrupted ||
		a.Nodes[1].Status != model.NodeDeploySucceeded || a.FinishedAt == nil {
		t.Errorf("a = %+v", a)
	}
	if b.Status != model.NodeDeploySucceeded {
		t.Errorf("b = %+v", b)
	}
	list := store.List()
	if len(list) != 2 || list[0].ID != "b" {
		t.Errorf("list order = %v, want newest first", list)
	}
	raw, _ := io.ReadAll(mustOpen(t, path))
	if !strings.Contains(string(raw), `"interrupted"`) {
		t.Error("the interrupted status was not written back")
	}
}

func mustOpen(t *testing.T, path string) *os.File {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func TestCloudNameOutsideThePatternIsRefused(t *testing.T) {
	f := newFixture(t, false)
	created, err := f.svc.Create(context.Background(), appReq("evil", "vm1"))
	if err != nil {
		t.Fatal(err)
	}
	d := waitFinished(t, f.svc, created.ID)
	n := d.Nodes[0]
	if n.Status != model.NodeDeployFailed || !strings.Contains(n.Error, "not a plain name") || n.StageDir != "" || len(n.Steps) != 0 {
		t.Errorf("evil node = %+v, want refused before staging", n)
	}
	if d.Nodes[1].Status != model.NodeDeploySucceeded || d.Status != model.NodeDeployPartial {
		t.Errorf("status = %q, vm1 = %q", d.Status, d.Nodes[1].Status)
	}
	// Only vm1's folder exists under the deployment, and nothing above the root.
	entries, _ := os.ReadDir(filepath.Join(f.stage, d.ID))
	if len(entries) != 1 || entries[0].Name() != "aws-vm1" {
		t.Errorf("stage folders = %v", entries)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(f.stage), "tmp")); err == nil {
		t.Error("a folder was created outside the stage root")
	}
}

func TestStageDirStaysUnderTheRoot(t *testing.T) {
	root := "/var/lib/aiapp/stage"
	got, err := stageDir(root, "3f9b2c4e-8a51-4d27-9e0c-5b1f6a7d2e48", "aws", "vm1")
	if err != nil || got != root+"/3f9b2c4e-8a51-4d27-9e0c-5b1f6a7d2e48/aws-vm1" {
		t.Errorf("stageDir = %q, %v", got, err)
	}
	for name, args := range map[string][3]string{
		"csp with dots":        {"id", "../../../../tmp/evil", "vm1"},
		"csp with a slash":     {"id", "aws/x", "vm1"},
		"empty csp":            {"id", "", "vm1"},
		"upper case csp":       {"id", "AWS", "vm1"},
		"node id climbing out": {"id", "aws", "x/../../../etc"},
		"deployment id out":    {"../..", "aws", "vm1"},
		"extra level":          {"id", "aws", "vm1/sub"},
	} {
		if got, err := stageDir(root, args[0], args[1], args[2]); err == nil {
			t.Errorf("%s: stageDir = %q, want refused", name, got)
		}
	}
}

func TestMalformedKeyFailsTheNodeWithoutSemaphore(t *testing.T) {
	f := newFixture(t, false)
	created, err := f.svc.Create(context.Background(), appReq("badkey"))
	if err != nil {
		t.Fatal(err)
	}
	d := waitFinished(t, f.svc, created.ID)
	n := d.Nodes[0]
	if d.Status != model.NodeDeployFailed || !strings.Contains(n.Error, "not a PEM block") || n.StageDir != "" {
		t.Errorf("node = %+v", n)
	}
	if f.runner.count() != 0 {
		t.Errorf("%d Semaphore runs with a malformed key, want 0", f.runner.count())
	}
}

// placementRun deploys req, waits for it, and returns the extra vars of its
// last playbook, which places the files on the node.
func placementRun(t *testing.T, f *fixture, req model.NodeDeploymentReq) (*model.NodeDeployment, map[string]any) {
	t.Helper()
	created, err := f.svc.Create(context.Background(), req)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	d := waitFinished(t, f.svc, created.ID)
	if d.Status != model.NodeDeploySucceeded {
		t.Fatalf("status = %q, nodes = %+v", d.Status, d.Nodes)
	}
	f.runner.mu.Lock()
	defer f.runner.mu.Unlock()
	last := f.runner.runs[len(f.runner.runs)-1]
	if last.Playbook != PlaybookComposeUp && last.Playbook != PlaybookCopyAndRun {
		t.Fatalf("last run = %s, want the playbook that places the files", last.Playbook)
	}
	return d, last.ExtraVars
}

func TestRedeployLandsInTheSameNodeFolderAndProject(t *testing.T) {
	f := newFixture(t, false)
	if _, err := f.svc.models.Register(context.Background(), model.Model{
		ID: "tiny-hf", Name: "Tiny HF", Tasks: []string{"embedding"},
		Artifacts: []model.Artifact{{
			ID: "hf", Source: "huggingface", Ref: "org/tiny", Format: model.FormatHF,
			Engines: []model.EngineCompat{{Engine: model.EngineVLLM}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.apps.Register(context.Background(), "api", "docker compose up -d", []appstore.File{
		{Name: "docker-compose.yml", Content: strings.NewReader("services: {}\n")},
	}); err != nil {
		t.Fatal(err)
	}
	vllm := modelReq("vm1")
	vllm.ModelService = &model.NodeModelServiceReq{ModelID: "tiny-hf", ArtifactID: "hf"}
	api := appReq("vm1")
	api.App.Name = "api"

	cases := []struct {
		name    string
		req     model.NodeDeploymentReq
		key     string
		project string
	}{
		{"ollama", modelReq("vm1"), "ms-ollama", "aiapp-ms-ollama"},
		{"ollama again", modelReq("vm1"), "ms-ollama", "aiapp-ms-ollama"},
		{"vllm", vllm, "ms-vllm", "aiapp-ms-vllm"},
		{"app web", appReq("vm1"), "app-web", "aiapp-app-web"},
		{"app web again", appReq("vm1"), "app-web", "aiapp-app-web"},
		{"app api", api, "app-api", "aiapp-app-api"},
	}
	stageDirs := map[string]bool{}
	for _, c := range cases {
		d, vars := placementRun(t, f, c.req)
		if vars["work_key"] != c.key || vars["node_dir"] != "/opt/aiapp/"+c.key || vars["compose_project"] != c.project {
			t.Errorf("%s: vars = %v, want work key %s and project %s", c.name, vars, c.key, c.project)
		}
		if n := d.Nodes[0]; n.WorkKey != c.key || n.NodeDir != "/opt/aiapp/"+c.key {
			t.Errorf("%s: record has work key %q and node dir %q, want %s", c.name, n.WorkKey, n.NodeDir, c.key)
		}
		if vars["deployment_id"] != d.ID {
			t.Errorf("%s: deployment_id = %v, want %s", c.name, vars["deployment_id"], d.ID)
		}
		dir := d.Nodes[0].StageDir
		if stageDirs[dir] || !strings.HasPrefix(dir, filepath.Join(f.stage, d.ID)) {
			t.Errorf("%s: stage folder %q is not one of its own deployment", c.name, dir)
		}
		stageDirs[dir] = true
	}
}

func TestStageFolderListsItsFilesInTheManifest(t *testing.T) {
	f := newFixture(t, false)
	app, _ := placementRun(t, f, appReq("vm1"))
	ms, _ := placementRun(t, f, modelReq("vm1"))
	for name, c := range map[string]struct {
		d    *model.NodeDeployment
		want string
	}{
		"app":           {app, ScriptFile + "\n" + EnvFile + "\ndocker-compose.yml\nindex.html\n"},
		"model service": {ms, EnvFile + "\ndocker-compose.yml\n"},
	} {
		got, err := os.ReadFile(filepath.Join(c.d.Nodes[0].StageDir, ManifestFile))
		if err != nil || string(got) != c.want {
			t.Errorf("%s manifest = %q, %v; want %q", name, got, err, c.want)
		}
	}
}

func TestAppNameComposeCannotTakeIsRefused(t *testing.T) {
	f := newFixture(t, false)
	if _, err := f.svc.apps.Register(context.Background(), "my.web", "true", nil); !errors.Is(err, model.ErrValidation) {
		t.Errorf("upload of an app name with a dot = %v, want a validation error", err)
	}
	req := appReq("vm1")
	req.App.Name = "my.web"
	if _, err := f.svc.Create(context.Background(), req); !errors.Is(err, model.ErrValidation) {
		t.Errorf("app name with a dot = %v, want a validation error", err)
	}
	if f.runner.count() != 0 || len(f.svc.List(context.Background())) != 0 {
		t.Error("a refused request left a run or a record")
	}
}

func TestSamePlacementOfANodeRunsOneDeploymentAtATime(t *testing.T) {
	f := newFixture(t, false)
	f.runner.block = make(chan struct{})
	first, err := f.svc.Create(context.Background(), modelReq("vm1"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.svc.Create(context.Background(), modelReq("vm1"))
	if err != nil {
		t.Fatal(err)
	}
	// Each deployment runs install-docker and compose-up. While the runner
	// holds every run, only one deployment may have started its steps.
	deadline := time.Now().Add(2 * time.Second)
	for f.runner.count() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if n := f.runner.count(); n != 1 {
		t.Errorf("%d runs started while the first deployment holds the node, want 1", n)
	}
	close(f.runner.block)
	for _, id := range []string{first.ID, second.ID} {
		if d := waitFinished(t, f.svc, id); d.Status != model.NodeDeploySucceeded {
			t.Errorf("deployment %s = %q", id, d.Status)
		}
	}
	if n := f.runner.count(); n != 4 {
		t.Errorf("%d runs, want 4", n)
	}
}
