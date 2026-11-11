package infrarec

import (
	"errors"
	"strings"
	"time"
)

// App is the part of an AI application the recommendation contract reads: its
// identifier, the accelerator and resource floor it needs, and the image family
// it installs onto.
type App struct {
	ID          string                 `json:"id"`
	Accelerator AcceleratorRequirement `json:"accelerator"`
	Resources   ResourceRequirement    `json:"resources,omitempty"`
	Install     InstallSpec            `json:"install"`
	SLO         SLORequirement         `json:"slo,omitempty"`
}

// AcceleratorRequirement is what an application needs from an AI semiconductor.
//
// The field names mirror the CB-Tumblebug spec attributes (acceleratorType,
// acceleratorModel, acceleratorCount, acceleratorMemoryGB) so that a requirement
// converts to a spec recommendation filter without a lookup table.
type AcceleratorRequirement struct {
	// Type is the accelerator class. Empty means a CPU node.
	Type string `json:"type"`
	// Model narrows the search to a device model. Empty means any model of Type.
	Model string `json:"model,omitempty"`
	// MinCount is the smallest acceptable number of devices per node.
	MinCount int `json:"minCount"`
	// MinMemoryGiB is the smallest acceptable per-device memory. Zero means unset.
	MinMemoryGiB float64 `json:"minMemoryGiB,omitempty"`
}

// ResourceRequirement is the non-accelerator floor an application needs.
type ResourceRequirement struct {
	MinVCPU        int     `json:"minVCPU,omitempty"`
	MinMemoryGiB   float64 `json:"minMemoryGiB,omitempty"`
	MinStorageGiB  int     `json:"minStorageGiB,omitempty"`
	MaxCostPerHour float64 `json:"maxCostPerHour,omitempty"`
}

// SLORequirement is carried through as an unverified serving requirement.
type SLORequirement struct {
	LatencyP95MSMax  float64 `json:"latencyP95MsMax,omitempty"`
	ThroughputRPSMin float64 `json:"throughputRpsMin,omitempty"`
}

// InstallSpec carries the image family an application installs onto.
type InstallSpec struct {
	// OSType selects the node image family. Empty uses the default.
	OSType string `json:"osType,omitempty"`
}

// SpecCandidate is one node spec that satisfies an accelerator requirement.
type SpecCandidate struct {
	ID                    string     `json:"id"`
	ProviderName          string     `json:"providerName"`
	RegionName            string     `json:"regionName"`
	AcceleratorType       string     `json:"acceleratorType"`
	AcceleratorModel      string     `json:"acceleratorModel"`
	AcceleratorCount      int        `json:"acceleratorCount"`
	AcceleratorMemoryGB   float64    `json:"acceleratorMemoryGB"`
	GPUVendorStatus       string     `json:"gpuRuntimeVendorStatus,omitempty"`
	VCPU                  int        `json:"vCPU"`
	MemoryGiB             float64    `json:"memoryGiB"`
	CostPerHour           float64    `json:"costPerHour"`
	AvailabilityStatus    string     `json:"availabilityStatus"`
	AvailabilityCheckedAt *time.Time `json:"availabilityCheckedAt,omitempty"`
}

// applicationProfile reads the recommendation inputs from the existing AI-Ops
// ApplicationProfile wire format. Deployment and analysis fields are accepted
// as part of that format but are not needed for selecting one VM.
type applicationProfile struct {
	AppID    string `json:"app_id"`
	Workload struct {
		ExpectedRPS float64 `json:"expected_rps"`
	} `json:"workload"`
	Requirements struct {
		Compute struct {
			CPUCoresMin   int `json:"cpu_cores_min"`
			MemoryMiBMin  int `json:"memory_mib_min"`
			StorageGiBMin int `json:"storage_gib_min"`
		} `json:"compute"`
		Accelerator struct {
			Required              bool   `json:"required"`
			Type                  string `json:"type"`
			CountMin              int    `json:"count_min"`
			MemoryMiBMinPerDevice int    `json:"memory_mib_min_per_device"`
		} `json:"accelerator"`
		SLO struct {
			LatencyP95MSMax  float64 `json:"latency_p95_ms_max"`
			ThroughputRPSMin float64 `json:"throughput_rps_min"`
		} `json:"slo"`
		Cost struct {
			Currency       string  `json:"currency"`
			CostPerHourMax float64 `json:"cost_per_hour_max"`
		} `json:"cost"`
	} `json:"requirements"`
}

func normalizeApp(app *App, profile *applicationProfile) (*App, error) {
	if (app == nil) == (profile == nil) {
		return nil, errors.New("supply either app or profile")
	}
	if profile != nil {
		req := profile.Requirements
		if req.Accelerator.Required && strings.TrimSpace(req.Accelerator.Type) == "" {
			return nil, errors.New("profile requires an accelerator type")
		}
		if !req.Accelerator.Required && (req.Accelerator.CountMin != 0 || req.Accelerator.MemoryMiBMinPerDevice != 0 ||
			strings.TrimSpace(req.Accelerator.Type) != "") {
			return nil, errors.New("profile has accelerator requirements but accelerator.required is false")
		}
		currency := strings.TrimSpace(req.Cost.Currency)
		if req.Cost.CostPerHourMax > 0 && currency != "" && !strings.EqualFold(currency, "USD") {
			return nil, errors.New("cost limit must be in USD")
		}
		if profile.Workload.ExpectedRPS < 0 || req.SLO.ThroughputRPSMin < 0 {
			return nil, errors.New("profile request rates must be nonnegative")
		}
		app = &App{
			ID: profile.AppID,
			Accelerator: AcceleratorRequirement{
				Type: req.Accelerator.Type, MinCount: req.Accelerator.CountMin,
				MinMemoryGiB: float64(req.Accelerator.MemoryMiBMinPerDevice) / 1024,
			},
			Resources: ResourceRequirement{
				MinVCPU: req.Compute.CPUCoresMin, MinMemoryGiB: float64(req.Compute.MemoryMiBMin) / 1024,
				MinStorageGiB: req.Compute.StorageGiBMin, MaxCostPerHour: req.Cost.CostPerHourMax,
			},
			SLO: SLORequirement{
				LatencyP95MSMax:  req.SLO.LatencyP95MSMax,
				ThroughputRPSMin: max(req.SLO.ThroughputRPSMin, profile.Workload.ExpectedRPS),
			},
		}
	}

	normalized := *app
	normalized.ID = strings.TrimSpace(app.ID)
	accel := &normalized.Accelerator
	accel.Type = strings.ToLower(strings.TrimSpace(accel.Type))
	accel.Model = strings.TrimSpace(accel.Model)
	normalized.Install.OSType = strings.TrimSpace(app.Install.OSType)
	switch {
	case normalized.ID == "":
		return nil, errors.New("app id is required")
	case accel.MinCount < 0 || accel.MinMemoryGiB < 0 || app.Resources.MinVCPU < 0 || app.Resources.MinMemoryGiB < 0 ||
		app.Resources.MinStorageGiB < 0 || app.Resources.MaxCostPerHour < 0 ||
		app.SLO.LatencyP95MSMax < 0 || app.SLO.ThroughputRPSMin < 0:
		return nil, errors.New("resource and SLO limits must be nonnegative")
	case strings.Contains(accel.Type, ",") || strings.Contains(accel.Model, ","):
		return nil, errors.New("supply one accelerator type and model")
	case accel.Type == "" && (accel.Model != "" || accel.MinCount != 0 || accel.MinMemoryGiB != 0):
		return nil, errors.New("accelerator type is required with device requirements")
	case accel.Type != "" && accel.MinCount == 0:
		accel.MinCount = 1
	}
	return &normalized, nil
}
