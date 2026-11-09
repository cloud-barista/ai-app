package rest

import (
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/config"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/semaphore"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

// Fake credentials, made up per run so no literal looks like a secret.
var (
	statusTestPassword      = "s-" + rand.Text()
	statusTumblebugPassword = "t-" + rand.Text()
)

// fakeStatusSemaphore answers the read-only routes Check calls: a project
// named aiapp with the compose-up template only.
func fakeStatusSemaphore(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.URL.Path != "/api/auth/login" {
			t.Errorf("status check sent %s %s, want only reads", r.Method, r.URL.Path)
		}
		switch r.URL.Path {
		case "/api/ping":
			_, _ = w.Write([]byte("pong"))
			return
		case "/api/auth/login":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["password"] != statusTestPassword {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "semaphore", Value: "s1", Path: "/"})
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if c, err := r.Cookie("semaphore"); err != nil || c.Value != "s1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/info":
			_, _ = w.Write([]byte(`{"version":"v2.12.17-5767d93-1741607759"}`))
		case "/api/projects":
			_, _ = w.Write([]byte(`[{"id":7,"name":"other"},{"id":3,"name":"aiapp"}]`))
		case "/api/project/3/templates":
			_, _ = w.Write([]byte(`[{"id":11,"name":"aiapp-compose-up","playbook":"compose-up.yml"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func fakeStatusTumblebug(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tumblebug/readyz" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":"CB-Tumblebug is ready"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// closedURL is the address of a server that is no longer listening.
func closedURL() string {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	return srv.URL
}

func newStatusServer(t *testing.T, cfg *config.Config) *Server {
	t.Helper()
	cfg.Engine = config.EngineConfig{URL: "http://127.0.0.1:1"}
	cfg.PartnerNL = config.PartnerNLConfig{URL: "http://127.0.0.1:1"}
	cfg.Auth = config.AuthConfig{Token: testToken}
	cfg.Tumblebug.Timeout = time.Minute
	return NewAppServer(cfg, AppDependencies{
		Tools:  tools.NewRegistry(true),
		Status: NewStatusChecker(cfg, semaphore.NewClient(cfg.Semaphore), tumblebug.NewClient(cfg.Tumblebug)),
	})
}

func getStatus(t *testing.T, s *Server) (model.ServiceStatus, string) {
	t.Helper()
	rec := do(s, http.MethodGet, "/aiapp/status", "Bearer "+testToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /status = %d: %s", rec.Code, rec.Body.String())
	}
	var status model.ServiceStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return status, rec.Body.String()
}

func TestStatusDependenciesUp(t *testing.T) {
	sem, tb := fakeStatusSemaphore(t), fakeStatusTumblebug(t)
	stage := t.TempDir()
	cfg := &config.Config{
		Tumblebug: config.TumblebugConfig{BaseURL: tb.URL + "/tumblebug", Username: "default", Password: statusTumblebugPassword},
		Semaphore: config.SemaphoreConfig{
			URL: sem.URL, Username: "admin", Password: statusTestPassword, Project: "aiapp",
			UIURL: "http://203.0.113.5:3001",
		},
		Stage:      config.StageConfig{Root: stage},
		NodeDeploy: config.NodeDeployConfig{OpenPorts: true},
	}
	s := newStatusServer(t, cfg)

	if rec := do(s, http.MethodGet, "/aiapp/status", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /status without a token = %d, want 401", rec.Code)
	}

	status, body := getStatus(t, s)
	sm := status.Semaphore
	if !sm.Configured || !sm.Reachable || !sm.Authenticated || sm.LatencyMs == nil ||
		sm.Version != "v2.12.17-5767d93-1741607759" || sm.ProjectID != 3 || sm.UIURL != "http://203.0.113.5:3001" {
		t.Errorf("semaphore = %+v", sm)
	}
	if len(sm.Templates) != 5 {
		t.Fatalf("templates = %+v, want the five playbooks", sm.Templates)
	}
	for _, tpl := range sm.Templates {
		want := tpl.Name == "aiapp-compose-up"
		if tpl.Exists != want || (want && tpl.ID != 11) {
			t.Errorf("template %+v, want exists %v", tpl, want)
		}
	}
	if !strings.Contains(sm.Message, "4 of 5 playbook templates do not exist yet") {
		t.Errorf("semaphore message = %q", sm.Message)
	}
	if !status.Tumblebug.Reachable || status.Tumblebug.Message != "CB-Tumblebug is ready" || status.Tumblebug.LatencyMs == nil {
		t.Errorf("tumblebug = %+v", status.Tumblebug)
	}
	if !status.Stage.Writable || status.Stage.Path != stage {
		t.Errorf("stage = %+v", status.Stage)
	}
	if status.DryRun || !status.OpenPorts {
		t.Errorf("dryRun = %v, openPorts = %v", status.DryRun, status.OpenPorts)
	}
	if entries, _ := os.ReadDir(stage); len(entries) != 0 {
		t.Errorf("stage holds %d entries after the check, want none", len(entries))
	}
	for _, secret := range []string{statusTestPassword, statusTumblebugPassword} {
		if strings.Contains(body, secret) {
			t.Errorf("status body holds a password: %s", body)
		}
	}
}

func TestStatusDependenciesDown(t *testing.T) {
	cfg := &config.Config{
		Tumblebug: config.TumblebugConfig{BaseURL: closedURL() + "/tumblebug", DryRun: true},
		Semaphore: config.SemaphoreConfig{URL: closedURL(), Username: "admin", Password: statusTestPassword, Project: "aiapp"},
		Stage:     config.StageConfig{Root: filepath.Join(t.TempDir(), "missing")},
	}
	s := newStatusServer(t, cfg)

	status, _ := getStatus(t, s)
	sm := status.Semaphore
	if !sm.Configured || sm.Reachable || sm.Authenticated || sm.LatencyMs != nil || sm.Message == "" {
		t.Errorf("semaphore = %+v, want configured and unreachable", sm)
	}
	if len(sm.Templates) != 5 || sm.Templates[0].Exists {
		t.Errorf("templates = %+v, want the five playbooks, none known to exist", sm.Templates)
	}
	if !strings.Contains(sm.Message, "dry run is on") || !strings.Contains(sm.Message, "AIAPP_SEMAPHORE_UI_URL is not set") {
		t.Errorf("semaphore message = %q", sm.Message)
	}
	if status.Tumblebug.Reachable || status.Tumblebug.LatencyMs != nil || status.Tumblebug.Message == "" {
		t.Errorf("tumblebug = %+v, want unreachable", status.Tumblebug)
	}
	if status.Stage.Writable || !strings.Contains(status.Stage.Message, "does not exist") {
		t.Errorf("stage = %+v", status.Stage)
	}
	if !status.DryRun {
		t.Error("dryRun = false, want true")
	}
}

func TestStatusSemaphoreNotConfigured(t *testing.T) {
	tb := fakeStatusTumblebug(t)
	cfg := &config.Config{
		Tumblebug: config.TumblebugConfig{BaseURL: tb.URL + "/tumblebug"},
		Semaphore: config.SemaphoreConfig{Project: "aiapp"},
		Stage:     config.StageConfig{Root: t.TempDir()},
	}
	status, body := getStatus(t, newStatusServer(t, cfg))
	if status.Semaphore.Configured || status.Semaphore.Reachable ||
		!strings.Contains(status.Semaphore.Message, "AIAPP_SEMAPHORE_URL") {
		t.Errorf("semaphore = %+v", status.Semaphore)
	}
	if strings.Contains(body, `"url":""`) {
		t.Errorf("status body = %s, want no empty url", body)
	}
	if !status.Tumblebug.Reachable {
		t.Errorf("tumblebug = %+v, want reachable", status.Tumblebug)
	}
}

func TestStatusTumblebugNotReady(t *testing.T) {
	tb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"message":"CB-Tumblebug is not ready"}`))
	}))
	t.Cleanup(tb.Close)
	cfg := &config.Config{
		Tumblebug: config.TumblebugConfig{BaseURL: tb.URL + "/tumblebug"},
		Semaphore: config.SemaphoreConfig{Project: "aiapp"},
		Stage:     config.StageConfig{Root: t.TempDir()},
	}
	status, _ := getStatus(t, newStatusServer(t, cfg))
	if !status.Tumblebug.Reachable || !strings.Contains(status.Tumblebug.Message, "not ready: tumblebug responded 503") {
		t.Errorf("tumblebug = %+v, want reachable and not ready", status.Tumblebug)
	}
}
