package modelcat

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	verified, notVerified := true, false
	vl, err := store.Get(ctx, "qwen3-vl-8b")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	at := time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC)
	vl.Artifacts[1].Runtimes[0].VerifiedAt = &at
	if _, err := store.Register(ctx, *vl); err != nil {
		t.Fatalf("Register: %v", err)
	}
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
		{"runtime vllm", Filter{RuntimeID: "vllm"}, []string{"ministral-3-8b", "qwen3-embedding-0.6b", "qwen3-vl-8b"}},
		// qwen3-vl-8b has a GGUF artifact and a vLLM artifact, but no single
		// artifact that is both.
		{"one artifact must match every artifact field", Filter{Format: "gguf", RuntimeID: "vllm"}, nil},
		{"model and artifact fields together", Filter{Modality: "image", Format: "hf"}, []string{"ministral-3-8b", "qwen3-vl-8b"}},
		{"origin seed", Filter{Origin: "seed"}, []string{"ministral-3-8b", "qwen3-embedding-0.6b", "qwen3-vl-8b"}},
		{"origin ignores case", Filter{Origin: "SEED"}, []string{"ministral-3-8b", "qwen3-embedding-0.6b", "qwen3-vl-8b"}},
		{"origin huggingface", Filter{Origin: "huggingface"}, nil},
		{"verified", Filter{Verified: &verified}, []string{"qwen3-vl-8b"}},
		{"not verified", Filter{Verified: &notVerified}, []string{"ministral-3-8b", "qwen3-embedding-0.6b"}},
		{"verified and origin", Filter{Verified: &verified, Origin: "manual"}, nil},
		{"deployable", Filter{Deployable: &verified}, []string{"ministral-3-8b", "qwen3-embedding-0.6b", "qwen3-vl-8b"}},
		{"not deployable", Filter{Deployable: &notVerified}, nil},
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
			Runtimes: []model.RuntimeCompat{{RuntimeID: model.RuntimeVLLM}},
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
	m.Artifacts[0].Runtimes[0].RuntimeID = "changed"
	m.Tasks[0] = "changed"

	again, _ := store.Get(ctx, "qwen3-vl-8b")
	if again.Artifacts[0].Runtimes[0].RuntimeID != model.RuntimeVLLM || again.Tasks[0] != "vision-chat" {
		t.Errorf("stored model changed through a returned copy: %+v", again)
	}

	ministral, err := store.Get(ctx, "ministral-3-8b")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	ministral.Artifacts[0].Runtimes[0].Args[1] = "changed"
	again, _ = store.Get(ctx, "ministral-3-8b")
	if again.Artifacts[0].Runtimes[0].Args[1] != "32768" {
		t.Errorf("stored runtime args changed through a returned copy: %q", again.Artifacts[0].Runtimes[0].Args)
	}

	at := time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC)
	synced, verifiedAt := at, at
	ministral.Artifacts[0].Runtimes[0].Args[1] = "32768"
	ministral.SyncedAt = &synced
	ministral.Artifacts[0].Runtimes[0].VerifiedAt = &verifiedAt
	if _, err := store.Register(ctx, *ministral); err != nil {
		t.Fatalf("Register: %v", err)
	}
	synced = synced.Add(time.Hour)
	verifiedAt = verifiedAt.Add(time.Hour)
	got, _ := store.Get(ctx, "ministral-3-8b")
	if !got.SyncedAt.Equal(at) || !got.Artifacts[0].Runtimes[0].VerifiedAt.Equal(at) {
		t.Errorf("stored times changed through the registered record: syncedAt %v, verifiedAt %v",
			got.SyncedAt, got.Artifacts[0].Runtimes[0].VerifiedAt)
	}
	*got.SyncedAt = got.SyncedAt.Add(time.Hour)
	*got.Artifacts[0].Runtimes[0].VerifiedAt = got.Artifacts[0].Runtimes[0].VerifiedAt.Add(time.Hour)
	again, _ = store.Get(ctx, "ministral-3-8b")
	if !again.SyncedAt.Equal(at) || !again.Artifacts[0].Runtimes[0].VerifiedAt.Equal(at) {
		t.Errorf("stored times changed through a returned copy: syncedAt %v, verifiedAt %v",
			again.SyncedAt, again.Artifacts[0].Runtimes[0].VerifiedAt)
	}
}

func TestOrigin(t *testing.T) {
	ctx := context.Background()
	store := seeded(t, "")
	for _, m := range store.List(ctx, Filter{}) {
		if m.Origin != model.OriginSeed {
			t.Errorf("%s origin = %q, want seed", m.ID, m.Origin)
		}
	}

	noOrigin := model.Model{ID: "no-origin", Name: "No origin", Artifacts: []model.Artifact{{
		ID: "a", Source: "huggingface", Ref: "org/no-origin", Format: model.FormatHF,
	}}}
	registered, err := store.Register(ctx, noOrigin)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if registered.Origin != model.OriginManual {
		t.Errorf("origin of a record without one = %q, want manual", registered.Origin)
	}
	if registered.Artifacts[0].Runtimes == nil {
		t.Error("runtimes of an artifact without runtimes = nil, want an empty list")
	}
	if got := ids(store.List(ctx, Filter{Origin: "manual"})); len(got) != 1 || got[0] != "no-origin" {
		t.Errorf("origin=manual = %v, want [no-origin]", got)
	}

	collected := noOrigin
	collected.Origin = "github"
	if _, err := store.Register(ctx, collected); !errors.Is(err, model.ErrValidation) {
		t.Errorf("Register with an unknown origin = %v, want a validation error", err)
	}
}

func TestFileWithoutOriginReadsAsManual(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "models.json")
	// The layout written before origin, syncedAt, stale and verifiedAt
	// existed, with one artifact that lists no runtime.
	old := `{"models":[{"id":"old","name":"Old","artifacts":[` +
		`{"id":"a","source":"huggingface","ref":"org/old","format":"hf","runtimes":[{"runtimeId":"vllm"}]},` +
		`{"id":"b","source":"ngc","ref":"nvidia/old","format":"hf","runtimes":[]}]}]}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	store, loaded, err := Open(path)
	if err != nil || !loaded {
		t.Fatalf("Open = %v, loaded %v", err, loaded)
	}
	m, err := store.Get(ctx, "old")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m.Origin != model.OriginManual || m.Stale || m.SyncedAt != nil || m.Verified() {
		t.Errorf("old record = origin %q, stale %v, syncedAt %v, verified %v", m.Origin, m.Stale, m.SyncedAt, m.Verified())
	}
	if got := ids(store.List(ctx, Filter{Origin: "manual"})); len(got) != 1 {
		t.Errorf("origin=manual = %v, want [old]", got)
	}
	if got := ids(store.List(ctx, Filter{Source: "ngc"})); len(got) != 1 {
		t.Errorf("source=ngc = %v, want the artifact without runtimes listed", got)
	}
	if !m.Deployable || !m.Artifacts[0].Deployable || m.Artifacts[1].Deployable {
		t.Errorf("deployable = model %v, a %v, b %v; want true, true, false", m.Deployable, m.Artifacts[0].Deployable, m.Artifacts[1].Deployable)
	}
}

// TestFileWithoutOriginReadsSeedIDsAsSeed reads an entry with no origin as
// seed when its ID is a built-in model ID, as in a file written by the seed
// before origin existed, and every other entry with no origin as manual.
func TestFileWithoutOriginReadsSeedIDsAsSeed(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "models.json")
	old := `{"models":[` +
		`{"id":"qwen3-vl-8b","name":"Qwen3-VL-8B-Instruct","artifacts":[{"id":"hf-bf16","source":"huggingface","ref":"Qwen/Qwen3-VL-8B-Instruct","format":"hf","runtimes":[{"runtimeId":"vllm"}]}]},` +
		`{"id":"ministral-3-8b","name":"Ministral","origin":"manual","artifacts":[{"id":"hf-fp8","source":"huggingface","ref":"mistralai/Ministral-3-8B-Instruct-2512","format":"hf","runtimes":[]}]},` +
		`{"id":"mine","name":"Mine","artifacts":[{"id":"a","source":"huggingface","ref":"org/mine","format":"hf","runtimes":[]}]}]}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	store, _, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for id, want := range map[string]string{
		"qwen3-vl-8b":    model.OriginSeed,
		"ministral-3-8b": model.OriginManual,
		"mine":           model.OriginManual,
	} {
		m, err := store.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get %s: %v", id, err)
		}
		if m.Origin != want {
			t.Errorf("%s origin = %q, want %q", id, m.Origin, want)
		}
	}
	if got := ids(store.List(ctx, Filter{Origin: "seed"})); strings.Join(got, ",") != "qwen3-vl-8b" {
		t.Errorf("origin=seed = %v, want [qwen3-vl-8b]", got)
	}
}

// TestDeployableIsComputed ignores a deployable the caller sends and
// computes it from the runtimes.
func TestDeployableIsComputed(t *testing.T) {
	store := seeded(t, "")
	ctx := context.Background()
	claimed := model.Model{ID: "claims", Name: "Claims", Deployable: true, Artifacts: []model.Artifact{{
		ID: "a", Source: "ngc", Ref: "nvidia/claims", Format: "hf", Deployable: true,
	}}}
	got, err := store.Register(ctx, claimed)
	if err != nil {
		t.Fatal(err)
	}
	if got.Deployable || got.Artifacts[0].Deployable {
		t.Errorf("registered = model %v, artifact %v; want false, false", got.Deployable, got.Artifacts[0].Deployable)
	}
	vl, err := store.Get(ctx, "qwen3-vl-8b")
	if err != nil {
		t.Fatal(err)
	}
	if !vl.Deployable || !vl.Artifacts[0].Deployable || !vl.Artifacts[1].Deployable {
		t.Errorf("qwen3-vl-8b deployable = %+v", vl)
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
	if _, err := Runtime("tgi"); !errors.Is(err, ErrRuntimeNotFound) {
		t.Errorf("Runtime error = %v, want ErrRuntimeNotFound", err)
	}
}

func TestRegisterRejectsInvalidModel(t *testing.T) {
	store := seeded(t, "")
	_, err := store.Register(context.Background(), model.Model{ID: "x", Name: "X"})
	if !errors.Is(err, model.ErrValidation) {
		t.Errorf("Register error = %v, want a validation error", err)
	}
}

func TestBuiltinRuntimesAreValid(t *testing.T) {
	engines := Runtimes()
	if len(engines) != 2 {
		t.Fatalf("engines = %d, want 2", len(engines))
	}
	for i := range engines {
		if err := engines[i].Validate(); err != nil {
			t.Errorf("engine %s: %v", engines[i].ID, err)
		}
	}
}

func TestSeedArtifactsRunOnTheirRuntimes(t *testing.T) {
	for _, m := range builtinModels() {
		for _, a := range m.Artifacts {
			for _, compat := range a.Runtimes {
				engine, err := Runtime(compat.RuntimeID)
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

func TestFileInTheEnginesFormatIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	// The layout written before commit 2e79094, when artifacts listed engines.
	old := `{"models":[{"id":"qwen3-vl-8b","name":"Qwen3-VL-8B-Instruct","artifacts":[` +
		`{"id":"hf-bf16","source":"huggingface","ref":"Qwen/Qwen3-VL-8B-Instruct","format":"hf","engines":[{"engine":"vllm"}]}]}]}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := Open(path)
	if err == nil {
		t.Fatal("Open loaded a file in the engines format")
	}
	for _, want := range []string{path, "format before runtimes", `model "qwen3-vl-8b" artifacts[0] lists engines`, "remove the file"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Open error = %q, want it to contain %q", err, want)
		}
	}
}
