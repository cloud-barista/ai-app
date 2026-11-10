package rest

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/recommend"
)

// RestPostModelRecommendation godoc
// @ID PostModelRecommendation
// @Summary Recommend models for a requirement
// @Description Ask the configured model recommender which model artifact and runtime pairings of the catalog fit a requirement. Every request field is optional; an empty body asks for any model. The request is validated first, so a malformed request is refused with 400 whether or not a recommender is configured. No recommender is built in: until AIAPP_RECOMMENDER_MODEL names a registered implementation of internal/recommend, a valid request is answered with 501. Candidates are ordered best first, at most maxCandidates of them, and each names a model, an artifact of it and a runtime the artifact lists, ready for the inference field of POST /deployments.
// @Tags [Recommendation] Model and Application Recommendation
// @Accept json
// @Produce json
// @Param request body recommend.ModelRecommendReq true "What the model is needed for"
// @Success 200 {object} recommend.ModelRecommendResp "Recommended models, best first"
// @Failure 400 {object} model.SimpleMsg "Malformed request, or a field out of range"
// @Failure 500 {object} model.SimpleMsg "The recommender failed or returned a candidate the catalog does not hold"
// @Failure 501 {object} model.SimpleMsg "No model recommender is configured"
// @Security BearerAuth
// @Router /recommendations/models [post]
func (h *Handler) RestPostModelRecommendation(c echo.Context) error {
	ctx := c.Request().Context()

	var req recommend.ModelRecommendReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: malformedBody})
	}

	resp, err := h.recommend.RecommendModels(ctx, &req)
	if err != nil {
		log.Warn().Err(err).Str("recommender", h.recommend.ModelName()).Msg("Failed to recommend models")
		return c.JSON(statusCodeFor(err), model.SimpleMsg{Message: messageFor(err, "Model recommendation failed")})
	}
	return c.JSON(http.StatusOK, resp)
}

// RestPostAppRecommendation godoc
// @ID PostAppRecommendation
// @Summary Recommend applications for a requirement
// @Description Ask the configured application recommender which uploaded application versions fit a requirement. Every request field is optional; an empty body asks for any application. The request is validated first, so a malformed request is refused with 400 whether or not a recommender is configured; inference[].modelId must name a model of the catalog. No recommender is built in: until AIAPP_RECOMMENDER_APP names a registered implementation of internal/recommend, a valid request is answered with 501. Candidates are ordered best first, at most maxCandidates of them, and each names an uploaded application and one of its versions, ready for the app field of POST /deployments.
// @Tags [Recommendation] Model and Application Recommendation
// @Accept json
// @Produce json
// @Param request body recommend.AppRecommendReq true "What the application is needed for"
// @Success 200 {object} recommend.AppRecommendResp "Recommended applications, best first"
// @Failure 400 {object} model.SimpleMsg "Malformed request, a field out of range, or a model not in the catalog"
// @Failure 500 {object} model.SimpleMsg "The recommender failed or returned a candidate the application store does not hold"
// @Failure 501 {object} model.SimpleMsg "No application recommender is configured"
// @Security BearerAuth
// @Router /recommendations/apps [post]
func (h *Handler) RestPostAppRecommendation(c echo.Context) error {
	ctx := c.Request().Context()

	var req recommend.AppRecommendReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: malformedBody})
	}

	resp, err := h.recommend.RecommendApps(ctx, &req)
	if err != nil {
		log.Warn().Err(err).Str("recommender", h.recommend.AppName()).Msg("Failed to recommend applications")
		return c.JSON(statusCodeFor(err), model.SimpleMsg{Message: messageFor(err, "Application recommendation failed")})
	}
	return c.JSON(http.StatusOK, resp)
}
