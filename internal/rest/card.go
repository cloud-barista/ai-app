package rest

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// RestGetAgentCard godoc
// @ID GetAgentCard
// @Summary Describe the automation agent
// @Description Report the agent card an agent registry reads: identity, the framework it serves, the MCP server it takes tools from and whether it is connected, the tools and their grades, what it needs of a planning model, and how registration went. Served by the automation agent and forwarded by the AI-App service
// @Tags [Agent] AI Application Automation Agent
// @Produce json
// @Success 200 {object} model.AgentCard "Agent card"
// @Failure 502 {object} model.SimpleMsg "Automation agent is not reachable from the AI-App service"
// @Security BearerAuth
// @Router /agent/card [get]
func (h *Handler) RestGetAgentCard(c echo.Context) error {
	return c.JSON(http.StatusOK, h.card.Build())
}
