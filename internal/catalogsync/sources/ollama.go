package sources

import (
	"context"
	"html"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/ish-hcc/ai-agent-proto/internal/catalogsync"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// ollamaManifestType is the manifest media type the Ollama registry serves.
const ollamaManifestType = "application/vnd.docker.distribution.manifest.v2+json"

// ollamaWorkers is how many manifest and config reads run at once.
const ollamaWorkers = 8

// The library page lists each model as an <li> holding a link to
// /library/<name>, a <p> with the model description, then capability spans
// (text-indigo-600: tools, thinking, vision, embedding ...) and size spans
// (text-blue-600: 0.6b, 8b ...). The description is the first <p> holding
// text only; the pull count <p> after it holds spans.
var (
	ollamaItem = regexp.MustCompile(`(?s)<li\b[^>]*>\s*<a href="/library/([A-Za-z0-9._-]+)"(.*?)</li>`)
	ollamaSpan = regexp.MustCompile(`<span\s[^>]*class="[^"]*\btext-(indigo|blue)-600\b[^"]*"[^>]*>([^<]*)</span>`)
	ollamaDesc = regexp.MustCompile(`<p\b[^>]*>([^<]*)</p>`)
)

// Ollama lists the Ollama library page and reads each size tag's manifest
// and config from the Ollama registry.
//
// The JSON listing at ollama.com/api/tags names only about 25 models, most
// of them cloud models with no weights in the registry; the library page
// names the downloadable ones.
type Ollama struct {
	// LibraryURL is the address of the library page, without a trailing
	// slash.
	LibraryURL string
	// RegistryURL is the address of the model registry, without a trailing
	// slash.
	RegistryURL string
}

// NewOllama returns the source for the Ollama library.
func NewOllama() *Ollama {
	return &Ollama{LibraryURL: "https://ollama.com", RegistryURL: "https://registry.ollama.ai"}
}

// Name implements catalogsync.Source.
func (s *Ollama) Name() string { return model.OriginOllama }

// ollamaEntry is one model on the library page.
type ollamaEntry struct {
	name         string
	description  string
	capabilities []string
	sizes        []string
}

type ollamaManifest struct {
	Config struct {
		Digest string `json:"digest"`
	} `json:"config"`
	Layers []struct {
		MediaType string `json:"mediaType"`
		Size      int64  `json:"size"`
	} `json:"layers"`
}

type ollamaConfig struct {
	ModelFormat string `json:"model_format"`
	ModelFamily string `json:"model_family"`
	ModelType   string `json:"model_type"`
	FileType    string `json:"file_type"`
}

// ollamaTag is what the registry states about one tag of a model.
type ollamaTag struct {
	artifact   model.Artifact
	family     string
	parameters string
}

// Fetch implements catalogsync.Source. It takes the first opt.Limit models
// of the library page sorted by popularity, in page order. Each size tag is
// one artifact; a model with no size tag has the latest tag only. A tag
// whose manifest or config cannot be read is left out, and so is a model
// none of whose tags can be read, such as a cloud model.
func (s *Ollama) Fetch(ctx context.Context, opt catalogsync.Options) ([]model.Model, error) {
	page, err := getBody(ctx, opt, s.LibraryURL+"/library?sort=popular", "text/html")
	if err != nil {
		return nil, err
	}
	entries := parseOllamaLibrary(string(page))
	if n := limit(opt); len(entries) > n {
		entries = entries[:n]
	}

	type job struct{ entry, tag int }
	var jobs []job
	tags := make([][]string, len(entries))
	for i, e := range entries {
		tags[i] = e.sizes
		if len(tags[i]) == 0 {
			tags[i] = []string{"latest"}
		}
		for j := range tags[i] {
			jobs = append(jobs, job{i, j})
		}
	}
	results := make([][]*ollamaTag, len(entries))
	for i := range entries {
		results[i] = make([]*ollamaTag, len(tags[i]))
	}
	forEach(ctx, len(jobs), ollamaWorkers, func(k int) {
		jb := jobs[k]
		results[jb.entry][jb.tag] = s.fetchTag(ctx, opt, entries[jb.entry].name, tags[jb.entry][jb.tag])
	})
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var out []model.Model
	for i, e := range entries {
		var found []*ollamaTag
		for _, t := range results[i] {
			if t != nil {
				found = append(found, t)
			}
		}
		if len(found) == 0 {
			continue
		}
		task := ollamaTask(e.capabilities)
		m := model.Model{
			ID:              catalogsync.ModelID("ollama", e.name),
			Name:            e.name,
			Description:     e.description,
			Family:          found[0].family,
			Tasks:           []string{task},
			InputModalities: modalities(task),
			Origin:          model.OriginOllama,
		}
		// Model.Parameters is one value; it is set only when the model has
		// one artifact, since each size tag has its own count. Each
		// artifact carries the count of its tag.
		if len(found) == 1 {
			m.Parameters = found[0].parameters
		}
		seen := map[string]bool{}
		for _, t := range found {
			a := t.artifact
			a.ID = artifactID(strings.TrimPrefix(a.Ref, e.name+":"), seen)
			m.Artifacts = append(m.Artifacts, a)
		}
		out = append(out, m)
	}
	return keepValid(out), nil
}

// parseOllamaLibrary reads the model entries of the library page in page
// order. A name listed twice is taken once.
func parseOllamaLibrary(page string) []ollamaEntry {
	var out []ollamaEntry
	seen := map[string]bool{}
	for _, item := range ollamaItem.FindAllStringSubmatch(page, -1) {
		e := ollamaEntry{name: item[1]}
		if seen[e.name] {
			continue
		}
		seen[e.name] = true
		if d := ollamaDesc.FindStringSubmatch(item[2]); d != nil {
			e.description = strings.TrimSpace(html.UnescapeString(d[1]))
		}
		for _, span := range ollamaSpan.FindAllStringSubmatch(item[2], -1) {
			text := strings.TrimSpace(html.UnescapeString(span[2]))
			if text == "" {
				continue
			}
			if span[1] == "indigo" {
				e.capabilities = append(e.capabilities, text)
			} else if !slices.Contains(e.sizes, text) {
				e.sizes = append(e.sizes, text)
			}
		}
		out = append(out, e)
	}
	return out
}

func (s *Ollama) fetchTag(ctx context.Context, opt catalogsync.Options, name, tag string) *ollamaTag {
	base := s.RegistryURL + "/v2/library/" + url.PathEscape(name)
	var man ollamaManifest
	if err := getJSON(ctx, opt, base+"/manifests/"+url.PathEscape(tag), ollamaManifestType, &man); err != nil {
		return nil
	}
	if man.Config.Digest == "" {
		return nil
	}
	var cfg ollamaConfig
	if err := getJSON(ctx, opt, base+"/blobs/"+url.PathEscape(man.Config.Digest), "", &cfg); err != nil {
		return nil
	}

	format := cfg.ModelFormat
	if format == "" {
		format = model.FormatGGUF
	}
	art := model.Artifact{
		Source:     model.OriginOllama,
		Ref:        name + ":" + tag,
		Format:     format,
		Precision:  cfg.FileType,
		Parameters: cfg.ModelType,
		Runtimes:   []model.RuntimeCompat{},
	}
	for _, l := range man.Layers {
		if strings.HasSuffix(l.MediaType, "image.model") {
			art.SizeGiB = gib(l.Size)
			break
		}
	}
	// The ollama runtime profile loads GGUF only.
	if format == model.FormatGGUF {
		art.Runtimes = []model.RuntimeCompat{{RuntimeID: model.RuntimeOllama}}
	}
	return &ollamaTag{artifact: art, family: cfg.ModelFamily, parameters: cfg.ModelType}
}

// ollamaTask picks one task from the library capabilities: embedding when
// listed, else vision-chat when vision is listed, else chat.
func ollamaTask(capabilities []string) string {
	switch {
	case slices.Contains(capabilities, "embedding"):
		return taskEmbedding
	case slices.Contains(capabilities, "vision"):
		return taskVisionChat
	}
	return taskChat
}
