package rest

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/ish-hcc/ai-agent-proto/web"
)

// RestGetConsole serves the operator console.
//
// It is served from this service rather than from somewhere of its own so that
// the page and the API share an origin: the console calls the same endpoints an
// operator would call by hand, and a separate origin would have meant either a
// CORS policy on an API that has no authentication yet, or a second thing to
// deploy next to the first.
//
// The page is documented as not cacheable because it is the one file that
// changes whenever the API it draws does, and a stale console showing fields
// that no longer exist is harder to diagnose than a reload.
//
// @ID GetConsole
// @Summary Open the operator console
// @Description Serve the single page console that calls this service's own API
// @Tags [Console] Operator Console
// @Produce html
// @Success 200 {string} string "The console page"
// @Router /ui [get]
func RestGetConsole(c echo.Context) error {
	page, err := web.Index()
	if err != nil {
		return c.JSON(http.StatusInternalServerError,
			echo.Map{"message": "Console page is missing from the binary"})
	}

	c.Response().Header().Set("Cache-Control", "no-store")

	return c.HTMLBlob(http.StatusOK, page)
}
