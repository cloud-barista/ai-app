// Package catalog stores AI application metadata.
//
// The prototype keeps the catalog in memory. Persisting it is second-year work
// ("large scale AI application repository"), and the interface here is the seam
// that work replaces.
package catalog

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// ErrNotFound is returned when no application matches an identifier.
var ErrNotFound = fmt.Errorf("ai application not found")

// Store holds registered AI application specs, keyed by ID.
type Store struct {
	mu   sync.RWMutex
	apps map[string]model.AppSpec
}

// NewStore builds an empty Store.
func NewStore() *Store {
	return &Store{apps: make(map[string]model.AppSpec)}
}

// Register adds or replaces an application spec.
//
// Replacing is how a new version is published: the ID is the application, the
// Version field records which revision is currently registered.
func (s *Store) Register(ctx context.Context, spec model.AppSpec) (*model.AppSpec, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if len(spec.DeployTarget) == 0 {
		spec.DeployTarget = []model.DeployTarget{model.DeployTargetVM}
	}

	s.mu.Lock()
	_, replaced := s.apps[spec.ID]
	s.apps[spec.ID] = spec
	s.mu.Unlock()

	log.Info().Str("appId", spec.ID).Str("version", spec.Version).
		Str("runtime", string(spec.Runtime)).Str("acceleratorType", spec.Accelerator.Type).
		Bool("replaced", replaced).Msg("Registered AI application")

	stored := spec
	return &stored, nil
}

// Get reads one application spec.
func (s *Store) Get(ctx context.Context, appID string) (*model.AppSpec, error) {
	s.mu.RLock()
	spec, ok := s.apps[appID]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, appID)
	}
	return &spec, nil
}

// List reads every registered application spec, ordered by ID.
func (s *Store) List(ctx context.Context) []model.AppSpec {
	s.mu.RLock()
	specs := make([]model.AppSpec, 0, len(s.apps))
	for _, spec := range s.apps {
		specs = append(specs, spec)
	}
	s.mu.RUnlock()

	sort.Slice(specs, func(i, j int) bool { return specs[i].ID < specs[j].ID })
	return specs
}

// Summaries reads the short form of every registered application, ordered by ID.
func (s *Store) Summaries(ctx context.Context) []model.AppSummary {
	specs := s.List(ctx)
	out := make([]model.AppSummary, 0, len(specs))
	for i := range specs {
		out = append(out, specs[i].Summary())
	}
	return out
}

// Delete removes one application spec.
func (s *Store) Delete(ctx context.Context, appID string) error {
	s.mu.Lock()
	_, ok := s.apps[appID]
	delete(s.apps, appID)
	s.mu.Unlock()

	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, appID)
	}
	log.Info().Str("appId", appID).Msg("Deleted AI application")
	return nil
}
