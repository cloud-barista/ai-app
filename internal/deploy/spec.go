// Package deploy turns AI application metadata into CB-Tumblebug requests.
package deploy

import (
	"context"
	"fmt"
	"strconv"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
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

// RecommendSpecs asks CB-Tumblebug for node specs that satisfy an application.
//
// The accelerator requirement maps field for field onto the spec filter, so a
// new accelerator vendor needs a catalog entry rather than code here.
func (r *localRecommender) RecommendSpecs(ctx context.Context, app *model.AppSpec, limit int) ([]model.SpecCandidate, error) {
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

	candidates := make([]model.SpecCandidate, 0, len(specs))
	for _, spec := range specs {
		candidates = append(candidates, model.SpecCandidate{
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
func buildSpecRequest(app *model.AppSpec, limit int) *tumblebug.RecommendSpecReq {
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
