package rest

import (
	"strconv"

	"github.com/labstack/echo/v4"
)

// intQueryParam reads an optional whole number query parameter.
// A missing parameter yields 0, which every caller treats as "use the default".
func intQueryParam(c echo.Context, name string) (int, error) {
	raw := c.QueryParam(name)
	if raw == "" {
		return 0, nil
	}
	return strconv.Atoi(raw)
}
