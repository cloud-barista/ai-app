package rest

import (
	"errors"
	"net/http"

	"github.com/ish-hcc/ai-agent-proto/internal/agent"
	"github.com/ish-hcc/ai-agent-proto/internal/agentcard"
	"github.com/ish-hcc/ai-agent-proto/internal/archive"
	"github.com/ish-hcc/ai-agent-proto/internal/catalog"
	"github.com/ish-hcc/ai-agent-proto/internal/deploy"
	"github.com/ish-hcc/ai-agent-proto/internal/llm"
	"github.com/ish-hcc/ai-agent-proto/internal/mcpclient"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

// Handler serves the endpoints of both services. Each server fills only the
// fields its routes use.
type Handler struct {
	catalog *catalog.Store
	deploy  *deploy.Service
	agent   *agent.Service
	archive *archive.Store
	tools   *tools.Registry
	card    *agentcard.Card
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
	case errors.Is(err, deploy.ErrAppMismatch):
		return http.StatusConflict
	case errors.Is(err, deploy.ErrNoAppLabel):
		return http.StatusBadRequest
	case errors.Is(err, deploy.ErrRequirementNotDeployed):
		return http.StatusConflict
	case errors.Is(err, llm.ErrDisabled):
		return http.StatusServiceUnavailable
	case errors.Is(err, mcpclient.ErrUnavailable):
		return http.StatusServiceUnavailable
	}

	// A remote placement service failing, whether it rejected the call or broke,
	// is an upstream failure from the caller's point of view.
	var remoteErr *deploy.RemoteError
	if errors.As(err, &remoteErr) {
		return http.StatusBadGateway
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
// A validation error and a missing model service already carry text written for
// the caller; anything else falls back to the handler's own wording so that
// library and driver strings never reach the response body.
func messageFor(err error, fallback string) string {
	if message := model.ValidationMessage(err); message != "" {
		return message
	}
	var requirementErr *deploy.RequirementError
	if errors.As(err, &requirementErr) {
		return requirementErr.Error()
	}
	return fallback
}
