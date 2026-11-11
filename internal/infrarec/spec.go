// Package infrarec implements the AI-App infrastructure recommendation API
// over the CB-Tumblebug spec and image catalogs.
package infrarec

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/config"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

type catalogSpec struct {
	tumblebug.SpecInfo
	AvailabilityStatus       string     `json:"availabilityStatus,omitempty"`
	AvailabilityCheckedAt    *time.Time `json:"availabilityCheckedAt,omitempty"`
	AvailabilityRefreshAfter *time.Time `json:"availabilityRefreshAfter,omitempty"`
}

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
	metricCostPerHour       = "costPerHour"

	available  = "Available"
	unverified = "Unverified"

	gpuVendorCompatible  = "Compatible"
	gpuVendorUnsupported = "Unsupported"
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
	var specs []catalogSpec
	if err := r.tumblebug.Do(ctx, http.MethodPost, "/recommendSpec", nil, req, &specs); err != nil {
		return nil, fmt.Errorf("failed to recommend specs for %q: %w", app.ID, err)
	}

	log.Info().Str("appId", app.ID).Str("acceleratorType", app.Accelerator.Type).
		Int("minCount", app.Accelerator.MinCount).Int("candidates", len(specs)).
		Msg("Recommended accelerator specs")

	candidates := make([]SpecCandidate, 0, len(specs))
	now := time.Now()
	for _, spec := range specs {
		if spec.ID == "" || requirementMismatch(app, &spec.SpecInfo) != "" {
			continue
		}
		candidates = append(candidates, SpecCandidate{
			ID:                    spec.ID,
			ProviderName:          spec.ProviderName,
			RegionName:            spec.RegionName,
			AcceleratorType:       spec.AcceleratorType,
			AcceleratorModel:      spec.AcceleratorModel,
			AcceleratorCount:      spec.AcceleratorCount,
			AcceleratorMemoryGB:   spec.AcceleratorMemoryGB,
			GPUVendorStatus:       gpuRuntimeVendorStatus(spec.AcceleratorType, model.AcceleratorVendorOf(spec.AcceleratorModel)),
			VCPU:                  spec.VCPU,
			MemoryGiB:             spec.MemoryGiB,
			CostPerHour:           spec.CostPerHour,
			AvailabilityStatus:    availability(spec, now),
			AvailabilityCheckedAt: spec.AvailabilityCheckedAt,
		})
	}
	// Preserve CB-Tumblebug's price ordering within each availability group.
	sort.SliceStable(candidates, func(i, j int) bool {
		return availabilityRank(candidates[i].AvailabilityStatus) < availabilityRank(candidates[j].AvailabilityStatus)
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates, nil
}

func gpuRuntimeVendorStatus(acceleratorType, vendor string) string {
	if !strings.EqualFold(acceleratorType, "gpu") {
		return ""
	}
	switch vendor {
	case "nvidia":
		return gpuVendorCompatible
	case "":
		return unverified
	default:
		return gpuVendorUnsupported
	}
}

func requirementMismatch(app *App, spec *tumblebug.SpecInfo) string {
	accel, res := app.Accelerator, app.Resources
	if accel.Type != "" {
		switch {
		case !strings.EqualFold(spec.AcceleratorType, accel.Type), accel.Model != "" && !strings.EqualFold(spec.AcceleratorModel, accel.Model):
			return "accelerator type or model"
		case spec.AcceleratorCount < accel.MinCount:
			return "accelerator count"
		case spec.AcceleratorMemoryGB < accel.MinMemoryGiB:
			return "memory per accelerator"
		}
	}
	switch {
	case spec.VCPU < res.MinVCPU:
		return "vCPU"
	case spec.MemoryGiB < res.MinMemoryGiB:
		return "system memory"
	case res.MaxCostPerHour > 0 && (spec.CostPerHour <= 0 || spec.CostPerHour > res.MaxCostPerHour):
		return "confirmed hourly cost"
	default:
		return ""
	}
}

func availability(spec catalogSpec, now time.Time) string {
	if spec.AvailabilityRefreshAfter != nil && !now.Before(*spec.AvailabilityRefreshAfter) {
		return unverified
	}
	switch spec.AvailabilityStatus {
	case available, "SoldOut", "QuotaExceeded", "Unsupported":
		return spec.AvailabilityStatus
	default:
		return unverified
	}
}

func availabilityRank(status string) int {
	switch status {
	case available:
		return 0
	case unverified:
		return 1
	default:
		return 2
	}
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
		policy = append(policy, condition(metricMemoryGiB, ">=", formatFloat(app.Resources.MinMemoryGiB)))
	}
	if app.Resources.MaxCostPerHour > 0 {
		policy = append(policy, condition(metricCostPerHour, "<=", formatFloat(app.Resources.MaxCostPerHour)))
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
