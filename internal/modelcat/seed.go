package modelcat

import (
	"context"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// Seed registers the built-in models: the three models of the ETRI travel
// planning portal scenario.
//
// Only values that were confirmed at the source are filled in. The Hugging
// Face values were read from https://huggingface.co/api/models/<id> on
// 2026-10-29; the Ollama tag is from its ollama.com tag page. A field left
// empty means "not confirmed", which is not the same as zero, and a consumer
// that sizes nodes from these records must treat it that way.
func Seed(ctx context.Context, store *Store) error {
	models := builtinModels()
	for _, m := range models {
		if _, err := store.Register(ctx, m); err != nil {
			return err
		}
	}
	log.Info().Int("count", len(models)).Msg("Seeded AI model catalog")
	return nil
}

func builtinModels() []model.Model {
	return []model.Model{
		{
			// Hugging Face pipeline tag feature-extraction, BF16 weights.
			ID:              "qwen3-embedding-0.6b",
			Name:            "Qwen3-Embedding-0.6B",
			Family:          "qwen3-embedding",
			Publisher:       "Qwen",
			Parameters:      "0.6B",
			Tasks:           []string{"embedding"},
			InputModalities: []string{"text"},
			Artifacts: []model.Artifact{
				{
					ID:        "hf-bf16",
					Source:    "huggingface",
					Ref:       "Qwen/Qwen3-Embedding-0.6B",
					Format:    model.FormatHF,
					Precision: "BF16",
					Engines:   []model.EngineCompat{{Engine: model.EngineVLLM}},
				},
			},
		},
		{
			// Hugging Face pipeline tag image-text-to-text, BF16 weights.
			ID:              "qwen3-vl-8b",
			Name:            "Qwen3-VL-8B-Instruct",
			Family:          "qwen3-vl",
			Publisher:       "Qwen",
			Parameters:      "8B",
			Tasks:           []string{"vision-chat"},
			InputModalities: []string{"text", "image"},
			Artifacts: []model.Artifact{
				{
					ID:        "hf-bf16",
					Source:    "huggingface",
					Ref:       "Qwen/Qwen3-VL-8B-Instruct",
					Format:    model.FormatHF,
					Precision: "BF16",
					Engines:   []model.EngineCompat{{Engine: model.EngineVLLM}},
				},
				{
					// ollama.com lists this tag at 6.2GB. That is a rounded
					// decimal figure, and converting it would print a GiB
					// value with precision it never had, so sizeGiB stays
					// unknown. The quantization is not stated on the tag
					// page either, so precision stays empty.
					ID:      "ollama-gguf",
					Source:  "ollama",
					Ref:     "qwen3-vl:8b",
					Format:  model.FormatGGUF,
					Engines: []model.EngineCompat{{Engine: model.EngineOllama}},
				},
			},
		},
		{
			// FP8 weights with some tensors in BF16; the architecture is
			// Mistral3ForConditionalGeneration. ETRI introduced it as an LLM,
			// but that architecture also takes images, so image input is
			// listed and still needs confirming.
			ID:              "ministral-3-8b",
			Name:            "Ministral-3-8B-Instruct-2512",
			Family:          "ministral-3",
			Publisher:       "mistralai",
			Parameters:      "8B",
			Tasks:           []string{"chat"},
			InputModalities: []string{"text", "image"},
			Artifacts: []model.Artifact{
				{
					ID:        "hf-fp8",
					Source:    "huggingface",
					Ref:       "mistralai/Ministral-3-8B-Instruct-2512",
					Format:    model.FormatHF,
					Precision: "FP8",
					Engines:   []model.EngineCompat{{Engine: model.EngineVLLM}},
				},
			},
		},
	}
}
