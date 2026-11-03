package sources

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ish-hcc/ai-agent-proto/internal/catalogsync"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

func dmrRoute(r *http.Request) (string, int) {
	p := r.URL.Path
	if p == "/v2/namespaces/ai/repositories" {
		if r.URL.Query().Get("page") != "1" {
			return "", http.StatusForbidden
		}
		return "dmr_repos.json", 0
	}
	if name, ok := strings.CutPrefix(p, "/v2/namespaces/ai/repositories/"); ok {
		return "dmr_tags_" + strings.TrimSuffix(name, "/tags") + ".json", 0
	}
	return "", 0
}

func TestDockerModelRunnerFetch(t *testing.T) {
	fs := newFixtureServer(t, dmrRoute)
	models, err := (&DockerModelRunner{HubURL: fs.URL}).Fetch(context.Background(), opts(fs, 10))
	if err != nil {
		t.Fatal(err)
	}
	mustValid(t, model.OriginDockerModelRunner, models)
	if len(models) != 3 {
		t.Fatalf("got %d models, want 3", len(models))
	}
	got := byID(models)

	smol, ok := got["dmr-smollm2"]
	if !ok || smol.ID != catalogsync.ModelID("dmr", "smollm2") || smol.Name != "smollm2" || len(smol.Tasks) != 0 {
		t.Fatalf("smollm2 = %+v", smol)
	}
	type art struct{ id, ref, format, precision string }
	var arts []art
	for _, a := range smol.Artifacts {
		arts = append(arts, art{a.ID, a.Ref, a.Format, a.Precision})
		if a.Source != "docker-model-runner" || len(a.Runtimes) != 0 || a.SizeGiB <= 0 {
			t.Errorf("artifact %+v", a)
		}
	}
	// "latest" and "360m" name no format and are skipped. 360M-Q4_K_M and
	// 360m-q4_K_M fold to the same ID, so the second gets a hash tail.
	want := []art{
		{"360m-safetensors", "ai/smollm2:360m-safetensors", "safetensors", ""},
		{"360m-q4_k_m", "ai/smollm2:360m-q4_K_M", "gguf", "q4_K_M"},
		{"360m-mlx", "ai/smollm2:360m-mlx", "mlx", ""},
		{"360m-f16", "ai/smollm2:360m-f16", "gguf", "f16"},
	}
	if len(arts) != 5 || !slices.Equal(arts[:4], want) {
		t.Fatalf("artifacts = %+v", arts)
	}
	if last := arts[4]; !strings.HasPrefix(last.id, "360m-q4_k_m-") || last.ref != "ai/smollm2:360M-Q4_K_M" || last.precision != "Q4_K_M" {
		t.Errorf("folded tag = %+v", last)
	}

	mx := got["dmr-mxbai-embed-large"]
	if len(mx.Artifacts) != 1 || mx.Artifacts[0].Format != "gguf" || mx.Artifacts[0].Precision != "F16" ||
		!slices.Equal(mx.Tasks, []string{"embedding"}) {
		t.Errorf("mxbai = %+v", mx)
	}
	q := got["dmr-qwen3-vllm"]
	if len(q.Artifacts) != 4 {
		t.Errorf("qwen3-vllm artifacts = %+v", q.Artifacts)
	}
	for _, a := range q.Artifacts {
		if a.Format != "safetensors" {
			t.Errorf("qwen3-vllm %s format %s", a.ID, a.Format)
		}
	}

	if reqs := fs.requests(); reqs[0].URL.Query().Get("page_size") != "10" {
		t.Errorf("first request %s", reqs[0].URL)
	}
}

func TestDockerModelRunnerLimitAndPaging(t *testing.T) {
	// Two repositories a page; the third page is refused as Docker Hub
	// refuses anonymous paging past 100 entries.
	var mu sync.Mutex
	var pages []string
	seenPages := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(pages)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/namespaces/ai/repositories" {
			page := r.URL.Query().Get("page")
			mu.Lock()
			pages = append(pages, page)
			mu.Unlock()
			switch page {
			case "1":
				fmt.Fprint(w, `{"next":"x","results":[{"name":"a"},{"name":"b"}]}`)
			case "2":
				fmt.Fprint(w, `{"next":"x","results":[{"name":"c"},{"name":"broken"}]}`)
			default:
				http.Error(w, `{"message":"pagination offset too large"}`, http.StatusForbidden)
			}
			return
		}
		if strings.Contains(r.URL.Path, "/broken/") {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, `{"results":[{"name":"1B-Q4_K_M","full_size":1073741824}]}`)
	}))
	defer srv.Close()
	src := &DockerModelRunner{HubURL: srv.URL}

	models, err := src.Fetch(context.Background(), catalogsync.Options{Limit: 3, Client: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 3 || !slices.Equal(seenPages(), []string{"1", "2"}) {
		t.Errorf("limit 3: %d models, pages %v", len(models), seenPages())
	}

	mu.Lock()
	pages = nil
	mu.Unlock()
	models, err = src.Fetch(context.Background(), catalogsync.Options{Limit: 10, Client: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	mustValid(t, model.OriginDockerModelRunner, models)
	// a, b, c; broken's tags fail and it is left out; page 3 is refused.
	if len(models) != 3 || !slices.Equal(seenPages(), []string{"1", "2", "3"}) {
		t.Errorf("limit 10: %d models, pages %v", len(models), seenPages())
	}
}

func TestDockerModelRunnerListingError(t *testing.T) {
	fs := newFixtureServer(t, func(r *http.Request) (string, int) { return "", http.StatusForbidden })
	if _, err := (&DockerModelRunner{HubURL: fs.URL}).Fetch(context.Background(), opts(fs, 3)); err == nil {
		t.Error("no error when the first page fails")
	}
}

func TestDMRTagFormat(t *testing.T) {
	for _, c := range []struct{ repo, tag, format, precision string }{
		{"gemma4", "e4b-q4_K_M", "gguf", "q4_K_M"},
		{"gemma4", "e4b-bf16", "gguf", "bf16"},
		{"gemma4", "31b-mlx-4bit", "mlx", "4bit"},
		{"gemma4", "12b-safetensors", "safetensors", ""},
		{"gemma4", "e4b-mtp", "", ""},
		{"gemma4", "latest", "", ""},
		{"stable-diffusion", "Q4", "gguf", "Q4"},
		{"gpt-oss", "20B-MXFP4", "gguf", "MXFP4"},
		{"qwen3-vllm", "8B", "safetensors", ""},
		{"gemma4-safetensors", "latest", "safetensors", ""},
		{"qwen3-vllm", "8B-FP8", "safetensors", ""},
	} {
		f, p := dmrTagFormat(c.repo, c.tag)
		if f != c.format || p != c.precision {
			t.Errorf("%s:%s = %q %q, want %q %q", c.repo, c.tag, f, p, c.format, c.precision)
		}
	}
}
