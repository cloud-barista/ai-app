package rest

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// RestGetAllApps godoc
// @ID GetAllApps
// @Summary List registered AI applications
// @Description List every AI application in the catalog with its runtime, model and accelerator requirement
// @Tags [App] AI Application Catalog
// @Produce json
// @Success 200 {object} model.AppSpecListResp "AI application list"
// @Security BearerAuth
// @Router /apps [get]
func (h *Handler) RestGetAllApps(c echo.Context) error {
	ctx := c.Request().Context()
	return c.JSON(http.StatusOK, model.AppSpecListResp{Apps: h.catalog.List(ctx)})
}

// RestPostApp godoc
// @ID PostApp
// @Summary Register an AI application
// @Description Register or replace an AI application metadata record. Replacing an existing id publishes a new version
// @Tags [App] AI Application Catalog
// @Accept json
// @Produce json
// @Param appSpec body model.AppSpec true "AI application metadata"
// @Success 200 {object} model.AppSpec "AI application registered"
// @Failure 400 {object} model.SimpleMsg "Malformed or incomplete metadata"
// @Security BearerAuth
// @Router /apps [post]
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

// RestGetAppMatches godoc
// @ID GetAppMatches
// @Summary Find AI applications matching a description
// @Description Rank registered AI applications against an operator's own words and report the signals that matched. An empty list means the catalog holds nothing that fits
// @Tags [App] AI Application Catalog
// @Produce json
// @Param q query string true "The operator's instruction, in their own words"
// @Param limit query int false "How many candidates to return" default(5)
// @Success 200 {array} catalog.Match "Ranked candidates"
// @Failure 400 {object} model.SimpleMsg "Query required"
// @Security BearerAuth
// @Router /apps/search [get]
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

// RestGetApp godoc
// @ID GetApp
// @Summary Get an AI application
// @Description Read one registered AI application, including its install commands and serving port
// @Tags [App] AI Application Catalog
// @Produce json
// @Param appId path string true "AI application ID" default(vllm-llama31-8b)
// @Success 200 {object} model.AppSpec "AI application metadata"
// @Failure 404 {object} model.SimpleMsg "AI application not found"
// @Security BearerAuth
// @Router /apps/{appId} [get]
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

// RestDeleteApp godoc
// @ID DeleteApp
// @Summary Delete an AI application
// @Description Remove an AI application from the catalog. Deployments already running are untouched
// @Tags [App] AI Application Catalog
// @Produce json
// @Param appId path string true "AI application ID"
// @Success 200 {object} model.SimpleMsg "AI application deleted"
// @Failure 404 {object} model.SimpleMsg "AI application not found"
// @Security BearerAuth
// @Router /apps/{appId} [delete]
func (h *Handler) RestDeleteApp(c echo.Context) error {
	ctx := c.Request().Context()
	appID := c.Param("appId")

	if err := h.catalog.Delete(ctx, appID); err != nil {
		log.Warn().Err(err).Str("appId", appID).Msg("Failed to delete AI application")
		return c.JSON(statusCodeFor(err), model.SimpleMsg{Message: "AI application not found"})
	}
	return c.JSON(http.StatusOK, model.SimpleMsg{Message: "AI application deleted"})
}

// RestGetAppSpecs godoc
// @ID GetAppSpecs
// @Summary Recommend accelerator specs for an AI application
// @Description Ask the partner recommender API (AIAPP_RECOMMENDER_URL) for node specs that satisfy the accelerator requirement. Without a recommender API this answers 422.
// @Tags [App] AI Application Catalog
// @Produce json
// @Param appId path string true "AI application ID" default(vllm-llama31-8b)
// @Param limit query int false "How many candidates to return" default(5)
// @Success 200 {array} model.SpecCandidate "Matching node specs"
// @Failure 404 {object} model.SimpleMsg "AI application not found"
// @Failure 422 {object} model.SimpleMsg "No recommender configured"
// @Failure 500 {object} model.SimpleMsg "Spec recommendation failed"
// @Failure 502 {object} model.SimpleMsg "Remote placement service failed"
// @Security BearerAuth
// @Router /apps/{appId}/specs [get]
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
