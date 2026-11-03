package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/modelcat"
)

func newModelRegistry(t *testing.T) *Registry {
	t.Helper()
	store, _, err := modelcat.Open("")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := modelcat.Seed(context.Background(), store); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	registry := NewRegistry(true)
	RegisterModels(registry, store)
	return registry
}

func TestModelToolsAreRead(t *testing.T) {
	registry := newModelRegistry(t)
	for _, name := range []string{"list_models", "get_model", "list_runtimes", "get_runtime"} {
		if grade := registry.Grade(name); grade != GradeRead {
			t.Errorf("%s grade = %q, want read", name, grade)
		}
	}
}

func TestListModelsFilters(t *testing.T) {
	registry := newModelRegistry(t)
	ctx := context.Background()
	for _, tc := range []struct {
		input string
		want  int
	}{
		{"", 3},
		{`{}`, 3},
		{`{"modality":"image"}`, 2},
		{`{"format":"gguf"}`, 1},
		{`{"format":"gguf","runtimeId":"vllm"}`, 0},
	} {
		out, err := registry.Call(ctx, "default", "list_models", json.RawMessage(tc.input))
		if err != nil {
			t.Fatalf("list_models %s: %v", tc.input, err)
		}
		if got := len(out.(model.ModelListResp).Models); got != tc.want {
			t.Errorf("list_models %s = %d models, want %d", tc.input, got, tc.want)
		}
	}
}

func TestGetModelAndListEngines(t *testing.T) {
	registry := newModelRegistry(t)
	ctx := context.Background()

	out, err := registry.Call(ctx, "default", "get_model", json.RawMessage(`{"modelId":"qwen3-vl-8b"}`))
	if err != nil {
		t.Fatalf("get_model: %v", err)
	}
	if m := out.(*model.Model); len(m.Artifacts) != 2 {
		t.Errorf("get_model artifacts = %d, want 2", len(m.Artifacts))
	}
	if _, err := registry.Call(ctx, "default", "get_model", json.RawMessage(`{"modelId":"nope"}`)); !errors.Is(err, modelcat.ErrNotFound) {
		t.Errorf("get_model unknown error = %v, want ErrNotFound", err)
	}

	out, err = registry.Call(ctx, "default", "list_runtimes", nil)
	if err != nil {
		t.Fatalf("list_runtimes: %v", err)
	}
	if got := len(out.(model.RuntimeProfileListResp).Runtimes); got != 2 {
		t.Errorf("list_runtimes = %d runtimes, want 2", got)
	}
}

func TestGetRuntime(t *testing.T) {
	registry := newModelRegistry(t)
	ctx := context.Background()

	out, err := registry.Call(ctx, "default", "get_runtime", json.RawMessage(`{"runtimeId":"vllm"}`))
	if err != nil {
		t.Fatalf("get_runtime: %v", err)
	}
	if r := out.(*model.RuntimeProfile); r.ID != model.RuntimeVLLM || r.Port != 8000 {
		t.Errorf("get_runtime = %+v", r)
	}
	if _, err := registry.Call(ctx, "default", "get_runtime", json.RawMessage(`{}`)); err == nil {
		t.Error("get_runtime without runtimeId was accepted")
	}
	if _, err := registry.Call(ctx, "default", "get_runtime", json.RawMessage(`{"runtimeId":"nope"}`)); !errors.Is(err, modelcat.ErrRuntimeNotFound) {
		t.Errorf("get_runtime unknown error = %v, want ErrRuntimeNotFound", err)
	}
}
