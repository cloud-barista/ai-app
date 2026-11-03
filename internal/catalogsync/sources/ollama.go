package sources

import (
	"context"
	"net/url"
	"slices"
	"strings"

	"github.com/ish-hcc/ai-agent-proto/internal/catalogsync"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// ollamaManifestType is the manifest media type the Ollama registry serves.
const ollamaManifestType = "application/vnd.docker.distribution.manifest.v2+json"

// Ollama lists the Ollama library and reads each model's manifest and config
// from the Ollama registry.
type Ollama struct {
	// LibraryURL is the address of the library listing, without a trailing
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

type ollamaTags struct {
	Models []struct {
		Name         string   `json:"name"`
		Capabilities []string `json:"capabilities"`
	} `json:"models"`
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

// Fetch implements catalogsync.Source. It reads the first opt.Limit library
// models in listing order. A model whose manifest or config cannot be read,
// such as a cloud-only model with no weights in the registry, is left out.
func (s *Ollama) Fetch(ctx context.Context, opt catalogsync.Options) ([]model.Model, error) {
	var tags ollamaTags
	if err := getJSON(ctx, opt, s.LibraryURL+"/api/tags", "", &tags); err != nil {
		return nil, err
	}
	listed := tags.Models
	if n := limit(opt); len(listed) > n {
		listed = listed[:n]
	}

	results := make([]*model.Model, len(listed))
	forEach(ctx, len(listed), fetchWorkers, func(i int) {
		results[i] = s.fetchOne(ctx, opt, listed[i].Name, listed[i].Capabilities)
	})
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var out []model.Model
	for _, m := range results {
		if m != nil {
			out = append(out, *m)
		}
	}
	return keepValid(out), nil
}

func (s *Ollama) fetchOne(ctx context.Context, opt catalogsync.Options, name string, capabilities []string) *model.Model {
	repo, tag, found := strings.Cut(name, ":")
	if !found {
		tag = "latest"
	}
	if repo == "" || tag == "" {
		return nil
	}
	base := s.RegistryURL + "/v2/library/" + url.PathEscape(repo)
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
		ID:        "ollama",
		Source:    model.OriginOllama,
		Ref:       repo + ":" + tag,
		Format:    format,
		Precision: cfg.FileType,
		Runtimes:  []model.RuntimeCompat{},
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
	task := ollamaTask(capabilities)
	return &model.Model{
		ID:              catalogsync.ModelID("ollama", name),
		Name:            name,
		Family:          cfg.ModelFamily,
		Parameters:      cfg.ModelType,
		Tasks:           []string{task},
		InputModalities: modalities(task),
		Artifacts:       []model.Artifact{art},
		Origin:          model.OriginOllama,
	}
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
