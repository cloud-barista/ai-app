package rest

import (
	"errors"
	"net/http"

	"github.com/ish-hcc/ai-agent-proto/internal/agent"
	"github.com/ish-hcc/ai-agent-proto/internal/archive"
	"github.com/ish-hcc/ai-agent-proto/internal/catalog"
	"github.com/ish-hcc/ai-agent-proto/internal/deploy"
	"github.com/ish-hcc/ai-agent-proto/internal/llm"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

// Handler serves every endpoint of this service.
type Handler struct {
	catalog *catalog.Store
	deploy  *deploy.Service
	agent   *agent.Service
	archive *archive.Store
	tools   *tools.Registry
}

// NewHandler builds a Handler around the service layer.
func NewHandler(
	apps *catalog.Store,
	deployer *deploy.Service,
	agentService *agent.Service,
	archiveStore *archive.Store,
	registry *tools.Registry,
) *Handler {
	return &Handler{
		catalog: apps,
		deploy:  deployer,
		agent:   agentService,
		archive: archiveStore,
		tools:   registry,
	}
}

// statusCodeFor maps a failure to the status this API returns.
//
// Upstream detail never leaves through the body, so the status is the only thing
// a caller can act on and it has to be right.
func statusCodeFor(err error) int {
	switch {
	case errors.Is(err, model.ErrValidation):
		return http.StatusBadRequest
	case errors.Is(err, catalog.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, deploy.ErrAlreadyExists):
		return http.StatusConflict
	case errors.Is(err, deploy.ErrNotViable):
		return http.StatusBadRequest
	case errors.Is(err, llm.ErrDisabled):
		return http.StatusServiceUnavailable
	}

	switch tumblebug.StatusCode(err) {
	case http.StatusNotFound:
		return http.StatusNotFound
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return http.StatusBadRequest
	case http.StatusConflict:
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

// messageFor picks the caller-facing text for a failure.
//
// A validation error already carries text written for the caller; anything else
// falls back to the handler's own wording so that library and driver strings
// never reach the response body.
func messageFor(err error, fallback string) string {
	if message := model.ValidationMessage(err); message != "" {
		return message
	}
	return fallback
}
