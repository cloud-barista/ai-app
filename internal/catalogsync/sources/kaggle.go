package sources

import (
	"context"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/ish-hcc/ai-agent-proto/internal/catalogsync"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// kaggleMaxPage is the largest page the model listing returns; it answers a
// larger pageSize with 50 entries.
const kaggleMaxPage = 50

// kaggleFormats maps the Kaggle frameworks the built-in runtimes have a
// format for. Other frameworks are kept as Kaggle writes them.
var kaggleFormats = map[string]string{
	"transformers": model.FormatHF,
	"gguf":         model.FormatGGUF,
}

// Kaggle lists the most downloaded public Kaggle models.
type Kaggle struct {
	// BaseURL is the Kaggle address, without a trailing slash.
	BaseURL string
}

// NewKaggle returns the source for kaggle.com.
func NewKaggle() *Kaggle {
	return &Kaggle{BaseURL: "https://www.kaggle.com"}
}

// Name implements catalogsync.Source.
func (s *Kaggle) Name() string { return model.OriginKaggle }

type kagglePage struct {
	Models        []kaggleModel `json:"models"`
	NextPageToken string        `json:"nextPageToken"`
}

type kaggleModel struct {
	Ref      string `json:"ref"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	Author   string `json:"author"`
	Tags     []struct {
		FullPath string `json:"fullPath"`
	} `json:"tags"`
	Instances []struct {
		Slug                   string `json:"slug"`
		Framework              string `json:"framework"`
		LicenseName            string `json:"licenseName"`
		TotalUncompressedBytes int64  `json:"totalUncompressedBytes"`
	} `json:"instances"`
}

// Fetch implements catalogsync.Source. It pages through the listing sorted
// by downloads until opt.Limit models are read. A model with no instance is
// left out.
func (s *Kaggle) Fetch(ctx context.Context, opt catalogsync.Options) ([]model.Model, error) {
	n := limit(opt)
	var listed []kaggleModel
	token := ""
	for len(listed) < n {
		q := url.Values{}
		q.Set("pageSize", strconv.Itoa(min(n-len(listed), kaggleMaxPage)))
		q.Set("sortBy", "downloadCount")
		if token != "" {
			q.Set("pageToken", token)
		}
		var p kagglePage
		if err := getJSON(ctx, opt, s.BaseURL+"/api/v1/models/list?"+q.Encode(), "", &p); err != nil {
			return nil, err
		}
		listed = append(listed, p.Models...)
		if p.NextPageToken == "" || len(p.Models) == 0 {
			break
		}
		token = p.NextPageToken
	}
	if len(listed) > n {
		listed = listed[:n]
	}

	var out []model.Model
	for _, km := range listed {
		if m, ok := kaggleToModel(km); ok {
			out = append(out, m)
		}
	}
	return keepValid(out), nil
}

func kaggleToModel(km kaggleModel) (model.Model, bool) {
	if km.Ref == "" || len(km.Instances) == 0 {
		return model.Model{}, false
	}
	name := km.Title
	if name == "" {
		name = km.Ref
	}
	// The subtitle is the one line summary; the description field is the
	// full model card in Markdown and is not copied.
	m := model.Model{
		ID:          catalogsync.ModelID("kaggle", km.Ref),
		Name:        name,
		Description: strings.TrimSpace(km.Subtitle),
		Publisher:   km.Author,
		Origin:      model.OriginKaggle,
	}
	seen := map[string]bool{}
	for _, inst := range km.Instances {
		if inst.Framework == "" || inst.Slug == "" {
			continue
		}
		format, ok := kaggleFormats[inst.Framework]
		if !ok {
			format = inst.Framework
		}
		if m.License == "" {
			m.License = inst.LicenseName
		}
		m.Artifacts = append(m.Artifacts, model.Artifact{
			ID:       artifactID(inst.Framework+"-"+inst.Slug, seen),
			Source:   model.OriginKaggle,
			Ref:      km.Ref + "/" + inst.Framework + "/" + inst.Slug,
			Format:   format,
			SizeGiB:  gib(inst.TotalUncompressedBytes),
			Runtimes: []model.RuntimeCompat{},
		})
	}
	if len(m.Artifacts) == 0 {
		return model.Model{}, false
	}
	// Kaggle task tags read "task > text-generation"; the ones with the
	// Hugging Face pipeline names the catalog collects become tasks.
	for _, t := range km.Tags {
		name, found := strings.CutPrefix(t.FullPath, "task > ")
		if !found {
			continue
		}
		task, ok := pipelineTasks[name]
		if !ok || slices.Contains(m.Tasks, task) {
			continue
		}
		m.Tasks = append(m.Tasks, task)
		for _, mod := range modalities(task) {
			if !slices.Contains(m.InputModalities, mod) {
				m.InputModalities = append(m.InputModalities, mod)
			}
		}
	}
	return m, true
}
