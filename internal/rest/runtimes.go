package rest

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/modelcat"
)

// RestGetAllRuntimeProfiles godoc
// @ID GetAllRuntimeProfiles
// @Summary List inference runtime profiles
// @Description List the built-in inference runtime profiles an inference deployment can run with: the pinned container image, the artifact formats each loads, and the port, health path and OpenAI compatible API path it serves. Profiles are read only. An artifact runs on a runtime when the artifact lists the runtime id in runtimes[].runtimeId and the runtime lists the artifact format
// @Tags [Model] Inference Runtime Profiles
// @Produce json
// @Success 200 {object} model.RuntimeProfileListResp "Runtime profile list, ordered by id"
// @Security BearerAuth
// @Router /runtimes [get]
func (h *Handler) RestGetAllRuntimeProfiles(c echo.Context) error {
	return c.JSON(http.StatusOK, model.RuntimeProfileListResp{Runtimes: modelcat.Runtimes()})
}

// RestGetRuntimeProfile godoc
// @ID GetRuntimeProfile
// @Summary Get an inference runtime profile
// @Description Read one built-in inference runtime profile
// @Tags [Model] Inference Runtime Profiles
// @Produce json
// @Param runtimeId path string true "Runtime profile ID" default(vllm)
// @Success 200 {object} model.RuntimeProfile "Runtime profile"
// @Failure 404 {object} model.SimpleMsg "Runtime profile not found"
// @Security BearerAuth
// @Router /runtimes/{runtimeId} [get]
func (h *Handler) RestGetRuntimeProfile(c echo.Context) error {
	runtime, err := modelcat.Runtime(c.Param("runtimeId"))
	if err != nil {
		return c.JSON(statusCodeFor(err), model.SimpleMsg{Message: "Runtime profile not found"})
	}
	return c.JSON(http.StatusOK, runtime)
}
