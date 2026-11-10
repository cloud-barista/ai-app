package recommender

// App is the part of an AI application the recommendation contract reads: its
// identifier, the accelerator and resource floor it needs, and the image family
// it installs onto.
//
// The AI-App service no longer sends these requests, so the shape is kept here
// as part of the contract example rather than shared with the service.
type App struct {
	ID          string                 `json:"id"`
	Accelerator AcceleratorRequirement `json:"accelerator"`
	Resources   ResourceRequirement    `json:"resources,omitempty"`
	Install     InstallSpec            `json:"install"`
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
	MinVCPU      int `json:"minVCPU,omitempty"`
	MinMemoryGiB int `json:"minMemoryGiB,omitempty"`
}

// InstallSpec carries the image family an application installs onto.
type InstallSpec struct {
	// OSType selects the node image family. Empty uses the default.
	OSType string `json:"osType,omitempty"`
}

// SpecCandidate is one node spec that satisfies an accelerator requirement.
type SpecCandidate struct {
	ID                  string  `json:"id"`
	ProviderName        string  `json:"providerName"`
	RegionName          string  `json:"regionName"`
	AcceleratorType     string  `json:"acceleratorType"`
	AcceleratorModel    string  `json:"acceleratorModel"`
	AcceleratorCount    int     `json:"acceleratorCount"`
	AcceleratorMemoryGB float64 `json:"acceleratorMemoryGB"`
	VCPU                int     `json:"vCPU"`
	MemoryGiB           float64 `json:"memoryGiB"`
	CostPerHour         float64 `json:"costPerHour"`
}
