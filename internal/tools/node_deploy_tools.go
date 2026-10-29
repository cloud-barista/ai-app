package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/nodedeploy"
)

// RegisterNodeDeploy adds the tools that deploy onto nodes CB-Tumblebug
// already runs, read those deployments, and control their nodes.
//
// The namespace a tool call runs in is not used: every target names its own
// CB-Tumblebug namespace, as the REST route does.
func RegisterNodeDeploy(registry *Registry, svc *nodedeploy.Service) {
	registry.Add(createDeploymentTool(svc))
	registry.Add(getDeploymentTool(svc))
	registry.Add(listNodeDeploymentsTool(svc))
	registry.Add(getDeploymentLogsTool(svc))
	registry.Add(controlNodeTool(svc))
}

func envProp(description string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"description":          description,
		"additionalProperties": map[string]any{"type": "string"},
	}
}

func createDeploymentTool(svc *nodedeploy.Service) Tool {
	target := object(map[string]any{
		"kind":    enumProp("node; k8s is reserved and refused", model.NodeTargetKindNode, model.NodeTargetKindK8s),
		"nsId":    stringProp("CB-Tumblebug namespace of the Infra"),
		"infraId": stringProp("CB-Tumblebug Infra the node belongs to"),
		"nodeId":  stringProp("Node within the Infra"),
	}, "kind", "nsId", "infraId", "nodeId")
	envNote := "Written to a .env file beside the deployed files. Names match [A-Z_][A-Z0-9_]*; values hold no line break and no single quote."
	return Tool{
		Name:  "create_deployment",
		Grade: GradeWrite,
		Description: "Deploy a model service or an uploaded AI application onto nodes CB-Tumblebug already runs; no VM " +
			"is created. type model-service renders a compose file for a model artifact and an engine (see list_models, " +
			"list_engines) and starts it; type app copies an uploaded application version (see list_apps) and runs its " +
			"script. Each node is reached with its own SSH key through Semaphore: docker is installed when missing, then " +
			"the service is started. Returns the deployment record at once with status pending; follow it with " +
			"get_deployment until it is succeeded, failed or partial, and read the task output with get_deployment_logs. " +
			"With dry run on, only the files are placed, nothing runs, and the record comes back with status dry-run.",
		InputSchema: object(map[string]any{
			"type": enumProp("What to deploy", model.NodeDeployTypeModelService, model.NodeDeployTypeApp),
			"targets": map[string]any{
				"type": "array", "description": "Nodes to deploy to, each once, at most 32", "items": target,
			},
			"modelService": object(map[string]any{
				"modelId":    stringProp("Model id from list_models"),
				"artifactId": stringProp("Artifact id of that model"),
				"engine":     stringProp("Engine profile id the artifact lists. Omit for its first engine"),
				"gpu": map[string]any{"type": "boolean", "description": "Give the engine every NVIDIA GPU of the node. " +
					"Omit to take it from the node spec (acceleratorType gpu)"},
				"env": envProp("Engine container environment. " + envNote),
			}, "modelId", "artifactId"),
			"app": object(map[string]any{
				"name":    stringProp("Uploaded application name from list_apps"),
				"version": integerProp("Version number. Omit or 0 for the latest"),
				"env":     envProp("Exported before the script runs, for example addresses of other nodes. " + envNote),
			}, "name"),
		}, "type", "targets"),
		Handle: func(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
			var req model.NodeDeploymentReq
			if err := decode(input, &req); err != nil {
				return nil, err
			}
			return svc.Create(ctx, req)
		},
	}
}

func deploymentIDArgs(input json.RawMessage) (string, error) {
	var args struct {
		ID string `json:"id"`
	}
	if err := decode(input, &args); err != nil {
		return "", err
	}
	if args.ID == "" {
		return "", fmt.Errorf("id required")
	}
	return args.ID, nil
}

func getDeploymentTool(svc *nodedeploy.Service) Tool {
	return Tool{
		Name:  "get_deployment",
		Grade: GradeRead,
		Description: "Read one deployment made by create_deployment: its status (pending, running, succeeded, failed, " +
			"partial, dry-run, interrupted) and for every node its status, address, whether it got the GPU, the " +
			"rendered compose and service URL of a model service, and each playbook run with its status. Task output " +
			"is left out; read it with get_deployment_logs.",
		InputSchema: object(map[string]any{
			"id": stringProp("Deployment id returned by create_deployment"),
		}, "id"),
		Handle: func(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
			id, err := deploymentIDArgs(input)
			if err != nil {
				return nil, err
			}
			return svc.Get(ctx, id)
		},
	}
}

func listNodeDeploymentsTool(svc *nodedeploy.Service) Tool {
	return Tool{
		Name:  "list_node_deployments",
		Grade: GradeRead,
		Description: "List every deployment made by create_deployment, newest first, with the status of every node. " +
			"Task output is left out.",
		InputSchema: object(map[string]any{}),
		Handle: func(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
			return model.NodeDeploymentListResp{Deployments: svc.List(ctx)}, nil
		},
	}
}

func getDeploymentLogsTool(svc *nodedeploy.Service) Tool {
	return Tool{
		Name:  "get_deployment_logs",
		Grade: GradeRead,
		Description: "Read the task output of every playbook run on every node of a deployment made by " +
			"create_deployment. A step's output appears when its task ends.",
		InputSchema: object(map[string]any{
			"id": stringProp("Deployment id returned by create_deployment"),
		}, "id"),
		Handle: func(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
			id, err := deploymentIDArgs(input)
			if err != nil {
				return nil, err
			}
			return svc.Logs(ctx, id)
		},
	}
}

func controlNodeTool(svc *nodedeploy.Service) Tool {
	return Tool{
		Name:  "control_node",
		Grade: GradeWrite,
		Description: "Suspend, resume, reboot or terminate every node of a deployment made by create_deployment, " +
			"through CB-Tumblebug. Terminate deletes the VMs. Each node is reported done, failed, or blocked when dry " +
			"run is on or the deployment was a dry run.",
		InputSchema: object(map[string]any{
			"id":     stringProp("Deployment id returned by create_deployment"),
			"action": enumProp("Lifecycle action", "suspend", "resume", "reboot", "terminate"),
		}, "id", "action"),
		Handle: func(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
			var args struct {
				ID     string `json:"id"`
				Action string `json:"action"`
			}
			if err := decode(input, &args); err != nil {
				return nil, err
			}
			if args.ID == "" {
				return nil, fmt.Errorf("id required")
			}
			return svc.Control(ctx, args.ID, args.Action)
		},
	}
}
