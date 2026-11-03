package model

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Origins of a catalog entry: who put it in the catalog and keeps it up to
// date.
const (
	// OriginSeed is a built-in model registered when the catalog file did not
	// exist yet.
	OriginSeed = "seed"
	// OriginManual is a model a person registered or edited through the API.
	OriginManual = "manual"
	// OriginHuggingFace, OriginOllama, OriginDockerModelRunner, OriginNGC and
	// OriginKaggle are models the catalog sync collected from that source.
	OriginHuggingFace       = "huggingface"
	OriginOllama            = "ollama"
	OriginDockerModelRunner = "docker-model-runner"
	OriginNGC               = "ngc"
	OriginKaggle            = "kaggle"
)

// Origins lists every origin a catalog entry may carry.
var Origins = []string{OriginSeed, OriginManual, OriginHuggingFace, OriginOllama,
	OriginDockerModelRunner, OriginNGC, OriginKaggle}

// ValidOrigin reports whether origin is one of Origins. Empty is valid and is
// read as OriginManual.
func ValidOrigin(origin string) bool {
	return origin == "" || slices.Contains(Origins, origin)
}

// catalogIDPattern is what a model, artifact or runtime identifier may look
// like. Identifiers are placed in URL paths and, later, in directory names on a
// shared volume, so they are kept to characters that need no escaping in
// either.
var catalogIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// artifactRefPattern is what an artifact reference may look like. The reference
// is handed to an inference runtime on its command line inside a generated
// compose file, so whitespace, quotes and shell or compose metacharacters are
// refused rather than escaped.
var artifactRefPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+-]*$`)

// Model is one AI model in the model catalog.
//
// The format of a model is not a property of the model: the same weights are
// published as BF16 safetensors on Hugging Face and as a GGUF quantization on
// Ollama. Everything that differs by publication lives on an Artifact.
type Model struct {
	// ID is the catalog identifier: lower case letters, digits, ".", "_" and
	// "-", at most 63 characters.
	ID string `json:"id" validate:"required" example:"qwen3-vl-8b"`
	// Name is the display name, usually the name the publisher uses.
	Name string `json:"name" validate:"required" example:"Qwen3-VL-8B-Instruct"`
	// Family is the model family the model belongs to.
	Family string `json:"family,omitempty" example:"qwen3-vl"`
	// Publisher is who released the model.
	Publisher string `json:"publisher,omitempty" example:"Qwen"`
	// Parameters is the parameter count as the publisher writes it.
	Parameters string `json:"parameters,omitempty" example:"8B"`
	// Tasks are what the model is used for, such as chat, vision-chat or
	// embedding. Filter with ?task=.
	Tasks []string `json:"tasks,omitempty" example:"vision-chat"`
	// InputModalities are the kinds of input the model accepts, such as text or
	// image. Filter with ?modality=.
	InputModalities []string `json:"inputModalities,omitempty" example:"text,image"`
	// ContextLength is the context window in tokens. Zero or absent means
	// unknown, not unlimited.
	ContextLength int `json:"contextLength,omitempty" example:"262144"`
	// License is the license identifier of the weights. Empty means unknown.
	License string `json:"license,omitempty" example:"apache-2.0"`
	// Artifacts are the published forms of the model that a runtime can load.
	// At least one is required.
	Artifacts []Artifact `json:"artifacts" validate:"required"`
	// Origin is who put the model in the catalog: seed for a built-in model,
	// manual for a model registered or edited through POST /models, or the
	// source the catalog sync collected it from (huggingface, ollama,
	// docker-model-runner, ngc, kaggle). Absent is read as manual. POST /models
	// always records manual. Filter with ?origin=.
	Origin string `json:"origin,omitempty" enums:"seed,manual,huggingface,ollama,docker-model-runner,ngc,kaggle" example:"huggingface"`
	// SyncedAt is the last time the catalog sync saw the model at its source.
	// Absent for a model the sync does not keep.
	SyncedAt *time.Time `json:"syncedAt,omitempty" example:"2026-11-04T03:00:00Z"`
	// Stale is true when the source of a collected model no longer lists it.
	// The entry is kept, and turns false again when the source lists it again.
	Stale bool `json:"stale,omitempty" example:"false"`
}

// Artifact is one published form of a model: where it comes from, in which
// file format and precision, and which inference runtimes load it.
type Artifact struct {
	// ID identifies the artifact within its model, with the same character
	// rules as a model ID.
	ID string `json:"id" validate:"required" example:"hf-bf16"`
	// Source is where the artifact is downloaded from, such as huggingface,
	// kaggle, ollama or docker-model-runner. Filter with ?source=.
	Source string `json:"source" validate:"required" example:"huggingface"`
	// Ref is the name of the artifact at its source: a Hugging Face repository
	// id, an Ollama model tag, or an image reference.
	Ref string `json:"ref" validate:"required" example:"Qwen/Qwen3-VL-8B-Instruct"`
	// Format is the file format the runtime loads: hf for Hugging Face
	// safetensors, gguf for GGUF. Filter with ?format=.
	Format string `json:"format" validate:"required" example:"hf"`
	// Precision is the precision or quantization exactly as the source states
	// it, such as BF16, FP8 or Q4_K_M. It is not normalized. Empty means
	// unknown.
	Precision string `json:"precision,omitempty" example:"BF16"`
	// SizeGiB is the download size in GiB (2^30 bytes). Zero or absent means
	// unknown.
	SizeGiB float64 `json:"sizeGiB,omitempty" example:"16.3"`
	// Runtimes are the inference runtimes known to load this artifact. Each
	// runtime must be listed explicitly: the same file format may load in one
	// runtime version and not in another. Filter with ?runtimeId=. An empty
	// list means no runtime is known to load the artifact: it is listed but
	// cannot be deployed.
	Runtimes []RuntimeCompat `json:"runtimes"`
}

// RuntimeCompat names one inference runtime an artifact runs on.
type RuntimeCompat struct {
	// RuntimeID is the runtime profile ID, as listed by GET /runtimes.
	RuntimeID string `json:"runtimeId" validate:"required" example:"vllm"`
	// MinVersion is the oldest runtime version known to load the artifact.
	// Empty means not recorded.
	MinVersion string `json:"minVersion,omitempty" example:"0.11.0"`
	// Args are arguments the runtime needs for this artifact, such as a
	// context length that fits the GPU memory. They are appended after the
	// arguments the service sets and before the inference.args of a deploy
	// request; vLLM takes the last value when a flag repeats, so the request
	// can override them. Only vllm takes them.
	Args []string `json:"args,omitempty" example:"--max-model-len,32768"`
	// VerifiedAt is when an inference deployment of this artifact on this
	// runtime last succeeded on a node. A dry run does not count. Absent means
	// never verified. Filter with ?verified=.
	VerifiedAt *time.Time `json:"verifiedAt,omitempty" example:"2026-11-04T03:00:00Z"`
}

// ModelListResp is the model catalog listing body.
type ModelListResp struct {
	Models []Model `json:"models"`
}

// Validate reports why a Model cannot be registered.
func (m *Model) Validate() error {
	switch {
	case m.ID == "":
		return errRequired("id")
	case !catalogIDPattern.MatchString(m.ID):
		return errInvalid("id", "must match [a-z0-9][a-z0-9._-]* and be at most 63 characters")
	case m.Name == "":
		return errRequired("name")
	case m.ContextLength < 0:
		return errInvalid("contextLength", "must not be negative")
	case len(m.Artifacts) == 0:
		return errInvalid("artifacts", "must hold at least one artifact")
	case !ValidOrigin(m.Origin):
		return errInvalid("origin", "must be one of "+strings.Join(Origins, ", "))
	}
	for i, task := range m.Tasks {
		if task == "" {
			return errRequired(fmt.Sprintf("tasks[%d]", i))
		}
	}
	for i, modality := range m.InputModalities {
		if modality == "" {
			return errRequired(fmt.Sprintf("inputModalities[%d]", i))
		}
	}

	seen := make(map[string]bool, len(m.Artifacts))
	for i := range m.Artifacts {
		field := fmt.Sprintf("artifacts[%d]", i)
		if err := m.Artifacts[i].validate(field); err != nil {
			return err
		}
		if seen[m.Artifacts[i].ID] {
			return errInvalid(field+".id", fmt.Sprintf("%q is used by another artifact of this model", m.Artifacts[i].ID))
		}
		seen[m.Artifacts[i].ID] = true
	}
	return nil
}

func (a *Artifact) validate(field string) error {
	switch {
	case a.ID == "":
		return errRequired(field + ".id")
	case !catalogIDPattern.MatchString(a.ID):
		return errInvalid(field+".id", "must match [a-z0-9][a-z0-9._-]* and be at most 63 characters")
	case a.Source == "":
		return errRequired(field + ".source")
	case a.Ref == "":
		return errRequired(field + ".ref")
	case !artifactRefPattern.MatchString(a.Ref):
		return errInvalid(field+".ref", "must match [A-Za-z0-9][A-Za-z0-9._:/@+-]*")
	case a.Format == "":
		return errRequired(field + ".format")
	case a.SizeGiB < 0:
		return errInvalid(field+".sizeGiB", "must not be negative")
	}
	for i, runtime := range a.Runtimes {
		if runtime.RuntimeID == "" {
			return errRequired(fmt.Sprintf("%s.runtimes[%d].runtimeId", field, i))
		}
		argsField := fmt.Sprintf("%s.runtimes[%d].args", field, i)
		if len(runtime.Args) > 0 && runtime.RuntimeID == RuntimeOllama {
			return errInvalid(argsField, "args are not supported by runtime "+RuntimeOllama)
		}
		if err := ValidateRuntimeArgs(argsField, runtime.Args); err != nil {
			return err
		}
	}
	return nil
}

// Artifact finds an artifact of this model by ID.
func (m *Model) Artifact(artifactID string) (*Artifact, bool) {
	for i := range m.Artifacts {
		if m.Artifacts[i].ID == artifactID {
			return &m.Artifacts[i], true
		}
	}
	return nil, false
}

// HasTask reports whether the model is used for task.
func (m *Model) HasTask(task string) bool {
	return slices.Contains(m.Tasks, task)
}

// SupportsRuntime reports whether the artifact names runtimeID as compatible.
func (a *Artifact) SupportsRuntime(runtimeID string) bool {
	for _, compat := range a.Runtimes {
		if compat.RuntimeID == runtimeID {
			return true
		}
	}
	return false
}

// Verified reports whether some artifact of the model has some runtime with
// VerifiedAt set.
func (m *Model) Verified() bool {
	for i := range m.Artifacts {
		for _, compat := range m.Artifacts[i].Runtimes {
			if compat.VerifiedAt != nil {
				return true
			}
		}
	}
	return false
}

// RuntimeArgs returns the catalog arguments the artifact lists for runtimeID.
func (a *Artifact) RuntimeArgs(runtimeID string) []string {
	for _, compat := range a.Runtimes {
		if compat.RuntimeID == runtimeID {
			return compat.Args
		}
	}
	return nil
}
