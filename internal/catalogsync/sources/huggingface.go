package sources

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/ish-hcc/ai-agent-proto/internal/catalogsync"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// hfPipelineTags are the pipeline tags listed, in this order.
var hfPipelineTags = []string{"text-generation", "image-text-to-text", "feature-extraction"}

// hfExpand are the fields asked of the listing. The listing refuses keys it
// does not know with 400; usedStorage is one of them, so the download size
// is not known.
var hfExpand = []string{"safetensors", "cardData", "config", "pipeline_tag", "library_name", "tags"}

var vllmArchSet = func() map[string]bool {
	set := make(map[string]bool, len(vllmArchitectures))
	for _, a := range vllmArchitectures {
		set[a] = true
	}
	return set
}()

// HuggingFace lists the most downloaded Hugging Face repositories of each
// collected pipeline tag.
type HuggingFace struct {
	// BaseURL is the Hub address, without a trailing slash.
	BaseURL string
}

// NewHuggingFace returns the source for huggingface.co.
func NewHuggingFace() *HuggingFace {
	return &HuggingFace{BaseURL: "https://huggingface.co"}
}

// Name implements catalogsync.Source.
func (s *HuggingFace) Name() string { return model.OriginHuggingFace }

type hfModel struct {
	ID       string `json:"id"`
	CardData struct {
		License json.RawMessage `json:"license"`
	} `json:"cardData"`
	Config struct {
		Architectures []string `json:"architectures"`
		ModelType     string   `json:"model_type"`
	} `json:"config"`
	Safetensors *struct {
		Parameters map[string]int64 `json:"parameters"`
		Total      int64            `json:"total"`
	} `json:"safetensors"`
	PipelineTag string `json:"pipeline_tag"`
}

// Fetch implements catalogsync.Source. It lists up to opt.Limit repositories
// per pipeline tag; a repository listed under two tags is one model.
func (s *HuggingFace) Fetch(ctx context.Context, opt catalogsync.Options) ([]model.Model, error) {
	var out []model.Model
	index := map[string]int{}
	for _, tag := range hfPipelineTags {
		q := url.Values{}
		q.Set("pipeline_tag", tag)
		q.Set("sort", "downloads")
		q.Set("direction", "-1")
		q.Set("limit", strconv.Itoa(limit(opt)))
		for _, e := range hfExpand {
			q.Add("expand[]", e)
		}
		var list []hfModel
		if err := getJSON(ctx, opt, s.BaseURL+"/api/models?"+q.Encode(), "", &list); err != nil {
			return nil, err
		}
		for _, hm := range list {
			m, ok := hfToModel(hm, pipelineTasks[tag])
			if !ok {
				continue
			}
			if i, dup := index[m.ID]; dup {
				if !slices.Contains(out[i].Tasks, m.Tasks[0]) {
					out[i].Tasks = append(out[i].Tasks, m.Tasks[0])
				}
				for _, mod := range m.InputModalities {
					if !slices.Contains(out[i].InputModalities, mod) {
						out[i].InputModalities = append(out[i].InputModalities, mod)
					}
				}
				continue
			}
			index[m.ID] = len(out)
			out = append(out, m)
		}
	}
	return keepValid(out), nil
}

func hfToModel(hm hfModel, task string) (model.Model, bool) {
	if hm.ID == "" {
		return model.Model{}, false
	}
	publisher, name, found := strings.Cut(hm.ID, "/")
	if !found {
		publisher, name = "", hm.ID
	}
	art := model.Artifact{
		ID:       "hf",
		Source:   model.OriginHuggingFace,
		Ref:      hm.ID,
		Format:   model.FormatHF,
		Runtimes: []model.RuntimeCompat{},
	}
	m := model.Model{
		ID:              catalogsync.ModelID("hf", hm.ID),
		Name:            name,
		Publisher:       publisher,
		Family:          hm.Config.ModelType,
		Tasks:           []string{task},
		InputModalities: modalities(task),
		License:         hfLicense(hm.CardData.License),
		Origin:          model.OriginHuggingFace,
	}
	if st := hm.Safetensors; st != nil && st.Total > 0 {
		m.Parameters = shortCount(st.Total)
		art.Precision = topDtype(st.Parameters)
		if slices.ContainsFunc(hm.Config.Architectures, func(a string) bool { return vllmArchSet[a] }) {
			art.Runtimes = []model.RuntimeCompat{{RuntimeID: model.RuntimeVLLM}}
		}
	}
	m.Artifacts = []model.Artifact{art}
	return m, true
}

// topDtype returns the dtype holding the most parameters, as written. Ties
// go to the dtype that sorts first.
func topDtype(params map[string]int64) string {
	best, bestN := "", int64(-1)
	for dtype, n := range params {
		if n > bestN || (n == bestN && dtype < best) {
			best, bestN = dtype, n
		}
	}
	return best
}

// hfLicense reads cardData.license, which is a string or a list of strings.
func hfLicense(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return strings.Join(list, ",")
	}
	return ""
}
