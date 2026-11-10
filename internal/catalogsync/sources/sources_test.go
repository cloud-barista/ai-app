package sources

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ish-hcc/ai-agent-proto/internal/catalogsync"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/modelcat"
)

// fixtureServer answers each request with what route returns: a testdata
// file name to serve with 200, or "" for 404. It records the requests.
type fixtureServer struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []*http.Request
}

func newFixtureServer(t *testing.T, route func(r *http.Request) (file string, status int)) *fixtureServer {
	t.Helper()
	fs := &fixtureServer{}
	fs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fs.mu.Lock()
		fs.reqs = append(fs.reqs, r.Clone(r.Context()))
		fs.mu.Unlock()
		file, status := route(r)
		if file == "" {
			if status == 0 {
				status = http.StatusNotFound
			}
			http.Error(w, `{"errors":[{"code":"NOT_FOUND"}]}`, status)
			return
		}
		body, err := os.ReadFile(filepath.Join("testdata", file))
		if err != nil {
			t.Errorf("fixture %s: %v", file, err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	t.Cleanup(fs.Close)
	return fs
}

func (fs *fixtureServer) requests() []*http.Request {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return slices.Clone(fs.reqs)
}

// mustValid fails the test unless every model passes model.Validate and
// carries origin.
func mustValid(t *testing.T, origin string, models []model.Model) {
	t.Helper()
	for i := range models {
		if err := models[i].Validate(); err != nil {
			t.Errorf("model %s does not validate: %v", models[i].ID, err)
		}
		if models[i].Origin != origin {
			t.Errorf("model %s origin = %q, want %q", models[i].ID, models[i].Origin, origin)
		}
		for _, a := range models[i].Artifacts {
			if a.Runtimes == nil {
				t.Errorf("model %s artifact %s runtimes is nil, want a list", models[i].ID, a.ID)
			}
		}
	}
}

func byID(models []model.Model) map[string]model.Model {
	out := make(map[string]model.Model, len(models))
	for _, m := range models {
		out[m.ID] = m
	}
	return out
}

func runtimeIDs(a model.Artifact) []string {
	var ids []string
	for _, r := range a.Runtimes {
		ids = append(ids, r.RuntimeID)
	}
	return ids
}

func TestNew(t *testing.T) {
	srcs, err := New(Names())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range srcs {
		got = append(got, s.Name())
	}
	if !slices.Equal(got, Names()) {
		t.Errorf("names = %v, want %v", got, Names())
	}
	for _, name := range got {
		if !model.ValidOrigin(name) || name == model.OriginSeed || name == model.OriginManual {
			t.Errorf("source name %q is not a sync origin", name)
		}
	}

	if _, err := New([]string{model.OriginHuggingFace, "civitai"}); err == nil || !strings.Contains(err.Error(), "civitai") {
		t.Errorf("unknown name: err = %v", err)
	}
	if _, err := New([]string{model.OriginNGC, model.OriginNGC}); err == nil {
		t.Error("repeated name: no error")
	}
	if srcs, err := New(nil); err != nil || len(srcs) != 0 {
		t.Errorf("no names: %v, %v", srcs, err)
	}
}

func TestVLLMVersionMatchesRuntimeImage(t *testing.T) {
	profile, err := modelcat.Runtime(model.RuntimeVLLM)
	if err != nil {
		t.Fatal(err)
	}
	_, tag, _ := strings.Cut(profile.Image, ":")
	if tag != VLLMVersion {
		t.Errorf("vllm runtime image %s, architecture list from vLLM %s: regenerate vllm_archs.go for %s",
			profile.Image, VLLMVersion, tag)
	}
}

func TestVLLMArchitectures(t *testing.T) {
	if !slices.IsSorted(vllmArchitectures) {
		t.Error("vllmArchitectures is not sorted")
	}
	if len(slices.Compact(slices.Clone(vllmArchitectures))) != len(vllmArchitectures) {
		t.Error("vllmArchitectures has duplicates")
	}
	for _, a := range []string{"Qwen3ForCausalLM", "LlamaForCausalLM", "BertModel", "Qwen3VLForConditionalGeneration"} {
		if !vllmArchSet[a] {
			t.Errorf("%s missing", a)
		}
	}
	// Previously supported and out-of-tree architectures are not listed.
	for _, a := range []string{"MotifForCausalLM", "BartModel"} {
		if vllmArchSet[a] {
			t.Errorf("%s listed", a)
		}
	}
}

func TestArtifactID(t *testing.T) {
	seen := map[string]bool{}
	a := artifactID("360m-q4_K_M", seen)
	b := artifactID("360M-Q4_K_M", seen)
	if a != "360m-q4_k_m" {
		t.Errorf("first = %q", a)
	}
	if b == a || !strings.HasPrefix(b, "360m-q4_k_m-") || len(b) != len(a)+9 {
		t.Errorf("second = %q, want %q with a hash tail", b, a)
	}
	long := artifactID(strings.Repeat("x", 80), seen)
	if len(long) > 63 {
		t.Errorf("long id %q is %d characters", long, len(long))
	}
	for _, id := range []string{a, b, long, artifactID("--", seen)} {
		m := model.Model{ID: "m", Name: "m", Artifacts: []model.Artifact{{ID: id, Source: "s", Ref: "r", Format: "f"}}}
		if err := m.Validate(); err != nil {
			t.Errorf("artifact id %q: %v", id, err)
		}
	}
}

func TestShortCount(t *testing.T) {
	for n, want := range map[int64]string{
		8767123696: "8.8B", 8000000000: "8B", 137022720: "137M", 33360512: "33.4M",
		1500: "1.5K", 999: "999", 0: "", 1_560_000_000_000: "1.6T",
	} {
		if got := shortCount(n); got != want {
			t.Errorf("shortCount(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestGiB(t *testing.T) {
	if got := gib(13793422816); got != 12.8 {
		t.Errorf("gib = %v", got)
	}
	if got := gib(0); got != 0 {
		t.Errorf("gib(0) = %v", got)
	}
}

// opts returns Options with the given limit and the test server's client.
func opts(fs *fixtureServer, limit int) catalogsync.Options {
	return catalogsync.Options{Limit: limit, Client: fs.Client()}
}
