package rest

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/compose"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/modelcat"
)

// RestGetAllModels godoc
// @ID GetAllModels
// @Summary List AI models
// @Description List the AI models in the model catalog with every artifact each is published as. Each filter is optional and compared without regard to case; all given filters must match. task, modality and origin match the model; verified=true keeps the models for which some artifact has some runtime with verifiedAt set, verified=false the others. format, runtimeId and source match an artifact, and a model is listed only when one of its artifacts matches all three of them at once. Values are returned as the source states them: an absent contextLength, sizeGiB or precision means unknown, not zero. Sizing nodes from these values is left to the caller.
// @Tags [Model] AI Model Catalog
// @Produce json
// @Param task query string false "Model task, such as chat, vision-chat or embedding"
// @Param modality query string false "Input modality the model accepts, such as text or image"
// @Param format query string false "Artifact file format: hf (Hugging Face safetensors) or gguf"
// @Param runtimeId query string false "Runtime profile ID an artifact runs on, as listed by GET /runtimes"
// @Param source query string false "Artifact source, such as huggingface or ollama"
// @Param origin query string false "Who put the model in the catalog. An absent origin is read as manual" Enums(seed,manual,huggingface,ollama,docker-model-runner,ngc,kaggle)
// @Param verified query bool false "true: an inference deployment of some artifact succeeded on some runtime (runtimes[].verifiedAt set); false: none did"
// @Success 200 {object} model.ModelListResp "AI model list, ordered by id"
// @Failure 400 {object} model.SimpleMsg "origin is not a known origin, or verified is not a boolean"
// @Security BearerAuth
// @Router /models [get]
func (h *Handler) RestGetAllModels(c echo.Context) error {
	ctx := c.Request().Context()
	filter := modelcat.Filter{
		Task:      c.QueryParam("task"),
		Modality:  c.QueryParam("modality"),
		Format:    c.QueryParam("format"),
		RuntimeID: c.QueryParam("runtimeId"),
		Source:    c.QueryParam("source"),
		Origin:    c.QueryParam("origin"),
	}
	if !model.ValidOrigin(strings.ToLower(filter.Origin)) {
		return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: "origin must be one of " + strings.Join(model.Origins, ", ")})
	}
	if raw := c.QueryParam("verified"); raw != "" {
		verified, err := strconv.ParseBool(raw)
		if err != nil {
			return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: "verified must be true or false"})
		}
		filter.Verified = &verified
	}
	return c.JSON(http.StatusOK, model.ModelListResp{Models: h.models.List(ctx, filter)})
}

// RestPostModel godoc
// @ID PostModel
// @Summary Register an AI model
// @Description Register an AI model, or replace the model of the same id with this record. A model needs an id, a name and at least one artifact; each artifact needs an id unique within the model, a source, a ref and a format. runtimes[] lists the compatible runtimes; an artifact with none is listed but refused with 422 when deployed. The model is recorded with origin manual, stale false and no syncedAt whatever the body says; runtimes[].verifiedAt is kept as given: the catalog sync leaves a model registered or edited here alone. Runtimes are not checked against the runtime profiles here; a pairing that does not fit is refused when a compose file is rendered for it.
// @Tags [Model] AI Model Catalog
// @Accept json
// @Produce json
// @Param model body model.Model true "AI model metadata"
// @Success 200 {object} model.Model "AI model registered"
// @Failure 400 {object} model.SimpleMsg "Malformed or incomplete metadata"
// @Failure 500 {object} model.SimpleMsg "The catalog file could not be written"
// @Security BearerAuth
// @Router /models [post]
func (h *Handler) RestPostModel(c echo.Context) error {
	ctx := c.Request().Context()

	var m model.Model
	if err := c.Bind(&m); err != nil {
		return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: malformedBody})
	}
	m.Origin = model.OriginManual
	m.Stale = false
	m.SyncedAt = nil

	registered, err := h.models.Register(ctx, m)
	if err != nil {
		log.Error().Err(err).Str("modelId", m.ID).Msg("Failed to register AI model")
		return c.JSON(statusCodeFor(err), model.SimpleMsg{Message: messageFor(err, "AI model registration failed")})
	}
	return c.JSON(http.StatusOK, registered)
}

// RestGetModel godoc
// @ID GetModel
// @Summary Get an AI model
// @Description Read one AI model with every artifact it is published as
// @Tags [Model] AI Model Catalog
// @Produce json
// @Param modelId path string true "AI model ID" default(qwen3-vl-8b)
// @Success 200 {object} model.Model "AI model metadata"
// @Failure 404 {object} model.SimpleMsg "AI model not found"
// @Security BearerAuth
// @Router /models/{modelId} [get]
func (h *Handler) RestGetModel(c echo.Context) error {
	ctx := c.Request().Context()
	modelID := c.Param("modelId")

	m, err := h.models.Get(ctx, modelID)
	if err != nil {
		log.Warn().Err(err).Str("modelId", modelID).Msg("Failed to get AI model")
		return c.JSON(statusCodeFor(err), model.SimpleMsg{Message: "AI model not found"})
	}
	return c.JSON(http.StatusOK, m)
}

// RestDeleteModel godoc
// @ID DeleteModel
// @Summary Delete an AI model
// @Description Remove an AI model from the catalog. Inference services already running are untouched
// @Tags [Model] AI Model Catalog
// @Produce json
// @Param modelId path string true "AI model ID"
// @Success 200 {object} model.SimpleMsg "AI model deleted"
// @Failure 404 {object} model.SimpleMsg "AI model not found"
// @Failure 500 {object} model.SimpleMsg "The catalog file could not be written"
// @Security BearerAuth
// @Router /models/{modelId} [delete]
func (h *Handler) RestDeleteModel(c echo.Context) error {
	ctx := c.Request().Context()
	modelID := c.Param("modelId")

	if err := h.models.Delete(ctx, modelID); err != nil {
		log.Warn().Err(err).Str("modelId", modelID).Msg("Failed to delete AI model")
		return c.JSON(statusCodeFor(err), model.SimpleMsg{Message: messageFor(err, "AI model deletion failed")})
	}
	return c.JSON(http.StatusOK, model.SimpleMsg{Message: "AI model deleted"})
}

// RestGetModelCompose godoc
// @ID GetModelCompose
// @Summary Preview the compose file of an inference service
// @Description Render the docker-compose.yml that runs one artifact of a model with one runtime profile, without deploying anything. The pairing is refused with 422 unless the artifact lists the runtime and the runtime loads the artifact format. The file starts with one `docker compose up -d`: for vllm it runs the OpenAI compatible server with --model set to the artifact ref (and --runner pooling for an embedding model); for ollama it runs the server and a one-shot container that pulls the model once the server is healthy. HF_TOKEN is left to the environment or a .env file beside the compose file. Each arg is appended to the vllm command after those arguments, as inference.args of POST /deployments is; ollama refuses them with 400.
// @Tags [Model] AI Model Catalog
// @Produce text/yaml,json
// @Param modelId path string true "AI model ID" default(qwen3-vl-8b)
// @Param artifactId path string true "Artifact ID within the model" default(hf-bf16)
// @Param runtimeId query string false "Runtime profile ID. Defaults to the first runtime the artifact lists; an artifact that lists none is refused with 422"
// @Param gpu query bool false "Reserve every NVIDIA GPU of the node for the runtime" default(false)
// @Param arg query []string false "Extra runtime argument, appended after the catalog args of the artifact (runtimes[].args) and repeated for each, such as arg=--max-model-len&arg=32768" collectionFormat(multi)
// @Success 200 {string} string "docker-compose.yml, as text/yaml"
// @Failure 400 {object} model.SimpleMsg "gpu is not a boolean, or an arg is refused"
// @Failure 404 {object} model.SimpleMsg "AI model, artifact or runtime profile not found"
// @Failure 422 {object} model.SimpleMsg "The artifact cannot be run with the runtime"
// @Security BearerAuth
// @Router /models/{modelId}/artifacts/{artifactId}/compose [get]
func (h *Handler) RestGetModelCompose(c echo.Context) error {
	ctx := c.Request().Context()
	modelID := c.Param("modelId")
	artifactID := c.Param("artifactId")

	gpu := false
	if raw := c.QueryParam("gpu"); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: "gpu must be true or false"})
		}
		gpu = parsed
	}

	m, artifact, err := h.models.Artifact(ctx, modelID, artifactID)
	if err != nil {
		log.Warn().Err(err).Str("modelId", modelID).Str("artifactId", artifactID).Msg("Failed to get model artifact")
		return c.JSON(statusCodeFor(err), model.SimpleMsg{Message: messageFor(err, "AI model or artifact not found")})
	}

	runtimeID := c.QueryParam("runtimeId")
	if runtimeID == "" {
		if len(artifact.Runtimes) == 0 {
			err := fmt.Errorf("%w: artifact %s/%s lists no runtime", compose.ErrIncompatible, m.ID, artifact.ID)
			return c.JSON(statusCodeFor(err), model.SimpleMsg{Message: messageFor(err, "Compose rendering failed")})
		}
		runtimeID = artifact.Runtimes[0].RuntimeID
	}
	runtime, err := modelcat.Runtime(runtimeID)
	if err != nil {
		return c.JSON(statusCodeFor(err), model.SimpleMsg{Message: messageFor(err, "Runtime profile not found")})
	}

	rendered, err := compose.Render(m, artifact, runtime, compose.Options{GPU: gpu, Args: c.QueryParams()["arg"]})
	if err != nil {
		log.Warn().Err(err).Str("modelId", modelID).Str("artifactId", artifactID).Str("runtimeId", runtimeID).
			Msg("Failed to render compose file")
		return c.JSON(statusCodeFor(err), model.SimpleMsg{Message: messageFor(err, "Compose rendering failed")})
	}
	return c.Blob(http.StatusOK, "text/yaml; charset=utf-8", []byte(rendered))
}
