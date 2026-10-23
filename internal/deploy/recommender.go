// Package deploy turns AI application metadata into CB-Tumblebug requests.
package deploy

import (
	"context"
	"errors"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// defaultSpecCandidates bounds a recommendation so an agent gets a choice it can
// reason about without paging through hundreds of near identical specs.
const defaultSpecCandidates = 5

// ErrNoRecommender reports that a plan needed a recommendation and no
// recommender API is configured.
//
// Infrastructure recommendation is the partner institution's area, so this
// service does not recommend on its own. Without AIAPP_RECOMMENDER_URL a caller
// has to pin both the spec and the image.
var ErrNoRecommender = errors.New("no recommender configured: pin specId and imageId")

// Recommender decides where an application goes: which node spec and which
// node image.
//
// The only implementation calls the partner recommender API. Without one the
// Service has no recommender and every plan must pin its spec and image.
type Recommender interface {
	RecommendSpecs(ctx context.Context, app *model.AppSpec, limit int) ([]model.SpecCandidate, error)
	ResolveImage(ctx context.Context, app *model.AppSpec, specID string) (*ImageChoice, error)
}

// ImageChoice is the image selected for a spec, with why it was selected.
type ImageChoice struct {
	ID       string `json:"id"`
	OSType   string `json:"osType"`
	GPUImage bool   `json:"gpuImage"`
	// Vendor is the accelerator vendor the image was matched against, when known.
	Vendor string `json:"vendor,omitempty"`
	// Source is where the recommender says the image came from.
	Source string `json:"source,omitempty"`
	// Reason explains the pick so an agent can report it and an operator can argue with it.
	Reason string `json:"reason"`
}

// sourceNamer is implemented by a backend that wants its name on the plan.
type sourceNamer interface {
	SourceName() string
}

// sourceOf qualifies a plan source value with the backend that produced it.
func sourceOf(base string, backend any) string {
	if named, ok := backend.(sourceNamer); ok {
		return base + ":" + named.SourceName()
	}
	return base
}

// RecommendSpecs asks the recommender API for node specs that satisfy an application.
func (s *Service) RecommendSpecs(ctx context.Context, app *model.AppSpec, limit int) ([]model.SpecCandidate, error) {
	if s.recommender == nil {
		return nil, ErrNoRecommender
	}
	return s.recommender.RecommendSpecs(ctx, app, limit)
}

// ResolveImage asks the recommender API for the node image of an application on a given spec.
func (s *Service) ResolveImage(ctx context.Context, app *model.AppSpec, specID string) (*ImageChoice, error) {
	if s.recommender == nil {
		return nil, ErrNoRecommender
	}
	return s.recommender.ResolveImage(ctx, app, specID)
}
