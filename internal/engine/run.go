package engine

import (
	"encoding/json"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
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
	Name  string `json:"name" example:"create the deployment"`
	// Tool is the AI-App MCP tool the step calls.
	Tool   string     `json:"tool" example:"create_deployment"`
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

// DeployInput is the node deployment request create_deployment takes: a
// model service or an uploaded application, onto nodes CB-Tumblebug already
// runs.
type DeployInput = model.NodeDeploymentReq

// HealInput asks for a deployment that did not succeed on every node to be
// run again once.
type HealInput struct {
	// DeploymentID is the id create_deployment returned.
	DeploymentID string `json:"deploymentId" example:"3f9b2c4e-8a51-4d27-9e0c-5b1f6a7d2e48"`
}

// ControlInput applies a lifecycle action to every node of a deployment.
type ControlInput struct {
	DeploymentID string `json:"deploymentId" example:"3f9b2c4e-8a51-4d27-9e0c-5b1f6a7d2e48"`
	Action       string `json:"action" example:"reboot" enums:"suspend,resume,reboot,terminate"`
}

// StatusInput asks for the state of a deployment.
type StatusInput struct {
	DeploymentID string `json:"deploymentId" example:"3f9b2c4e-8a51-4d27-9e0c-5b1f6a7d2e48"`
}

// CatalogQuery asks for the models and the uploaded applications. Every
// filter is optional and narrows the models only, as list_models does.
type CatalogQuery struct {
	Task     string `json:"task,omitempty" example:"vision-chat"`
	Modality string `json:"modality,omitempty" example:"image"`
	Format   string `json:"format,omitempty" example:"gguf"`
	Engine   string `json:"engine,omitempty" example:"ollama"`
	Source   string `json:"source,omitempty" example:"ollama"`
}

// Catalog is the short form of the models and the uploaded applications a
// caller chooses a deployment request from.
type Catalog struct {
	Models []CatalogModel `json:"models"`
	Apps   []CatalogApp   `json:"apps"`
}

// CatalogModel is one model with the artifacts a model-service request can
// name.
type CatalogModel struct {
	ID              string            `json:"id" example:"qwen3-vl-8b"`
	Name            string            `json:"name" example:"Qwen3-VL-8B-Instruct"`
	Tasks           []string          `json:"tasks,omitempty"`
	InputModalities []string          `json:"inputModalities,omitempty"`
	Artifacts       []CatalogArtifact `json:"artifacts"`
}

// CatalogArtifact is one artifact of a model and the engines it runs on.
type CatalogArtifact struct {
	ID        string  `json:"id" example:"ollama-gguf"`
	Source    string  `json:"source" example:"ollama"`
	Ref       string  `json:"ref" example:"qwen3-vl:8b"`
	Format    string  `json:"format" example:"gguf"`
	Precision string  `json:"precision,omitempty"`
	SizeGiB   float64 `json:"sizeGiB,omitempty"`
	// Engines are the engine profile ids, the first being the one taken when
	// a request names none.
	Engines []string `json:"engines"`
}

// CatalogApp is one uploaded application and its version numbers.
type CatalogApp struct {
	Name     string `json:"name" example:"open-webui"`
	Versions []int  `json:"versions"`
	// Latest is the version an app request without a version takes.
	Latest int `json:"latest" example:"2"`
}
