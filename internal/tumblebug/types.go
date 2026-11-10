package tumblebug

// The wire types below model only the fields this service sets or reads. They
// follow the live CB-Tumblebug Swagger 2.0 document served at
// /tumblebug/api/doc.json, where the resource is named "infra" (not "mci").

// SpecInfo is a node spec as CB-Tumblebug knows it.
//
// The Accelerator* fields are the AI semiconductor axis this service filters on.
type SpecInfo struct {
	ID                  string  `json:"id"`
	Name                string  `json:"name"`
	ProviderName        string  `json:"providerName"`
	RegionName          string  `json:"regionName"`
	ConnectionName      string  `json:"connectionName"`
	VCPU                int     `json:"vCPU"`
	MemoryGiB           float64 `json:"memoryGiB"`
	DiskSizeGB          float64 `json:"diskSizeGB"`
	CostPerHour         float64 `json:"costPerHour"`
	AcceleratorType     string  `json:"acceleratorType"`
	AcceleratorModel    string  `json:"acceleratorModel"`
	AcceleratorCount    int     `json:"acceleratorCount"`
	AcceleratorMemoryGB float64 `json:"acceleratorMemoryGB"`
}

// RecommendSpecReq asks CB-Tumblebug for specs matching a filter.
type RecommendSpecReq struct {
	Filter   FilterInfo   `json:"filter"`
	Priority PriorityInfo `json:"priority"`
	// Limit must be a number on the wire: CB-Tumblebug rejects a string with 400.
	Limit int `json:"limit"`
}

// FilterInfo groups the conditions a spec must satisfy.
type FilterInfo struct {
	Policy []FilterCondition `json:"policy"`
}

// FilterCondition constrains one spec metric.
type FilterCondition struct {
	Metric    string      `json:"metric"`
	Condition []Operation `json:"condition"`
}

// Operation is one comparison against a spec metric.
type Operation struct {
	Operator string `json:"operator"`
	Operand  string `json:"operand"`
}

// PriorityInfo orders the specs that pass the filter.
type PriorityInfo struct {
	Policy []PriorityCondition `json:"policy"`
}

// PriorityCondition weights one ordering metric.
type PriorityCondition struct {
	Metric string  `json:"metric"`
	Weight float64 `json:"weight"`
}
