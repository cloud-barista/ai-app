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
// is read per model from its file list instead.
var hfExpand = []string{"safetensors", "cardData", "config", "pipeline_tag", "library_name", "tags"}

var vllmArchSet = func() map[string]bool {
	set := make(map[string]bool, len(vllmArchitectures))
	for _, a := range vllmArchitectures {
		set[a] = true
	}
	return set
}()

// hfWorkers is how many file list reads run at once.
const hfWorkers = 8

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
		License     json.RawMessage `json:"license"`
		Description json.RawMessage `json:"description"`
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

// hfFiles is the file list of one repository, as GET /api/models/<id>?blobs=true
// returns it.
type hfFiles struct {
	Siblings []struct {
		RFilename string `json:"rfilename"`
		Size      int64  `json:"size"`
	} `json:"siblings"`
}

// Fetch implements catalogsync.Source. It lists up to opt.Limit repositories
// per pipeline tag; a repository listed under two tags is one model. The
// download size of a repository with safetensors is the sum of its
// *.safetensors files, read from its file list; it is left unknown when the
// file list cannot be read.
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
	var sized []int
	for i := range out {
		if out[i].Artifacts[0].Parameters != "" {
			sized = append(sized, i)
		}
	}
	forEach(ctx, len(sized), hfWorkers, func(k int) {
		a := &out[sized[k]].Artifacts[0]
		a.SizeGiB = s.safetensorsGiB(ctx, opt, a.Ref)
	})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return keepValid(out), nil
}

// safetensorsGiB returns the summed size of the *.safetensors files of
// repository ref in GiB, or zero when the file list cannot be read.
func (s *HuggingFace) safetensorsGiB(ctx context.Context, opt catalogsync.Options, ref string) float64 {
	var files hfFiles
	parts := strings.Split(ref, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	if err := getJSON(ctx, opt, s.BaseURL+"/api/models/"+strings.Join(parts, "/")+"?blobs=true", "", &files); err != nil {
		return 0
	}
	var total int64
	for _, f := range files.Siblings {
		if strings.HasSuffix(f.RFilename, ".safetensors") {
			total += f.Size
		}
	}
	return gib(total)
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
		Description:     hfDescription(hm.CardData.Description),
		License:         hfLicense(hm.CardData.License),
		Origin:          model.OriginHuggingFace,
	}
	if st := hm.Safetensors; st != nil && st.Total > 0 {
		m.Parameters = shortCount(st.Total)
		art.Parameters = m.Parameters
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

// hfDescription reads cardData.description when it is a string. The listing
// gives one only when the model card states it in its front matter.
func hfDescription(raw json.RawMessage) string {
	var s string
	if len(raw) == 0 || json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return strings.TrimSpace(s)
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
