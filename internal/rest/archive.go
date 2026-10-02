package rest

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// defaultArchiveLimit bounds a listing so a long archive does not arrive whole.
const defaultArchiveLimit = 20

// RestGetArchive godoc
// @ID GetArchive
// @Summary List archived runs
// @Description List archived deployment and control runs, newest first. A record holds the instruction, every tool call with its arguments and the guard verdict
// @Tags [Archive] Deploy and Control Archive
// @Produce json
// @Param limit query int false "How many records to return" default(20)
// @Success 200 {object} model.ArchiveListResp "Archived runs"
// @Failure 400 {object} model.SimpleMsg "Malformed limit"
// @Failure 500 {object} model.SimpleMsg "Archive lookup failed"
// @Router /archive [get]
func (h *Handler) RestGetArchive(c echo.Context) error {
	ctx := c.Request().Context()

	limit, err := intQueryParam(c, "limit")
	if err != nil {
		return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: "Limit must be a whole number"})
	}
	if limit <= 0 {
		limit = defaultArchiveLimit
	}

	records, err := h.archive.List(ctx, limit)
	if err != nil {
		log.Error().Err(err).Int("limit", limit).Msg("Failed to list archive")
		return c.JSON(http.StatusInternalServerError, model.SimpleMsg{Message: "Archive lookup failed"})
	}
	return c.JSON(http.StatusOK, model.ArchiveListResp{Records: records})
}

// RestGetArchiveRun godoc
// @ID GetArchiveRun
// @Summary Get one archived run
// @Description Read one archived run by its identifier
// @Tags [Archive] Deploy and Control Archive
// @Produce json
// @Param runId path string true "Run ID"
// @Success 200 {object} model.ArchiveRecord "Archived run"
// @Failure 404 {object} model.SimpleMsg "Run not found"
// @Failure 500 {object} model.SimpleMsg "Archive lookup failed"
// @Router /archive/{runId} [get]
func (h *Handler) RestGetArchiveRun(c echo.Context) error {
	ctx := c.Request().Context()
	runID := c.Param("runId")

	record, err := h.archive.Get(ctx, runID)
	if err != nil {
		log.Warn().Err(err).Str("runId", runID).Msg("Failed to get archived run")
		return c.JSON(http.StatusNotFound, model.SimpleMsg{Message: "Run not found"})
	}
	return c.JSON(http.StatusOK, record)
}
