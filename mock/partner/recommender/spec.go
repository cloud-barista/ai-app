// Package recommender is a mock of the infrastructure recommendation API.
//
// Requirement analysis and infrastructure recommendation are the partner
// institution's area. This code is a mock and not part of the Innogrid
// prototype. The AI-App service no longer calls it; it is kept as an example of
// the recommendation contract. It carries the first-year recommendation logic
// (accelerator requirement to a CB-Tumblebug spec filter, cheapest first,
// vendor matched images).
package recommender

import (
	"context"
	"fmt"
	"strconv"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/config"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

// Spec filter metrics CB-Tumblebug accepts. The accelerator metrics are the AI
// semiconductor axis and are the reason this service can stay provider neutral.
const (
	metricAcceleratorType   = "acceleratorType"
	metricAcceleratorModel  = "acceleratorModel"
	metricAcceleratorCount  = "acceleratorCount"
	metricAcceleratorMemory = "acceleratorMemoryGB"
	metricVCPU              = "vCPU"
	metricMemoryGiB         = "memoryGiB"
	metricCost              = "cost"
)

// defaultSpecCandidates bounds a recommendation so an agent gets a choice it can
// reason about without paging through hundreds of near identical specs.
const defaultSpecCandidates = 5

// Recommender recommends from the CB-Tumblebug spec and image catalogs.
type Recommender struct {
	tumblebug *tumblebug.Client
	// imageNamespace holds the shared image catalog images are searched in.
	imageNamespace string
	// specNamespace holds the shared spec catalog specs are read from.
	specNamespace string
	// defaultOSType is the image family used when an application does not name one.
	defaultOSType string
}

// New builds a Recommender over a CB-Tumblebug client.
func New(client *tumblebug.Client, cfg config.TumblebugConfig) *Recommender {
	return &Recommender{
		tumblebug:      client,
		imageNamespace: cfg.ImageNamespace,
		specNamespace:  cfg.SpecNamespace,
		defaultOSType:  cfg.DefaultOSType,
	}
}

// RecommendSpecs asks CB-Tumblebug for node specs that satisfy an application.
//
// The accelerator requirement maps field for field onto the spec filter, so a
// new accelerator vendor needs a catalog entry rather than code here.
func (r *Recommender) RecommendSpecs(ctx context.Context, app *App, limit int) ([]SpecCandidate, error) {
	if limit <= 0 {
		limit = defaultSpecCandidates
	}

	req := buildSpecRequest(app, limit)
	specs, err := r.tumblebug.RecommendSpec(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to recommend specs for %q: %w", app.ID, err)
	}

	log.Info().Str("appId", app.ID).Str("acceleratorType", app.Accelerator.Type).
		Int("minCount", app.Accelerator.MinCount).Int("candidates", len(specs)).
		Msg("Recommended accelerator specs")

	candidates := make([]SpecCandidate, 0, len(specs))
	for _, spec := range specs {
		candidates = append(candidates, SpecCandidate{
			ID:                  spec.ID,
			ProviderName:        spec.ProviderName,
			RegionName:          spec.RegionName,
			AcceleratorType:     spec.AcceleratorType,
			AcceleratorModel:    spec.AcceleratorModel,
			AcceleratorCount:    spec.AcceleratorCount,
			AcceleratorMemoryGB: spec.AcceleratorMemoryGB,
			VCPU:                spec.VCPU,
			MemoryGiB:           spec.MemoryGiB,
			CostPerHour:         spec.CostPerHour,
		})
	}
	return candidates, nil
}

// buildSpecRequest converts an application requirement into a spec filter.
func buildSpecRequest(app *App, limit int) *tumblebug.RecommendSpecReq {
	policy := make([]tumblebug.FilterCondition, 0, 6)
	// An application with no accelerator type runs on a CPU node, so only the
	// vCPU and memory floor narrow the search.
	if app.Accelerator.Type != "" {
		policy = append(policy,
			condition(metricAcceleratorType, "=", app.Accelerator.Type),
			condition(metricAcceleratorCount, ">=", strconv.Itoa(app.Accelerator.MinCount)))
		if app.Accelerator.Model != "" {
			policy = append(policy, condition(metricAcceleratorModel, "=", app.Accelerator.Model))
		}
		if app.Accelerator.MinMemoryGiB > 0 {
			policy = append(policy, condition(metricAcceleratorMemory, ">=", formatFloat(app.Accelerator.MinMemoryGiB)))
		}
	}
	if app.Resources.MinVCPU > 0 {
		policy = append(policy, condition(metricVCPU, ">=", strconv.Itoa(app.Resources.MinVCPU)))
	}
	if app.Resources.MinMemoryGiB > 0 {
		policy = append(policy, condition(metricMemoryGiB, ">=", strconv.Itoa(app.Resources.MinMemoryGiB)))
	}

	return &tumblebug.RecommendSpecReq{
		Filter:   tumblebug.FilterInfo{Policy: policy},
		Priority: tumblebug.PriorityInfo{Policy: []tumblebug.PriorityCondition{{Metric: metricCost, Weight: 1.0}}},
		Limit:    limit,
	}
}

func condition(metric, operator, operand string) tumblebug.FilterCondition {
	return tumblebug.FilterCondition{
		Metric:    metric,
		Condition: []tumblebug.Operation{{Operator: operator, Operand: operand}},
	}
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}
