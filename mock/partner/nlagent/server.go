// Package nlagent serves the partner institution's natural language agent;
// mock implementation for integration, not part of the Innogrid prototype.
//
// Every route sits under Prefix, the path the AI-App service forwards
// unchanged, so the console and operators keep the AI-App address.
package nlagent

import (
	"errors"
	"net/http"
	"strconv"
	"sync/atomic"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/mcpclient"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/rest"
	"github.com/ish-hcc/ai-agent-proto/mock/partner/nlagent/agent"
	"github.com/ish-hcc/ai-agent-proto/mock/partner/nlagent/card"
	"github.com/ish-hcc/ai-agent-proto/mock/partner/nlagent/llm"
)

// Prefix is the mount point of every route of the mock.
const Prefix = rest.BasePath + "/partner/nl"

// ReadyPath is the readiness route. It is open, as readiness is on every
// service here.
const ReadyPath = Prefix + "/readyz"

// Handler serves the mock routes.
type Handler struct {
	agent *agent.Service
	card  *card.Card
	ready atomic.Bool
}

// NewHandler builds the mock routes on svc and agentCard, requiring token as a
// bearer token on every route but readiness. An empty token turns the check off.
func NewHandler(svc *agent.Service, agentCard *card.Card, token string) (*Handler, *echo.Echo) {
	h := &Handler{agent: svc, card: agentCard}

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Use(middleware.Recover())
	e.Use(middleware.RequestID())
	e.Use(rest.RequireBearer(token, ReadyPath))

	g := e.Group(Prefix)
	g.GET("/readyz", h.getReadyz)
	g.POST("/ns/:nsId/intents", h.postIntent)
	g.GET("/runs", h.getRuns)
	g.GET("/runs/:runId", h.getRun)
	g.GET("/card", h.getCard)
	return h, e
}

// MarkReady flips readiness to 200.
func (h *Handler) MarkReady() { h.ready.Store(true) }

func (h *Handler) getReadyz(c echo.Context) error {
	if !h.ready.Load() {
		return c.JSON(http.StatusServiceUnavailable, model.SimpleMsg{Message: agent.Marker + " Service is initializing"})
	}
	return c.JSON(http.StatusOK, model.SimpleMsg{Message: agent.Marker + " Service is ready"})
}

func (h *Handler) postIntent(c echo.Context) error {
	nsID := c.Param("nsId")

	var req agent.IntentReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: agent.Marker + " Request body is malformed"})
	}
	if err := req.Validate(); err != nil {
		return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: agent.Marker + " " + err.Error()})
	}

	result, err := h.agent.Run(c.Request().Context(), nsID, req.Intent)
	if err != nil {
		status, message := intentFailure(err)
		// The instruction itself is operational content, so only its length is logged.
		log.Error().Err(err).Str("nsId", nsID).Int("intentLength", len(req.Intent)).Int("status", status).
			Msg(agent.Marker + " Failed to run intent")
		return c.JSON(status, model.SimpleMsg{Message: message})
	}
	return c.JSON(http.StatusOK, result)
}

func (h *Handler) getRuns(c echo.Context) error {
	limit := 0
	if raw := c.QueryParam("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: agent.Marker + " Limit must be a whole number"})
		}
		limit = n
	}
	runs, err := h.agent.ListRuns(limit)
	if err != nil {
		log.Error().Err(err).Msg(agent.Marker + " Failed to list runs")
		return c.JSON(http.StatusInternalServerError, model.SimpleMsg{Message: agent.Marker + " Run lookup failed"})
	}
	return c.JSON(http.StatusOK, agent.RunListResp{Runs: runs})
}

func (h *Handler) getRun(c echo.Context) error {
	run, found, err := h.agent.GetRun(c.Param("runId"))
	if err != nil {
		log.Error().Err(err).Msg(agent.Marker + " Failed to read run")
		return c.JSON(http.StatusInternalServerError, model.SimpleMsg{Message: agent.Marker + " Run lookup failed"})
	}
	if !found {
		return c.JSON(http.StatusNotFound, model.SimpleMsg{Message: agent.Marker + " Run not found"})
	}
	return c.JSON(http.StatusOK, run)
}

func (h *Handler) getCard(c echo.Context) error {
	return c.JSON(http.StatusOK, h.card.Build())
}

// intentFailure keeps model and driver wording out of the response body.
func intentFailure(err error) (int, string) {
	switch {
	case errors.Is(err, llm.ErrDisabled):
		return http.StatusServiceUnavailable, agent.Marker + " Planning model is not configured"
	case errors.Is(err, mcpclient.ErrUnavailable):
		return http.StatusServiceUnavailable, agent.Marker + " Automation engine tool server is not reachable"
	}
	return http.StatusInternalServerError, agent.Marker + " Agent run failed"
}
