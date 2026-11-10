package modelcat

import (
	"errors"
	"fmt"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// ErrRuntimeNotFound is returned when no runtime profile matches an identifier.
var ErrRuntimeNotFound = errors.New("runtime profile not found")

// builtinRuntimes are the runtime profiles this service can run a model with.
//
// The image tags are pinned releases that were confirmed on Docker Hub
// (vllm/vllm-openai:v0.32.0 pushed 2026-10-18, ollama/ollama:0.40.13 pushed
// 2026-10-29). A floating tag would make the same deployment request start a
// different runtime on a different day.
func builtinRuntimes() []model.RuntimeProfile {
	return []model.RuntimeProfile{
		{
			ID:          model.RuntimeOllama,
			Image:       "ollama/ollama:0.40.13",
			Formats:     []string{model.FormatGGUF},
			Port:        11434,
			HealthPath:  "/",
			APIBasePath: "/v1",
		},
		{
			ID:          model.RuntimeVLLM,
			Image:       "vllm/vllm-openai:v0.32.0",
			Formats:     []string{model.FormatHF},
			Port:        8000,
			HealthPath:  "/health",
			APIBasePath: "/v1",
		},
	}
}

// Runtimes reads every built-in runtime profile, ordered by ID.
func Runtimes() []model.RuntimeProfile {
	return builtinRuntimes()
}

// Runtime reads one built-in runtime profile.
func Runtime(runtimeID string) (*model.RuntimeProfile, error) {
	for _, profile := range builtinRuntimes() {
		if profile.ID == runtimeID {
			return &profile, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrRuntimeNotFound, runtimeID)
}
