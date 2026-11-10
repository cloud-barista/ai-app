package model

import "slices"

// Runtime profile IDs built into this service.
const (
	RuntimeVLLM   = "vllm"
	RuntimeOllama = "ollama"
)

// Artifact formats the built-in runtime profiles load.
const (
	// FormatHF is a Hugging Face repository of safetensors weights.
	FormatHF = "hf"
	// FormatGGUF is a GGUF file, as Ollama and llama.cpp load it.
	FormatGGUF = "gguf"
)

// RuntimeProfile is an inference runtime an inference deployment can be run
// with: the container image, the artifact formats it loads, and how it is
// reached.
//
// Profiles are built in and read only. An inference deployment pairs an
// artifact with a profile, and the pairing is refused unless the artifact
// names the runtime and the runtime loads the artifact format.
type RuntimeProfile struct {
	// ID is the runtime identifier artifacts refer to in runtimes[].runtimeId.
	ID string `json:"id" validate:"required" example:"vllm"`
	// Image is the container image, pinned to a release tag.
	Image string `json:"image" validate:"required" example:"vllm/vllm-openai:v0.32.0"`
	// Formats are the artifact formats this runtime loads.
	Formats []string `json:"formats" validate:"required" example:"hf"`
	// Port is the TCP port the runtime serves its API on, inside the container
	// and on the node.
	Port int `json:"port" validate:"required" example:"8000"`
	// HealthPath answers 200 once the runtime is up.
	HealthPath string `json:"healthPath,omitempty" example:"/health"`
	// APIBasePath is the path of the OpenAI compatible API, appended to
	// http://<node>:<port> by whoever calls the inference service.
	APIBasePath string `json:"apiBasePath,omitempty" example:"/v1"`
}

// RuntimeProfileListResp is the runtime profile listing body.
type RuntimeProfileListResp struct {
	Runtimes []RuntimeProfile `json:"runtimes"`
}

// Validate reports why an RuntimeProfile is not usable.
func (e *RuntimeProfile) Validate() error {
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

// LoadsFormat reports whether the runtime loads artifacts of format.
func (e *RuntimeProfile) LoadsFormat(format string) bool {
	return slices.Contains(e.Formats, format)
}
