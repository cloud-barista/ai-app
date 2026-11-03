package main

import (
	"context"
	"errors"

	"github.com/ish-hcc/ai-agent-proto/internal/catalogsync"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// errSourcesNotBuilt is what every source reports until the source
// implementations are linked in.
var errSourcesNotBuilt = errors.New("catalog sources are not built yet")

// newSyncSources builds the catalog sync sources named in the configuration.
func newSyncSources(names []string) ([]catalogsync.Source, error) {
	out := make([]catalogsync.Source, 0, len(names))
	for _, name := range names {
		out = append(out, unbuiltSource(name))
	}
	return out, nil
}

// unbuiltSource is a source whose fetch always fails with errSourcesNotBuilt.
type unbuiltSource string

func (s unbuiltSource) Name() string { return string(s) }

func (s unbuiltSource) Fetch(ctx context.Context, opt catalogsync.Options) ([]model.Model, error) {
	return nil, errSourcesNotBuilt
}
