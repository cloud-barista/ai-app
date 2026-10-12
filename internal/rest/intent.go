package rest

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/mcpclient"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// RestPostIntent godoc
// @ID PostIntent
// @Summary Run a natural language deployment intent
// @Description Hand an operator instruction to the deployment agent. The agent resolves the application, recommends an accelerator spec, reviews the request and, when the dry-run guard is off, provisions. Every step is archived
// @Tags [Agent] AI Application Automation Agent
// @Accept json
// @Produce json
// @Param nsId path string true "Namespace ID" default(default)
// @Param intentReq body model.IntentReq true "Operator instruction"
// @Success 200 {object} model.IntentResp "Agent run finished"
// @Failure 400 {object} model.SimpleMsg "Malformed or empty instruction"
// @Failure 503 {object} model.SimpleMsg "Planning model is not configured or the AI-App tool server is not reachable"
// @Failure 502 {object} model.SimpleMsg "Automation agent is not reachable from the AI-App service"
// @Failure 500 {object} model.SimpleMsg "Agent run failed"
// @Security BearerAuth
// @Router /ns/{nsId}/intents [post]
func (h *Handler) RestPostIntent(c echo.Context) error {
	ctx := c.Request().Context()
	nsID := c.Param("nsId")

	var req model.IntentReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: malformedBody})
	}
	if err := req.Validate(); err != nil {
		return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: model.ValidationMessage(err)})
	}

	result, err := h.agent.Run(ctx, nsID, req.Intent)
	if err != nil {
		// The instruction itself is operational content, so only its length is logged.
		log.Error().Err(err).Str("nsId", nsID).Int("intentLength", len(req.Intent)).
			Msg("Failed to run agent intent")
		return c.JSON(statusCodeFor(err), model.SimpleMsg{Message: agentFailureMessage(err)})
	}
	return c.JSON(http.StatusOK, result)
}

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

// agentFailureMessage keeps model and driver wording out of the response body.
func agentFailureMessage(err error) string {
	if errors.Is(err, mcpclient.ErrUnavailable) {
		return "AI-App tool server is not reachable"
	}
	if statusCodeFor(err) == http.StatusServiceUnavailable {
		return "Planning model is not configured"
	}
	return "Agent run failed"
}
