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

	return NewAppServer(&config.Config{Agent: config.AgentConfig{URL: "http://127.0.0.1:1"}}, AppDependencies{
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
