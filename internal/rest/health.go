package rest

import (
	"net/http"
	"sync/atomic"

	"github.com/labstack/echo/v4"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// readiness reports whether initialization has completed.
var readiness atomic.Bool

// MarkReady flips the service to ready so that RestGetReadyz starts returning 200.
func MarkReady() {
	readiness.Store(true)
}

// RestGetReadyz godoc
// @ID GetReadyz
// @Summary Check service readiness
// @Description Report whether the service finished initialization and can accept requests
// @Tags [Health] Service Health
// @Produce json
// @Success 200 {object} model.SimpleMsg "Service is ready"
// @Failure 503 {object} model.SimpleMsg "Service is still initializing"
// @Router /readyz [get]
func RestGetReadyz(c echo.Context) error {
	if !readiness.Load() {
		return c.JSON(http.StatusServiceUnavailable, model.SimpleMsg{Message: "Service is initializing"})
	}
	return c.JSON(http.StatusOK, model.SimpleMsg{Message: "Service is ready"})
}
