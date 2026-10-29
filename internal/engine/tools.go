package engine

import (
	"context"
	"encoding/json"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
)

// MCPServerName is how the engine MCP server introduces itself.
const MCPServerName = "ai-mcmp-app-automation-engine"

// Names of the tools the engine publishes: one per workflow, and a catalog
// lookup that runs no workflow.
const (
	ToolDeploy       = "deploy_application"
	ToolHeal         = "heal_application"
	ToolControl      = "control_application"
	ToolStatus       = "application_status"
	ToolApplications = "list_applications"
)

// gateNote tells a caller what a write workflow does under the dry-run gate.
const gateNote = " Write steps go through the AI-App dry-run gate; a held step is reported as blocked and the " +
	"steps that need it as waiting, never as done."

// NewRegistry builds the tool registry the engine publishes over MCP: one tool
// per workflow, and the catalog lookup a caller needs to write a deployment
// request. The dry-run state is the AI-App one, read when asked.
func NewRegistry(e *Engine) *tools.Registry {
	registry := tools.NewRegistryFunc(func() bool { return e.DryRun(context.Background()) })
	deploymentID := stringProp("Deployment id returned by " + ToolDeploy + " (the id of create_deployment)")
	registry.Add(tools.Tool{
		Name:  ToolDeploy,
		Grade: tools.GradeWrite,
		Description: "Deploy a model service or an uploaded AI application onto nodes CB-Tumblebug already runs; no " +
			"VM is created. type model-service serves one artifact of a catalog model with an engine; type app " +
			"copies an uploaded application version and runs its script. Use " + ToolApplications + " to pick " +
			"modelId and artifactId, or the app name. The run reads the model or application, creates the " +
			"deployment, and follows it until every node ended; a model service counts as succeeded only once its " +
			"engine answered on the node. A deployment that succeeded on some nodes only fails the run. Returns " +
			"the run with every step; the create step output carries the deployment id." + gateNote,
		InputSchema: deployRequestSchema(),
		Handle:      workflowHandler(e, WorkflowDeploy),
	})
	registry.Add(tools.Tool{
		Name:  ToolHeal,
		Grade: tools.GradeWrite,
		Description: "Read a deployment; when it did not succeed on every node (failed, partial, interrupted or dry " +
			"run), run the request it was made with once more as a new deployment and follow it until it ends. " +
			"Nothing is done to a deployment that succeeded or is still running." + gateNote,
		InputSchema: object(map[string]any{"deploymentId": deploymentID}, "deploymentId"),
		Handle:      workflowHandler(e, WorkflowHeal),
	})
	registry.Add(tools.Tool{
		Name:  ToolControl,
		Grade: tools.GradeWrite,
		Description: "Apply a lifecycle action to every node of a deployment through CB-Tumblebug. terminate " +
			"deletes the VMs." + gateNote,
		InputSchema: object(map[string]any{
			"deploymentId": deploymentID,
			"action":       enumProp("Lifecycle action to apply", "suspend", "resume", "reboot", "terminate"),
		}, "deploymentId", "action"),
		Handle: workflowHandler(e, WorkflowControl),
	})
	registry.Add(tools.Tool{
		Name:  ToolStatus,
		Grade: tools.GradeRead,
		Description: "Report the state of a deployment: its status and that of every node with the service URL or " +
			"the error, and the last lines of the task output of every node. Changes nothing.",
		InputSchema: object(map[string]any{"deploymentId": deploymentID}, "deploymentId"),
		Handle:      workflowHandler(e, WorkflowStatus),
	})
	registry.Add(tools.Tool{
		Name:  ToolApplications,
		Grade: tools.GradeRead,
		Description: "Look up what can be deployed: the catalog models, each with its artifacts (id, source, ref, " +
			"format, precision, size and the engines it runs on, the first being the default), and the uploaded " +
			"applications with their version numbers. Every filter is optional, ignores case and narrows the " +
			"models only. Runs no workflow and changes nothing.",
		InputSchema: object(map[string]any{
			"task":     stringProp("Model task, such as chat, vision-chat or embedding"),
			"modality": stringProp("Input modality the model accepts, such as text or image"),
			"format":   stringProp("Artifact file format: hf or gguf"),
			"engine":   stringProp("Engine profile id an artifact runs on, such as vllm or ollama"),
			"source":   stringProp("Artifact source, such as huggingface or ollama"),
		}),
		Handle: func(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
			var in CatalogQuery
			if err := decodeInput(input, &in); err != nil {
				return nil, err
			}
			return e.ListApplications(ctx, nsID, in)
		},
	})
	return registry
}

// deployRequestSchema is the input schema of the deploy workflow: the node
// deployment request create_deployment takes.
func deployRequestSchema() map[string]any {
	envNote := "Names match [A-Z_][A-Z0-9_]*; values hold no line break and no single quote."
	target := object(map[string]any{
		"kind":    enumProp("node; k8s is reserved and refused", model.NodeTargetKindNode, model.NodeTargetKindK8s),
		"nsId":    stringProp("CB-Tumblebug namespace of the Infra"),
		"infraId": stringProp("CB-Tumblebug Infra the node belongs to"),
		"nodeId":  stringProp("Node within the Infra"),
	}, "kind", "nsId", "infraId", "nodeId")
	return object(map[string]any{
		"type": enumProp("What to deploy; the field of the same name carries it",
			model.NodeDeployTypeModelService, model.NodeDeployTypeApp),
		"targets": map[string]any{
			"type": "array", "description": "Nodes to deploy to, each once, at most 32", "items": target,
		},
		"modelService": object(map[string]any{
			"modelId":    stringProp("Model id from " + ToolApplications),
			"artifactId": stringProp("Artifact id of that model"),
			"engine":     stringProp("Engine profile id the artifact lists. Omit for its first engine"),
			"gpu": map[string]any{"type": "boolean", "description": "Give the engine every NVIDIA GPU of the node. " +
				"Omit to take it from the node spec"},
			"env": envProp("Engine container environment. " + envNote),
		}, "modelId", "artifactId"),
		"app": object(map[string]any{
			"name":    stringProp("Uploaded application name from " + ToolApplications),
			"version": map[string]any{"type": "integer", "description": "Version number. Omit or 0 for the latest"},
			"env":     envProp("Exported before the script runs, for example addresses of other nodes. " + envNote),
		}, "name"),
	}, "type", "targets")
}

func workflowHandler(e *Engine, workflow string) tools.Handler {
	return func(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
		return e.Execute(ctx, nsID, workflow, input)
	}
}

func object(properties map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{
		"type":       "object",
		"properties": properties,
		"required":   required,
	}
}

func stringProp(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func enumProp(description string, values ...string) map[string]any {
	return map[string]any{"type": "string", "description": description, "enum": values}
}

func envProp(description string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"description":          description,
		"additionalProperties": map[string]any{"type": "string"},
	}
}
