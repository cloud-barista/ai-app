package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/modelcat"
)

// RegisterModels adds the model catalog tools to the registry. They only read.
func RegisterModels(registry *Registry, models *modelcat.Store) {
	registry.Add(listModelsTool(models))
	registry.Add(getModelTool(models))
	registry.Add(listEnginesTool())
}

func listModelsTool(models *modelcat.Store) Tool {
	return Tool{
		Name:  "list_models",
		Grade: GradeRead,
		Description: "List AI models in the model catalog with every artifact each is published as: source, ref, " +
			"file format, precision, size and compatible engines. Every filter is optional and ignores case. " +
			"task and modality match the model; format, engine and source match an artifact, and a model is " +
			"listed only when one artifact matches all three. An absent contextLength, sizeGiB or precision " +
			"means unknown, not zero.",
		InputSchema: object(map[string]any{
			"task":     stringProp("Model task, such as chat, vision-chat or embedding"),
			"modality": stringProp("Input modality the model accepts, such as text or image"),
			"format":   stringProp("Artifact file format: hf (Hugging Face safetensors) or gguf"),
			"engine":   stringProp("Engine profile id an artifact runs on, as list_engines reports it"),
			"source":   stringProp("Artifact source, such as huggingface or ollama"),
		}),
		Handle: func(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
			var args modelcat.Filter
			if len(input) > 0 {
				var raw struct {
					Task     string `json:"task"`
					Modality string `json:"modality"`
					Format   string `json:"format"`
					Engine   string `json:"engine"`
					Source   string `json:"source"`
				}
				if err := decode(input, &raw); err != nil {
					return nil, err
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

func listEnginesTool() Tool {
	return Tool{
		Name:  "list_engines",
		Grade: GradeRead,
		Description: "List the inference engine profiles a model service can run with: pinned container image, " +
			"artifact formats it loads, serving port, health path and OpenAI compatible API path. An artifact " +
			"runs on an engine when the artifact lists the engine id and the engine lists the artifact format.",
		InputSchema: object(map[string]any{}),
		Handle: func(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
			return model.EngineProfileListResp{Engines: modelcat.Engines()}, nil
		},
	}
}
