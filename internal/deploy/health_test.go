package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/config"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

// fakeHealthTumblebug answers the CB-Tumblebug calls a real deployment makes
// after provisioning. Health checks are answered from answers in order, the
// last one repeating; the accelerator probe gets an empty answer.
type fakeHealthTumblebug struct {
	mu      sync.Mutex
	answers [][]string
	labels  map[string]string
	// health holds every health check request, in order.
	health []tumblebug.InfraCmdReq
}

func newFakeHealthTumblebug(t *testing.T, answers ...[]string) (*fakeHealthTumblebug, *tumblebug.Client) {
	t.Helper()
	fake := &fakeHealthTumblebug{answers: answers}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/ns/default/infra":
			_, _ = w.Write([]byte(`{"infra":[]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/ns/default/infra/vllm-lab-01":
			fake.mu.Lock()
			labels := fake.labels
			fake.mu.Unlock()
			_ = json.NewEncoder(w).Encode(tumblebug.InfraInfo{ID: "vllm-lab-01", Label: labels})
		case r.Method == http.MethodPost && r.URL.Path == "/ns/default/cmd/infra/vllm-lab-01":
			var req tumblebug.InfraCmdReq
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Command) != 1 {
				http.Error(w, `{"message":"bad command"}`, http.StatusBadRequest)
				return
			}
			if strings.Contains(req.Command[0], sectionMarker) {
				_ = json.NewEncoder(w).Encode(tumblebug.CommandResults{Results: []tumblebug.CommandResult{
					{NodeID: "n1", Stdout: map[string]string{"0": ""}},
				}})
				return
			}
			_ = json.NewEncoder(w).Encode(fake.answer(req))
		default:
			http.Error(w, `{"message":"unexpected call"}`, http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	return fake, tumblebug.NewClient(config.TumblebugConfig{BaseURL: srv.URL, Timeout: 5 * time.Second})
}

func (f *fakeHealthTumblebug) answer(req tumblebug.InfraCmdReq) tumblebug.CommandResults {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.health = append(f.health, req)
	stdouts := f.answers[min(len(f.health), len(f.answers))-1]

	results := make([]tumblebug.CommandResult, 0, len(stdouts))
	for i, stdout := range stdouts {
		id := string(rune('1' + i))
		results = append(results, tumblebug.CommandResult{
			NodeID: "n" + id, NodeIP: "203.0.113.1" + id, Stdout: map[string]string{"0": stdout},
		})
	}
	return tumblebug.CommandResults{Results: results}
}

func (f *fakeHealthTumblebug) setLabels(labels map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.labels = labels
}

func (f *fakeHealthTumblebug) healthCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.health)
}

// fakeCreator reviews every plan as viable and creates a one node Infra with
// no security group, so opening the serving port makes no call.
type fakeCreator struct{}

func (fakeCreator) Review(ctx context.Context, plan *model.DeploymentPlan) (*model.ReviewSummary, error) {
	return &model.ReviewSummary{CreationViable: true, OverallStatus: "Ready"}, nil
}

func (fakeCreator) Create(ctx context.Context, nsID string, plan *model.DeploymentPlan) (*model.ProvisionResult, error) {
	infra := &tumblebug.InfraInfo{ID: plan.InfraName, Name: plan.InfraName, Status: "Running",
		Node: []tumblebug.NodeInfo{{ID: "n1", Status: "Running", PublicIP: "203.0.113.11"}}}
	return &model.ProvisionResult{InfraID: infra.ID, Infra: infra}, nil
}

func (fakeCreator) Update(ctx context.Context, nsID, infraID string, app *model.AppSpec) (*model.ProvisionResult, error) {
	return nil, errors.New("not used")
}

func vllmDeployReq() *model.DeployAppReq {
	return &model.DeployAppReq{
		AppID: "vllm-llama31-8b", InfraName: "vllm-lab-01",
		SpecID: "aws+us-west-2+g6.xlarge", ImageID: "ubuntu22.04",
	}
}

func TestHealthCommandCarriesPortAndPath(t *testing.T) {
	store := seededCatalog(t)
	ctx := context.Background()

	for _, tc := range []struct {
		name       string
		port       int
		healthPath string
		wantURL    string
	}{
		{"seeded health path", 8000, "/health", " http://127.0.0.1:8000/health"},
		{"empty health path", 11434, "", " http://127.0.0.1:11434/"},
		{"path needing quotes", 8080, "/ready?x=1&y=2", ` 'http://127.0.0.1:8080/ready?x=1&y=2'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake, client := newFakeHealthTumblebug(t, []string{"200"})
			service := NewService(client, store, config.TumblebugConfig{DryRun: true})
			app, err := store.Get(ctx, "vllm-llama31-8b")
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			changed := *app
			changed.Serving.Port = tc.port
			changed.Serving.HealthPath = tc.healthPath

			if _, err := service.CheckServingHealth(ctx, "default", "vllm-lab-01", &changed); err != nil {
				t.Fatalf("CheckServingHealth: %v", err)
			}
			if fake.healthCalls() != 1 {
				t.Fatalf("health checks = %d, want 1", fake.healthCalls())
			}
			req := fake.health[0]
			command := req.Command[0]
			for _, want := range []string{
				"curl -s -o /dev/null -w '%{http_code}' --max-time 5" + tc.wantURL,
				"command -v curl",
			} {
				if !strings.Contains(command, want) {
					t.Errorf("command %q does not contain %q", command, want)
				}
			}
			if strings.Contains(command, "wget") {
				t.Errorf("command %q falls back to another client", command)
			}
			if req.UserName != "cb-user" || req.TimeoutMinutes != healthTimeoutMinutes {
				t.Errorf("request user/timeout = %q/%d", req.UserName, req.TimeoutMinutes)
			}
		})
	}
}

func TestCheckServingHealthReadsStatus(t *testing.T) {
	store := seededCatalog(t)
	ctx := context.Background()
	app, err := store.Get(ctx, "vllm-llama31-8b")
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	for _, tc := range []struct {
		name      string
		stdouts   []string
		wantReady bool
		wantCodes []int
		wantError []string
	}{
		{"200 is ready", []string{"200"}, true, []int{200}, []string{""}},
		{"redirect is ready", []string{"302\n"}, true, []int{302}, []string{""}},
		{"nothing listening", []string{"000"}, false, []int{0}, []string{"nothing answered on 127.0.0.1:8000"}},
		{"still loading", []string{"503"}, false, []int{503}, []string{""}},
		{"no curl", []string{healthNoCurl + "\n"}, false, []int{0}, []string{"curl not found on node"}},
		{"one node of two", []string{"200", "503"}, false, []int{200, 503}, []string{"", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, client := newFakeHealthTumblebug(t, tc.stdouts)
			service := NewService(client, store, config.TumblebugConfig{DryRun: true})

			health, err := service.CheckServingHealth(ctx, "default", "vllm-lab-01", app)
			if err != nil {
				t.Fatalf("CheckServingHealth: %v", err)
			}
			if health.Ready != tc.wantReady || health.Attempts != 1 || health.Port != 8000 {
				t.Errorf("health = %+v", health)
			}
			if len(health.Nodes) != len(tc.wantCodes) {
				t.Fatalf("nodes = %+v", health.Nodes)
			}
			for i, node := range health.Nodes {
				if node.HTTPCode != tc.wantCodes[i] || node.Error != tc.wantError[i] {
					t.Errorf("node %d = %+v, want code %d error %q", i, node, tc.wantCodes[i], tc.wantError[i])
				}
				if node.Ready != (node.HTTPCode >= 200 && node.HTTPCode < 400) {
					t.Errorf("node %d ready = %v for code %d", i, node.Ready, node.HTTPCode)
				}
				if node.NodeID == "" || node.PublicIP == "" {
					t.Errorf("node %d lost its identity: %+v", i, node)
				}
			}
		})
	}
}

func TestDeployAndWaitRetriesUntilReady(t *testing.T) {
	fake, client := newFakeHealthTumblebug(t, []string{"000"}, []string{"200"})
	service := NewService(client, seededCatalog(t), config.TumblebugConfig{DryRun: false},
		WithProvisioner(fakeCreator{}), WithHealthWait(5*time.Second, time.Millisecond))

	result, err := service.DeployAndWait(context.Background(), "default", vllmDeployReq())
	if err != nil {
		t.Fatalf("DeployAndWait: %v", err)
	}
	if result.Health == nil || !result.Health.Ready || result.Health.Attempts != 2 {
		t.Fatalf("health = %+v, want ready on the second attempt", result.Health)
	}
	if fake.healthCalls() != 2 {
		t.Errorf("health checks = %d, want 2", fake.healthCalls())
	}
}

func TestDeployAndWaitGivesUpWithoutFailing(t *testing.T) {
	fake, client := newFakeHealthTumblebug(t, []string{"503"})
	service := NewService(client, seededCatalog(t), config.TumblebugConfig{DryRun: false},
		WithProvisioner(fakeCreator{}), WithHealthWait(30*time.Millisecond, 5*time.Millisecond))

	result, err := service.DeployAndWait(context.Background(), "default", vllmDeployReq())
	if err != nil {
		t.Fatalf("DeployAndWait failed the deployment: %v", err)
	}
	if result.Infra == nil || result.DryRun {
		t.Errorf("result = %+v, want the created infra", result)
	}
	health := result.Health
	if health == nil || health.Ready {
		t.Fatalf("health = %+v, want not ready", health)
	}
	if health.Attempts < 2 || health.Attempts != fake.healthCalls() {
		t.Errorf("attempts = %d, health checks = %d", health.Attempts, fake.healthCalls())
	}
	if len(health.Nodes) != 1 || health.Nodes[0].HTTPCode != 503 {
		t.Errorf("nodes = %+v, want the last answer kept", health.Nodes)
	}
	if !strings.Contains(health.Message, "not ready after") {
		t.Errorf("message = %q", health.Message)
	}
}

func TestDeployChecksHealthOnce(t *testing.T) {
	fake, client := newFakeHealthTumblebug(t, []string{"000"}, []string{"200"})
	service := NewService(client, seededCatalog(t), config.TumblebugConfig{DryRun: false},
		WithProvisioner(fakeCreator{}), WithHealthWait(5*time.Second, time.Millisecond))

	result, err := service.Deploy(context.Background(), "default", vllmDeployReq())
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if result.Health == nil || result.Health.Ready || result.Health.Attempts != 1 {
		t.Errorf("health = %+v, want one not ready check", result.Health)
	}
	if fake.healthCalls() != 1 {
		t.Errorf("health checks = %d, want 1", fake.healthCalls())
	}
}

func TestDeployDryRunLeavesHealthOut(t *testing.T) {
	fake, client := newFakeHealthTumblebug(t, []string{"200"})
	service := NewService(client, seededCatalog(t), config.TumblebugConfig{DryRun: true},
		WithProvisioner(fakeCreator{}))

	for name, run := range map[string]func(context.Context, string, *model.DeployAppReq) (*model.DeploymentResult, error){
		"Deploy": service.Deploy, "DeployAndWait": service.DeployAndWait,
	} {
		result, err := run(context.Background(), "default", vllmDeployReq())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !result.DryRun || result.Health != nil {
			t.Errorf("%s: dryRun %v health %+v, want no health on a dry run", name, result.DryRun, result.Health)
		}
	}
	if fake.healthCalls() != 0 {
		t.Errorf("dry run ran %d health checks", fake.healthCalls())
	}
}

func TestDeployedAppReadsTheLabel(t *testing.T) {
	fake, client := newFakeHealthTumblebug(t, []string{"200"})
	service := NewService(client, seededCatalog(t), config.TumblebugConfig{DryRun: true})
	ctx := context.Background()

	fake.setLabels(map[string]string{labelAppID: "vllm-llama31-8b"})
	app, err := service.DeployedApp(ctx, "default", "vllm-lab-01")
	if err != nil || app.ID != "vllm-llama31-8b" {
		t.Fatalf("DeployedApp = %v, %v", app, err)
	}

	fake.setLabels(nil)
	if _, err := service.DeployedApp(ctx, "default", "vllm-lab-01"); !errors.Is(err, ErrNoAppLabel) {
		t.Errorf("DeployedApp without a label = %v, want ErrNoAppLabel", err)
	}
}

func TestWithHealthWaitDefaults(t *testing.T) {
	service := NewService(nil, seededCatalog(t), config.TumblebugConfig{})
	if service.healthTimeout != 15*time.Minute || service.healthInterval != 20*time.Second {
		t.Errorf("defaults = %s/%s", service.healthTimeout, service.healthInterval)
	}
	service = NewService(nil, seededCatalog(t), config.TumblebugConfig{}, WithHealthWait(0, 0))
	if service.healthTimeout != 15*time.Minute || service.healthInterval != 20*time.Second {
		t.Errorf("zero values replaced the defaults: %s/%s", service.healthTimeout, service.healthInterval)
	}
}
