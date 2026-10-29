package modelcat

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

func seeded(t *testing.T, path string) *Store {
	t.Helper()
	store, loaded, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !loaded {
		if err := Seed(context.Background(), store); err != nil {
			t.Fatalf("Seed: %v", err)
		}
	}
	return store
}

func ids(models []model.Model) []string {
	out := make([]string, 0, len(models))
	for _, m := range models {
		out = append(out, m.ID)
	}
	return out
}

func TestSeedHoldsTheThreeScenarioModels(t *testing.T) {
	store := seeded(t, "")
	got := ids(store.List(context.Background(), Filter{}))
	want := []string{"ministral-3-8b", "qwen3-embedding-0.6b", "qwen3-vl-8b"}
	if len(got) != len(want) {
		t.Fatalf("models = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("models = %v, want %v", got, want)
		}
	}
}

func TestFilter(t *testing.T) {
	store := seeded(t, "")
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		filter Filter
		want   []string
	}{
		{"modality image", Filter{Modality: "image"}, []string{"ministral-3-8b", "qwen3-vl-8b"}},
		{"modality ignores case", Filter{Modality: "IMAGE"}, []string{"ministral-3-8b", "qwen3-vl-8b"}},
		{"task embedding", Filter{Task: "embedding"}, []string{"qwen3-embedding-0.6b"}},
		{"format gguf", Filter{Format: "gguf"}, []string{"qwen3-vl-8b"}},
		{"source ollama", Filter{Source: "ollama"}, []string{"qwen3-vl-8b"}},
		{"engine vllm", Filter{Engine: "vllm"}, []string{"ministral-3-8b", "qwen3-embedding-0.6b", "qwen3-vl-8b"}},
		// qwen3-vl-8b has a GGUF artifact and a vLLM artifact, but no single
		// artifact that is both.
		{"one artifact must match every artifact field", Filter{Format: "gguf", Engine: "vllm"}, nil},
		{"model and artifact fields together", Filter{Modality: "image", Format: "hf"}, []string{"ministral-3-8b", "qwen3-vl-8b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ids(store.List(ctx, tc.filter))
			if len(got) != len(tc.want) {
				t.Fatalf("models = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("models = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestFileKeepsChangesAndIsNotSeededAgain(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sub", "models.json")

	store := seeded(t, path)
	if err := store.Delete(ctx, "ministral-3-8b"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	added := model.Model{
		ID:   "added",
		Name: "Added",
		Artifacts: []model.Artifact{{
			ID: "a", Source: "huggingface", Ref: "org/added", Format: model.FormatHF,
			Engines: []model.EngineCompat{{Engine: model.EngineVLLM}},
		}},
	}
	if _, err := store.Register(ctx, added); err != nil {
		t.Fatalf("Register: %v", err)
	}

	reopened, loaded, err := Open(path)
	if err != nil {
		t.Fatalf("Open again: %v", err)
	}
	if !loaded {
		t.Fatal("Open did not report the existing file")
	}
	got := ids(reopened.List(ctx, Filter{}))
	want := []string{"added", "qwen3-embedding-0.6b", "qwen3-vl-8b"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("models after reopen = %v, want %v", got, want)
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("store directory holds %d entries, want only the store file", len(entries))
	}
}

func TestStoreReturnsCopies(t *testing.T) {
	ctx := context.Background()
	store := seeded(t, "")
	m, err := store.Get(ctx, "qwen3-vl-8b")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	m.Artifacts[0].Engines[0].Engine = "changed"
	m.Tasks[0] = "changed"

	again, _ := store.Get(ctx, "qwen3-vl-8b")
	if again.Artifacts[0].Engines[0].Engine != model.EngineVLLM || again.Tasks[0] != "vision-chat" {
		t.Errorf("stored model changed through a returned copy: %+v", again)
	}
}

func TestNotFound(t *testing.T) {
	ctx := context.Background()
	store := seeded(t, "")
	if _, err := store.Get(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get error = %v, want ErrNotFound", err)
	}
	if err := store.Delete(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete error = %v, want ErrNotFound", err)
	}
	if _, _, err := store.Artifact(ctx, "qwen3-vl-8b", "nope"); !errors.Is(err, ErrArtifactNotFound) {
		t.Errorf("Artifact error = %v, want ErrArtifactNotFound", err)
	}
	if _, err := Engine("tgi"); !errors.Is(err, ErrEngineNotFound) {
		t.Errorf("Engine error = %v, want ErrEngineNotFound", err)
	}
}

func TestRegisterRejectsInvalidModel(t *testing.T) {
	store := seeded(t, "")
	_, err := store.Register(context.Background(), model.Model{ID: "x", Name: "X"})
	if !errors.Is(err, model.ErrValidation) {
		t.Errorf("Register error = %v, want a validation error", err)
	}
}

func TestBuiltinEnginesAreValid(t *testing.T) {
	engines := Engines()
	if len(engines) != 2 {
		t.Fatalf("engines = %d, want 2", len(engines))
	}
	for i := range engines {
		if err := engines[i].Validate(); err != nil {
			t.Errorf("engine %s: %v", engines[i].ID, err)
		}
	}
}

func TestSeedArtifactsRunOnTheirEngines(t *testing.T) {
	for _, m := range builtinModels() {
		for _, a := range m.Artifacts {
			for _, compat := range a.Engines {
				engine, err := Engine(compat.Engine)
				if err != nil {
					t.Fatalf("%s/%s: %v", m.ID, a.ID, err)
				}
				if !engine.LoadsFormat(a.Format) {
					t.Errorf("%s/%s: engine %s does not load %s", m.ID, a.ID, engine.ID, a.Format)
				}
			}
		}
	}
}
