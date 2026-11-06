package rest

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// RestGetArchive godoc
// @ID GetArchive
// @Summary List archived runs
// @Description List archived deployment and control runs, newest first. A record holds the instruction, every tool call with its arguments and the guard verdict. The list is paged: limit and offset pick the page, and total counts every record.
// @Tags [Archive] Deploy and Control Archive
// @Produce json
// @Param limit query int false "Most records to return, from 1 to 1000" default(100)
// @Param offset query int false "Records to skip from the newest, 0 or more. Past the end gives an empty list" default(0)
// @Success 200 {object} model.ArchiveListResp "Archived runs, newest first, with total, limit and offset"
// @Failure 400 {object} model.SimpleMsg "limit is not from 1 to 1000, or offset is negative or not a whole number"
// @Failure 500 {object} model.SimpleMsg "Archive lookup failed"
// @Security BearerAuth
// @Router /archive [get]
func (h *Handler) RestGetArchive(c echo.Context) error {
	ctx := c.Request().Context()

	page, err := pageQuery(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: model.ValidationMessage(err)})
	}

	records, err := h.archive.List(ctx, 0)
	if err != nil {
		log.Error().Err(err).Msg("Failed to list archive")
		return c.JSON(http.StatusInternalServerError, model.SimpleMsg{Message: "Archive lookup failed"})
	}
	resp := model.ArchiveListResp{}
	resp.Records, resp.Page = model.Paginate(records, page)
	return c.JSON(http.StatusOK, resp)
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
// @Security BearerAuth
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
