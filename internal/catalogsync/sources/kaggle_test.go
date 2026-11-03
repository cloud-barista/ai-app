package sources

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/ish-hcc/ai-agent-proto/internal/catalogsync"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

func kaggleRoute(r *http.Request) (string, int) {
	if r.URL.Path != "/api/v1/models/list" {
		return "", 0
	}
	switch r.URL.Query().Get("pageToken") {
	case "":
		return "kaggle_page1.json", 0
	case "page-2-token":
		return "kaggle_page2.json", 0
	}
	return "", http.StatusBadRequest
}

func TestKaggleFetch(t *testing.T) {
	fs := newFixtureServer(t, kaggleRoute)
	models, err := (&Kaggle{BaseURL: fs.URL}).Fetch(context.Background(), opts(fs, 120))
	if err != nil {
		t.Fatal(err)
	}
	mustValid(t, model.OriginKaggle, models)

	reqs := fs.requests()
	if len(reqs) != 2 {
		t.Fatalf("%d requests, want 2 pages", len(reqs))
	}
	for _, r := range reqs {
		if q := r.URL.Query(); q.Get("pageSize") != "50" || q.Get("sortBy") != "downloadCount" {
			t.Errorf("query %s", r.URL.RawQuery)
		}
	}

	// ganmodel has no instance and is left out.
	if len(models) != 2 {
		t.Fatalf("got %d models, want 2", len(models))
	}
	got := byID(models)
	g := got["kaggle-google--gemma"]
	if g.ID != catalogsync.ModelID("kaggle", "google/gemma") || g.Name != "Gemma" || g.Publisher != "Google" || g.License != "Gemma" ||
		!slices.Equal(g.Tasks, []string{"chat"}) || !slices.Equal(g.InputModalities, []string{"text"}) {
		t.Errorf("gemma = %+v", g)
	}
	type art struct{ id, ref, format string }
	var arts []art
	for _, a := range g.Artifacts {
		arts = append(arts, art{a.ID, a.Ref, a.Format})
		if a.Source != "kaggle" || len(a.Runtimes) != 0 || a.SizeGiB <= 0 {
			t.Errorf("artifact %+v", a)
		}
	}
	want := []art{
		{"pytorch-1.1-2b-it", "google/gemma/pyTorch/1.1-2b-it", "pyTorch"},
		{"transformers-2b", "google/gemma/transformers/2b", "hf"},
		{"gguf-2b", "google/gemma/gguf/2b", "gguf"},
	}
	if !slices.Equal(arts, want) {
		t.Errorf("artifacts = %+v, want %+v", arts, want)
	}

	b := got["kaggle-tensorflow--blazeface"]
	if len(b.Tasks) != 0 || len(b.Artifacts) != 1 || b.Artifacts[0].Format != "tfJs" || b.License != "Apache 2.0" {
		t.Errorf("blazeface = %+v", b)
	}
}

func TestKaggleLimit(t *testing.T) {
	fs := newFixtureServer(t, kaggleRoute)
	models, err := (&Kaggle{BaseURL: fs.URL}).Fetch(context.Background(), opts(fs, 1))
	if err != nil {
		t.Fatal(err)
	}
	reqs := fs.requests()
	if len(models) != 1 || models[0].ID != "kaggle-google--gemma" || len(reqs) != 1 || reqs[0].URL.Query().Get("pageSize") != "1" {
		t.Errorf("models %d, requests %d", len(models), len(reqs))
	}
}

func TestKaggleListingError(t *testing.T) {
	fs := newFixtureServer(t, func(r *http.Request) (string, int) {
		if r.URL.Query().Get("pageToken") == "" {
			return "kaggle_page1.json", 0
		}
		return "", http.StatusTooManyRequests
	})
	if _, err := (&Kaggle{BaseURL: fs.URL}).Fetch(context.Background(), opts(fs, 100)); err == nil {
		t.Error("no error when a page fails")
	}
}
