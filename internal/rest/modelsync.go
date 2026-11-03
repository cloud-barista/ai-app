package rest

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/ish-hcc/ai-agent-proto/internal/catalogsync"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// RestGetModelSync godoc
// @ID GetModelSync
// @Summary Read the catalog sync status
// @Description Report whether the catalog sync is enabled and running, how long it waits between scheduled runs, and how the last run of each configured source went: when it started and finished, how many models the source returned, what they did to the catalog (added, updated, marked stale, skipped because another origin holds the id, dropped as invalid) and the error when the source failed. A failed source leaves its entries as they were. Before a source has run once only its name is given. With AIAPP_CATALOG_SYNC_ENABLED off, enabled is false and sources is empty.
// @Tags [Model] AI Model Catalog
// @Produce json
// @Success 200 {object} catalogsync.Status "Catalog sync status"
// @Security BearerAuth
// @Router /models/sync [get]
func (h *Handler) RestGetModelSync(c echo.Context) error {
	if h.sync == nil {
		return c.JSON(http.StatusOK, catalogsync.Status{Sources: []catalogsync.SourceResult{}})
	}
	return c.JSON(http.StatusOK, h.sync.Status())
}

// RestPostModelSync godoc
// @ID PostModelSync
// @Summary Run the catalog sync now
// @Description Start one run of every configured source in the background and return at once with the status, running true. Poll GET /models/sync for the results. The sync adds the models a source lists, updates the ones it added before (keeping runtimes[].verifiedAt), marks stale the ones the source no longer lists and never changes a model of origin seed or manual or of another source. Only one run happens at a time: while one is in progress, including the scheduled one, this answers 409.
// @Tags [Model] AI Model Catalog
// @Produce json
// @Success 202 {object} catalogsync.Status "Run started"
// @Failure 409 {object} model.SimpleMsg "A run is already in progress"
// @Failure 503 {object} model.SimpleMsg "The catalog sync is disabled (AIAPP_CATALOG_SYNC_ENABLED is off)"
// @Security BearerAuth
// @Router /models/sync [post]
func (h *Handler) RestPostModelSync(c echo.Context) error {
	if h.sync == nil {
		return c.JSON(http.StatusServiceUnavailable,
			model.SimpleMsg{Message: "The catalog sync is disabled: set AIAPP_CATALOG_SYNC_ENABLED=true to turn it on"})
	}
	if !h.sync.Trigger() {
		return c.JSON(http.StatusConflict, model.SimpleMsg{Message: catalogsync.ErrRunning.Error()})
	}
	return c.JSON(http.StatusAccepted, h.sync.Status())
}
