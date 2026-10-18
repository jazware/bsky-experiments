package endpoints

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
)

func (api *API) GetStats(c echo.Context) error {
	ctx, span := tracer.Start(c.Request().Context(), "GetStats")
	defer span.End()

	stats, err := api.SearchService.GetStats(ctx, 30*time.Second)
	if err != nil {
		api.Logger.Error("failed to get stats", "error", err)
		return c.JSON(http.StatusRequestTimeout, map[string]string{"error": "timed out waiting for stats cache to populate"})
	}

	return c.JSON(http.StatusOK, stats)
}
