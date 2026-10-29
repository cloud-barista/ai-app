package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/appstore"
	"github.com/ish-hcc/ai-agent-proto/internal/config"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/modelcat"
	"github.com/ish-hcc/ai-agent-proto/internal/nodedeploy"
	"github.com/ish-hcc/ai-agent-proto/internal/semaphore"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

// nodeTestKey is fake. Its PEM label is split so that secret scanners do not
// take it for a real key.
const nodeTestKey = "-----BEGIN OPENSSH " + "PRIVATE KEY-----\ncmVzdC10ZXN0LWtleS1ib2R5\n-----END OPENSSH " + "PRIVATE KEY-----\n"

// countingRunner stands in for Semaphore and succeeds every run.
type countingRunner struct {
	mu   sync.Mutex
	runs int
}

func (r *countingRunner) Run(ctx context.Context, spec semaphore.RunSpec) (*semaphore.RunResult, error) {
	r.mu.Lock()
	r.runs++
	id := r.runs
	r.mu.Unlock()
	return &semaphore.RunResult{TaskID: id, Status: semaphore.StatusSuccess, Output: []string{"ran " + spec.Playbook}}, nil
}

func (r *countingRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runs
}

func newNodeDeployServer(t *testing.T, dryRun bool) (*Server, *countingRunner, *[]string) {
	t.Helper()
	var mu sync.Mutex
	controls := []string{}
	tb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/tumblebug/ns/etri/infra/travel/node/vm1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "vm1", "publicIP": "203.0.113.21", "sshKeyId": "k", "nodeUserName": "cb-user",
				"connectionConfig": map[string]any{"providerName": "AWS"},
			})
		case "/tumblebug/ns/etri/resources/sshKey/k":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "k", "privateKey": nodeTestKey})
		case "/tumblebug/ns/etri/control/infra/travel/node/vm1":
			mu.Lock()
			controls = append(controls, r.URL.Query().Get("action"))
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "ok"})
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "no such resource"})
		}
	}))
	t.Cleanup(tb.Close)

	models, _, err := modelcat.Open("")
	if err != nil {
		t.Fatal(err)
	}
	if err := modelcat.Seed(context.Background(), models); err != nil {
		t.Fatal(err)
	}
	apps, err := appstore.Open(filepath.Join(t.TempDir(), "apps"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := apps.Register(context.Background(), "web", "docker compose up -d",
		[]appstore.File{{Name: "docker-compose.yml", Content: strings.NewReader("services: {}\n")}}); err != nil {
		t.Fatal(err)
	}
	store, err := nodedeploy.OpenStore("")
	if err != nil {
		t.Fatal(err)
	}
	runner := &countingRunner{}
	svc := nodedeploy.NewService(
		tumblebug.NewClient(config.TumblebugConfig{BaseURL: tb.URL + "/tumblebug", Timeout: 5 * time.Second}),
		runner, models, apps, store, nil,
		nodedeploy.Options{StageRoot: filepath.Join(t.TempDir(), "stage"), SpecNamespace: "system", DryRun: dryRun},
	)
	t.Cleanup(func() { _ = svc.Shutdown(context.Background()) })

	registry := tools.NewRegistry(dryRun)
	tools.RegisterNodeDeploy(registry, svc)
	cfg := &config.Config{
		Engine:    config.EngineConfig{URL: "http://127.0.0.1:1"},
		PartnerNL: config.PartnerNLConfig{URL: "http://127.0.0.1:1"},
	}
	s := NewAppServer(cfg, AppDependencies{Models: models, Apps: apps, Tools: registry, NodeDeploy: svc})
	return s, runner, &controls
}

func sendNodeJSON(s *Server, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	return rec
}

const nodeAppBody = `{"type":"app","targets":[{"kind":"node","nsId":"etri","infraId":"travel","nodeId":"vm1"}],` +
	`"app":{"name":"web","env":{"MODEL_URL":"http://10.0.0.5:8000/v1"}}}`

func TestNodeDeploymentRoutes(t *testing.T) {
	s, runner, controls := newNodeDeployServer(t, false)

	rec := sendNodeJSON(s, http.MethodPost, "/aiapp/deployments", nodeAppBody)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /deployments = %d: %s", rec.Code, rec.Body.String())
	}
	var created model.NodeDeployment
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("created = %s (%v)", rec.Body.String(), err)
	}

	var got model.NodeDeployment
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rec = sendNodeJSON(s, http.MethodGet, "/aiapp/deployments/"+created.ID, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET = %d: %s", rec.Code, rec.Body.String())
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		if got.Status == model.NodeDeploySucceeded {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got.Status != model.NodeDeploySucceeded || runner.count() != 2 {
		t.Fatalf("status = %q after %d runs: %s", got.Status, runner.count(), rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "cmVzdC10ZXN0") || strings.Contains(rec.Body.String(), `"output"`) {
		t.Errorf("GET returns the key or task output: %s", rec.Body.String())
	}

	rec = sendNodeJSON(s, http.MethodGet, "/aiapp/deployments/"+created.ID+"/logs", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ran copy-and-run.yml") {
		t.Errorf("GET logs = %d: %s", rec.Code, rec.Body.String())
	}
	rec = sendNodeJSON(s, http.MethodGet, "/aiapp/deployments", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), created.ID) {
		t.Errorf("GET list = %d: %s", rec.Code, rec.Body.String())
	}

	rec = sendNodeJSON(s, http.MethodPost, "/aiapp/deployments/"+created.ID+"/control", `{"action":"reboot"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"done"`) || strings.Join(*controls, ",") != "reboot" {
		t.Errorf("control = %d: %s, calls %v", rec.Code, rec.Body.String(), *controls)
	}
	if rec := sendNodeJSON(s, http.MethodPost, "/aiapp/deployments/"+created.ID+"/control", `{"action":"melt"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown action = %d, want 400", rec.Code)
	}

	for path, want := range map[string]int{
		"/aiapp/deployments/missing":      http.StatusNotFound,
		"/aiapp/deployments/missing/logs": http.StatusNotFound,
	} {
		if rec := sendNodeJSON(s, http.MethodGet, path, ""); rec.Code != want {
			t.Errorf("GET %s = %d, want %d", path, rec.Code, want)
		}
	}
	if rec := sendNodeJSON(s, http.MethodPost, "/aiapp/deployments/missing/control", `{"action":"reboot"}`); rec.Code != http.StatusNotFound {
		t.Errorf("control missing = %d, want 404", rec.Code)
	}
}

func TestNodeDeploymentRefusals(t *testing.T) {
	s, runner, _ := newNodeDeployServer(t, false)
	target := `"targets":[{"kind":"node","nsId":"etri","infraId":"travel","nodeId":"vm1"}]`
	for name, tc := range map[string]struct {
		body string
		want int
	}{
		"k8s target": {`{"type":"app","targets":[{"kind":"k8s","nsId":"etri","k8sClusterId":"c1"}],"app":{"name":"web"}}`,
			http.StatusUnprocessableEntity},
		"bad env name":    {`{"type":"app",` + target + `,"app":{"name":"web","env":{"lower":"x"}}}`, http.StatusBadRequest},
		"newline in env":  {`{"type":"app",` + target + `,"app":{"name":"web","env":{"A":"x\ny"}}}`, http.StatusBadRequest},
		"malformed":       {`{"type":`, http.StatusBadRequest},
		"unknown app":     {`{"type":"app",` + target + `,"app":{"name":"nope"}}`, http.StatusNotFound},
		"unknown model":   {`{"type":"model-service",` + target + `,"modelService":{"modelId":"nope","artifactId":"x"}}`, http.StatusNotFound},
		"engine mismatch": {`{"type":"model-service",` + target + `,"modelService":{"modelId":"qwen3-vl-8b","artifactId":"ollama-gguf","engine":"vllm"}}`, http.StatusUnprocessableEntity},
	} {
		rec := sendNodeJSON(s, http.MethodPost, "/aiapp/deployments", tc.body)
		if rec.Code != tc.want {
			t.Errorf("%s = %d, want %d: %s", name, rec.Code, tc.want, rec.Body.String())
		}
	}
	if runner.count() != 0 {
		t.Errorf("%d runs for refused requests", runner.count())
	}
}

func TestNodeDeploymentDryRunAnswersAtOnce(t *testing.T) {
	s, runner, controls := newNodeDeployServer(t, true)
	rec := sendNodeJSON(s, http.MethodPost, "/aiapp/deployments", nodeAppBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST dry run = %d: %s", rec.Code, rec.Body.String())
	}
	var d model.NodeDeployment
	_ = json.Unmarshal(rec.Body.Bytes(), &d)
	if d.Status != model.NodeDeployDryRun || d.Nodes[0].Node == nil || d.Nodes[0].Node.Host != "203.0.113.21" || runner.count() != 0 {
		t.Errorf("dry run = %s, runs %d", rec.Body.String(), runner.count())
	}
	rec = sendNodeJSON(s, http.MethodPost, "/aiapp/deployments/"+d.ID+"/control", `{"action":"terminate"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"blocked"`) || len(*controls) != 0 {
		t.Errorf("control in dry run = %d: %s, calls %v", rec.Code, rec.Body.String(), *controls)
	}
}
