package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/config"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/modelcat"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
)

func newModelServer(t *testing.T, storeFile string) *Server {
	t.Helper()
	store, loaded, err := modelcat.Open(storeFile)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !loaded {
		if err := modelcat.Seed(context.Background(), store); err != nil {
			t.Fatalf("Seed: %v", err)
		}
	}
	registry := tools.NewRegistry(true)
	tools.RegisterModels(registry, store)
	cfg := &config.Config{
		Engine:    config.EngineConfig{URL: "http://127.0.0.1:1"},
		PartnerNL: config.PartnerNLConfig{URL: "http://127.0.0.1:1"},
	}
	return NewAppServer(cfg, AppDependencies{Models: store, Tools: registry})
}

func modelIDs(t *testing.T, s *Server, path string) []string {
	t.Helper()
	rec := do(s, http.MethodGet, path, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", path, rec.Code, rec.Body.String())
	}
	var resp model.ModelListResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	out := make([]string, 0, len(resp.Models))
	for _, m := range resp.Models {
		out = append(out, m.ID)
	}
	return out
}

func TestModelRoutes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	s := newModelServer(t, path)

	if got := modelIDs(t, s, "/aiapp/models"); len(got) != 3 {
		t.Errorf("models = %v, want 3", got)
	}
	if got := modelIDs(t, s, "/aiapp/models?modality=image"); strings.Join(got, ",") != "ministral-3-8b,qwen3-vl-8b" {
		t.Errorf("modality=image = %v", got)
	}
	if got := modelIDs(t, s, "/aiapp/models?format=gguf"); strings.Join(got, ",") != "qwen3-vl-8b" {
		t.Errorf("format=gguf = %v", got)
	}

	const added = `{"id":"added","name":"Added","artifacts":[{"id":"a","source":"huggingface",` +
		`"ref":"org/added","format":"hf","runtimes":[{"runtimeId":"vllm"}]}]}`
	if rec := postJSON(s, "/aiapp/models", added); rec.Code != http.StatusOK {
		t.Fatalf("POST /models = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := postJSON(s, "/aiapp/models", `{"id":"bad","name":"Bad"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("POST invalid model = %d, want 400", rec.Code)
	}

	// A new server on the same file sees the added model and is not reseeded.
	s = newModelServer(t, path)
	if rec := do(s, http.MethodGet, "/aiapp/models/added", ""); rec.Code != http.StatusOK {
		t.Errorf("GET added model after reopening = %d", rec.Code)
	}
	if rec := do(s, http.MethodDelete, "/aiapp/models/added", ""); rec.Code != http.StatusOK {
		t.Errorf("DELETE = %d", rec.Code)
	}
	if rec := do(s, http.MethodGet, "/aiapp/models/added", ""); rec.Code != http.StatusNotFound {
		t.Errorf("GET deleted model = %d, want 404", rec.Code)
	}
	if rec := do(s, http.MethodDelete, "/aiapp/models/added", ""); rec.Code != http.StatusNotFound {
		t.Errorf("DELETE again = %d, want 404", rec.Code)
	}
}

func TestModelOriginAndVerifiedFilters(t *testing.T) {
	s := newModelServer(t, "")

	if got := modelIDs(t, s, "/aiapp/models?origin=seed"); len(got) != 3 {
		t.Errorf("origin=seed = %v, want the 3 seed models", got)
	}
	if got := modelIDs(t, s, "/aiapp/models?origin=huggingface"); len(got) != 0 {
		t.Errorf("origin=huggingface = %v, want none", got)
	}
	if got := modelIDs(t, s, "/aiapp/models?verified=false"); len(got) != 3 {
		t.Errorf("verified=false = %v, want the 3 seed models", got)
	}
	if got := modelIDs(t, s, "/aiapp/models?verified=true"); len(got) != 0 {
		t.Errorf("verified=true = %v, want none", got)
	}

	// The body claims a collected, stale, synced, verified entry; POST takes it
	// over as manual, not stale and not synced, and keeps verifiedAt as given.
	const posted = `{"id":"posted","name":"Posted","origin":"huggingface","stale":true,"syncedAt":"2026-11-04T02:00:00Z","artifacts":[` +
		`{"id":"a","source":"huggingface","ref":"org/posted","format":"hf",` +
		`"runtimes":[{"runtimeId":"vllm","verifiedAt":"2026-11-04T03:00:00Z"}]}]}`
	rec := postJSON(s, "/aiapp/models", posted)
	var registered model.Model
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &registered) != nil {
		t.Fatalf("POST /models = %d: %s", rec.Code, rec.Body.String())
	}
	if registered.Origin != model.OriginManual || registered.Stale || registered.SyncedAt != nil {
		t.Errorf("POST /models = origin %q, stale %v, syncedAt %v, want manual, not stale, no syncedAt",
			registered.Origin, registered.Stale, registered.SyncedAt)
	}
	if at := registered.Artifacts[0].Runtimes[0].VerifiedAt; at == nil || !at.Equal(time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC)) {
		t.Errorf("POST /models verifiedAt = %v, want 2026-11-04T03:00:00Z as given", at)
	}
	if got := modelIDs(t, s, "/aiapp/models?origin=MANUAL&verified=true"); strings.Join(got, ",") != "posted" {
		t.Errorf("origin=MANUAL&verified=true = %v, want [posted]", got)
	}
	if got := modelIDs(t, s, "/aiapp/models?verified=false"); len(got) != 3 {
		t.Errorf("verified=false = %v, want the 3 seed models", got)
	}

	for _, path := range []string{"/aiapp/models?origin=github", "/aiapp/models?verified=maybe"} {
		if rec := do(s, http.MethodGet, path, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400: %s", path, rec.Code, rec.Body.String())
		}
	}
	if rec := postJSON(s, "/aiapp/models", strings.Replace(posted, `"origin":"huggingface"`, `"origin":"github"`, 1)); rec.Code != http.StatusOK {
		t.Errorf("POST with an unknown origin in the body = %d, want 200 as manual: %s", rec.Code, rec.Body.String())
	}
}

// An artifact without runtimes is listed but cannot be deployed: the compose
// preview and the deployment are refused with 422 whether or not runtimeId is
// given.
func TestArtifactWithoutRuntimes(t *testing.T) {
	s, runner, _ := newNodeDeployServer(t, false)
	const listed = `{"id":"listed","name":"Listed","artifacts":[{"id":"a","source":"ngc","ref":"nvidia/listed","format":"hf"}]}`
	rec := postJSON(s, "/aiapp/models", listed)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /models = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"runtimes":[]`) {
		t.Errorf("POST /models body lists runtimes other than []: %s", rec.Body.String())
	}
	if got := modelIDs(t, s, "/aiapp/models?source=ngc"); strings.Join(got, ",") != "listed" {
		t.Errorf("source=ngc = %v, want [listed]", got)
	}

	for _, path := range []string{
		"/aiapp/models/listed/artifacts/a/compose",
		"/aiapp/models/listed/artifacts/a/compose?runtimeId=vllm",
	} {
		rec := do(s, http.MethodGet, path, "")
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("GET %s = %d, want 422: %s", path, rec.Code, rec.Body.String())
		}
	}
	target := `"targets":[{"kind":"node","nsId":"etri","infraId":"travel","nodeId":"vm1"}]`
	for _, inference := range []string{
		`{"modelId":"listed","artifactId":"a"}`,
		`{"modelId":"listed","artifactId":"a","runtimeId":"vllm"}`,
	} {
		rec := sendNodeJSON(s, http.MethodPost, "/aiapp/deployments", `{"type":"inference",`+target+`,"inference":`+inference+`}`)
		var msg model.SimpleMsg
		_ = json.Unmarshal(rec.Body.Bytes(), &msg)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(msg.Message, "listed/a") {
			t.Errorf("deploy %s = %d %q, want 422 naming listed/a", inference, rec.Code, msg.Message)
		}
	}
	if runner.count() != 0 {
		t.Errorf("%d runs for refused deployments", runner.count())
	}
}

func TestRuntimeProfileRoutes(t *testing.T) {
	s := newModelServer(t, "")

	rec := do(s, http.MethodGet, "/aiapp/runtimes", "")
	var resp model.RuntimeProfileListResp
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &resp) != nil || len(resp.Runtimes) != 2 {
		t.Errorf("GET /runtimes = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := do(s, http.MethodGet, "/aiapp/runtimes/vllm", ""); rec.Code != http.StatusOK {
		t.Errorf("GET /runtimes/vllm = %d", rec.Code)
	}
	if rec := do(s, http.MethodGet, "/aiapp/runtimes/tgi", ""); rec.Code != http.StatusNotFound {
		t.Errorf("GET /runtimes/tgi = %d, want 404", rec.Code)
	}
}

func TestModelComposeRoute(t *testing.T) {
	s := newModelServer(t, "")

	rec := do(s, http.MethodGet, "/aiapp/models/qwen3-vl-8b/artifacts/ollama-gguf/compose?gpu=true", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("compose = %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/yaml") {
		t.Errorf("Content-Type = %q, want text/yaml", ct)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"pull", "qwen3-vl:8b"`) || !strings.Contains(body, "driver: nvidia") {
		t.Errorf("compose body:\n%s", body)
	}

	rec = do(s, http.MethodGet, "/aiapp/models/ministral-3-8b/artifacts/hf-fp8/compose?arg=--max-model-len&arg=16384", "")
	if body := rec.Body.String(); rec.Code != http.StatusOK || !strings.Contains(body, `"--load-format", "mistral", "--max-model-len", "16384"]`) {
		t.Errorf("compose with args = %d:\n%s", rec.Code, body)
	}

	for _, tc := range []struct {
		path string
		want int
	}{
		{"/aiapp/models/qwen3-vl-8b/artifacts/hf-bf16/compose?runtimeId=ollama", http.StatusUnprocessableEntity},
		{"/aiapp/models/qwen3-vl-8b/artifacts/hf-bf16/compose?runtimeId=tgi", http.StatusNotFound},
		{"/aiapp/models/qwen3-vl-8b/artifacts/nope/compose", http.StatusNotFound},
		{"/aiapp/models/nope/artifacts/hf-bf16/compose", http.StatusNotFound},
		{"/aiapp/models/qwen3-vl-8b/artifacts/hf-bf16/compose?gpu=maybe", http.StatusBadRequest},
		{"/aiapp/models/qwen3-vl-8b/artifacts/hf-bf16/compose?arg=--port&arg=9000", http.StatusBadRequest},
		{"/aiapp/models/qwen3-vl-8b/artifacts/ollama-gguf/compose?arg=--verbose", http.StatusBadRequest},
	} {
		if rec := do(s, http.MethodGet, tc.path, ""); rec.Code != tc.want {
			t.Errorf("GET %s = %d, want %d: %s", tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
}
