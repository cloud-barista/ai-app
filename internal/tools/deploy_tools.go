package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ish-hcc/ai-agent-proto/internal/catalog"
	"github.com/ish-hcc/ai-agent-proto/internal/deploy"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// Lifecycle actions this service forwards.
//
// Recovery actions (refine, reconcile, abort, withdraw) are deliberately left
// out: they change resource bookkeeping in ways that need a deliberate decision
// by an operator, not by a planning model.
var allowedActions = map[string]bool{
	"suspend":   true,
	"resume":    true,
	"reboot":    true,
	"terminate": true,
}

// Register adds every deployment tool to the registry.
func Register(registry *Registry, apps *catalog.Store, service *deploy.Service) {
	registry.Add(listAppsTool(apps))
	registry.Add(findAppsTool(apps))
	registry.Add(getAppTool(apps))
	registry.Add(recommendSpecTool(apps, service))
	registry.Add(resolveImageTool(apps, service))
	registry.Add(listDeploymentsTool(service))
	registry.Add(deploymentStatusTool(service))
	registry.Add(probeAcceleratorTool(apps, service))
	registry.Add(planDeploymentTool(service))
	registry.Add(reviewDeploymentTool(service))
	registry.Add(deployTool(service))
	registry.Add(controlTool(service))
	registry.Add(deleteTool(service))
}

func listAppsTool(apps *catalog.Store) Tool {
	return Tool{
		Name:  "list_ai_apps",
		Grade: GradeRead,
		Description: "List every registered AI application in short form: id, runtime, model, accelerator " +
			"requirement and serving port. Use it to see what the catalog holds. " +
			"When the operator described an application in words, call find_ai_apps instead.",
		InputSchema: object(map[string]any{}),
		Handle: func(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
			return model.AppSummaryListResp{Apps: apps.Summaries(ctx)}, nil
		},
	}
}

func findAppsTool(apps *catalog.Store) Tool {
	return Tool{
		Name:  "find_ai_apps",
		Grade: GradeRead,
		Description: "Rank registered AI applications against the operator's own words and return the best " +
			"candidates with the signals that matched. An empty result means the catalog holds nothing " +
			"that fits, which is an answer: say so rather than deploying the closest entry.",
		InputSchema: object(map[string]any{
			"query": stringProp("The operator's instruction, in their own words"),
			"limit": integerProp("How many candidates to return. Defaults to 5"),
		}, "query"),
		Handle: func(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
			var args struct {
				Query string `json:"query"`
				Limit int    `json:"limit"`
			}
			if err := decode(input, &args); err != nil {
				return nil, err
			}
			if args.Query == "" {
				return nil, fmt.Errorf("query required")
			}
			return map[string]any{"candidates": apps.Resolve(ctx, args.Query, args.Limit)}, nil
		},
	}
}

func getAppTool(apps *catalog.Store) Tool {
	return Tool{
		Name:        "get_ai_app",
		Grade:       GradeRead,
		Description: "Read one registered AI application, including its install commands and serving port.",
		InputSchema: object(map[string]any{
			"appId": stringProp("Identifier of the registered AI application"),
		}, "appId"),
		Handle: func(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
			var args struct {
				AppID string `json:"appId"`
			}
			if err := decode(input, &args); err != nil {
				return nil, err
			}
			return apps.Get(ctx, args.AppID)
		},
	}
}

func recommendSpecTool(apps *catalog.Store, service *deploy.Service) Tool {
	return Tool{
		Name:  "recommend_accelerator_spec",
		Grade: GradeRead,
		Description: "Find node specs across every connected cloud that satisfy the accelerator requirement of an " +
			"AI application. Results are ordered cheapest first and include the accelerator model, count and memory.",
		InputSchema: object(map[string]any{
			"appId": stringProp("Identifier of the registered AI application"),
			"limit": integerProp("How many candidates to return. Defaults to 5"),
		}, "appId"),
		Handle: func(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
			var args struct {
				AppID string `json:"appId"`
				Limit int    `json:"limit"`
			}
			if err := decode(input, &args); err != nil {
				return nil, err
			}
			app, err := apps.Get(ctx, args.AppID)
			if err != nil {
				return nil, err
			}
			candidates, err := service.RecommendSpecs(ctx, app, args.Limit)
			if err != nil {
				return nil, err
			}
			return map[string]any{"candidates": candidates}, nil
		},
	}
}

func resolveImageTool(apps *catalog.Store, service *deploy.Service) Tool {
	return Tool{
		Name:  "resolve_node_image",
		Grade: GradeRead,
		Description: "Pick the node image an AI application should boot on a given spec. " +
			"An accelerator image is preferred because it ships the vendor driver. " +
			"Only call this when you want to explain or override the image; deploying resolves it on its own.",
		InputSchema: object(map[string]any{
			"appId":  stringProp("Identifier of the registered AI application"),
			"specId": stringProp("Node spec the image has to boot on"),
		}, "appId", "specId"),
		Handle: func(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
			var args struct {
				AppID  string `json:"appId"`
				SpecID string `json:"specId"`
			}
			if err := decode(input, &args); err != nil {
				return nil, err
			}
			app, err := apps.Get(ctx, args.AppID)
			if err != nil {
				return nil, err
			}
			return service.ResolveImage(ctx, app, args.SpecID)
		},
	}
}

func listDeploymentsTool(service *deploy.Service) Tool {
	return Tool{
		Name:        "list_deployments",
		Grade:       GradeRead,
		Description: "List every deployment in the namespace with its node status.",
		InputSchema: object(map[string]any{}),
		Handle: func(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
			list, err := service.List(ctx, nsID)
			if err != nil {
				return nil, err
			}
			return map[string]any{"deployments": list}, nil
		},
	}
}

func deploymentStatusTool(service *deploy.Service) Tool {
	return Tool{
		Name:        "get_deployment_status",
		Grade:       GradeRead,
		Description: "Read the node status summary of one deployment. Use it to confirm an application came up.",
		InputSchema: object(map[string]any{
			"infraId": stringProp("Identifier of the deployment"),
		}, "infraId"),
		Handle: func(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
			var args struct {
				InfraID string `json:"infraId"`
			}
			if err := decode(input, &args); err != nil {
				return nil, err
			}
			return service.Status(ctx, nsID, args.InfraID)
		},
	}
}

func probeAcceleratorTool(apps *catalog.Store, service *deploy.Service) Tool {
	return Tool{
		Name:  "probe_accelerator",
		Grade: GradeRead,
		Description: "Ask the deployed nodes what accelerator they actually carry and compare it against what " +
			"the application asked for. Reports the device name, memory, driver version and MIG state, and " +
			"lists where the node and the catalog disagree. Use it when a deployment looks healthy but the " +
			"application is not using the accelerator.",
		InputSchema: object(map[string]any{
			"infraId": stringProp("Identifier of the deployment"),
			"appId":   stringProp("Registered application to compare against. Optional"),
		}, "infraId"),
		Handle: func(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
			var args struct {
				InfraID string `json:"infraId"`
				AppID   string `json:"appId"`
			}
			if err := decode(input, &args); err != nil {
				return nil, err
			}
			var app *model.AppSpec
			if args.AppID != "" {
				found, err := apps.Get(ctx, args.AppID)
				if err != nil {
					return nil, err
				}
				app = found
			}
			return service.ProbeAccelerator(ctx, nsID, args.InfraID, app)
		},
	}
}

func planDeploymentTool(service *deploy.Service) Tool {
	return Tool{
		Name:  "plan_deployment",
		Grade: GradeRead,
		Description: "Build the provisioning request for an AI application without contacting any cloud. " +
			"Leave specId empty to let the accelerator requirement pick the cheapest matching spec.",
		InputSchema: deployInputSchema(),
		Handle: func(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
			req, err := decodeDeployReq(input)
			if err != nil {
				return nil, err
			}
			return service.Plan(ctx, nsID, req)
		},
	}
}

func reviewDeploymentTool(service *deploy.Service) Tool {
	return Tool{
		Name:  "review_deployment",
		Grade: GradeReview,
		Description: "Ask the cloud manager to validate a provisioning request without creating anything. " +
			"Returns whether creation is viable and the estimated hourly cost. Always review before deploying.",
		InputSchema: deployInputSchema(),
		Handle: func(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
			req, err := decodeDeployReq(input)
			if err != nil {
				return nil, err
			}
			plan, err := service.Plan(ctx, nsID, req)
			if err != nil {
				return nil, err
			}
			review, err := service.Review(ctx, plan)
			if err != nil {
				return nil, err
			}
			return map[string]any{"plan": plan, "review": review}, nil
		},
	}
}

func deployTool(service *deploy.Service) Tool {
	return Tool{
		Name:  "deploy_ai_app",
		Grade: GradeWrite,
		Description: "Provision nodes and install the AI application on them in one call. " +
			"This creates billable resources and blocks for several minutes. " +
			"The request is reviewed first and refused if a deployment of the same name already exists.",
		InputSchema: deployInputSchema(),
		Handle: func(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
			req, err := decodeDeployReq(input)
			if err != nil {
				return nil, err
			}
			return service.Deploy(ctx, nsID, req)
		},
	}
}

func controlTool(service *deploy.Service) Tool {
	return Tool{
		Name:  "control_deployment",
		Grade: GradeWrite,
		Description: "Apply a lifecycle action to every node of a deployment. " +
			"terminate stops the nodes but keeps the records, so delete afterwards to stop billing.",
		InputSchema: object(map[string]any{
			"infraId": stringProp("Identifier of the deployment"),
			"action":  enumProp("Lifecycle action to apply", "suspend", "resume", "reboot", "terminate"),
		}, "infraId", "action"),
		Handle: func(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
			var args struct {
				InfraID string `json:"infraId"`
				Action  string `json:"action"`
			}
			if err := decode(input, &args); err != nil {
				return nil, err
			}
			if !allowedActions[args.Action] {
				return nil, fmt.Errorf("action must be suspend, resume, reboot or terminate")
			}
			return service.Control(ctx, nsID, args.InfraID, args.Action)
		},
	}
}

func deleteTool(service *deploy.Service) Tool {
	return Tool{
		Name:  "delete_deployment",
		Grade: GradeDestructive,
		Description: "Terminate every node of a deployment and remove its records. This cannot be undone. " +
			"Only call it when the operator asked for the deployment to be removed.",
		InputSchema: object(map[string]any{
			"infraId": stringProp("Identifier of the deployment"),
		}, "infraId"),
		Handle: func(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
			var args struct {
				InfraID string `json:"infraId"`
			}
			if err := decode(input, &args); err != nil {
				return nil, err
			}
			return service.Delete(ctx, nsID, args.InfraID)
		},
	}
}

func deployInputSchema() map[string]any {
	return object(map[string]any{
		"appId":        stringProp("Identifier of the registered AI application"),
		"infraName":    stringProp("Name for the deployment. It is also the idempotency key"),
		"specId":       stringProp("Node spec to pin. Leave empty to pick the cheapest matching spec"),
		"imageId":      stringProp("Node image to pin. Leave empty for the default"),
		"sgTemplateId": stringProp("Security group template to start from. Leave empty for the cloud manager default, which opens every port"),
		"nodeCount":    integerProp("How many serving nodes to provision. Defaults to 1"),
	}, "appId", "infraName")
}

func decodeDeployReq(input json.RawMessage) (*model.DeployAppReq, error) {
	var req model.DeployAppReq
	if err := decode(input, &req); err != nil {
		return nil, err
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	return &req, nil
}

// decode reads tool arguments.
//
// Tool inputs are model generated JSON and must always be parsed, never matched
// as strings: escaping of the serialized form is not stable.
func decode(input json.RawMessage, out any) error {
	if len(input) == 0 {
		return fmt.Errorf("tool arguments required")
	}
	if err := json.Unmarshal(input, out); err != nil {
		return fmt.Errorf("failed to decode tool arguments: %w", err)
	}
	return nil
}
