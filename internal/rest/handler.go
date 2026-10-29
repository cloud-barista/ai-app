package rest

import (
	"errors"
	"net/http"

	"github.com/ish-hcc/ai-agent-proto/internal/appstore"
	"github.com/ish-hcc/ai-agent-proto/internal/archive"
	"github.com/ish-hcc/ai-agent-proto/internal/compose"
	"github.com/ish-hcc/ai-agent-proto/internal/engine"
	"github.com/ish-hcc/ai-agent-proto/internal/mcpclient"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/modelcat"
	"github.com/ish-hcc/ai-agent-proto/internal/nodedeploy"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

// malformedBody is the caller-facing text for an unreadable request body.
const malformedBody = "Malformed request body: check JSON syntax"

// Handler serves the endpoints of both services. Each server fills only the
// fields its routes use.
type Handler struct {
	archive *archive.Store
	tools   *tools.Registry
	models  *modelcat.Store
	apps    *appstore.Store
	// nodeDeploy deploys onto existing nodes through Semaphore.
	nodeDeploy *nodedeploy.Service

	engine     *engine.Engine
	engineCard *engine.CardBuilder
}

// statusCodeFor maps a failure to the status this API returns.
//
// Upstream detail never leaves through the body, so the status is the only thing
// a caller can act on and it has to be right.
func statusCodeFor(err error) int {
	switch {
	case errors.Is(err, model.ErrValidation):
		return http.StatusBadRequest
	case errors.Is(err, modelcat.ErrNotFound), errors.Is(err, modelcat.ErrArtifactNotFound),
		errors.Is(err, modelcat.ErrEngineNotFound):
		return http.StatusNotFound
	case errors.Is(err, appstore.ErrNotFound), errors.Is(err, appstore.ErrVersionNotFound):
		return http.StatusNotFound
	case errors.Is(err, appstore.ErrTooLarge):
		return http.StatusRequestEntityTooLarge
	case errors.Is(err, compose.ErrIncompatible), errors.Is(err, model.ErrUnsupportedTarget):
		return http.StatusUnprocessableEntity
	case errors.Is(err, nodedeploy.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, compose.ErrInvalidOption):
		return http.StatusBadRequest
	case errors.Is(err, mcpclient.ErrUnavailable):
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
// A validation error, a missing model service and a missing recommender already
// carry text written for the caller; anything else falls back to the handler's
// own wording so that library and driver strings never reach the response body.
func messageFor(err error, fallback string) string {
	if message := model.ValidationMessage(err); message != "" {
		return message
	}
	// The model catalog, compose and application store errors name only
	// identifiers the caller sent, the reason a pairing does not fit and the
	// upload bound.
	for _, own := range []error{modelcat.ErrNotFound, modelcat.ErrArtifactNotFound, modelcat.ErrEngineNotFound,
		compose.ErrIncompatible, compose.ErrInvalidOption,
		appstore.ErrNotFound, appstore.ErrVersionNotFound, appstore.ErrTooLarge,
		model.ErrUnsupportedTarget, nodedeploy.ErrNotFound} {
		if errors.Is(err, own) {
			return err.Error()
		}
	}
	return fallback
}
