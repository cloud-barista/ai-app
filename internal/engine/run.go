package engine

import (
	"encoding/json"
	"time"
)

// StepStatus is how one workflow step ended.
type StepStatus string

// Step outcomes.
const (
	// StepSucceeded means the AI-App tool ran and its result let the workflow go on.
	StepSucceeded StepStatus = "succeeded"
	// StepFailed means the tool failed, or its result said the workflow cannot go on.
	StepFailed StepStatus = "failed"
	// StepBlocked means the AI-App dry-run gate kept a write tool from taking effect.
	StepBlocked StepStatus = "blocked"
	// StepWaiting means the step needs something a blocked step would have
	// produced, so it was not run.
	StepWaiting StepStatus = "waiting"
	// StepSkipped means an earlier step failed, so this one was not run.
	StepSkipped StepStatus = "skipped"
)

// RunStatus is how a whole workflow run ended.
type RunStatus string

// Run outcomes.
const (
	RunSucceeded RunStatus = "succeeded"
	RunFailed    RunStatus = "failed"
	// RunBlocked means no step failed but at least one was held by the dry-run gate.
	RunBlocked RunStatus = "blocked"
)

// Step is one AI-App tool call a workflow made, or would have made.
type Step struct {
	Index int    `json:"index"`
	Name  string `json:"name" example:"plan model service vllm-llama31-8b"`
	// Tool is the AI-App MCP tool the step calls.
	Tool   string     `json:"tool" example:"plan_deployment"`
	Input  any        `json:"input"`
	Status StepStatus `json:"status" example:"succeeded" enums:"succeeded,failed,blocked,waiting,skipped"`
	// Output is the tool result as the AI-App service returned it.
	Output json.RawMessage `json:"output,omitempty" swaggertype:"object"`
	Error  string          `json:"error,omitempty"`
	// Message says why a step was blocked, waits or was skipped, or what its
	// result meant for the workflow.
	Message string `json:"message,omitempty"`
	// StartedAt and EndedAt are absent on a step that was not run.
	StartedAt *time.Time `json:"startedAt,omitempty"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`
}

// Run is one workflow execution and every step of it. It is what the engine
// archives, and the input the partner institution's inference optimization
// reads.
type Run struct {
	RunID     string    `json:"runId" example:"eng-mbt3x2k1"`
	Workflow  string    `json:"workflow" example:"deploy" enums:"deploy,heal,control,status"`
	Namespace string    `json:"nsId" example:"default"`
	Input     any       `json:"input"`
	Steps     []Step    `json:"steps"`
	Status    RunStatus `json:"status" example:"blocked" enums:"succeeded,failed,blocked"`
	// DryRun is the AI-App dry-run state the run executed under.
	DryRun    bool      `json:"dryRun"`
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt"`
	Message   string    `json:"message"`
}

// RunListResp is the engine run listing body.
type RunListResp struct {
	Runs []Run `json:"runs"`
}

// DeployInput asks for an AI application, and any model service it needs that
// is not running yet, to be deployed.
type DeployInput struct {
	AppID string `json:"appId" example:"open-webui-llama31"`
	// DeploymentName names the deployment of AppID. A model service deployed
	// on its behalf is named "<deploymentName>-<model service appId>".
	DeploymentName string `json:"deploymentName" example:"demo-webui"`
	// SpecID and ImageID pin the node of AppID itself. Empty asks the
	// recommender through the AI-App service.
	SpecID  string `json:"specId,omitempty"`
	ImageID string `json:"imageId,omitempty"`
}

// HealInput asks for a deployment that does not answer to be updated once.
type HealInput struct {
	DeploymentID string `json:"deploymentId" example:"demo-webui"`
	// AppID is the application whose install commands are applied. Empty takes
	// the one the deployment was created with.
	AppID string `json:"appId,omitempty"`
}

// ControlInput applies a lifecycle action to a deployment.
type ControlInput struct {
	DeploymentID string `json:"deploymentId" example:"demo-webui"`
	Action       string `json:"action" example:"suspend" enums:"suspend,resume,reboot,terminate"`
}

// StatusInput asks for the state of a deployment.
type StatusInput struct {
	DeploymentID string `json:"deploymentId" example:"demo-webui"`
	// AppID is the application to check against. Empty takes the one the
	// deployment was created with.
	AppID string `json:"appId,omitempty"`
}
