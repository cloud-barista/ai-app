package modelcat

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

func fetched(id, name string, runtimes ...string) model.Model {
	compat := make([]model.RuntimeCompat, 0, len(runtimes))
	for _, r := range runtimes {
		compat = append(compat, model.RuntimeCompat{RuntimeID: r})
	}
	return model.Model{ID: id, Name: name, Origin: model.OriginHuggingFace, Artifacts: []model.Artifact{{
		ID: "hf", Source: "huggingface", Ref: "org/" + id, Format: "hf", Runtimes: compat,
	}}}
}

func TestApplySync(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "models.json")
	store := seeded(t, path)
	first := time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC)

	counts, err := store.ApplySync(ctx, model.OriginHuggingFace, []model.Model{
		fetched("hf-a", "A", "vllm"),
		fetched("hf-b", "B"),
		fetched("qwen3-vl-8b", "Collides with the seed", "vllm"),
		{ID: "hf-bad", Name: "No artifacts"},
		fetched("hf-a", "A again", "vllm"),
	}, first)
	if err != nil {
		t.Fatalf("ApplySync: %v", err)
	}
	if want := (SyncCounts{Added: 2, Skipped: 1, Invalid: 2}); counts != want {
		t.Errorf("first counts = %+v, want %+v", counts, want)
	}
	seed, _ := store.Get(ctx, "qwen3-vl-8b")
	if seed.Origin != model.OriginSeed || seed.Name == "Collides with the seed" || seed.SyncedAt != nil {
		t.Errorf("seed entry changed: %+v", seed)
	}
	a, err := store.Get(ctx, "hf-a")
	if err != nil {
		t.Fatalf("Get hf-a: %v", err)
	}
	if a.Name != "A" || a.Origin != model.OriginHuggingFace || a.Stale || a.SyncedAt == nil || !a.SyncedAt.Equal(first) {
		t.Errorf("added = %+v", a)
	}

	// A deployment verifies hf-a on vllm, then a person registers a model of
	// an ID the next fetch also returns.
	verified := first.Add(time.Hour)
	a.Artifacts[0].Runtimes[0].VerifiedAt = &verified
	a.Origin = model.OriginHuggingFace
	if _, err := store.Register(ctx, *a); err != nil {
		t.Fatalf("Register hf-a: %v", err)
	}
	manual := fetched("hf-c", "Manual")
	manual.Origin = model.OriginManual
	if _, err := store.Register(ctx, manual); err != nil {
		t.Fatalf("Register hf-c: %v", err)
	}

	second := first.Add(24 * time.Hour)
	renamed := fetched("hf-a", "A renamed", "vllm", "ollama")
	renamed.Artifacts[0].Runtimes[1].VerifiedAt = &second
	counts, err = store.ApplySync(ctx, model.OriginHuggingFace, []model.Model{renamed, fetched("hf-c", "From the source")}, second)
	if err != nil {
		t.Fatalf("ApplySync: %v", err)
	}
	if want := (SyncCounts{Updated: 1, Staled: 1, Skipped: 1}); counts != want {
		t.Errorf("second counts = %+v, want %+v", counts, want)
	}
	a, _ = store.Get(ctx, "hf-a")
	if a.Name != "A renamed" || !a.SyncedAt.Equal(second) || len(a.Artifacts[0].Runtimes) != 2 {
		t.Fatalf("updated = %+v", a)
	}
	if got := a.Artifacts[0].Runtimes[0].VerifiedAt; got == nil || !got.Equal(verified) {
		t.Errorf("vllm verifiedAt = %v, want %v kept", got, verified)
	}
	if got := a.Artifacts[0].Runtimes[1].VerifiedAt; got != nil {
		t.Errorf("ollama verifiedAt = %v, want none: a source cannot verify", got)
	}
	if c, _ := store.Get(ctx, "hf-c"); c.Origin != model.OriginManual || c.Name != "Manual" {
		t.Errorf("manual entry changed: %+v", c)
	}
	b, _ := store.Get(ctx, "hf-b")
	if !b.Stale || !b.SyncedAt.Equal(first) || b.Name != "B" {
		t.Errorf("missing entry = %+v, want stale with syncedAt kept", b)
	}

	// The file holds the merge: a reopened store sees the same.
	reopened, _, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if b, _ := reopened.Get(ctx, "hf-b"); !b.Stale {
		t.Errorf("reopened hf-b = %+v, want stale", b)
	}

	third := second.Add(24 * time.Hour)
	counts, err = store.ApplySync(ctx, model.OriginHuggingFace, []model.Model{fetched("hf-a", "A"), fetched("hf-b", "B")}, third)
	if err != nil {
		t.Fatalf("ApplySync: %v", err)
	}
	if want := (SyncCounts{Updated: 2}); counts != want {
		t.Errorf("third counts = %+v, want %+v", counts, want)
	}
	if b, _ := store.Get(ctx, "hf-b"); b.Stale || !b.SyncedAt.Equal(third) {
		t.Errorf("reappeared = %+v, want not stale", b)
	}
	if got := ids(store.List(ctx, Filter{Origin: model.OriginSeed})); len(got) != 3 {
		t.Errorf("seed models = %v, want the three untouched", got)
	}
}

func TestApplySyncLeavesOtherSourcesAlone(t *testing.T) {
	ctx := context.Background()
	store := seeded(t, "")
	at := time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC)
	ollama := fetched("ollama-a", "Ollama A")
	if _, err := store.ApplySync(ctx, model.OriginOllama, []model.Model{ollama}, at); err != nil {
		t.Fatalf("ApplySync ollama: %v", err)
	}
	counts, err := store.ApplySync(ctx, model.OriginHuggingFace, nil, at)
	if err != nil {
		t.Fatalf("ApplySync huggingface: %v", err)
	}
	if counts != (SyncCounts{}) {
		t.Errorf("counts = %+v, want none", counts)
	}
	if m, _ := store.Get(ctx, "ollama-a"); m.Stale || m.Origin != model.OriginOllama {
		t.Errorf("ollama entry = %+v, want untouched by a huggingface run", m)
	}
}

func TestApplySyncRefusesNonSyncOrigins(t *testing.T) {
	store := seeded(t, "")
	for _, origin := range []string{"", model.OriginSeed, model.OriginManual, "unknown"} {
		if _, err := store.ApplySync(context.Background(), origin, nil, time.Now()); err == nil {
			t.Errorf("ApplySync(%q) = nil error, want refused", origin)
		}
	}
}
