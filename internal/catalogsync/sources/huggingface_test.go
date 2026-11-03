package sources

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/ish-hcc/ai-agent-proto/internal/catalogsync"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

func TestHuggingFaceFetch(t *testing.T) {
	fs := newFixtureServer(t, func(r *http.Request) (string, int) {
		if r.URL.Path != "/api/models" {
			return "", 0
		}
		return "hf_" + r.URL.Query().Get("pipeline_tag") + ".json", 0
	})
	src := &HuggingFace{BaseURL: fs.URL}
	models, err := src.Fetch(context.Background(), opts(fs, 7))
	if err != nil {
		t.Fatal(err)
	}
	mustValid(t, model.OriginHuggingFace, models)
	if len(models) != 8 {
		t.Fatalf("got %d models, want 8", len(models))
	}

	reqs := fs.requests()
	var tags []string
	for _, r := range reqs {
		q := r.URL.Query()
		tags = append(tags, q.Get("pipeline_tag"))
		if q.Get("limit") != "7" || q.Get("sort") != "downloads" || q.Get("direction") != "-1" {
			t.Errorf("query %s", r.URL.RawQuery)
		}
		if !slices.Equal(q["expand[]"], hfExpand) {
			t.Errorf("expand[] = %v", q["expand[]"])
		}
	}
	if !slices.Equal(tags, hfPipelineTags) {
		t.Errorf("tags asked = %v", tags)
	}

	got := byID(models)
	qwen, ok := got["hf-qwen--qwen3-0.6b"]
	if !ok {
		t.Fatalf("hf-qwen--qwen3-0.6b missing from %v", keys(got))
	}
	if qwen.ID != catalogsync.ModelID("hf", "Qwen/Qwen3-0.6B") || qwen.Name != "Qwen3-0.6B" || qwen.Publisher != "Qwen" ||
		qwen.Family != "qwen3" || qwen.Parameters != "751.6M" || qwen.License != "apache-2.0" || qwen.ContextLength != 0 {
		t.Errorf("qwen = %+v", qwen)
	}
	if !slices.Equal(qwen.Tasks, []string{"chat"}) || !slices.Equal(qwen.InputModalities, []string{"text"}) {
		t.Errorf("qwen tasks %v modalities %v", qwen.Tasks, qwen.InputModalities)
	}
	a := qwen.Artifacts[0]
	if a.ID != "hf" || a.Source != "huggingface" || a.Ref != "Qwen/Qwen3-0.6B" || a.Format != "hf" || a.Precision != "BF16" || a.SizeGiB != 0 {
		t.Errorf("qwen artifact = %+v", a)
	}
	if !slices.Equal(runtimeIDs(a), []string{"vllm"}) {
		t.Errorf("qwen runtimes = %v", runtimeIDs(a))
	}

	vl := got["hf-qwen--qwen3-vl-8b-instruct"]
	if !slices.Equal(vl.Tasks, []string{"vision-chat"}) || !slices.Equal(vl.InputModalities, []string{"text", "image"}) ||
		vl.Parameters != "8.8B" || !slices.Equal(runtimeIDs(vl.Artifacts[0]), []string{"vllm"}) {
		t.Errorf("vl = %+v", vl)
	}

	// A GGUF repository has no safetensors: no precision, no runtime.
	gguf := got[catalogsync.ModelID("hf", "ISTA-DASLab/Qwen3.8-Flash-Next-GSQ-RCO-GGUF")]
	if gguf.ID == "" || len(gguf.Artifacts[0].Runtimes) != 0 || gguf.Artifacts[0].Precision != "" || gguf.Parameters != "" {
		t.Errorf("gguf repo = %+v", gguf)
	}

	bge := got["hf-baai--bge-small-en-v1.5"]
	if !slices.Equal(bge.Tasks, []string{"embedding"}) || bge.Artifacts[0].Precision != "F32" || bge.License != "mit" {
		t.Errorf("bge = %+v", bge)
	}
}

func keys(m map[string]model.Model) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// TestHuggingFaceRuntimeRule checks the vllm rule on inline listings: an
// architecture in the vLLM list with safetensors gets vllm; one outside the
// list, or one without safetensors, gets no runtime. A repository listed
// under two tags is one model with both tasks.
func TestHuggingFaceRuntimeRule(t *testing.T) {
	listing := map[string]string{
		"text-generation": `[
			{"id":"acme/in-list","config":{"architectures":["LlamaForCausalLM"]},"safetensors":{"parameters":{"F16":10,"BF16":2000},"total":2010}},
			{"id":"acme/not-in-list","config":{"architectures":["FooBarForCausalLM"]},"safetensors":{"parameters":{"BF16":5},"total":5}},
			{"id":"acme/no-safetensors","config":{"architectures":["LlamaForCausalLM"]}},
			{"id":"acme/both","config":{"architectures":["LlamaForCausalLM"]},"safetensors":{"parameters":{"BF16":5},"total":5}},
			{"id":"acme/bad ref"}
		]`,
		"image-text-to-text": `[{"id":"acme/both","config":{"architectures":["LlamaForCausalLM"]},"safetensors":{"parameters":{"BF16":5},"total":5}}]`,
		"feature-extraction": `[]`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, listing[r.URL.Query().Get("pipeline_tag")])
	}))
	defer srv.Close()

	models, err := (&HuggingFace{BaseURL: srv.URL}).Fetch(context.Background(), catalogsync.Options{Limit: 5, Client: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	mustValid(t, model.OriginHuggingFace, models)
	got := byID(models)
	if len(got) != 4 {
		t.Errorf("models = %v, want 4 (the invalid ref left out)", keys(got))
	}
	for id, want := range map[string][]string{
		"hf-acme--in-list":        {"vllm"},
		"hf-acme--not-in-list":    nil,
		"hf-acme--no-safetensors": nil,
		"hf-acme--both":           {"vllm"},
	} {
		m, ok := got[id]
		if !ok {
			t.Errorf("%s missing", id)
			continue
		}
		if rt := runtimeIDs(m.Artifacts[0]); !slices.Equal(rt, want) {
			t.Errorf("%s runtimes = %v, want %v", id, rt, want)
		}
	}
	if p := got["hf-acme--in-list"].Artifacts[0].Precision; p != "BF16" {
		t.Errorf("precision = %q, want the dtype with the most parameters", p)
	}
	both := got["hf-acme--both"]
	if !slices.Equal(both.Tasks, []string{"chat", "vision-chat"}) || !slices.Equal(both.InputModalities, []string{"text", "image"}) {
		t.Errorf("both tasks %v modalities %v", both.Tasks, both.InputModalities)
	}
}

func TestHuggingFaceListingError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"bad"}`, http.StatusBadRequest)
	}))
	defer srv.Close()
	if _, err := (&HuggingFace{BaseURL: srv.URL}).Fetch(context.Background(), catalogsync.Options{Limit: 1, Client: srv.Client()}); err == nil {
		t.Error("no error on 400")
	}
}

func TestHFLicense(t *testing.T) {
	for raw, want := range map[string]string{`"mit"`: "mit", `["mit","apache-2.0"]`: "mit,apache-2.0", `3`: "", ``: ""} {
		if got := hfLicense([]byte(raw)); got != want {
			t.Errorf("hfLicense(%s) = %q, want %q", raw, got, want)
		}
	}
}
