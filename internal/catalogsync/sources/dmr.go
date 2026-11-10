package sources

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/ish-hcc/ai-agent-proto/internal/catalogsync"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// dmrNamespace is the Docker Hub namespace Docker Model Runner models are
// published in.
const dmrNamespace = "ai"

// Artifact formats a Docker Model Runner tag names that the built-in
// runtimes do not load.
const (
	formatSafetensors = "safetensors"
	formatMLX         = "mlx"
)

// ggufQuant matches the tag part that names a GGUF quantization, such as
// Q4_K_M, q8_0, IQ4_XS, F16, BF16 or MXFP4.
var ggufQuant = regexp.MustCompile(`(?i)^(i?q[0-9][a-z0-9_]*|f16|f32|bf16|mxfp4)$`)

// DockerModelRunner lists the Docker Model Runner models in the ai namespace
// of Docker Hub, one artifact per tag.
type DockerModelRunner struct {
	// HubURL is the Docker Hub address, without a trailing slash.
	HubURL string
}

// NewDockerModelRunner returns the source for Docker Hub's ai namespace.
func NewDockerModelRunner() *DockerModelRunner {
	return &DockerModelRunner{HubURL: "https://hub.docker.com"}
}

// Name implements catalogsync.Source.
func (s *DockerModelRunner) Name() string { return model.OriginDockerModelRunner }

type dmrRepoPage struct {
	Next    string `json:"next"`
	Results []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"results"`
}

type dmrTagPage struct {
	Results []struct {
		Name     string `json:"name"`
		FullSize int64  `json:"full_size"`
	} `json:"results"`
}

// Fetch implements catalogsync.Source. It lists up to opt.Limit repositories.
// Docker Hub refuses anonymous paging past the first 100 entries with 403;
// a refused later page ends the listing with what was read so far. A
// repository whose tags cannot be read is left out.
func (s *DockerModelRunner) Fetch(ctx context.Context, opt catalogsync.Options) ([]model.Model, error) {
	n := limit(opt)
	pageSize := min(n, 100)
	var names, descriptions []string
	for page := 1; len(names) < n; page++ {
		u := fmt.Sprintf("%s/v2/namespaces/%s/repositories?page_size=%d&page=%d", s.HubURL, dmrNamespace, pageSize, page)
		var p dmrRepoPage
		if err := getJSON(ctx, opt, u, "", &p); err != nil {
			if page == 1 {
				return nil, err
			}
			break
		}
		for _, r := range p.Results {
			if len(names) < n && r.Name != "" {
				names = append(names, r.Name)
				descriptions = append(descriptions, strings.TrimSpace(r.Description))
			}
		}
		if p.Next == "" || len(p.Results) == 0 {
			break
		}
	}

	results := make([]*model.Model, len(names))
	forEach(ctx, len(names), fetchWorkers, func(i int) {
		results[i] = s.fetchOne(ctx, opt, names[i], descriptions[i])
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

func (s *DockerModelRunner) fetchOne(ctx context.Context, opt catalogsync.Options, name, description string) *model.Model {
	u := fmt.Sprintf("%s/v2/namespaces/%s/repositories/%s/tags?page_size=100", s.HubURL, dmrNamespace, url.PathEscape(name))
	var tags dmrTagPage
	if err := getJSON(ctx, opt, u, "", &tags); err != nil {
		return nil
	}
	seen := map[string]bool{}
	var arts []model.Artifact
	for _, t := range tags.Results {
		format, precision := dmrTagFormat(name, t.Name)
		if format == "" {
			continue
		}
		arts = append(arts, model.Artifact{
			ID:        artifactID(t.Name, seen),
			Source:    model.OriginDockerModelRunner,
			Ref:       dmrNamespace + "/" + name + ":" + t.Name,
			Format:    format,
			Precision: precision,
			SizeGiB:   gib(t.FullSize),
			Runtimes:  []model.RuntimeCompat{},
		})
	}
	if len(arts) == 0 {
		return nil
	}
	m := &model.Model{
		ID:          catalogsync.ModelID("dmr", name),
		Name:        name,
		Description: description,
		Artifacts:   arts,
		Origin:      model.OriginDockerModelRunner,
	}
	// Docker Hub states no task. The only task given is embedding, and only
	// when the repository name says "embed"; that is a guess from the name.
	if namesEmbedding(name) {
		m.Tasks = []string{taskEmbedding}
		m.InputModalities = modalities(taskEmbedding)
	}
	return m
}

// dmrTagFormat reads the artifact format and precision a tag names. Tags are
// "<size>-<quantization>", "<size>-safetensors", "<size>-mlx-<bits>",
// "<size>" or "latest". A repository named "*-vllm" or "*-safetensors" holds
// safetensors in every tag. A tag that names no format in a repository that
// does not either is skipped: format "" is returned.
func dmrTagFormat(repo, tag string) (format, precision string) {
	repoFormat := ""
	if strings.HasSuffix(repo, "-vllm") || strings.HasSuffix(repo, "-safetensors") {
		repoFormat = formatSafetensors
	}
	parts := strings.Split(tag, "-")
	last := parts[len(parts)-1]
	for i, p := range parts {
		switch strings.ToLower(p) {
		case "safetensors":
			return formatSafetensors, ""
		case "mlx":
			if i+1 < len(parts) {
				return formatMLX, strings.Join(parts[i+1:], "-")
			}
			return formatMLX, ""
		}
	}
	if ggufQuant.MatchString(last) {
		if repoFormat != "" {
			return repoFormat, last
		}
		return model.FormatGGUF, last
	}
	return repoFormat, ""
}
