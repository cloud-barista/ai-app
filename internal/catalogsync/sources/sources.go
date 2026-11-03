// Package sources holds the catalogsync.Source implementations, one per
// public model source. Each copies what its source states and attaches a
// runtime to an artifact only by a mechanical rule.
package sources

//go:generate go run ./gen -version v0.32.0 -o vllm_archs.go

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/ish-hcc/ai-agent-proto/internal/catalogsync"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// userAgent is sent with every request so that the sources can tell this
// service apart.
const userAgent = "ai-agent-proto-catalogsync"

// maxBody bounds a response body read into memory.
const maxBody = 64 << 20

// defaultLimit is the listing cap used when Options.Limit is not positive.
const defaultLimit = 100

// limit returns opt.Limit, or defaultLimit when it is not positive.
func limit(opt catalogsync.Options) int {
	if opt.Limit > 0 {
		return opt.Limit
	}
	return defaultLimit
}

// fetchWorkers is how many per-model requests a source runs at once.
const fetchWorkers = 4

// Names lists every source origin New accepts, in the default order.
func Names() []string {
	return []string{model.OriginHuggingFace, model.OriginOllama, model.OriginDockerModelRunner,
		model.OriginNGC, model.OriginKaggle}
}

// New returns the sources named by origin, in the order given. An unknown
// or repeated name is an error.
func New(names []string) ([]catalogsync.Source, error) {
	out := make([]catalogsync.Source, 0, len(names))
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			return nil, fmt.Errorf("catalog source %q is listed twice", name)
		}
		seen[name] = true
		switch name {
		case model.OriginHuggingFace:
			out = append(out, NewHuggingFace())
		case model.OriginOllama:
			out = append(out, NewOllama())
		case model.OriginDockerModelRunner:
			out = append(out, NewDockerModelRunner())
		case model.OriginNGC:
			out = append(out, NewNGC())
		case model.OriginKaggle:
			out = append(out, NewKaggle())
		default:
			return nil, fmt.Errorf("unknown catalog source %q (known: %s)", name, strings.Join(Names(), ", "))
		}
	}
	return out, nil
}

// getJSON GETs url and decodes a 200 response into out.
func getJSON(ctx context.Context, opt catalogsync.Options, url, accept string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	if accept == "" {
		accept = "application/json"
	}
	req.Header.Set("Accept", accept)
	resp, err := opt.HTTPClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(out); err != nil {
		return fmt.Errorf("GET %s: decode: %w", url, err)
	}
	return nil
}

// forEach calls fn for 0..n-1 with at most workers calls at once and stops
// starting new calls once ctx is done.
func forEach(ctx context.Context, n, workers int, fn func(i int)) {
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i := range n {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			fn(i)
		}()
	}
	wg.Wait()
}

// keepValid drops the models model.Validate refuses, so that one odd entry
// at a source does not fail the whole fetch.
func keepValid(models []model.Model) []model.Model {
	return slices.DeleteFunc(models, func(m model.Model) bool { return m.Validate() != nil })
}

var artifactIDUnsafe = regexp.MustCompile(`[^a-z0-9._-]+`)

// artifactID turns raw into an artifact ID that is not in seen yet and
// records it there. Characters outside [a-z0-9._-] become "-"; an ID that is
// taken or too long gets 8 hex characters of raw's SHA-256 as a tail.
func artifactID(raw string, seen map[string]bool) string {
	id := artifactIDUnsafe.ReplaceAllString(strings.ToLower(raw), "-")
	id = strings.Trim(id, "-._")
	if id == "" {
		id = "artifact"
	}
	if len(id) > 63 || seen[id] {
		sum := sha256.Sum256([]byte(raw))
		tail := "-" + hex.EncodeToString(sum[:])[:8]
		if len(id) > 63-len(tail) {
			id = strings.TrimRight(id[:63-len(tail)], "-._")
		}
		id += tail
	}
	seen[id] = true
	return id
}

// gib converts bytes to GiB rounded to one decimal. Zero or less is zero.
func gib(bytes int64) float64 {
	if bytes <= 0 {
		return 0
	}
	return math.Round(float64(bytes)/(1<<30)*10) / 10
}

// shortCount writes a parameter count the way model cards do, such as
// 8.2B or 137M. It rounds for display only.
func shortCount(n int64) string {
	units := []struct {
		size   float64
		suffix string
	}{{1e12, "T"}, {1e9, "B"}, {1e6, "M"}, {1e3, "K"}}
	for _, u := range units {
		if float64(n) >= u.size {
			s := fmt.Sprintf("%.1f", float64(n)/u.size)
			return strings.TrimSuffix(s, ".0") + u.suffix
		}
	}
	if n <= 0 {
		return ""
	}
	return fmt.Sprint(n)
}

// Tasks and the input they take.
const (
	taskChat       = "chat"
	taskVisionChat = "vision-chat"
	taskEmbedding  = "embedding"
)

// modalities returns the input modalities a task implies.
func modalities(task string) []string {
	switch task {
	case taskVisionChat:
		return []string{"text", "image"}
	case taskChat, taskEmbedding:
		return []string{"text"}
	}
	return nil
}

// pipelineTasks maps the Hugging Face pipeline tags the catalog collects to
// catalog tasks. Kaggle task tags use the same names.
var pipelineTasks = map[string]string{
	"text-generation":    taskChat,
	"image-text-to-text": taskVisionChat,
	"feature-extraction": taskEmbedding,
}

// namesEmbedding reports whether any of the texts says "embed". Sources that
// state no task are given the embedding task only when their own name or
// labels say so.
func namesEmbedding(texts ...string) bool {
	for _, t := range texts {
		if strings.Contains(strings.ToLower(t), "embed") {
			return true
		}
	}
	return false
}
