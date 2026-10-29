package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

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
		`"ref":"org/added","format":"hf","engines":[{"engine":"vllm"}]}]}`
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

func TestEngineProfileRoutes(t *testing.T) {
	s := newModelServer(t, "")

	rec := do(s, http.MethodGet, "/aiapp/engines", "")
	var resp model.EngineProfileListResp
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &resp) != nil || len(resp.Engines) != 2 {
		t.Errorf("GET /engines = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := do(s, http.MethodGet, "/aiapp/engines/vllm", ""); rec.Code != http.StatusOK {
		t.Errorf("GET /engines/vllm = %d", rec.Code)
	}
	if rec := do(s, http.MethodGet, "/aiapp/engines/tgi", ""); rec.Code != http.StatusNotFound {
		t.Errorf("GET /engines/tgi = %d, want 404", rec.Code)
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

	for _, tc := range []struct {
		path string
		want int
	}{
		{"/aiapp/models/qwen3-vl-8b/artifacts/hf-bf16/compose?engine=ollama", http.StatusUnprocessableEntity},
		{"/aiapp/models/qwen3-vl-8b/artifacts/hf-bf16/compose?engine=tgi", http.StatusNotFound},
		{"/aiapp/models/qwen3-vl-8b/artifacts/nope/compose", http.StatusNotFound},
		{"/aiapp/models/nope/artifacts/hf-bf16/compose", http.StatusNotFound},
		{"/aiapp/models/qwen3-vl-8b/artifacts/hf-bf16/compose?gpu=maybe", http.StatusBadRequest},
	} {
		if rec := do(s, http.MethodGet, tc.path, ""); rec.Code != tc.want {
			t.Errorf("GET %s = %d, want %d: %s", tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
}
