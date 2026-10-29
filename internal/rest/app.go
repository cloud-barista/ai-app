package rest

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// RestGetAllApps lists registered AI applications.
func (h *Handler) RestGetAllApps(c echo.Context) error {
	ctx := c.Request().Context()
	return c.JSON(http.StatusOK, model.AppSpecListResp{Apps: h.catalog.List(ctx)})
}

// RestPostApp registers an AI application.
func (h *Handler) RestPostApp(c echo.Context) error {
	ctx := c.Request().Context()

	var spec model.AppSpec
	if err := c.Bind(&spec); err != nil {
		return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: malformedBody})
	}

	registered, err := h.catalog.Register(ctx, spec)
	if err != nil {
		log.Error().Err(err).Str("appId", spec.ID).Msg("Failed to register AI application")
		return c.JSON(statusCodeFor(err), model.SimpleMsg{Message: messageFor(err, "AI application registration failed")})
	}
	return c.JSON(http.StatusOK, registered)
}

// RestGetAppMatches finds AI applications matching a description.
func (h *Handler) RestGetAppMatches(c echo.Context) error {
	ctx := c.Request().Context()

	query := c.QueryParam("q")
	if query == "" {
		return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: "Query required"})
	}
	limit, err := intQueryParam(c, "limit")
	if err != nil {
		return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: "Limit must be a whole number"})
	}
	return c.JSON(http.StatusOK, h.catalog.Resolve(ctx, query, limit))
}

// RestGetApp gets an AI application.
func (h *Handler) RestGetApp(c echo.Context) error {
	ctx := c.Request().Context()
	appID := c.Param("appId")

	spec, err := h.catalog.Get(ctx, appID)
	if err != nil {
		log.Warn().Err(err).Str("appId", appID).Msg("Failed to get AI application")
		return c.JSON(statusCodeFor(err), model.SimpleMsg{Message: "AI application not found"})
	}
	return c.JSON(http.StatusOK, spec)
}

// RestDeleteApp deletes an AI application.
func (h *Handler) RestDeleteApp(c echo.Context) error {
	ctx := c.Request().Context()
	appID := c.Param("appId")

	if err := h.catalog.Delete(ctx, appID); err != nil {
		log.Warn().Err(err).Str("appId", appID).Msg("Failed to delete AI application")
		return c.JSON(statusCodeFor(err), model.SimpleMsg{Message: "AI application not found"})
	}
	return c.JSON(http.StatusOK, model.SimpleMsg{Message: "AI application deleted"})
}

// RestGetAppSpecs recommends accelerator specs for an AI application.
func (h *Handler) RestGetAppSpecs(c echo.Context) error {
	ctx := c.Request().Context()
	appID := c.Param("appId")

	spec, err := h.catalog.Get(ctx, appID)
	if err != nil {
		log.Warn().Err(err).Str("appId", appID).Msg("Failed to get AI application")
		return c.JSON(statusCodeFor(err), model.SimpleMsg{Message: "AI application not found"})
	}

	limit, err := intQueryParam(c, "limit")
	if err != nil {
		return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: "Limit must be a whole number"})
	}

	candidates, err := h.deploy.RecommendSpecs(ctx, spec, limit)
	if err != nil {
		log.Error().Err(err).Str("appId", appID).Msg("Failed to recommend specs")
		return c.JSON(statusCodeFor(err), model.SimpleMsg{Message: messageFor(err, "Spec recommendation failed")})
	}
	return c.JSON(http.StatusOK, candidates)
}
