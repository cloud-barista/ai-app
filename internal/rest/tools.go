package rest

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// RestGetTools godoc
// @ID GetTools
// @Summary List agent tools
// @Description List every tool the agent may call, with its risk grade and whether the dry-run guard applies. Exposed so another agent framework can adopt the same tool surface
// @Tags [Agent] AI Application Automation Agent
// @Produce json
// @Success 200 {object} model.ToolListResp "Tool catalog"
// @Security BearerAuth
// @Router /tools [get]
func (h *Handler) RestGetTools(c echo.Context) error {
	return c.JSON(http.StatusOK, model.ToolListResp{
		DryRun: h.tools.DryRun(),
		Tools:  h.tools.Descriptors(),
	})
}
