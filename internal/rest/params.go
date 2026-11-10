package rest

import (
	"github.com/labstack/echo/v4"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// pageQuery reads the limit and offset query parameters every list takes.
// A missing parameter takes its default; see model.NewPage.
func pageQuery(c echo.Context) (model.Page, error) {
	return model.ParsePage(c.QueryParam("limit"), c.QueryParam("offset"))
}
