package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/catalogsync"
	"github.com/ish-hcc/ai-agent-proto/internal/config"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/modelcat"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
)

// gatedSource returns one model once gate is closed. It never dials out.
type gatedSource struct{ gate chan struct{} }

func (gatedSource) Name() string { return model.OriginHuggingFace }

func (g gatedSource) Fetch(ctx context.Context, opt catalogsync.Options) ([]model.Model, error) {
	<-g.gate
	return []model.Model{{ID: "hf-synced", Name: "Synced", Artifacts: []model.Artifact{{
		ID: "hf", Source: "huggingface", Ref: "org/synced", Format: "hf"}}}}, nil
}

func newSyncServer(t *testing.T, runner *catalogsync.Runner, store *modelcat.Store) *Server {
	t.Helper()
	cfg := &config.Config{
		Engine:    config.EngineConfig{URL: "http://127.0.0.1:1"},
		PartnerNL: config.PartnerNLConfig{URL: "http://127.0.0.1:1"},
	}
	registry := tools.NewRegistry(true)
	tools.RegisterModels(registry, store)
	return NewAppServer(cfg, AppDependencies{Models: store, Tools: registry, Sync: runner})
}

func syncStatus(t *testing.T, s *Server) catalogsync.Status {
	t.Helper()
	rec := do(s, http.MethodGet, "/aiapp/models/sync", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /models/sync = %d: %s", rec.Code, rec.Body.String())
	}
	var st catalogsync.Status
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("GET /models/sync: %v", err)
	}
	return st
}

func TestModelSyncRoutes(t *testing.T) {
	store, _, _ := modelcat.Open("")
	if err := modelcat.Seed(context.Background(), store); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	gate := make(chan struct{})
	runner := catalogsync.NewRunner(store, []catalogsync.Source{gatedSource{gate: gate}},
		catalogsync.Options{Limit: 1}, time.Hour)
	runner.SetChecker(func(ctx context.Context, src catalogsync.Source, opt catalogsync.Options) error { return nil })
	s := newSyncServer(t, runner, store)

	st := syncStatus(t, s)
	if !st.Enabled || st.Running || st.Interval != "1h0m0s" || len(st.Sources) != 1 || st.Sources[0].Name != model.OriginHuggingFace {
		t.Errorf("status before a run = %+v", st)
	}

	rec := do(s, http.MethodPost, "/aiapp/models/sync", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /models/sync = %d: %s", rec.Code, rec.Body.String())
	}
	var started catalogsync.Status
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil || !started.Running {
		t.Errorf("POST body = %s (%v), want running", rec.Body.String(), err)
	}
	if rec := do(s, http.MethodPost, "/aiapp/models/sync", ""); rec.Code != http.StatusConflict {
		t.Errorf("POST while running = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	close(gate)
	for i := 0; i < 200 && syncStatus(t, s).Running; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	st = syncStatus(t, s)
	if st.Running || st.Sources[0].Counts.Added != 1 || st.Sources[0].FinishedAt == nil {
		t.Errorf("status after the run = %+v", st)
	}

	// sync is a route, not a model ID, and the synced model is listed.
	if got := modelIDs(t, s, "/aiapp/models?origin=huggingface"); len(got) != 1 || got[0] != "hf-synced" {
		t.Errorf("origin=huggingface = %v, want hf-synced", got)
	}
	if rec := do(s, http.MethodGet, "/aiapp/models/qwen3-vl-8b", ""); rec.Code != http.StatusOK {
		t.Errorf("GET /models/qwen3-vl-8b = %d", rec.Code)
	}
}

func TestModelSyncDisabled(t *testing.T) {
	store, _, _ := modelcat.Open("")
	s := newSyncServer(t, nil, store)

	if st := syncStatus(t, s); st.Enabled || st.Running || st.Sources == nil || len(st.Sources) != 0 {
		t.Errorf("disabled status = %+v, want enabled false and no sources", st)
	}
	if rec := do(s, http.MethodPost, "/aiapp/models/sync", ""); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("POST disabled = %d, want 503: %s", rec.Code, rec.Body.String())
	}
}
