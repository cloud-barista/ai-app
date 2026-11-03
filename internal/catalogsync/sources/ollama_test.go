package sources

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// ollamaRoute serves the recorded library listing, manifests and config
// blobs. kimi-k2.6 is a cloud model: the registry answers its manifest with
// 404, as it did when recorded.
func ollamaRoute(r *http.Request) (string, int) {
	p := r.URL.Path
	if p == "/api/tags" {
		return "ollama_tags.json", 0
	}
	rest, ok := strings.CutPrefix(p, "/v2/library/")
	if !ok {
		return "", 0
	}
	repo, rest, _ := strings.Cut(rest, "/")
	if tag, ok := strings.CutPrefix(rest, "manifests/"); ok {
		if r.Header.Get("Accept") != ollamaManifestType {
			return "", http.StatusNotAcceptable
		}
		if repo == "kimi-k2.6" {
			return "", http.StatusNotFound
		}
		return "ollama_man_" + repo + "_" + tag + ".json", 0
	}
	if strings.HasPrefix(rest, "blobs/sha256:") {
		return "ollama_blob_" + repo + ".json", 0
	}
	return "", 0
}

func TestOllamaFetch(t *testing.T) {
	fs := newFixtureServer(t, ollamaRoute)
	src := &Ollama{LibraryURL: fs.URL, RegistryURL: fs.URL}
	models, err := src.Fetch(context.Background(), opts(fs, 10))
	if err != nil {
		t.Fatal(err)
	}
	mustValid(t, model.OriginOllama, models)

	var ids []string
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	// Listing order is kept; the cloud model with no manifest is left out.
	if want := []string{"ollama-nemotron-3-super", "ollama-gpt-oss-20b", "ollama-clef-27b"}; !slices.Equal(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}

	got := byID(models)
	gpt := got["ollama-gpt-oss-20b"]
	if gpt.Name != "gpt-oss:20b" || gpt.Family != "gpt-oss" || gpt.Parameters != "20.9B" ||
		!slices.Equal(gpt.Tasks, []string{"chat"}) || !slices.Equal(gpt.InputModalities, []string{"text"}) {
		t.Errorf("gpt-oss = %+v", gpt)
	}
	a := gpt.Artifacts[0]
	if a.ID != "ollama" || a.Source != "ollama" || a.Ref != "gpt-oss:20b" || a.Format != "gguf" ||
		a.Precision != "MXFP4" || a.SizeGiB != 12.8 || !slices.Equal(runtimeIDs(a), []string{"ollama"}) {
		t.Errorf("gpt-oss artifact = %+v", a)
	}

	nem := got["ollama-nemotron-3-super"]
	if nem.Artifacts[0].Ref != "nemotron-3-super:latest" || nem.Family != "nemotron_h_moe" || nem.Artifacts[0].Precision != "Q4_K_M" {
		t.Errorf("nemotron = %+v", nem)
	}
	clef := got["ollama-clef-27b"]
	if !slices.Equal(clef.Tasks, []string{"vision-chat"}) || !slices.Equal(clef.InputModalities, []string{"text", "image"}) {
		t.Errorf("clef tasks %v modalities %v", clef.Tasks, clef.InputModalities)
	}
	// The model layer is the size, not the projector before it.
	if clef.Artifacts[0].SizeGiB != 17.9 {
		t.Errorf("clef size = %v", clef.Artifacts[0].SizeGiB)
	}
}

func TestOllamaLimit(t *testing.T) {
	fs := newFixtureServer(t, ollamaRoute)
	models, err := (&Ollama{LibraryURL: fs.URL, RegistryURL: fs.URL}).Fetch(context.Background(), opts(fs, 1))
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "ollama-nemotron-3-super" {
		t.Errorf("models = %v", models)
	}
	manifests := 0
	for _, r := range fs.requests() {
		if strings.Contains(r.URL.Path, "/manifests/") {
			manifests++
		}
	}
	if manifests != 1 {
		t.Errorf("%d manifests fetched, want 1", manifests)
	}
}

func TestOllamaListingError(t *testing.T) {
	fs := newFixtureServer(t, func(r *http.Request) (string, int) { return "", http.StatusBadGateway })
	if _, err := (&Ollama{LibraryURL: fs.URL, RegistryURL: fs.URL}).Fetch(context.Background(), opts(fs, 3)); err == nil {
		t.Error("no error when the listing fails")
	}
}

func TestOllamaTask(t *testing.T) {
	for caps, want := range map[string]string{
		"completion,tools":           "chat",
		"completion,vision,thinking": "vision-chat",
		"embedding":                  "embedding",
		"embedding,vision":           "embedding",
		"":                           "chat",
	} {
		if got := ollamaTask(strings.Split(caps, ",")); got != want {
			t.Errorf("ollamaTask(%s) = %s, want %s", caps, got, want)
		}
	}
}
