package sources

import (
	"context"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// ollamaRoute serves the recorded library page, manifests and config blobs.
// minimax-m2.7 is a cloud model: the registry has no manifest for it, as
// when recorded. failTags names tags whose manifest answers 500.
func ollamaRoute(failTags ...string) func(r *http.Request) (string, int) {
	return func(r *http.Request) (string, int) {
		p := r.URL.Path
		if p == "/library" {
			if r.URL.Query().Get("sort") != "popular" {
				return "", http.StatusBadRequest
			}
			return "ollama_library.html", 0
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
			if slices.Contains(failTags, repo+":"+tag) {
				return "", http.StatusInternalServerError
			}
			file := "ollama_manifest_" + repo + "_" + tag + ".json"
			if _, err := os.Stat("testdata/" + file); err != nil {
				return "", http.StatusNotFound
			}
			return file, 0
		}
		if digest, ok := strings.CutPrefix(rest, "blobs/sha256:"); ok {
			return "ollama_blob_" + digest + ".json", 0
		}
		return "", 0
	}
}

func TestParseOllamaLibrary(t *testing.T) {
	page, err := os.ReadFile("testdata/ollama_library.html")
	if err != nil {
		t.Fatal(err)
	}
	got := parseOllamaLibrary(string(page) + string(page))
	want := []ollamaEntry{
		{name: "nomic-embed-text", capabilities: []string{"embedding"}},
		{name: "qwen3", capabilities: []string{"tools", "thinking"}, sizes: []string{"0.6b", "8b"}},
		{name: "gemma3", capabilities: []string{"vision"}, sizes: []string{"1b", "4b"}},
		{name: "minimax-m2.7", capabilities: []string{"tools", "thinking"}},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i].name != want[i].name || !slices.Equal(got[i].capabilities, want[i].capabilities) || !slices.Equal(got[i].sizes, want[i].sizes) {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestOllamaFetch(t *testing.T) {
	fs := newFixtureServer(t, ollamaRoute())
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
	// Page order is kept; the cloud model with no manifest is left out.
	if want := []string{"ollama-nomic-embed-text", "ollama-qwen3", "ollama-gemma3"}; !slices.Equal(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	got := byID(models)

	nomic := got["ollama-nomic-embed-text"]
	if nomic.Name != "nomic-embed-text" || nomic.Family != "nomic-bert" || nomic.Parameters != "137M" ||
		!slices.Equal(nomic.Tasks, []string{"embedding"}) || len(nomic.Artifacts) != 1 {
		t.Fatalf("nomic = %+v", nomic)
	}
	if a := nomic.Artifacts[0]; a.ID != "latest" || a.Ref != "nomic-embed-text:latest" || a.Precision != "F16" || a.SizeGiB != 0.3 {
		t.Errorf("nomic artifact = %+v", a)
	}

	qwen := got["ollama-qwen3"]
	if qwen.Family != "qwen3" || qwen.Parameters != "" || !slices.Equal(qwen.Tasks, []string{"chat"}) ||
		!slices.Equal(qwen.InputModalities, []string{"text"}) || len(qwen.Artifacts) != 2 {
		t.Fatalf("qwen3 = %+v", qwen)
	}
	type art struct {
		id, ref, format, precision string
		size                       float64
	}
	var arts []art
	for _, a := range qwen.Artifacts {
		arts = append(arts, art{a.ID, a.Ref, a.Format, a.Precision, a.SizeGiB})
		if a.Source != "ollama" || !slices.Equal(runtimeIDs(a), []string{"ollama"}) {
			t.Errorf("qwen3 artifact %+v", a)
		}
	}
	if want := []art{{"0.6b", "qwen3:0.6b", "gguf", "Q4_K_M", 0.5}, {"8b", "qwen3:8b", "gguf", "Q4_K_M", 4.9}}; !slices.Equal(arts, want) {
		t.Errorf("qwen3 artifacts = %+v, want %+v", arts, want)
	}

	gemma := got["ollama-gemma3"]
	if !slices.Equal(gemma.Tasks, []string{"vision-chat"}) || !slices.Equal(gemma.InputModalities, []string{"text", "image"}) {
		t.Errorf("gemma3 tasks %v modalities %v", gemma.Tasks, gemma.InputModalities)
	}
	// The model layer is the size, not the projector after it.
	if a, ok := gemma.Artifact("4b"); !ok || a.SizeGiB != 2.3 {
		t.Errorf("gemma3:4b = %+v", a)
	}
}

func TestOllamaFailingTag(t *testing.T) {
	fs := newFixtureServer(t, ollamaRoute("qwen3:8b", "nomic-embed-text:latest"))
	models, err := (&Ollama{LibraryURL: fs.URL, RegistryURL: fs.URL}).Fetch(context.Background(), opts(fs, 10))
	if err != nil {
		t.Fatal(err)
	}
	mustValid(t, model.OriginOllama, models)
	got := byID(models)
	if _, ok := got["ollama-nomic-embed-text"]; ok {
		t.Error("nomic-embed-text kept with its only tag failing")
	}
	qwen := got["ollama-qwen3"]
	if len(qwen.Artifacts) != 1 || qwen.Artifacts[0].Ref != "qwen3:0.6b" || qwen.Parameters != "751.63M" {
		t.Errorf("qwen3 = %+v", qwen)
	}
}

func TestOllamaLimit(t *testing.T) {
	fs := newFixtureServer(t, ollamaRoute())
	models, err := (&Ollama{LibraryURL: fs.URL, RegistryURL: fs.URL}).Fetch(context.Background(), opts(fs, 2))
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "ollama-nomic-embed-text" || models[1].ID != "ollama-qwen3" {
		t.Errorf("models = %v", models)
	}
	var manifests []string
	for _, r := range fs.requests() {
		if strings.Contains(r.URL.Path, "/manifests/") {
			manifests = append(manifests, r.URL.Path)
		}
	}
	slices.Sort(manifests)
	want := []string{"/v2/library/nomic-embed-text/manifests/latest", "/v2/library/qwen3/manifests/0.6b", "/v2/library/qwen3/manifests/8b"}
	if !slices.Equal(manifests, want) {
		t.Errorf("manifests fetched = %v, want %v", manifests, want)
	}
}

func TestOllamaListingError(t *testing.T) {
	fs := newFixtureServer(t, func(r *http.Request) (string, int) { return "", http.StatusBadGateway })
	if _, err := (&Ollama{LibraryURL: fs.URL, RegistryURL: fs.URL}).Fetch(context.Background(), opts(fs, 3)); err == nil {
		t.Error("no error when the library page fails")
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
