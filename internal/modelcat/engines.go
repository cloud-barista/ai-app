package modelcat

import (
	"errors"
	"fmt"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// ErrEngineNotFound is returned when no engine profile matches an identifier.
var ErrEngineNotFound = errors.New("engine profile not found")

// builtinEngines are the engine profiles this service can run a model with.
//
// The image tags are pinned releases that were confirmed on Docker Hub
// (vllm/vllm-openai:v0.32.0 pushed 2026-10-18, ollama/ollama:0.40.13 pushed
// 2026-10-29). A floating tag would make the same deployment request start a
// different engine on a different day.
func builtinEngines() []model.EngineProfile {
	return []model.EngineProfile{
		{
			ID:          model.EngineOllama,
			Image:       "ollama/ollama:0.40.13",
			Formats:     []string{model.FormatGGUF},
			Port:        11434,
			HealthPath:  "/",
			APIBasePath: "/v1",
		},
		{
			ID:          model.EngineVLLM,
			Image:       "vllm/vllm-openai:v0.32.0",
			Formats:     []string{model.FormatHF},
			Port:        8000,
			HealthPath:  "/health",
			APIBasePath: "/v1",
		},
	}
}

// Engines reads every built-in engine profile, ordered by ID.
func Engines() []model.EngineProfile {
	return builtinEngines()
}

// Engine reads one built-in engine profile.
func Engine(engineID string) (*model.EngineProfile, error) {
	for _, profile := range builtinEngines() {
		if profile.ID == engineID {
			return &profile, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrEngineNotFound, engineID)
}
