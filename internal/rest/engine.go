package rest

import (
	"errors"
	"io"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/engine"
	"github.com/ish-hcc/ai-agent-proto/internal/mcpclient"
	"github.com/ish-hcc/ai-agent-proto/internal/mcpserver"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// workflowBodyLimit caps a workflow request body. The largest input is a
// deployment request of at most 32 targets with its env.
const workflowBodyLimit = 64 << 10

// RestPostEngineWorkflow godoc
// @ID PostEngineWorkflow
// @Summary Run an automation engine workflow
// @Description Run one fixed workflow as ordered AI-App tool calls, without a model, and archive the run.
// @Description deploy takes the node deployment request of POST /deployments (model.NodeDeploymentReq: {type, targets, modelService | app}); it reads the model (get_model) or the application (get_app), creates the deployment (create_deployment) and reads it (get_deployment) until every node ended. A model service is checked to answer by the compose-up playbook inside the deployment. A deployment that succeeded on some nodes only (partial) fails the run.
// @Description heal takes {deploymentId}; a deployment that is failed, partial, interrupted or a dry run is created once more from the request it was made with, and followed the same way. A succeeded deployment is left alone, a running one is refused.
// @Description control takes {deploymentId, action} with action suspend, resume, reboot or terminate (control_node).
// @Description status takes {deploymentId} and reports the deployment record and the last task output lines of every node (get_deployment, get_deployment_logs).
// @Description A step held by the AI-App dry-run gate is reported as blocked and the steps that need it as waiting. A run whose steps failed is still returned with 200 and status failed. Served by the automation engine and forwarded by the AI-App service
// @Tags [Engine] AI Application Automation Engine
// @Accept json
// @Produce json
// @Param workflow path string true "Workflow" Enums(deploy, heal, control, status)
// @Param ns query string false "Namespace ID" default(default)
// @Param input body object true "Workflow input: model.NodeDeploymentReq for deploy, {deploymentId} for heal and status, {deploymentId, action} for control"
// @Success 200 {object} engine.Run "Run finished; see its status"
// @Failure 400 {object} model.SimpleMsg "Malformed or incomplete input"
// @Failure 404 {object} model.SimpleMsg "Unknown workflow"
// @Failure 502 {object} model.SimpleMsg "Automation engine is not reachable from the AI-App service"
// @Failure 503 {object} model.SimpleMsg "AI-App tool server is not reachable"
// @Security BearerAuth
// @Router /engine/workflows/{workflow} [post]
func (h *Handler) RestPostEngineWorkflow(c echo.Context) error {
	ctx := c.Request().Context()
	workflow := c.Param("workflow")
	nsID := c.QueryParam("ns")
	if nsID == "" {
		nsID = mcpserver.DefaultNamespace
	}

	body, err := io.ReadAll(io.LimitReader(c.Request().Body, workflowBodyLimit))
	if err != nil {
		return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: malformedBody})
	}

	run, err := h.engine.Execute(ctx, nsID, workflow, body)
	if err != nil {
		status, message := engineFailure(err)
		log.Warn().Str("workflow", workflow).Str("nsId", nsID).Int("status", status).Str("reason", message).
			Msg("Engine workflow did not run")
		return c.JSON(status, model.SimpleMsg{Message: message})
	}
	return c.JSON(http.StatusOK, run)
}

// RestGetEngineRuns godoc
// @ID GetEngineRuns
// @Summary List automation engine runs
// @Description List archived engine runs, newest first, each with every step, its input and result, and whether the dry-run gate held it
// @Tags [Engine] AI Application Automation Engine
// @Produce json
// @Param limit query int false "How many runs to return" default(20)
// @Success 200 {object} engine.RunListResp "Archived runs"
// @Failure 400 {object} model.SimpleMsg "Malformed limit"
// @Failure 500 {object} model.SimpleMsg "Archive lookup failed"
// @Security BearerAuth
// @Router /engine/runs [get]
func (h *Handler) RestGetEngineRuns(c echo.Context) error {
	limit, err := intQueryParam(c, "limit")
	if err != nil {
		return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: "Limit must be a whole number"})
	}
	runs, err := h.engine.ListRuns(limit)
	if err != nil {
		log.Error().Err(err).Int("limit", limit).Msg("Failed to list engine runs")
		return c.JSON(http.StatusInternalServerError, model.SimpleMsg{Message: "Archive lookup failed"})
	}
	return c.JSON(http.StatusOK, engine.RunListResp{Runs: runs})
}

// RestGetEngineRun godoc
// @ID GetEngineRun
// @Summary Get one automation engine run
// @Description Read one archived engine run by its identifier
// @Tags [Engine] AI Application Automation Engine
// @Produce json
// @Param runId path string true "Run ID"
// @Success 200 {object} engine.Run "Archived run"
// @Failure 404 {object} model.SimpleMsg "Run not found"
// @Failure 500 {object} model.SimpleMsg "Archive lookup failed"
// @Security BearerAuth
// @Router /engine/runs/{runId} [get]
func (h *Handler) RestGetEngineRun(c echo.Context) error {
	runID := c.Param("runId")
	run, err := h.engine.GetRun(runID)
	if errors.Is(err, engine.ErrRunNotFound) {
		return c.JSON(http.StatusNotFound, model.SimpleMsg{Message: "Run not found"})
	}
	if err != nil {
		log.Error().Err(err).Str("runId", runID).Msg("Failed to read engine run")
		return c.JSON(http.StatusInternalServerError, model.SimpleMsg{Message: "Archive lookup failed"})
	}
	return c.JSON(http.StatusOK, run)
}

// RestGetEngineCard godoc
// @ID GetEngineCard
// @Summary Describe the automation engine
// @Description Report the engine card an agent registry reads: identity, that it uses no LLM, its workflows, the AI-App MCP server it calls and whether it is connected, the tools it publishes and the ones it calls, the dry-run state and how registration went
// @Tags [Engine] AI Application Automation Engine
// @Produce json
// @Success 200 {object} engine.Card "Engine card"
// @Failure 502 {object} model.SimpleMsg "Automation engine is not reachable from the AI-App service"
// @Security BearerAuth
// @Router /engine/card [get]
func (h *Handler) RestGetEngineCard(c echo.Context) error {
	return c.JSON(http.StatusOK, h.engineCard.Build())
}

// engineFailure maps an error that kept a workflow from running to a status
// and a caller-facing message.
func engineFailure(err error) (int, string) {
	switch {
	case errors.Is(err, engine.ErrUnknownWorkflow), errors.Is(err, engine.ErrInvalidInput):
		status := http.StatusBadRequest
		if errors.Is(err, engine.ErrUnknownWorkflow) {
			status = http.StatusNotFound
		}
		return status, err.Error()
	case errors.Is(err, mcpclient.ErrUnavailable):
		return http.StatusServiceUnavailable, "AI-App tool server is not reachable"
	}
	return http.StatusInternalServerError, "Engine run failed"
}
