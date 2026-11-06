package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/modelcat"
)

// RegisterModels adds the model catalog tools to the registry. They only read.
func RegisterModels(registry *Registry, models *modelcat.Store) {
	registry.Add(listModelsTool(models))
	registry.Add(getModelTool(models))
	registry.Add(listRuntimesTool())
	registry.Add(getRuntimeTool())
}

func listModelsTool(models *modelcat.Store) Tool {
	return Tool{
		Name:  "list_models",
		Grade: GradeRead,
		Description: "List AI models in the model catalog with every artifact each is published as: source, ref, " +
			"file format, precision, size and compatible runtimes. Every filter is optional and ignores case. " +
			"task and modality match the model; format, runtimeId and source match an artifact, and a model is " +
			"listed only when one artifact matches all three. origin matches who put the model in the catalog; " +
			"verified=true keeps models with an artifact that has deployed on a runtime (runtimes[].verifiedAt " +
			"set), verified=false the others. An artifact with no runtimes cannot be deployed: its deployable is " +
			"false, and a model is deployable when some artifact is; deployable=true keeps the deployable models, " +
			"deployable=false the others. An absent " +
			"contextLength, sizeGiB or precision means unknown, not zero.",
		InputSchema: object(map[string]any{
			"task":      stringProp("Model task, such as chat, vision-chat or embedding"),
			"modality":  stringProp("Input modality the model accepts, such as text or image"),
			"format":    stringProp("Artifact file format: hf (Hugging Face safetensors) or gguf"),
			"runtimeId": stringProp("Runtime profile id an artifact runs on, as list_runtimes reports it"),
			"source":    stringProp("Artifact source, such as huggingface or ollama"),
			"origin": enumProp("Who put the model in the catalog: seed, manual, or the source the catalog sync "+
				"collected it from", model.Origins...),
			"verified": map[string]any{"type": "boolean", "description": "true keeps models with an artifact that " +
				"has deployed on a runtime, false the others"},
			"deployable": map[string]any{"type": "boolean", "description": "true keeps models with an artifact that " +
				"lists a runtime, false the others"},
		}),
		Handle: func(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
			var args modelcat.Filter
			if len(input) > 0 {
				var raw struct {
					Task       string `json:"task"`
					Modality   string `json:"modality"`
					Format     string `json:"format"`
					RuntimeID  string `json:"runtimeId"`
					Source     string `json:"source"`
					Origin     string `json:"origin"`
					Verified   *bool  `json:"verified"`
					Deployable *bool  `json:"deployable"`
				}
				if err := decode(input, &raw); err != nil {
					return nil, err
				}
				if !model.ValidOrigin(strings.ToLower(raw.Origin)) {
					return nil, fmt.Errorf("origin must be one of %s", strings.Join(model.Origins, ", "))
				}
				args = modelcat.Filter(raw)
			}
			return model.ModelListResp{Models: models.List(ctx, args)}, nil
		},
	}
}

func getModelTool(models *modelcat.Store) Tool {
	return Tool{
		Name:        "get_model",
		Grade:       GradeRead,
		Description: "Read one AI model of the model catalog with every artifact it is published as.",
		InputSchema: object(map[string]any{
			"modelId": stringProp("Identifier of the model in the model catalog"),
		}, "modelId"),
		Handle: func(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
			var args struct {
				ModelID string `json:"modelId"`
			}
			if err := decode(input, &args); err != nil {
				return nil, err
			}
			if args.ModelID == "" {
				return nil, fmt.Errorf("modelId required")
			}
			return models.Get(ctx, args.ModelID)
		},
	}
}

func listRuntimesTool() Tool {
	return Tool{
		Name:  "list_runtimes",
		Grade: GradeRead,
		Description: "List the inference runtime profiles an inference deployment can run with: pinned container " +
			"image, artifact formats it loads, serving port, health path and OpenAI compatible API path. An " +
			"artifact runs on a runtime when the artifact lists the runtime id in runtimes[].runtimeId and the " +
			"runtime lists the artifact format.",
		InputSchema: object(map[string]any{}),
		Handle: func(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
			return model.RuntimeProfileListResp{Runtimes: modelcat.Runtimes()}, nil
		},
	}
}

func getRuntimeTool() Tool {
	return Tool{
		Name:  "get_runtime",
		Grade: GradeRead,
		Description: "Read one inference runtime profile: pinned container image, artifact formats it loads, serving " +
			"port, health path and OpenAI compatible API path.",
		InputSchema: object(map[string]any{
			"runtimeId": stringProp("Runtime profile id, as list_runtimes reports it"),
		}, "runtimeId"),
		Handle: func(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
			var args struct {
				RuntimeID string `json:"runtimeId"`
			}
			if err := decode(input, &args); err != nil {
				return nil, err
			}
			if args.RuntimeID == "" {
				return nil, fmt.Errorf("runtimeId required")
			}
			return modelcat.Runtime(args.RuntimeID)
		},
	}
}
