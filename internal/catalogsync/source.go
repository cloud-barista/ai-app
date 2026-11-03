// Package catalogsync fills the AI model catalog from public model sources
// (Hugging Face, the Ollama library, Docker Model Runner, NVIDIA NGC and
// Kaggle) on a schedule. It copies what a source states, as the source
// states it; choosing which model suits a request is the recommender's job.
package catalogsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"regexp"
	"strings"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// Source reads one public model source.
type Source interface {
	// Name is the origin the source's models are stored with, one of the
	// sync origins in package model (model.OriginHuggingFace and so on).
	Name() string
	// Fetch returns the models the source lists now, each with Origin set
	// to Name(). An artifact lists a runtime only when a mechanical rule
	// says that runtime loads it; otherwise its runtimes are empty and it
	// cannot be deployed.
	Fetch(ctx context.Context, opt Options) ([]model.Model, error)
}

// Options bounds one fetch.
type Options struct {
	// Limit caps how many models one listing returns (per task where the
	// source lists by task). It bounds volume; it is not a quality filter.
	Limit int
	// Client does the HTTP requests. Nil uses http.DefaultClient.
	Client *http.Client
}

// HTTPClient returns the client to use.
func (o Options) HTTPClient() *http.Client {
	if o.Client != nil {
		return o.Client
	}
	return http.DefaultClient
}

var idUnsafe = regexp.MustCompile(`[^a-z0-9._-]+`)

// maxIDLen is the length catalog IDs allow (model.catalogIDPattern).
const maxIDLen = 63

// ModelID builds a catalog ID for a model a source names ref, prefixed so
// that sources never collide: lower case, "/" as "--", other characters
// outside [a-z0-9._-] as "-". An ID that would be too long is cut and ends
// with 8 hex characters of the ref's SHA-256, so two long refs stay apart.
func ModelID(prefix, ref string) string {
	s := strings.ToLower(ref)
	s = strings.ReplaceAll(s, "/", "--")
	s = idUnsafe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-._")
	id := prefix + "-" + s
	if len(id) <= maxIDLen {
		return id
	}
	sum := sha256.Sum256([]byte(ref))
	tail := "-" + hex.EncodeToString(sum[:])[:8]
	return strings.TrimRight(id[:maxIDLen-len(tail)], "-._") + tail
}
