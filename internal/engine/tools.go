package engine

import (
	"context"
	"encoding/json"

	"github.com/ish-hcc/ai-agent-proto/internal/tools"
)

// MCPServerName is how the engine MCP server introduces itself.
const MCPServerName = "ai-mcmp-app-automation-engine"

// Names of the tools the engine publishes, one per workflow.
const (
	ToolDeploy  = "deploy_application"
	ToolHeal    = "heal_application"
	ToolControl = "control_application"
	ToolStatus  = "application_status"
)

// gateNote tells a caller what a write workflow does under the dry-run gate.
const gateNote = " Write steps go through the AI-App dry-run gate; a held step is reported as blocked and the " +
	"steps that need it as waiting, never as done."

// NewRegistry builds the tool registry the engine publishes over MCP: one tool
// per workflow. The dry-run state is the AI-App one, read when asked.
func NewRegistry(e *Engine) *tools.Registry {
	registry := tools.NewRegistryFunc(func() bool { return e.DryRun(context.Background()) })
	registry.Add(tools.Tool{
		Name:  ToolDeploy,
		Grade: tools.GradeWrite,
		Description: "Deploy a registered AI application in the namespace. For an application, every model " +
			"service it requires that is not running is deployed first as <deploymentName>-<model service appId>. " +
			"Each deployment is planned (spec and image recommended unless pinned), reviewed, deployed and " +
			"checked until it answers. Returns the run with every step." + gateNote,
		InputSchema: object(map[string]any{
			"appId":          stringProp("Identifier of the registered AI application"),
			"deploymentName": stringProp("Name of the deployment of appId"),
			"specId":         stringProp("Node spec to pin for appId. Leave empty to have it recommended"),
			"imageId":        stringProp("Node image to pin for appId. Leave empty to have it recommended"),
		}, "appId", "deploymentName"),
		Handle: workflowHandler(e, WorkflowDeploy),
	})
	registry.Add(tools.Tool{
		Name:  ToolHeal,
		Grade: tools.GradeWrite,
		Description: "Check whether a deployment answers on its serving port; when it does not, apply its " +
			"application again once (install commands rerun on the same nodes) and check again." + gateNote,
		InputSchema: object(map[string]any{
			"deploymentId": stringProp("Identifier of the deployment"),
			"appId":        stringProp("Application to apply. Optional; defaults to the one the deployment was created with"),
		}, "deploymentId"),
		Handle: workflowHandler(e, WorkflowHeal),
	})
	registry.Add(tools.Tool{
		Name:  ToolControl,
		Grade: tools.GradeWrite,
		Description: "Apply a lifecycle action to every node of a deployment. terminate stops the nodes but " +
			"keeps the records." + gateNote,
		InputSchema: object(map[string]any{
			"deploymentId": stringProp("Identifier of the deployment"),
			"action": map[string]any{
				"type":        "string",
				"description": "Lifecycle action to apply",
				"enum":        []string{"suspend", "resume", "reboot", "terminate"},
			},
		}, "deploymentId", "action"),
		Handle: workflowHandler(e, WorkflowControl),
	})
	registry.Add(tools.Tool{
		Name:  ToolStatus,
		Grade: tools.GradeRead,
		Description: "Report the state of a deployment: node status, whether the application answers on its " +
			"serving port, and what accelerator the nodes actually carry. Changes nothing.",
		InputSchema: object(map[string]any{
			"deploymentId": stringProp("Identifier of the deployment"),
			"appId":        stringProp("Application to check against. Optional; defaults to the one the deployment was created with"),
		}, "deploymentId"),
		Handle: workflowHandler(e, WorkflowStatus),
	})
	return registry
}

func workflowHandler(e *Engine, workflow string) tools.Handler {
	return func(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
		return e.Execute(ctx, nsID, workflow, input)
	}
}

func object(properties map[string]any, required ...string) map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": properties,
		"required":   required,
	}
}

func stringProp(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}
