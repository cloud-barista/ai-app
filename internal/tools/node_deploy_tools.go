package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/nodedeploy"
)

// RegisterNodeDeploy adds the tools that deploy onto nodes CB-Tumblebug
// already runs, read those deployments, take them down, and control their
// nodes.
//
// The namespace a tool call runs in is not used: every target names its own
// CB-Tumblebug namespace, as the REST route does.
func RegisterNodeDeploy(registry *Registry, svc *nodedeploy.Service) {
	registry.Add(createDeploymentTool(svc))
	registry.Add(getDeploymentTool(svc))
	registry.Add(listNodeDeploymentsTool(svc))
	registry.Add(listEndpointsTool(svc))
	registry.Add(getDeploymentLogsTool(svc))
	registry.Add(deleteDeploymentTool(svc))
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
		"sshUser": stringProp("SSH user to run the playbooks as, with the key CB-Tumblebug holds. Omit it and the " +
			"CB-Tumblebug user is tried first; when the node refuses it (CB-Tumblebug could not create cb-user on RHEL, " +
			"Rocky, AlmaLinux or Amazon Linux images), the image default user such as ec2-user is found and used. " +
			"Give it to choose the user yourself"),
	}, "kind", "nsId", "infraId", "nodeId")
	envNote := "Written to a .env file beside the deployed files. Names match [A-Z_][A-Z0-9_]*; values hold no line break and no single quote."
	return Tool{
		Name:  "create_deployment",
		Grade: GradeWrite,
		Description: "Deploy an inference service or an uploaded AI application onto nodes CB-Tumblebug already runs; " +
			"no VM is created. type inference renders a compose file for a model artifact and a runtime (see " +
			"list_models, list_runtimes) and starts it; type app copies an uploaded application version (see list_apps) and runs its " +
			"script. Each node is reached with its own SSH key through Semaphore: docker is installed when missing, then " +
			"the service is started, and the ports it serves are opened in the node's CB-Tumblebug security groups. Returns the deployment record at once with status pending; follow it with " +
			"get_deployment until it is running, failed or partial, and read the task output with get_deployment_logs. " +
			"With dry run on, only the files are placed, nothing runs, and the record comes back with status dry-run.",
		InputSchema: object(map[string]any{
			"type": enumProp("What to deploy", model.NodeDeployTypeInference, model.NodeDeployTypeApp),
			"targets": map[string]any{
				"type": "array", "description": "Nodes to deploy to, each once, at most 32", "items": target,
			},
			"inference": object(map[string]any{
				"modelId":    stringProp("Model id from list_models"),
				"artifactId": stringProp("Artifact id of that model"),
				"runtimeId":  stringProp("Runtime profile id from list_runtimes that the artifact lists. Omit for its first runtime"),
				"gpu": map[string]any{"type": "boolean", "description": "Give the runtime every NVIDIA GPU of the node. " +
					"Omit to take it from the node spec (acceleratorType gpu)"},
				"env": envProp("Runtime container environment. " + envNote),
				"args": map[string]any{
					"type": "array", "items": map[string]any{"type": "string"},
					"description": "Extra arguments appended to the runtime command after the ones the service sets " +
						"and the artifact's catalog args (runtimes[].args, which a repeated flag here overrides for vllm), " +
						"such as [\"--max-model-len\", \"32768\"] for vllm. At most 32, each 1 to 256 characters without control " +
						"characters; --model, --port and --host are refused. Runtime ollama refuses args",
				},
			}, "modelId", "artifactId"),
			"app": object(map[string]any{
				"name":    stringProp("Uploaded application name from list_apps"),
				"version": integerProp("Version number. Omit or 0 for the latest"),
				"env":     envProp("Exported before the script runs, for example addresses of other nodes. " + envNote),
				"ports": map[string]any{
					"type": "array", "items": map[string]any{"type": "integer", "minimum": 1, "maximum": 65535},
					"description": "TCP ports to open for this deployment, at most 16. Omit to take the ports the app version was uploaded with",
				},
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
		Description: "Read one deployment made by create_deployment: its status (pending, deploying, running, failed, " +
			"partial, dry-run, interrupted, or removing, removed, remove-failed once taken down by delete_deployment) and for every node its status, address, whether it got the GPU, the " +
			"rendered compose and service URL of an inference service, and each playbook run with its status. Task output " +
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
		Description: "List the deployments made by create_deployment, newest first, with the status of every node. " +
			"Every filter is optional and matched exactly; nsId matches a deployment when any of its targets is in " +
			"that namespace. Task output is left out.",
		InputSchema: object(map[string]any{
			"type":   enumProp("Deployment type", model.NodeDeployTypeInference, model.NodeDeployTypeApp),
			"nsId":   stringProp("CB-Tumblebug namespace of any target"),
			"status": stringProp("Deployment status: pending, deploying, running, failed, partial, dry-run, interrupted, removing, removed or remove-failed"),
		}),
		Handle: func(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
			var filter nodedeploy.ListFilter
			if len(input) > 0 {
				var raw struct {
					Type   string `json:"type"`
					NsID   string `json:"nsId"`
					Status string `json:"status"`
				}
				if err := decode(input, &raw); err != nil {
					return nil, err
				}
				filter = nodedeploy.ListFilter(raw)
			}
			return model.NodeDeploymentListResp{Deployments: svc.List(ctx, filter)}, nil
		},
	}
}

func listEndpointsTool(svc *nodedeploy.Service) Tool {
	return Tool{
		Name:  "list_endpoints",
		Grade: GradeRead,
		Description: "List the addresses of the inference services and applications serving on the nodes now, one per " +
			"node and work key (ms-<runtimeId> for an inference service, app-<name> for an app), derived from the " +
			"deployments of create_deployment. The latest deployment of a work key on a node decides it: it is listed " +
			"only when that deployment is running there, so a redeploy replaces it, delete_deployment removes it, and a " +
			"later deployment that failed after starting its service leaves none. Dry runs are never listed. An " +
			"inference endpoint has runtimeId, modelId, artifactId, url (OpenAI compatible API) and healthUrl; an app " +
			"endpoint has name, version and one url per port. portsOpened says whether every port was opened in the " +
			"node's security groups; portsError and notes say why not. Every filter is optional and matched exactly.",
		InputSchema: object(map[string]any{
			"nsId":    stringProp("CB-Tumblebug namespace of the node"),
			"type":    enumProp("Endpoint type", model.NodeDeployTypeInference, model.NodeDeployTypeApp),
			"infraId": stringProp("CB-Tumblebug Infra of the node"),
			"nodeId":  stringProp("Node within the Infra"),
		}),
		Handle: func(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
			var filter nodedeploy.EndpointFilter
			if len(input) > 0 {
				var raw struct {
					NsID    string `json:"nsId"`
					Type    string `json:"type"`
					InfraID string `json:"infraId"`
					NodeID  string `json:"nodeId"`
				}
				if err := decode(input, &raw); err != nil {
					return nil, err
				}
				filter = nodedeploy.EndpointFilter(raw)
			}
			return model.EndpointListResp{Endpoints: svc.Endpoints(ctx, filter)}, nil
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

func deleteDeploymentTool(svc *nodedeploy.Service) Tool {
	return Tool{
		Name:  "delete_deployment",
		Grade: GradeDestructive,
		Description: "Take a deployment made by create_deployment down from its nodes: its containers are stopped " +
			"and removed (docker compose down of its work key), while named volumes such as downloaded model weights " +
			"and the node folder are kept. A node where a later deployment of the same work key replaced this one is " +
			"left running and marked removed with a note naming the later deployment. Returns the record with status " +
			"removing; follow it with get_deployment until it is removed or remove-failed. Nothing needs running, and " +
			"the record comes back removed at once, for a dry-run deployment or nodes where the service never started. " +
			"A deployment still pending or deploying is refused. With dry run on, nothing is sent and the nodes keep " +
			"their status with a note.",
		InputSchema: object(map[string]any{
			"id": stringProp("Deployment id returned by create_deployment"),
		}, "id"),
		Handle: func(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
			id, err := deploymentIDArgs(input)
			if err != nil {
				return nil, err
			}
			return svc.Remove(ctx, id)
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
