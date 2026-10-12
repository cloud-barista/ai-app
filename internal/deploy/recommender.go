package deploy

import (
	"context"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

// Recommender decides where an application goes: which node spec and which
// node image.
//
// The local implementation filters the CB-Tumblebug spec catalog and matches
// images by accelerator vendor. A remote implementation hands the same decision
// to an external placement service.
type Recommender interface {
	RecommendSpecs(ctx context.Context, app *model.AppSpec, limit int) ([]model.SpecCandidate, error)
	ResolveImage(ctx context.Context, app *model.AppSpec, specID string) (*ImageChoice, error)
}

// sourceNamer is implemented by a backend that wants its name on the plan.
//
// The local backends do not implement it, so the plan keeps the source values
// it always had and only a remote decision is marked as such.
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

// localRecommender recommends from the CB-Tumblebug spec and image catalogs.
type localRecommender struct {
	tumblebug *tumblebug.Client
	// imageNamespace holds the shared image catalog images are searched in.
	imageNamespace string
	// specNamespace holds the shared spec catalog specs are read from.
	specNamespace string
	// defaultOSType is the image family used when an application does not name one.
	defaultOSType string
}

// RecommendSpecs finds node specs that satisfy an application.
func (s *Service) RecommendSpecs(ctx context.Context, app *model.AppSpec, limit int) ([]model.SpecCandidate, error) {
	return s.recommender.RecommendSpecs(ctx, app, limit)
}

// ResolveImage picks a node image for an application on a given spec.
func (s *Service) ResolveImage(ctx context.Context, app *model.AppSpec, specID string) (*ImageChoice, error) {
	return s.recommender.ResolveImage(ctx, app, specID)
}
