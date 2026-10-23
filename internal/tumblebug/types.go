package tumblebug

// The wire types below model only the fields this service sets or reads. They
// follow the live CB-Tumblebug Swagger 2.0 document served at
// /tumblebug/api/doc.json, where the resource is named "infra" (not "mci").

// InfraDynamicReq is the CB-Tumblebug dynamic provisioning request.
type InfraDynamicReq struct {
	Name                   string `json:"name"`
	Description            string `json:"description,omitempty"`
	PolicyOnPartialFailure string `json:"policyOnPartialFailure,omitempty"`
	InstallMonAgent        string `json:"installMonAgent,omitempty"`
	// SGTemplateID selects the security group template provisioning starts from.
	// Empty leaves CB-Tumblebug on its own default, which opens every port.
	SGTemplateID string                      `json:"sgTemplateId,omitempty"`
	NodeGroups   []CreateNodeGroupDynamicReq `json:"nodeGroups"`
	// PostCommands run on the provisioned nodes as part of the same call, which
	// is how an AI application install rides along with provisioning.
	PostCommands     []PostCommandReq  `json:"postCommands,omitempty"`
	PostCommandAsync bool              `json:"postCommandAsync,omitempty"`
	Label            map[string]string `json:"label,omitempty"`
}

// CreateNodeGroupDynamicReq describes one NodeGroup of a dynamic provisioning request.
type CreateNodeGroupDynamicReq struct {
	Name          string            `json:"name,omitempty"`
	NodeGroupSize int               `json:"nodeGroupSize,omitempty"`
	SpecID        string            `json:"specId"`
	ImageID       string            `json:"imageId"`
	RootDiskType  string            `json:"rootDiskType,omitempty"`
	RootDiskSize  int               `json:"rootDiskSize,omitempty"`
	Description   string            `json:"description,omitempty"`
	Label         map[string]string `json:"label,omitempty"`
}

// PostCommandReq is a command CB-Tumblebug runs after provisioning.
type PostCommandReq struct {
	Command         []string `json:"command"`
	UserName        string   `json:"userName,omitempty"`
	TimeoutMinutes  int      `json:"timeoutMinutes,omitempty"`
	NodeGroupID     string   `json:"nodeGroupId,omitempty"`
	NodeID          string   `json:"nodeId,omitempty"`
	ContinueOnError bool     `json:"continueOnError,omitempty"`
}

// ReviewResult is the pre-flight validation of a dynamic provisioning request.
type ReviewResult struct {
	OverallStatus   string   `json:"overallStatus"`
	OverallMessage  string   `json:"overallMessage"`
	CreationViable  bool     `json:"creationViable"`
	EstimatedCost   string   `json:"estimatedCost,omitempty"`
	InfraName       string   `json:"infraName"`
	TotalNodeCount  int      `json:"totalNodeCount"`
	Recommendations []string `json:"recommendations,omitempty"`
}

// InfraInfo is the CB-Tumblebug view of a provisioned Infra.
type InfraInfo struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Status       string            `json:"status"`
	TargetStatus string            `json:"targetStatus"`
	StatusCount  StatusCountInfo   `json:"statusCount"`
	Description  string            `json:"description"`
	Label        map[string]string `json:"label,omitempty"`
	Node         []NodeInfo        `json:"node"`
}

// StatusCountInfo summarizes node states of an Infra.
type StatusCountInfo struct {
	CountTotal      int `json:"countTotal"`
	CountCreating   int `json:"countCreating"`
	CountRunning    int `json:"countRunning"`
	CountSuspended  int `json:"countSuspended"`
	CountFailed     int `json:"countFailed"`
	CountTerminated int `json:"countTerminated"`
}

// NodeInfo is a single provisioned node.
type NodeInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	NodeGroupID string `json:"nodeGroupId"`
	Status      string `json:"status"`
	PublicIP    string `json:"publicIP"`
	PrivateIP   string `json:"privateIP"`
	SSHPort     int    `json:"sshPort"`
	SpecID      string `json:"specId"`
	ImageID     string `json:"imageId"`
	// SecurityGroupIDs are the groups attached to the node, and are how the
	// serving port is opened after provisioning.
	SecurityGroupIDs []string `json:"securityGroupIds"`
}

// InfraStatusView is the status view of an Infra.
//
// CB-Tumblebug wraps this under a "status" key when option=status is used, and
// the shape differs from InfraInfo, so it needs its own type.
type InfraStatusView struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Status       string          `json:"status"`
	StatusCount  StatusCountInfo `json:"statusCount"`
	TargetStatus string          `json:"targetStatus"`
	TargetAction string          `json:"targetAction"`
}

// IDList is the CB-Tumblebug response carrying affected resource IDs.
type IDList struct {
	Output []string `json:"output"`
}

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
