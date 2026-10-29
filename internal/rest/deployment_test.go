package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/archive"
	"github.com/ish-hcc/ai-agent-proto/internal/catalog"
	"github.com/ish-hcc/ai-agent-proto/internal/config"
	"github.com/ish-hcc/ai-agent-proto/internal/deploy"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

// sentCommands collects the remote commands the fake CB-Tumblebug received.
type sentCommands struct {
	mu   sync.Mutex
	list []string
}

func (c *sentCommands) add(commands []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.list = append(c.list, commands...)
}

func (c *sentCommands) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.list...)
}

// newHealthServer builds an AI-App server on a fake CB-Tumblebug holding two
// deployments: vllm-lab-01 with an application label and bare-01 without one.
// Every remote command answers 200. commands collects what was sent.
func newHealthServer(t *testing.T, commands *sentCommands) *Server {
	t.Helper()
	MarkReady()

	tb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/ns/default/infra/vllm-lab-01":
			_ = json.NewEncoder(w).Encode(tumblebug.InfraInfo{ID: "vllm-lab-01",
				Label: map[string]string{"ai-mcmp.app-id": "vllm-llama31-8b"}})
		case r.Method == http.MethodGet && r.URL.Path == "/ns/default/infra/bare-01":
			_ = json.NewEncoder(w).Encode(tumblebug.InfraInfo{ID: "bare-01"})
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/ns/default/cmd/infra/"):
			var req tumblebug.InfraCmdReq
			_ = json.NewDecoder(r.Body).Decode(&req)
			commands.add(req.Command)
			_ = json.NewEncoder(w).Encode(tumblebug.CommandResults{Results: []tumblebug.CommandResult{
				{NodeID: "n1", NodeIP: "203.0.113.10", Stdout: map[string]string{"0": "200"}},
			}})
		default:
			http.Error(w, `{"message":"unexpected call"}`, http.StatusInternalServerError)
		}
	}))
	t.Cleanup(tb.Close)

	appArchive, err := archive.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("app archive: %v", err)
	}
	tbCfg := config.TumblebugConfig{BaseURL: tb.URL, Timeout: 5 * time.Second, DryRun: true}
	apps := catalog.NewStore()
	if err := catalog.Seed(context.Background(), apps); err != nil {
		t.Fatalf("seed: %v", err)
	}
	registry := tools.NewRegistry(tbCfg.DryRun)
	deployService := deploy.NewService(tumblebug.NewClient(tbCfg), apps, tbCfg)
	tools.Register(registry, apps, deployService)

	return NewAppServer(&config.Config{PartnerNL: config.PartnerNLConfig{URL: "http://127.0.0.1:1"}}, AppDependencies{
		Catalog: apps,
		Deploy:  deployService,
		Archive: appArchive,
		Tools:   registry,
	})
}

func TestDeploymentHealthFindsAppFromLabel(t *testing.T) {
	sent := &sentCommands{}
	s := newHealthServer(t, sent)

	rec := do(s, http.MethodGet, "/aiapp/ns/default/deployments/vllm-lab-01/health", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var health model.ServingHealth
	if err := json.Unmarshal(rec.Body.Bytes(), &health); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !health.Ready || health.AppID != "vllm-llama31-8b" || len(health.Nodes) != 1 || health.Nodes[0].HTTPCode != 200 {
		t.Errorf("health = %+v", health)
	}
	if commands := sent.all(); len(commands) != 1 || !strings.Contains(commands[0], "http://127.0.0.1:8000/health") {
		t.Errorf("commands = %q, want the vllm serving port and health path", commands)
	}
}

func TestDeploymentHealthTakesAppID(t *testing.T) {
	sent := &sentCommands{}
	s := newHealthServer(t, sent)

	rec := do(s, http.MethodGet, "/aiapp/ns/default/deployments/bare-01/health?appId=ollama-qwen25-7b", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if commands := sent.all(); len(commands) != 1 || !strings.Contains(commands[0], "http://127.0.0.1:11434/") {
		t.Errorf("commands = %q, want the ollama serving port", commands)
	}

	rec = do(s, http.MethodGet, "/aiapp/ns/default/deployments/bare-01/health?appId=no-such-app", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown appId status = %d, want 404", rec.Code)
	}
}

func TestDeploymentHealthWithoutLabelIsBadRequest(t *testing.T) {
	sent := &sentCommands{}
	s := newHealthServer(t, sent)

	rec := do(s, http.MethodGet, "/aiapp/ns/default/deployments/bare-01/health", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "pass appId") {
		t.Errorf("body = %s, want it to ask for appId", rec.Body)
	}
	if commands := sent.all(); len(commands) != 0 {
		t.Errorf("a deployment of unknown application was checked: %q", commands)
	}
}

// newProvisioningServer builds an AI-App server with the dry-run guard off, on a
// fake CB-Tumblebug that reviews and creates a one node Infra. The health
// checks answer healthCodes in order, the last one repeating; every other
// remote command, such as the accelerator probe, answers empty. healthChecks
// counts the health checks that reached the fake.
func newProvisioningServer(t *testing.T, healthCodes []string, healthChecks *int) *Server {
	t.Helper()
	MarkReady()

	var mu sync.Mutex
	tb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/ns/default/infra":
			_, _ = w.Write([]byte(`{"infra":[]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/ns/default/infraDynamicReview":
			_ = json.NewEncoder(w).Encode(tumblebug.ReviewResult{CreationViable: true, OverallStatus: "Ready"})
		case r.Method == http.MethodPost && r.URL.Path == "/ns/default/infraDynamic":
			_ = json.NewEncoder(w).Encode(tumblebug.InfraInfo{ID: "vllm-lab-01", Name: "vllm-lab-01", Status: "Running",
				Node: []tumblebug.NodeInfo{{ID: "n1", Status: "Running", PublicIP: "203.0.113.10"}}})
		case r.Method == http.MethodPost && r.URL.Path == "/ns/default/cmd/infra/vllm-lab-01":
			var req tumblebug.InfraCmdReq
			_ = json.NewDecoder(r.Body).Decode(&req)
			stdout := ""
			if len(req.Command) == 1 && strings.Contains(req.Command[0], "%{http_code}") {
				mu.Lock()
				*healthChecks++
				stdout = healthCodes[min(*healthChecks, len(healthCodes))-1]
				mu.Unlock()
			}
			_ = json.NewEncoder(w).Encode(tumblebug.CommandResults{Results: []tumblebug.CommandResult{
				{NodeID: "n1", NodeIP: "203.0.113.10", Stdout: map[string]string{"0": stdout}},
			}})
		default:
			http.Error(w, `{"message":"unexpected call"}`, http.StatusInternalServerError)
		}
	}))
	t.Cleanup(tb.Close)

	appArchive, err := archive.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("app archive: %v", err)
	}
	tbCfg := config.TumblebugConfig{BaseURL: tb.URL, Timeout: 5 * time.Second, DryRun: false}
	apps := catalog.NewStore()
	if err := catalog.Seed(context.Background(), apps); err != nil {
		t.Fatalf("seed: %v", err)
	}
	registry := tools.NewRegistry(tbCfg.DryRun)
	deployService := deploy.NewService(tumblebug.NewClient(tbCfg), apps, tbCfg,
		deploy.WithHealthWait(5*time.Second, time.Millisecond))
	tools.Register(registry, apps, deployService)

	return NewAppServer(&config.Config{PartnerNL: config.PartnerNLConfig{URL: "http://127.0.0.1:1"}}, AppDependencies{
		Catalog: apps,
		Deploy:  deployService,
		Archive: appArchive,
		Tools:   registry,
	})
}

func postJSON(s *Server, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	return rec
}

func TestPostDeploymentWaitHealthy(t *testing.T) {
	const body = `{"appId":"vllm-llama31-8b","infraName":"vllm-lab-01",` +
		`"specId":"aws+us-west-2+g6.xlarge","imageId":"ubuntu22.04"}`

	for _, tc := range []struct {
		name         string
		path         string
		wantReady    bool
		wantAttempts int
	}{
		{"waitHealthy checks again", "/aiapp/ns/default/deployments?waitHealthy=true", true, 2},
		{"no query checks once", "/aiapp/ns/default/deployments", false, 1},
		{"waitHealthy=false checks once", "/aiapp/ns/default/deployments?waitHealthy=false", false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			healthChecks := 0
			s := newProvisioningServer(t, []string{"000", "200"}, &healthChecks)

			rec := postJSON(s, tc.path, body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
			}
			var result model.DeploymentResult
			if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if result.DryRun || result.Infra == nil {
				t.Fatalf("result = %+v, want a provisioned deployment", result)
			}
			health := result.Health
			if health == nil {
				t.Fatalf("health missing from %s", rec.Body)
			}
			if health.Attempts != tc.wantAttempts || health.Ready != tc.wantReady {
				t.Errorf("health attempts/ready = %d/%v, want %d/%v", health.Attempts, health.Ready, tc.wantAttempts, tc.wantReady)
			}
			if healthChecks != tc.wantAttempts {
				t.Errorf("health checks sent = %d, want %d", healthChecks, tc.wantAttempts)
			}
		})
	}
}

func TestNoRecommenderIsUnprocessable(t *testing.T) {
	s := newHealthServer(t, &sentCommands{})
	const want = "no recommender configured: pin specId and imageId"

	for _, tc := range []struct {
		name string
		rec  *httptest.ResponseRecorder
	}{
		{"plan without spec", postJSON(s, "/aiapp/ns/default/deployments/plan",
			`{"appId":"vllm-llama31-8b","infraName":"vllm-lab-02"}`)},
		{"plan with spec only", postJSON(s, "/aiapp/ns/default/deployments/plan",
			`{"appId":"vllm-llama31-8b","infraName":"vllm-lab-02","specId":"aws+us-east-1+g6.2xlarge"}`)},
		{"deploy without spec", postJSON(s, "/aiapp/ns/default/deployments",
			`{"appId":"vllm-llama31-8b","infraName":"vllm-lab-02"}`)},
	} {
		if tc.rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d, want 422; body %s", tc.name, tc.rec.Code, tc.rec.Body)
			continue
		}
		var msg model.SimpleMsg
		if err := json.Unmarshal(tc.rec.Body.Bytes(), &msg); err != nil || msg.Message != want {
			t.Errorf("%s: body = %s, want message %q", tc.name, tc.rec.Body, want)
		}
	}

	rec := postJSON(s, "/aiapp/ns/default/deployments/plan",
		`{"appId":"vllm-llama31-8b","infraName":"vllm-lab-02","specId":"aws+us-east-1+g6.2xlarge","imageId":"ubuntu22.04"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("pinned plan: status = %d, body %s", rec.Code, rec.Body)
	}
	var plan model.DeploymentPlan
	if err := json.Unmarshal(rec.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.SpecSource != "requested" || plan.ImageSource != "requested" {
		t.Errorf("pinned plan sources = %q, %q", plan.SpecSource, plan.ImageSource)
	}
}
