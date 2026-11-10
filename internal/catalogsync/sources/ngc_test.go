package sources

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/ish-hcc/ai-agent-proto/internal/catalogsync"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

func ngcRoute(r *http.Request) (string, int) {
	if r.URL.Path == "/v2/search/catalog/resources/MODEL" {
		return "ngc_models.json", 0
	}
	return "", 0
}

func TestNGCFetch(t *testing.T) {
	fs := newFixtureServer(t, ngcRoute)
	models, err := (&NGC{APIURL: fs.URL}).Fetch(context.Background(), opts(fs, 25))
	if err != nil {
		t.Fatal(err)
	}
	mustValid(t, model.OriginNGC, models)

	var q struct {
		Query    string `json:"query"`
		Page     int    `json:"page"`
		PageSize int    `json:"pageSize"`
	}
	if err := json.Unmarshal([]byte(fs.requests()[0].URL.Query().Get("q")), &q); err != nil {
		t.Fatal(err)
	}
	if q.Query != "" || q.Page != 0 || q.PageSize != 25 {
		t.Errorf("q = %+v", q)
	}

	// The scored group repeats two resources of the MODEL group.
	if len(models) != 4 {
		t.Fatalf("got %d models, want 4", len(models))
	}
	got := byID(models)
	audio := got["ngc-nim--nvidia--audio2emotion_model"]
	if audio.ID != catalogsync.ModelID("ngc", "nim/nvidia/audio2emotion_model") || audio.Name != "Audio2Emotion Model" ||
		audio.Publisher != "NVIDIA" || len(audio.Tasks) != 0 {
		t.Errorf("audio2emotion = %+v", audio)
	}
	a := audio.Artifacts[0]
	if a.ID != "ngc" || a.Source != "ngc" || a.Ref != "nim/nvidia/audio2emotion_model" || a.Format != "SavedModel" ||
		a.Precision != "FP16" || a.SizeGiB != 1.2 || len(a.Runtimes) != 0 {
		t.Errorf("audio2emotion artifact = %+v", a)
	}
	embed := got["ngc-nim--snowflake--arctic-embed-l"]
	if !slices.Equal(embed.Tasks, []string{"embedding"}) || embed.Artifacts[0].Format != "N/A" {
		t.Errorf("arctic-embed = %+v", embed)
	}
	if code := got["ngc-nvidia--code_llama"]; len(code.Tasks) != 0 || code.Artifacts[0].Format != "PY_TORCH_PTH" {
		t.Errorf("code_llama = %+v", code)
	}
	if cuda := got["ngc-nvidia--blueprint--cuda-autocomplete"]; cuda.Artifacts[0].Format != "safetensors" || cuda.Artifacts[0].Precision != "FP8" {
		t.Errorf("cuda-autocomplete = %+v", cuda)
	}
}

// TestNGCDescription copies the description NGC writes on a resource. The
// recorded search holds no description, so the resource is inline.
func TestNGCDescription(t *testing.T) {
	var r ngcResource
	if err := json.Unmarshal([]byte(`{"resourceId":"nim/nvidia/audio2emotion_model","displayName":"Audio2Emotion Model",
		"description":" Audio2Emotion TRT Engine. ","attributes":[{"key":"format","value":"SavedModel"}]}`), &r); err != nil {
		t.Fatal(err)
	}
	m, ok := ngcToModel(&r)
	if !ok || m.Description != "Audio2Emotion TRT Engine." {
		t.Errorf("model = %+v", m)
	}
}

func TestNGCLimit(t *testing.T) {
	fs := newFixtureServer(t, ngcRoute)
	models, err := (&NGC{APIURL: fs.URL}).Fetch(context.Background(), opts(fs, 2))
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Errorf("got %d models, want 2", len(models))
	}
}

func TestNGCListingError(t *testing.T) {
	fs := newFixtureServer(t, func(r *http.Request) (string, int) { return "", http.StatusInternalServerError })
	if _, err := (&NGC{APIURL: fs.URL}).Fetch(context.Background(), opts(fs, 2)); err == nil {
		t.Error("no error when the search fails")
	}
}
