package model

import (
	"fmt"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

// DeployAppReq asks for an AI application to be placed on an AI semiconductor node.
type DeployAppReq struct {
	// AppID selects the registered application to deploy.
	AppID string `json:"appId" validate:"required" example:"vllm-llama31-8b"`
	// InfraName becomes the CB-Tumblebug Infra ID and is the idempotency key.
	InfraName string `json:"infraName" validate:"required" example:"vllm-lab-01"`
	// SpecID pins the node spec. Empty asks for a recommendation first.
	SpecID string `json:"specId,omitempty" example:"aws+us-west-2+g6.xlarge"`
	// ImageID pins the node image. Empty asks CB-Tumblebug to select one.
	ImageID string `json:"imageId,omitempty" example:"ubuntu22.04"`
	// NodeCount is how many serving nodes to provision.
	NodeCount int `json:"nodeCount,omitempty" example:"1"`
	// SGTemplateID pins the security group template provisioning starts from.
	// Empty leaves the cloud manager default, which opens every port and is
	// documented upstream as being for development and testing only.
	SGTemplateID string `json:"sgTemplateId,omitempty" example:"sg-usecase-web"`
}

// Validate reports why a deployment request cannot be planned.
func (r *DeployAppReq) Validate() error {
	switch {
	case r.AppID == "":
		return errRequired("appId")
	case r.InfraName == "":
		return errRequired("infraName")
	case r.NodeCount < 0:
		return errInvalid("nodeCount", "cannot be negative")
	}
	return nil
}

// DeploymentPlan is what the service would send to CB-Tumblebug.
//
// It is produced whether or not the call is actually made, so that a dry run and
// a real run archive the same shape and stay comparable.
type DeploymentPlan struct {
	AppID        string `json:"appId"`
	AppName      string `json:"appName"`
	Namespace    string `json:"nsId"`
	InfraName    string `json:"infraName"`
	SpecID       string `json:"specId"`
	ImageID      string `json:"imageId"`
	SGTemplateID string `json:"sgTemplateId,omitempty"`
	NodeCount    int    `json:"nodeCount"`
	// SpecSource records how SpecID was chosen: "requested" or "recommended".
	SpecSource string `json:"specSource"`
	// ImageSource records how ImageID was chosen: "requested" or "resolved".
	ImageSource string `json:"imageSource"`
	// ImageReason explains an image that was resolved rather than requested.
	ImageReason string `json:"imageReason,omitempty"`
	// AcceleratorSummary describes the accelerator the chosen spec provides.
	AcceleratorSummary string `json:"acceleratorSummary,omitempty"`
	// EstimatedCost is the CB-Tumblebug review estimate, when a review ran.
	EstimatedCost string `json:"estimatedCost,omitempty"`
	// Request is the dynamic Infra request body, including the install commands.
	Request any `json:"request"`
}

// ServingAccess reports whether the application's serving port can be reached.
//
// Provisioning opens SSH only, so this is what turns a running node into a
// usable endpoint. It is part of the result rather than a side effect so that a
// caller learns immediately when the port stayed closed.
type ServingAccess struct {
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	// Opened is false when the rule could not be added; Message says why.
	Opened bool `json:"opened"`
	// SourceIP is the range the port was opened to.
	SourceIP         string   `json:"sourceIp,omitempty"`
	SecurityGroupIDs []string `json:"securityGroupIds,omitempty"`
	// Endpoints are the reachable addresses, one per node with a public address.
	Endpoints []string `json:"endpoints,omitempty"`
	Message   string   `json:"message"`
}

// AcceleratorDevice is one accelerator as the node reports it.
//
// Fields the node could not report are left empty and paired with a Known flag
// rather than filled with zero, because zero reads as a measurement.
type AcceleratorDevice struct {
	NodeID string `json:"nodeId"`
	Index  int    `json:"index"`
	// UUID keeps the vendor prefix the driver prints. Stripping it splits one
	// card into two identities wherever the prefixed form is used as a join key.
	UUID   string `json:"uuid,omitempty"`
	Name   string `json:"name,omitempty"`
	Vendor string `json:"vendor,omitempty"`
	// Kind is gpu, npu or tpu. It says which class of AI semiconductor answered,
	// which the vendor alone does not: one vendor can ship more than one class.
	Kind string `json:"kind,omitempty"`
	// Source names what produced this entry, for example nvidia-smi or pci. A
	// reading taken off the PCI bus and one taken from a vendor driver carry
	// different amounts of truth, and merging them without saying which is which
	// makes an inventory look like a measurement.
	Source string `json:"source,omitempty"`
	// PCIBusID is the bus address in the domain:bus:device.function form the
	// kernel prints. It is the only identifier every source shares, so it is
	// what joins a vendor reading to its PCI entry.
	PCIBusID      string `json:"pciBusId,omitempty"`
	MemoryMiB     int    `json:"memoryMiB,omitempty"`
	MemoryKnown   bool   `json:"memoryKnown"`
	DriverVersion string `json:"driverVersion,omitempty"`
	// KernelDriver is the driver the kernel actually bound to the device. An
	// accelerator image whose driver never bound leaves this empty while the
	// device itself still appears, which is the case the catalog comparison
	// cannot see from the vendor tool alone.
	KernelDriver string `json:"kernelDriver,omitempty"`
	MIGEnabled   bool   `json:"migEnabled"`
	MIGKnown     bool   `json:"migKnown"`
	// VirtualFunction marks an SR-IOV virtual function. Counting a physical
	// function and its virtual functions as separate cards inflates the device
	// count on a virtualisation host.
	VirtualFunction bool `json:"virtualFunction,omitempty"`
}

// Label names a device for a message an operator reads.
func (d *AcceleratorDevice) Label() string {
	name := d.Name
	if name == "" {
		name = "accelerator"
	}
	if d.NodeID == "" {
		return fmt.Sprintf("%s #%d", name, d.Index)
	}
	return fmt.Sprintf("%s %s #%d", d.NodeID, name, d.Index)
}

// AcceleratorExpectation is what the catalog entry asked for.
type AcceleratorExpectation struct {
	Type         string  `json:"type,omitempty"`
	Model        string  `json:"model,omitempty"`
	MinCount     int     `json:"minCount,omitempty"`
	MinMemoryGiB float64 `json:"minMemoryGiB,omitempty"`
}

// AcceleratorReport is what the deployed nodes answered, next to what was asked.
type AcceleratorReport struct {
	InfraID string `json:"infraId"`
	// Probed is false when the nodes were never asked.
	Probed bool `json:"probed"`
	// Mode is mig, bare_metal or unknown. Passthrough and bare metal look the
	// same from inside a guest and are not split here.
	Mode string `json:"mode"`
	// Sources lists the probes that answered on at least one node, in the order
	// they were tried. An empty list with Probed true means the nodes answered
	// but carried nothing this probe knows how to read.
	Sources  []string               `json:"sources,omitempty"`
	Devices  []AcceleratorDevice    `json:"devices"`
	Expected AcceleratorExpectation `json:"expected,omitempty"`
	// Findings are the disagreements between the two. They are reported, not
	// raised as errors: the nodes are already running either way.
	Findings []string `json:"findings"`
	Message  string   `json:"message"`
}

// DeploymentResult reports the outcome of a deployment attempt.
type DeploymentResult struct {
	Plan *DeploymentPlan `json:"plan"`
	// DryRun is true when the plan was archived but never sent.
	DryRun bool `json:"dryRun"`
	// Review is the CB-Tumblebug pre-flight verdict, when a review ran.
	Review *ReviewSummary `json:"review,omitempty"`
	// Infra is the provisioned Infra, present only on a real run.
	Infra any `json:"infra,omitempty"`
	// Serving reports whether the application's port was opened, on a real run.
	Serving *ServingAccess `json:"serving,omitempty"`
	// Accelerator reports what the nodes answered when asked what they carry.
	Accelerator *AcceleratorReport `json:"accelerator,omitempty"`
	// Message explains the outcome to the caller.
	Message string `json:"message"`
}

// ReviewSummary is the caller-facing part of a CB-Tumblebug provisioning review.
type ReviewSummary struct {
	CreationViable  bool     `json:"creationViable"`
	OverallStatus   string   `json:"overallStatus"`
	OverallMessage  string   `json:"overallMessage"`
	EstimatedCost   string   `json:"estimatedCost,omitempty"`
	TotalNodeCount  int      `json:"totalNodeCount"`
	Recommendations []string `json:"recommendations,omitempty"`
}

// ControlAppReq applies a lifecycle action to a deployed application.
type ControlAppReq struct {
	// Action is one of suspend, resume, reboot, terminate.
	Action string `json:"action" validate:"required" example:"suspend" enums:"suspend,resume,reboot,terminate"`
}

// ControlResult reports a lifecycle action outcome.
type ControlResult struct {
	InfraID  string   `json:"infraId"`
	Action   string   `json:"action"`
	DryRun   bool     `json:"dryRun"`
	Affected []string `json:"affected,omitempty"`
	Message  string   `json:"message"`
}

// UpdateDeploymentReq asks for a running deployment to take a new application version.
type UpdateDeploymentReq struct {
	// AppID selects the registered application whose install commands are applied.
	AppID string `json:"appId" validate:"required" example:"vllm-llama31-8b"`
}

// Validate reports why an update request cannot be applied.
func (r *UpdateDeploymentReq) Validate() error {
	if r.AppID == "" {
		return errRequired("appId")
	}
	return nil
}

// ProvisionResult is what a provisioner reports after creating or updating an Infra.
type ProvisionResult struct {
	InfraID string `json:"infraId"`
	// Infra is the Infra as the provisioner left it, when it reports one. A
	// create must carry it: opening the serving port and probing the
	// accelerator both read the nodes from it.
	Infra *tumblebug.InfraInfo `json:"infra,omitempty"`
	// AppID and AppVersion name what an update applied.
	AppID      string `json:"appId,omitempty"`
	AppVersion string `json:"appVersion,omitempty"`
	// DryRun is true when the update was prepared but never sent.
	DryRun bool `json:"dryRun"`
	// Nodes is the per-node outcome of an update.
	Nodes   []NodeUpdate `json:"nodes,omitempty"`
	Message string       `json:"message"`
}

// NodeUpdate is the outcome of an update on one node.
type NodeUpdate struct {
	NodeID string `json:"nodeId"`
	NodeIP string `json:"nodeIp,omitempty"`
	// Succeeded is false when the node reported an error for the command set.
	Succeeded bool   `json:"succeeded"`
	Error     string `json:"error,omitempty"`
	// Stderr is the tail of what the commands wrote to standard error.
	Stderr string `json:"stderr,omitempty"`
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

// ArchiveRecord is one archived agent run or direct deployment action.
//
// The record holds the operator instruction, every tool the agent chose, what it
// answered, and what the guard did. Keeping the intermediate steps is what makes
// the archive reusable as feedback for later planning, not just an audit log.
type ArchiveRecord struct {
	RunID     string      `json:"runId"`
	StartedAt time.Time   `json:"startedAt"`
	EndedAt   time.Time   `json:"endedAt"`
	Namespace string      `json:"nsId"`
	Kind      string      `json:"kind" example:"intent"`
	Intent    string      `json:"intent,omitempty"`
	Steps     []ToolStep  `json:"steps"`
	Answer    string      `json:"answer,omitempty"`
	Error     string      `json:"error,omitempty"`
	Usage     *TokenUsage `json:"usage,omitempty"`
}

// ToolStep is one tool the agent called during a run.
type ToolStep struct {
	Index int    `json:"index"`
	Tool  string `json:"tool"`
	// Grade is the tool risk grade that decided whether the call was executed.
	Grade string `json:"grade"`
	Input any    `json:"input"`
	// Executed is false when the dry-run guard blocked an infrastructure change.
	Executed bool      `json:"executed"`
	Output   any       `json:"output,omitempty"`
	Error    string    `json:"error,omitempty"`
	CalledAt time.Time `json:"calledAt"`
	Duration string    `json:"duration"`
}

// TokenUsage is the planning cost of one agent run.
type TokenUsage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
}

// ArchiveListResp is the archive listing body.
type ArchiveListResp struct {
	Records []ArchiveRecord `json:"records"`
}
