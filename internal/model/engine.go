package model

import "slices"

// Engine profile IDs built into this service.
const (
	EngineVLLM   = "vllm"
	EngineOllama = "ollama"
)

// Artifact formats the built-in engine profiles load.
const (
	// FormatHF is a Hugging Face repository of safetensors weights.
	FormatHF = "hf"
	// FormatGGUF is a GGUF file, as Ollama and llama.cpp load it.
	FormatGGUF = "gguf"
)

// EngineProfile is an inference engine a model service can be run with: the
// container image, the artifact formats it loads, and how it is reached.
//
// Profiles are built in and read only. A model service is deployed by pairing
// an artifact with a profile, and the pairing is refused unless the artifact
// names the engine and the engine loads the artifact format.
type EngineProfile struct {
	// ID is the engine identifier artifacts refer to in engines[].engine.
	ID string `json:"id" validate:"required" example:"vllm"`
	// Image is the container image, pinned to a release tag.
	Image string `json:"image" validate:"required" example:"vllm/vllm-openai:v0.32.0"`
	// Formats are the artifact formats this engine loads.
	Formats []string `json:"formats" validate:"required" example:"hf"`
	// Port is the TCP port the engine serves its API on, inside the container
	// and on the node.
	Port int `json:"port" validate:"required" example:"8000"`
	// HealthPath answers 200 once the engine is up.
	HealthPath string `json:"healthPath,omitempty" example:"/health"`
	// APIBasePath is the path of the OpenAI compatible API, appended to
	// http://<node>:<port> by whoever calls the model service.
	APIBasePath string `json:"apiBasePath,omitempty" example:"/v1"`
}

// EngineProfileListResp is the engine profile listing body.
type EngineProfileListResp struct {
	Engines []EngineProfile `json:"engines"`
}

// Validate reports why an EngineProfile is not usable.
func (e *EngineProfile) Validate() error {
	switch {
	case e.ID == "":
		return errRequired("id")
	case !catalogIDPattern.MatchString(e.ID):
		return errInvalid("id", "must match [a-z0-9][a-z0-9._-]* and be at most 63 characters")
	case e.Image == "":
		return errRequired("image")
	case len(e.Formats) == 0:
		return errInvalid("formats", "must name at least one format")
	case e.Port <= 0 || e.Port > 65535:
		return errInvalid("port", "must be between 1 and 65535")
	}
	if err := validateURLPath("healthPath", e.HealthPath); err != nil {
		return err
	}
	return validateURLPath("apiBasePath", e.APIBasePath)
}

// LoadsFormat reports whether the engine loads artifacts of format.
func (e *EngineProfile) LoadsFormat(format string) bool {
	return slices.Contains(e.Formats, format)
}
