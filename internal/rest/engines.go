package rest

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/modelcat"
)

// RestGetAllEngineProfiles godoc
// @ID GetAllEngineProfiles
// @Summary List inference engine profiles
// @Description List the built-in inference engine profiles a model service can run with: the pinned container image, the artifact formats each loads, and the port, health path and OpenAI compatible API path it serves. Profiles are read only. An artifact runs on an engine when the artifact lists the engine id and the engine lists the artifact format
// @Tags [Model] Inference Engine Profiles
// @Produce json
// @Success 200 {object} model.EngineProfileListResp "Engine profile list, ordered by id"
// @Security BearerAuth
// @Router /engines [get]
func (h *Handler) RestGetAllEngineProfiles(c echo.Context) error {
	return c.JSON(http.StatusOK, model.EngineProfileListResp{Engines: modelcat.Engines()})
}

// RestGetEngineProfile godoc
// @ID GetEngineProfile
// @Summary Get an inference engine profile
// @Description Read one built-in inference engine profile
// @Tags [Model] Inference Engine Profiles
// @Produce json
// @Param engineId path string true "Engine profile ID" default(vllm)
// @Success 200 {object} model.EngineProfile "Engine profile"
// @Failure 404 {object} model.SimpleMsg "Engine profile not found"
// @Security BearerAuth
// @Router /engines/{engineId} [get]
func (h *Handler) RestGetEngineProfile(c echo.Context) error {
	engine, err := modelcat.Engine(c.Param("engineId"))
	if err != nil {
		return c.JSON(statusCodeFor(err), model.SimpleMsg{Message: "Engine profile not found"})
	}
	return c.JSON(http.StatusOK, engine)
}
