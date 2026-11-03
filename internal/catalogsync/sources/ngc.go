package sources

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"

	"github.com/ish-hcc/ai-agent-proto/internal/catalogsync"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// NGC lists the public models of the NVIDIA NGC catalog.
type NGC struct {
	// APIURL is the NGC API address, without a trailing slash.
	APIURL string
}

// NewNGC returns the source for the NGC catalog.
func NewNGC() *NGC {
	return &NGC{APIURL: "https://api.ngc.nvidia.com"}
}

// Name implements catalogsync.Source.
func (s *NGC) Name() string { return model.OriginNGC }

type ngcSearch struct {
	Results []struct {
		GroupValue string        `json:"groupValue"`
		Resources  []ngcResource `json:"resources"`
	} `json:"results"`
}

type ngcResource struct {
	ResourceID  string `json:"resourceId"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Labels      []struct {
		Key    string   `json:"key"`
		Values []string `json:"values"`
	} `json:"labels"`
	Attributes []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	} `json:"attributes"`
}

func (r *ngcResource) label(key string) []string {
	for _, l := range r.Labels {
		if l.Key == key {
			return l.Values
		}
	}
	return nil
}

func (r *ngcResource) attribute(key string) string {
	for _, a := range r.Attributes {
		if a.Key == key {
			return a.Value
		}
	}
	return ""
}

// Fetch implements catalogsync.Source. It asks for one page of opt.Limit
// models. The search answers with a scored group and a group per resource
// type that repeat the same resources; each resource is taken once.
func (s *NGC) Fetch(ctx context.Context, opt catalogsync.Options) ([]model.Model, error) {
	n := limit(opt)
	q, err := json.Marshal(map[string]any{"query": "", "page": 0, "pageSize": n})
	if err != nil {
		return nil, err
	}
	var res ngcSearch
	u := s.APIURL + "/v2/search/catalog/resources/MODEL?q=" + url.QueryEscape(string(q))
	if err := getJSON(ctx, opt, u, "", &res); err != nil {
		return nil, err
	}
	var out []model.Model
	seen := map[string]bool{}
	for _, g := range res.Results {
		for i := range g.Resources {
			r := &g.Resources[i]
			if r.ResourceID == "" || seen[r.ResourceID] || len(out) >= n {
				continue
			}
			seen[r.ResourceID] = true
			if m, ok := ngcToModel(r); ok {
				out = append(out, m)
			}
		}
	}
	return keepValid(out), nil
}

func ngcToModel(r *ngcResource) (model.Model, bool) {
	// The format attribute is copied as NGC writes it (SavedModel, NEMO,
	// safetensors, PyTorch PTH and so on). A resource without one is left
	// out because an artifact needs a format.
	format := r.attribute("format")
	if format == "" {
		return model.Model{}, false
	}
	name := r.DisplayName
	if name == "" {
		name = r.Name
	}
	art := model.Artifact{
		ID:       "ngc",
		Source:   model.OriginNGC,
		Ref:      r.ResourceID,
		Format:   format,
		Runtimes: []model.RuntimeCompat{},
	}
	if p := r.label("precision"); len(p) > 0 {
		art.Precision = p[0]
	}
	if size, err := strconv.ParseInt(r.attribute("latestVersionSizeInBytes"), 10, 64); err == nil {
		art.SizeGiB = gib(size)
	}
	m := model.Model{
		ID:        catalogsync.ModelID("ngc", r.ResourceID),
		Name:      name,
		Artifacts: []model.Artifact{art},
		Origin:    model.OriginNGC,
	}
	if p := r.label("publisher"); len(p) > 0 {
		m.Publisher = p[0]
	}
	// NGC states no task: its application attribute is Other for most
	// models and Language Modeling for BERT classifiers as well as for
	// generators, so chat cannot be read from it. Embedding is given only
	// when the name or a label says "embed".
	texts := []string{r.ResourceID, r.DisplayName}
	for _, l := range r.Labels {
		texts = append(texts, l.Values...)
	}
	if namesEmbedding(texts...) {
		m.Tasks = []string{taskEmbedding}
		m.InputModalities = modalities(taskEmbedding)
	}
	return m, true
}
