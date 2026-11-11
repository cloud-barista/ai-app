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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/appstore"
	"github.com/ish-hcc/ai-agent-proto/internal/archive"
	"github.com/ish-hcc/ai-agent-proto/internal/compose"
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

// fakeTumblebug answers the node, ssh key, spec, node control and security
// group routes for nodes vm1 (aws, GPU spec, RHEL image), vm2 (azure, CPU
// spec, two security groups, no image) and vm3 (gcp, spec that cannot be read), all in namespace
// etri, infra travel. Node gone answers 404, node down answers 503, and any
// other node answers 500 "no Node found" as CB-Tumblebug 0.13.8 does. vm1,
// vm2 and vm3 are Running unless edited.
// Security group sg-azure-b already lets in TCP 8000-9000 from anywhere.
type fakeTumblebug struct {
	mu       sync.Mutex
	controls []string
	// failControl makes control of this node answer 500.
	failControl string
	// rules are the rule requests received, in order.
	rules []rulesCall
	// failRules makes adding rules to this security group answer 500.
	failRules string
	// nodeEdits are set on the node of that ID when it is read, such as
	// another status or publicIP.
	nodeEdits map[string]map[string]any
	// nodeGone makes reading this node answer 500 "no Node found".
	nodeGone string
	// nodeDown makes reading this node answer 503.
	nodeDown string
	// nodeReads counts the reads of each node.
	nodeReads map[string]int
}

// edit sets the fields of node id that later reads answer with.
func (f *fakeTumblebug) edit(id string, fields map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.nodeEdits == nil {
		f.nodeEdits = map[string]map[string]any{}
	}
	f.nodeEdits[id] = fields
}

func (f *fakeTumblebug) reads(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nodeReads[id]
}

// rulesCall is one POST .../securityGroup/{id}/rules the fake received.
type rulesCall struct {
	SecurityGroupID string
	Body            tumblebug.SecurityGroupUpdateReq
}

func (f *fakeTumblebug) rulesCalls() []rulesCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]rulesCall(nil), f.rules...)
}

func (f *fakeTumblebug) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	nodes := map[string]map[string]any{
		"vm1": {"id": "vm1", "status": "Running", "publicIP": "203.0.113.11", "sshKeyId": "key1", "nodeUserName": "cb-user",
			"imageId": "ami-rhel9", "image": map[string]any{"osType": "Red Hat Enterprise Linux 9"},
			"specId": "aws+gpu", "securityGroupIds": []string{"sg-aws"}, "connectionConfig": map[string]any{"providerName": "AWS",
				"regionZoneInfo": map[string]any{"assignedRegion": "ap-northeast-2", "assignedZone": "ap-northeast-2a"}}},
		"vm2": {"id": "vm2", "status": "Running", "privateIP": "198.51.100.12", "sshKeyId": "key1", "nodeUserName": "cb-user",
			"specId": "azure+cpu", "securityGroupIds": []string{"sg-azure-a", "sg-azure-b", "sg-azure-a"},
			"connectionConfig": map[string]any{"providerName": "Azure"}},
		"vm3": {"id": "vm3", "status": "Running", "publicIP": "203.0.113.13", "sshKeyId": "key1", "nodeUserName": "cb-user",
			"specId": "gcp+missing", "securityGroupIds": []string{"sg-gcp"}, "connectionConfig": map[string]any{"providerName": "GCP"}},
		"evil": {"id": "evil", "publicIP": "203.0.113.14", "sshKeyId": "key1", "nodeUserName": "cb-user",
			"connectionConfig": map[string]any{"providerName": "../../../../tmp/evil"}},
		"badkey": {"id": "badkey", "publicIP": "203.0.113.15", "sshKeyId": "broken", "nodeUserName": "cb-user",
			"connectionConfig": map[string]any{"providerName": "AWS"}},
	}
	path := strings.TrimPrefix(r.URL.Path, "/tumblebug")
	if path == "/ns/etri/infra/travel/node/gone" {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "node gone does not exist"})
		return
	}
	if path == "/ns/etri/infra/travel/node/down" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "spider is not ready"})
		return
	}
	if m := regexp.MustCompile(`^/ns/etri/infra/travel/node/([^/]+)$`).FindStringSubmatch(path); m != nil {
		f.mu.Lock()
		if f.nodeReads == nil {
			f.nodeReads = map[string]int{}
		}
		f.nodeReads[m[1]]++
		gone, down := f.nodeGone == m[1], f.nodeDown == m[1]
		if node, ok := nodes[m[1]]; ok {
			for k, v := range f.nodeEdits[m[1]] {
				node[k] = v
			}
		}
		f.mu.Unlock()
		if down {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "spider is not ready"})
			return
		}
		if node, ok := nodes[m[1]]; ok && !gone {
			writeJSON(w, http.StatusOK, node)
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "no Node found (ID: " + path + ")"})
		return
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
	if m := regexp.MustCompile(`^/ns/etri/resources/securityGroup/([^/]+)(/rules)?$`).FindStringSubmatch(path); m != nil {
		sgID := m[1]
		rules := []map[string]any{{"Direction": "inbound", "Protocol": "TCP", "Port": "22", "CIDR": "0.0.0.0/0"}}
		if sgID == "sg-azure-b" {
			rules = append(rules, map[string]any{"Direction": "inbound", "Protocol": "TCP", "Port": "8000-9000", "CIDR": "0.0.0.0/0"})
		}
		if m[2] == "" && r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, map[string]any{"id": sgID, "name": sgID, "firewallRules": rules})
			return
		}
		if m[2] != "" && r.Method == http.MethodPost {
			var body tumblebug.SecurityGroupUpdateReq
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
				return
			}
			f.mu.Lock()
			f.rules = append(f.rules, rulesCall{SecurityGroupID: sgID, Body: body})
			fail := f.failRules == sgID
			f.mu.Unlock()
			if fail {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "cannot change " + sgID})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"success": true, "id": sgID, "name": sgID,
				"message": "Successfully added new firewall rules"})
			return
		}
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
	if spec.Progress != nil {
		spec.Progress(semaphore.RunProgress{TaskID: taskID, TaskURL: "http://ui/task", Status: semaphore.StatusWaiting})
		spec.Progress(semaphore.RunProgress{TaskID: taskID, TaskURL: "http://ui/task", Status: semaphore.StatusRunning,
			Output: []string{"PLAY [node] ***", "TASK [Wait until the service answers] ***"}})
	}
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
	output := []string{"TASK [" + spec.Playbook + "]", "ok: [node] " + spec.Host}
	if method, _ := spec.ExtraVars["gpu_check"].(string); method != "" && status == semaphore.StatusSuccess {
		output = append(output, "TASK [Show the line that puts the runtime on the GPU] ***",
			"ok: [node] => {", `    "msg": "aiapp-gpu-check: `+method+`: `+fakeGPUEvidence[method]+`"`, "}")
	}
	return &semaphore.RunResult{TaskID: taskID, Status: status, Output: output}, nil
}

// fakeGPUEvidence is what the GPU check of compose-up prints per method, as
// it appears JSON escaped in the debug task output.
var fakeGPUEvidence = map[string]string{
	"nvidia-smi": "3499, VLLM::EngineCore, 13524 MiB",
	"log":        `time=2026-11-03T08:00:00.000Z level=INFO source=types.go:50 msg=\"inference compute\" id=0 library=CUDA compute=7.5 description=\"Tesla T4\" total=\"15.6 GiB\"`,
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
	prober  *fakeProber
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
			Runtimes: []model.RuntimeCompat{{RuntimeID: model.RuntimeOllama}},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	apps, err := appstore.Open(filepath.Join(t.TempDir(), "apps"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := apps.Register(context.Background(), "web", "echo v1", nil, []appstore.File{
		{Name: "docker-compose.yml", Content: strings.NewReader("services: {}\n")},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := apps.Register(context.Background(), "web", "docker compose up -d", []int{3000, 8080}, []appstore.File{
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
	prober := &fakeProber{results: map[string]error{}}
	svc := NewService(tbClient, runner, models, apps, store, archiveStore, Options{
		StageRoot: stage, SpecNamespace: "system", DryRun: dryRun, MaxParallel: 2, OpenPorts: true, SSHProber: prober,
	})
	t.Cleanup(func() { _ = svc.Shutdown(context.Background()) })
	return &fixture{svc: svc, tb: tb, runner: runner, prober: prober, stage: stage, archive: archiveStore, store: storePath}
}

func target(node string) model.NodeDeployTarget {
	return model.NodeDeployTarget{Kind: model.NodeTargetKindNode, NsID: "etri", InfraID: "travel", NodeID: node}
}

func modelReq(nodes ...string) model.NodeDeploymentReq {
	req := model.NodeDeploymentReq{
		Type:      model.NodeDeployTypeInference,
		Inference: &model.NodeInferenceReq{ModelID: "tiny", ArtifactID: "gguf", Env: map[string]string{"OLLAMA_KEEP_ALIVE": "24h"}},
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
		if d.Status != model.NodeDeployPending && d.Status != model.NodeDeployDeploying {
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

func TestInferenceDeploymentSucceeds(t *testing.T) {
	f := newFixture(t, false)
	created, err := f.svc.Create(context.Background(), modelReq("vm1", "vm2"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Status != model.NodeDeployPending && created.Status != model.NodeDeployDeploying {
		t.Errorf("created status = %q, want pending", created.Status)
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(created.ID) {
		t.Errorf("id = %q, want a version 4 UUID", created.ID)
	}
	if created.Request.Inference.RuntimeID != model.RuntimeOllama {
		t.Errorf("engine = %q, want the artifact's first engine filled in", created.Request.Inference.RuntimeID)
	}

	d := waitFinished(t, f.svc, created.ID)
	if d.Status != model.NodeDeployRunning {
		t.Fatalf("status = %q, nodes = %+v", d.Status, d.Nodes)
	}
	vm1, vm2 := d.Nodes[0], d.Nodes[1]
	if vm1.Node == nil || vm1.Node.CSP != "aws" || vm1.Node.Host != "203.0.113.11" || vm1.Node.Region != "ap-northeast-2" {
		t.Errorf("vm1 node = %+v", vm1.Node)
	}
	if vm2.Node == nil || vm2.Node.Host != "198.51.100.12" || vm2.Node.CSP != "azure" {
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
	if d.Status != model.NodeDeployRunning {
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
	if ports, _ := last.ExtraVars["app_ports"].([]int); !slices.Equal(ports, []int{3000, 8080}) {
		t.Errorf("app_ports = %#v, want the ports of app version 2", last.ExtraVars["app_ports"])
	}
}

func TestAppDeploymentWithoutPortsPassesAnEmptyPortList(t *testing.T) {
	f := newFixture(t, false)
	req := appReq("vm1")
	req.App.Version = 1
	created, err := f.svc.Create(context.Background(), req)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if d := waitFinished(t, f.svc, created.ID); d.Status != model.NodeDeployRunning {
		t.Fatalf("status = %q, nodes = %+v", d.Status, d.Nodes)
	}

	f.runner.mu.Lock()
	defer f.runner.mu.Unlock()
	last := f.runner.runs[len(f.runner.runs)-1]
	ports, ok := last.ExtraVars["app_ports"].([]int)
	if last.Playbook != PlaybookCopyAndRun || !ok || ports == nil || len(ports) != 0 {
		t.Errorf("last run = %s app_ports %#v, want an empty, non-nil list", last.Playbook, last.ExtraVars["app_ports"])
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
	if d.Nodes[1].Status != model.NodeDeployRunning {
		t.Errorf("vm2 = %s", d.Nodes[1].Status)
	}

	// install-docker failing stops the node before compose-up.
	f.runner.statuses["198.51.100.12 "+PlaybookInstallDocker] = semaphore.StatusError
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

func TestGPUCheckRunsForGPUInferenceOnly(t *testing.T) {
	f := newFixture(t, false)
	if _, err := f.svc.models.Register(context.Background(), model.Model{
		ID: "tiny-hf", Name: "Tiny HF", Tasks: []string{"embedding"},
		Artifacts: []model.Artifact{{
			ID: "hf", Source: "huggingface", Ref: "org/tiny", Format: model.FormatHF,
			Runtimes: []model.RuntimeCompat{{RuntimeID: model.RuntimeVLLM}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	vllm := modelReq("vm1")
	vllm.Inference = &model.NodeInferenceReq{ModelID: "tiny-hf", ArtifactID: "hf"}
	noGPU := modelReq("vm1")
	off := false
	noGPU.Inference.GPU = &off

	cases := []struct {
		name     string
		req      model.NodeDeploymentReq
		playbook string
		vars     map[string]any
		evidence string
	}{
		{"vllm on a GPU node", vllm, PlaybookComposeUp,
			map[string]any{"gpu_check": "nvidia-smi"},
			"nvidia-smi: 3499, VLLM::EngineCore, 13524 MiB"},
		{"ollama on a GPU node", modelReq("vm1"), PlaybookComposeUp,
			map[string]any{"gpu_check": "log", "gpu_log_service": "engine",
				"gpu_log_pattern": `msg="inference compute".*library=(CUDA|cuda|ROCm|rocm)`, "gpu_log_evidence": `msg="inference compute"`},
			`log: time=2026-11-03T08:00:00.000Z level=INFO source=types.go:50 msg="inference compute" id=0 library=CUDA compute=7.5 description="Tesla T4" total="15.6 GiB"`},
		{"ollama with gpu false", noGPU, PlaybookComposeUp, nil, ""},
		{"ollama on a CPU node", modelReq("vm2"), PlaybookComposeUp, nil, ""},
		{"app on a GPU node", appReq("vm1"), PlaybookCopyAndRun, nil, ""},
	}
	gpuVars := []string{"gpu_check", "gpu_log_service", "gpu_log_pattern", "gpu_log_evidence"}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			created, err := f.svc.Create(context.Background(), c.req)
			if err != nil {
				t.Fatal(err)
			}
			d := waitFinished(t, f.svc, created.ID)
			if d.Status != model.NodeDeployRunning {
				t.Fatalf("status = %q, nodes = %+v", d.Status, d.Nodes)
			}
			f.runner.mu.Lock()
			last := f.runner.runs[len(f.runner.runs)-1]
			f.runner.mu.Unlock()
			if last.Playbook != c.playbook {
				t.Fatalf("last run = %s, want %s", last.Playbook, c.playbook)
			}
			for _, k := range gpuVars {
				want, wanted := c.vars[k]
				got, present := last.ExtraVars[k]
				if wanted != present || got != want {
					t.Errorf("%s = %v (set %v), want %v (set %v)", k, got, present, want, wanted)
				}
			}
			if d.Nodes[0].GPUCheck != c.evidence {
				t.Errorf("gpuCheck = %q, want %q", d.Nodes[0].GPUCheck, c.evidence)
			}
		})
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
	if d.Status != model.NodeDeployRunning {
		t.Errorf("status = %q; an unreadable spec must not fail the node", d.Status)
	}
}

func TestRequestedGPUOverridesTheSpec(t *testing.T) {
	f := newFixture(t, false)
	req := modelReq("vm1")
	no := false
	req.Inference.GPU = &no
	created, err := f.svc.Create(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	d := waitFinished(t, f.svc, created.ID)
	if d.Nodes[0].GPU || d.Nodes[0].GPUSource != "request" || strings.Contains(d.Nodes[0].Compose, "nvidia") {
		t.Errorf("node = %+v", d.Nodes[0])
	}
}

func TestMissingNodeIsRefusedBeforeAnythingRuns(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		f := newFixture(t, dryRun)
		for _, node := range []string{"vm9", "gone"} {
			_, err := f.svc.Create(context.Background(), appReq("vm1", node))
			if !errors.Is(err, ErrNodeNotFound) || !strings.Contains(err.Error(), "etri/travel/"+node) {
				t.Errorf("dry run %v, %s: err = %v, want ErrNodeNotFound naming the target", dryRun, node, err)
			}
		}
		_, err := f.svc.Create(context.Background(), appReq("vm1", "down"))
		if !errors.Is(err, ErrTumblebug) || errors.Is(err, ErrNodeNotFound) || !strings.Contains(err.Error(), "etri/travel/down") ||
			!strings.Contains(err.Error(), "503") || strings.Contains(err.Error(), "spider") {
			t.Errorf("dry run %v, down: err = %v, want ErrTumblebug naming the target and the status only", dryRun, err)
		}
		if f.runner.count() != 0 || len(f.svc.List(context.Background(), ListFilter{})) != 0 {
			t.Errorf("dry run %v: a refused request left a run or a record", dryRun)
		}
		if entries, _ := os.ReadDir(f.stage); len(entries) != 0 {
			t.Errorf("dry run %v: a refused request staged %d folders", dryRun, len(entries))
		}
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
	dryRun := modelReq("vm1")
	dryRun.RequireDryRun = true
	if _, err := f.svc.Create(context.Background(), dryRun); err == nil || !strings.Contains(err.Error(), "requires the AI-App dry-run gate") {
		t.Errorf("dry-run-only request against a live service = %v", err)
	}
	k8s := modelReq("vm1")
	k8s.Targets[0].Kind = model.NodeTargetKindK8s
	if _, err := f.svc.Create(context.Background(), k8s); !errors.Is(err, model.ErrUnsupportedTarget) {
		t.Errorf("k8s target = %v, want ErrUnsupportedTarget", err)
	}

	cases := map[string]func(*model.NodeDeploymentReq){
		"lower case env name":  func(r *model.NodeDeploymentReq) { r.Inference.Env = map[string]string{"bad": "x"} },
		"env value with \\n":   func(r *model.NodeDeploymentReq) { r.Inference.Env = map[string]string{"A": "x\ny"} },
		"env value with quote": func(r *model.NodeDeploymentReq) { r.Inference.Env = map[string]string{"A": "it's"} },
		"no targets":           func(r *model.NodeDeploymentReq) { r.Targets = nil },
		"duplicate target":     func(r *model.NodeDeploymentReq) { r.Targets = append(r.Targets, r.Targets[0]) },
		"path in node id":      func(r *model.NodeDeploymentReq) { r.Targets[0].NodeID = "../x" },
		"upper case ssh user":  func(r *model.NodeDeploymentReq) { r.Targets[0].SSHUser = "Admin" },
		"ssh user with space":  func(r *model.NodeDeploymentReq) { r.Targets[0].SSHUser = "ec2 user" },
		"ssh user too long":    func(r *model.NodeDeploymentReq) { r.Targets[0].SSHUser = strings.Repeat("u", 33) },
		"ssh user digit first": func(r *model.NodeDeploymentReq) { r.Targets[0].SSHUser = "1user" },
		"no model service":     func(r *model.NodeDeploymentReq) { r.Inference = nil },
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
	unknown.Inference.ModelID = "nope"
	if _, err := f.svc.Create(context.Background(), unknown); !errors.Is(err, modelcat.ErrNotFound) {
		t.Errorf("unknown model = %v", err)
	}
	wrongEngine := modelReq("vm1")
	wrongEngine.Inference.RuntimeID = model.RuntimeVLLM
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

	if f.runner.count() != 0 || len(f.svc.List(context.Background(), ListFilter{})) != 0 {
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

func TestOpenStoreMarksDeployingRecordsInterrupted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.json")
	body := `{"version":2,"deployments":[
		{"id":"a","status":"deploying","createdAt":"2026-10-29T12:00:00Z","nodes":[{"status":"deploying"},{"status":"running"}]},
		{"id":"b","status":"running","createdAt":"2026-10-29T13:00:00Z","nodes":[{"status":"running"}]}]}`
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
		a.Nodes[1].Status != model.NodeDeployRunning || a.FinishedAt == nil {
		t.Errorf("a = %+v", a)
	}
	if b.Status != model.NodeDeployRunning {
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

func TestOpenStoreMigratesAVersion1File(t *testing.T) {
	old, err := os.ReadFile(filepath.Join("testdata", "deployments-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "d.json")
	if err := os.WriteFile(path, old, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}

	want := map[string][]string{
		"0b5d3c1e-1111-4a00-8000-000000000001": {model.NodeDeployRunning, model.NodeDeployRunning},
		"0b5d3c1e-2222-4a00-8000-000000000002": {model.NodeDeployPartial, model.NodeDeployRunning, model.NodeDeployFailed},
		"0b5d3c1e-3333-4a00-8000-000000000003": {model.NodeDeployInterrupted, model.NodeDeployRunning, model.NodeDeployInterrupted},
		"0b5d3c1e-4444-4a00-8000-000000000004": {model.NodeDeployRemoved, model.NodeDeployRemoved},
	}
	check := func(what string, s *Store) {
		t.Helper()
		for id, statuses := range want {
			d, ok := s.Get(id)
			if !ok {
				t.Errorf("%s: %s missing", what, id)
				continue
			}
			got := []string{d.Status}
			for _, n := range d.Nodes {
				got = append(got, n.Status)
			}
			if !slices.Equal(got, statuses) {
				t.Errorf("%s: %s statuses = %v, want %v", what, id, got, statuses)
			}
		}
	}
	check("migrated", store)
	if d, _ := store.Get("0b5d3c1e-3333-4a00-8000-000000000003"); d.FinishedAt == nil || d.Error == "" {
		t.Errorf("interrupted record = %+v, want finishedAt and error set", d)
	}

	var written struct {
		Version     int               `json:"version"`
		Deployments []json.RawMessage `json:"deployments"`
	}
	raw, _ := io.ReadAll(mustOpen(t, path))
	if err := json.Unmarshal(raw, &written); err != nil {
		t.Fatal(err)
	}
	if written.Version != storeVersion || len(written.Deployments) != 4 {
		t.Errorf("written version %d with %d records, want %d with 4", written.Version, len(written.Deployments), storeVersion)
	}
	if strings.Contains(string(raw), `"succeeded"`) {
		t.Error("the written file still holds a succeeded status")
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	check("reopened", reopened)
	if d, _ := reopened.Get("0b5d3c1e-1111-4a00-8000-000000000001"); d.Nodes[0].Steps[1].Status != semaphore.StatusSuccess {
		t.Errorf("step status = %q, want the Semaphore status kept", d.Nodes[0].Steps[1].Status)
	}
}

func TestOpenStoreRefusesANewerVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.json")
	if err := os.WriteFile(path, []byte(`{"version":3,"deployments":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(path); err == nil || !strings.Contains(err.Error(), "version 3") {
		t.Errorf("err = %v, want version 3 refused", err)
	}
}

func TestSSHUserReplacesTheCBTumblebugUser(t *testing.T) {
	f := newFixture(t, false)
	req := appReq("vm1", "vm2")
	req.Targets[0].SSHUser = "ec2-user"
	d := deployed(t, f, req)
	if d.Status != model.NodeDeployRunning {
		t.Fatalf("deploy = %q", d.Status)
	}
	wantUser := map[string]string{"vm1": "ec2-user", "vm2": "cb-user"}
	for _, n := range d.Nodes {
		want := wantUser[n.Target.NodeID]
		if n.SSHUser != want || n.Node == nil || n.Node.User != "cb-user" {
			t.Errorf("%s: sshUser %q node %+v, want %q and node.user cb-user", n.Target.NodeID, n.SSHUser, n.Node, want)
		}
		for _, st := range n.Steps {
			if st.SSHUser != want {
				t.Errorf("%s: step %s sshUser %q, want %q", n.Target.NodeID, st.Playbook, st.SSHUser, want)
			}
		}
	}
	f.runner.mu.Lock()
	for _, run := range f.runner.runs {
		want := "cb-user"
		if strings.Contains(run.Name, "-vm1") {
			want = "ec2-user"
		}
		if run.User != want || run.PrivateKey != testKey {
			t.Errorf("run %s as %q, want %q with the CB-Tumblebug key", run.Name, run.User, want)
		}
	}
	f.runner.mu.Unlock()

	logs, err := f.svc.Logs(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st := logs.Nodes[0].Steps[0]; st.SSHUser != "ec2-user" {
		t.Errorf("logs step = %+v, want sshUser ec2-user", st)
	}
}

func TestSSHUserIsRecordedInADryRun(t *testing.T) {
	f := newFixture(t, true)
	req := modelReq("vm1")
	req.Targets[0].SSHUser = "azureuser"
	d, err := f.svc.Create(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != model.NodeDeployDryRun || d.Nodes[0].SSHUser != "azureuser" || d.Request.Targets[0].SSHUser != "azureuser" {
		t.Errorf("dry run = %q, node sshUser %q, target sshUser %q", d.Status, d.Nodes[0].SSHUser, d.Request.Targets[0].SSHUser)
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
	if d.Nodes[1].Status != model.NodeDeployRunning || d.Status != model.NodeDeployPartial {
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
	if d.Status != model.NodeDeployRunning {
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
			Runtimes: []model.RuntimeCompat{{RuntimeID: model.RuntimeVLLM}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.apps.Register(context.Background(), "api", "docker compose up -d", nil, []appstore.File{
		{Name: "docker-compose.yml", Content: strings.NewReader("services: {}\n")},
	}); err != nil {
		t.Fatal(err)
	}
	vllm := modelReq("vm1")
	vllm.Inference = &model.NodeInferenceReq{ModelID: "tiny-hf", ArtifactID: "hf"}
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
	if _, err := f.svc.apps.Register(context.Background(), "my.web", "true", nil, nil); !errors.Is(err, model.ErrValidation) {
		t.Errorf("upload of an app name with a dot = %v, want a validation error", err)
	}
	req := appReq("vm1")
	req.App.Name = "my.web"
	if _, err := f.svc.Create(context.Background(), req); !errors.Is(err, model.ErrValidation) {
		t.Errorf("app name with a dot = %v, want a validation error", err)
	}
	if f.runner.count() != 0 || len(f.svc.List(context.Background(), ListFilter{})) != 0 {
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
		if d := waitFinished(t, f.svc, id); d.Status != model.NodeDeployRunning {
			t.Errorf("deployment %s = %q", id, d.Status)
		}
	}
	if n := f.runner.count(); n != 4 {
		t.Errorf("%d runs, want 4", n)
	}
}

func TestGPUCheckEvidenceReadsBothCallbackFormats(t *testing.T) {
	line := `time=2026-11-03T09:01:48.524Z level=INFO source=types.go:32 msg="inference compute" id=0 library=CUDA description="Tesla T4"`
	for name, out := range map[string][]string{
		"json":        {"ok: [node] => {", `    "msg": "aiapp-gpu-check: log: ` + strings.ReplaceAll(line, `"`, `\"`) + `"`, "}"},
		"yaml single": {"ok: [node] =>", `  msg: 'aiapp-gpu-check: log: ` + line + `'`},
		"yaml quote":  {"ok: [node] =>", `  msg: 'aiapp-gpu-check: log: it''s ` + line + `'`},
		"yaml plain":  {"ok: [node] =>", `  msg: aiapp-gpu-check: nvidia-smi: 3499, VLLM::EngineCore, 13524 MiB`},
	} {
		got := gpuCheckEvidence([]model.NodeDeployStep{{Playbook: PlaybookComposeUp, Output: out}})
		want := "log: " + line
		switch name {
		case "yaml quote":
			want = "log: it's " + line
		case "yaml plain":
			want = "nvidia-smi: 3499, VLLM::EngineCore, 13524 MiB"
		}
		if got != want {
			t.Errorf("%s: got %q, want %q", name, got, want)
		}
	}
}

// verifiedAt reads the verifiedAt of the fixture model tiny on ollama.
func verifiedAt(t *testing.T, f *fixture) *time.Time {
	t.Helper()
	_, artifact, err := f.svc.models.Artifact(context.Background(), "tiny", "gguf")
	if err != nil {
		t.Fatal(err)
	}
	return artifact.Runtimes[0].VerifiedAt
}

func TestSucceededInferenceMarksTheRuntimeVerified(t *testing.T) {
	f := newFixture(t, false)
	before := time.Now().UTC()
	created, err := f.svc.Create(context.Background(), modelReq("vm2"))
	if err != nil {
		t.Fatal(err)
	}
	if d := waitFinished(t, f.svc, created.ID); d.Status != model.NodeDeployRunning {
		t.Fatalf("status = %q", d.Status)
	}
	got := verifiedAt(t, f)
	if got == nil || got.Before(before) || got.After(time.Now().UTC()) {
		t.Errorf("verifiedAt = %v, want the time the node succeeded", got)
	}
}

func TestFailedNodeAndDryRunDoNotMarkVerified(t *testing.T) {
	f := newFixture(t, false)
	f.runner.statuses["198.51.100.12 "+PlaybookComposeUp] = semaphore.StatusError
	created, err := f.svc.Create(context.Background(), modelReq("vm2"))
	if err != nil {
		t.Fatal(err)
	}
	if d := waitFinished(t, f.svc, created.ID); d.Status != model.NodeDeployFailed {
		t.Fatalf("status = %q, want failed", d.Status)
	}
	if got := verifiedAt(t, f); got != nil {
		t.Errorf("failed node: verifiedAt = %v, want none", got)
	}

	dry := newFixture(t, true)
	d, err := dry.svc.Create(context.Background(), modelReq("vm1", "vm2"))
	if err != nil || d.Status != model.NodeDeployDryRun {
		t.Fatalf("dry run = %v, %v", d, err)
	}
	if got := verifiedAt(t, dry); got != nil {
		t.Errorf("dry run: verifiedAt = %v, want none", got)
	}
}

func TestModelDeletedDuringTheDeploymentLeavesTheNodeSucceeded(t *testing.T) {
	f := newFixture(t, false)
	f.runner.block = make(chan struct{})
	created, err := f.svc.Create(context.Background(), modelReq("vm2"))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for f.runner.count() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if err := f.svc.models.Delete(context.Background(), "tiny"); err != nil {
		t.Fatal(err)
	}
	close(f.runner.block)
	if d := waitFinished(t, f.svc, created.ID); d.Status != model.NodeDeployRunning || d.Nodes[0].Error != "" {
		t.Errorf("deployment = %q %q, want running", d.Status, d.Nodes[0].Error)
	}
	if _, err := f.svc.models.Get(context.Background(), "tiny"); !errors.Is(err, modelcat.ErrNotFound) {
		t.Errorf("deleted model came back: %v", err)
	}
}

// vllmReq deploys a vllm artifact registered as tiny-hf to nodes.
func vllmReq(t *testing.T, f *fixture, nodes ...string) model.NodeDeploymentReq {
	t.Helper()
	if _, err := f.svc.models.Get(context.Background(), "tiny-hf"); err != nil {
		if _, err := f.svc.models.Register(context.Background(), model.Model{
			ID: "tiny-hf", Name: "Tiny HF", Tasks: []string{"embedding"},
			Artifacts: []model.Artifact{{
				ID: "hf", Source: "huggingface", Ref: "org/tiny", Format: model.FormatHF,
				Runtimes: []model.RuntimeCompat{{RuntimeID: model.RuntimeVLLM}},
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	req := modelReq(nodes...)
	req.Inference = &model.NodeInferenceReq{ModelID: "tiny-hf", ArtifactID: "hf"}
	return req
}

func TestVLLMDeploymentGetsAnAPIKeyInItsEnvironment(t *testing.T) {
	f := newFixture(t, false)
	d := deployed(t, f, vllmReq(t, f, "vm1", "vm2"))
	if d.Status != model.NodeDeployRunning {
		t.Fatalf("status = %q, nodes = %+v", d.Status, d.Nodes)
	}
	generated := regexp.MustCompile(`^[0-9a-f]{32}$`)
	vm1, vm2 := d.Nodes[0], d.Nodes[1]
	if !generated.MatchString(vm1.APIKey) || !generated.MatchString(vm2.APIKey) || vm1.APIKey == vm2.APIKey {
		t.Errorf("keys = %q, %q, want a new random one per node", vm1.APIKey, vm2.APIKey)
	}
	if d.Request.Inference.APIKey != "" {
		t.Errorf("request apiKey = %q, want what the caller sent (nothing)", d.Request.Inference.APIKey)
	}
	if !strings.Contains(vm1.Compose, `"VLLM_API_KEY": "`+vm1.APIKey+`"`) {
		t.Errorf("compose lacks the key of the node\n%s", vm1.Compose)
	}
	staged, err := os.ReadFile(filepath.Join(vm1.StageDir, ComposeFile))
	if err != nil || !strings.Contains(string(staged), vm1.APIKey) {
		t.Errorf("staged compose = %v, want the key", err)
	}

	f.runner.mu.Lock()
	runs := append([]semaphore.RunSpec(nil), f.runner.runs...)
	f.runner.mu.Unlock()
	for _, run := range runs {
		vars, _ := json.Marshal(run.ExtraVars)
		if strings.Contains(string(vars), vm1.APIKey) || strings.Contains(string(vars), vm2.APIKey) {
			t.Errorf("%s vars carry the key: %s", run.Playbook, vars)
		}
		if run.Playbook == PlaybookComposeUp && run.ExtraVars["health_url"] != "http://127.0.0.1:8000/health" {
			t.Errorf("health url = %v, want /health, which vLLM leaves open", run.ExtraVars["health_url"])
		}
	}

	list := endpointsOf(f, EndpointFilter{})
	if len(list) != 2 || list[0].APIKey != vm1.APIKey || list[1].APIKey != vm2.APIKey {
		t.Errorf("endpoints = %+v, want the key of each node", list)
	}
	f.tb.edit("vm1", map[string]any{"status": "Suspended"})
	list = endpointsOf(f, EndpointFilter{NodeID: "vm1"})
	if len(list) != 1 || list[0].Available || list[0].APIKey != vm1.APIKey {
		t.Errorf("suspended endpoint = %+v, want the key kept while not available", list)
	}
}

func TestVLLMRedeployKeepsTheKeyOfTheNode(t *testing.T) {
	f := newFixture(t, false)
	first := deployed(t, f, vllmReq(t, f, "vm1"))
	key := first.Nodes[0].APIKey
	if key == "" {
		t.Fatal("first deployment got no key")
	}

	again := deployed(t, f, vllmReq(t, f, "vm1", "vm2"))
	if again.Nodes[0].APIKey != key {
		t.Errorf("redeploy key = %q, want the previous key %q", again.Nodes[0].APIKey, key)
	}
	if again.Nodes[1].APIKey == "" || again.Nodes[1].APIKey == key {
		t.Errorf("vm2 key = %q, want a new one of its own", again.Nodes[1].APIKey)
	}

	given := vllmReq(t, f, "vm1")
	given.Inference.APIKey = "Given.Key_of-Caller~01"
	replaced := deployed(t, f, given)
	if replaced.Nodes[0].APIKey != given.Inference.APIKey || replaced.Request.Inference.APIKey != given.Inference.APIKey {
		t.Errorf("key = %q, request %q, want the one given", replaced.Nodes[0].APIKey, replaced.Request.Inference.APIKey)
	}
	if !strings.Contains(replaced.Nodes[0].Compose, given.Inference.APIKey) {
		t.Error("compose does not carry the given key")
	}

	after := deployed(t, f, vllmReq(t, f, "vm1"))
	if after.Nodes[0].APIKey != given.Inference.APIKey {
		t.Errorf("key after the given one = %q, want it kept", after.Nodes[0].APIKey)
	}

	ollama := deployed(t, f, modelReq("vm1"))
	if ollama.Nodes[0].APIKey != "" || strings.Contains(ollama.Nodes[0].Compose, compose.APIKeyEnv) {
		t.Errorf("ollama node = %q, want no key", ollama.Nodes[0].APIKey)
	}
	if e := endpointsOf(f, EndpointFilter{NodeID: "vm1", Type: model.NodeDeployTypeInference}); len(e) != 2 ||
		e[0].WorkKey != "ms-ollama" || e[0].APIKey != "" || e[1].WorkKey != "ms-vllm" || e[1].APIKey != given.Inference.APIKey {
		t.Errorf("endpoints = %+v", e)
	}
}

func TestAPIKeyRequestsRefused(t *testing.T) {
	f := newFixture(t, false)
	ollama := modelReq("vm1")
	ollama.Inference.APIKey = "0123456789abcdef"
	if _, err := f.svc.Create(context.Background(), ollama); !errors.Is(err, compose.ErrInvalidOption) {
		t.Errorf("ollama with a key = %v, want ErrInvalidOption", err)
	}
	for _, key := range []string{"short", strings.Repeat("k", 129), "0123456789abcdef with space", "0123456789abcdef'"} {
		req := vllmReq(t, f, "vm1")
		req.Inference.APIKey = key
		if _, err := f.svc.Create(context.Background(), req); !errors.Is(err, model.ErrValidation) {
			t.Errorf("key %q = %v, want a validation error", key, err)
		}
	}
	inEnv := vllmReq(t, f, "vm1")
	inEnv.Inference.Env = map[string]string{compose.APIKeyEnv: "0123456789abcdef"}
	if _, err := f.svc.Create(context.Background(), inEnv); !errors.Is(err, compose.ErrInvalidOption) {
		t.Errorf("key in env = %v, want ErrInvalidOption", err)
	}
	if f.runner.count() != 0 || len(f.svc.List(context.Background(), ListFilter{})) != 0 {
		t.Error("a refused request left a run or a record")
	}
}
