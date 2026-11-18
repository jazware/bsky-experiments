package endpoints

import (
	"errors"
	"net/http"

	"github.com/jazware/bsky-experiments/pkg/search"
	"github.com/labstack/echo/v4"
)

func (api *API) GetStats(c echo.Context) error {
	ctx, span := tracer.Start(c.Request().Context(), "GetStats")
	defer span.End()

	stats, err := api.SearchService.GetStats(ctx)
	if err != nil {
		if errors.Is(err, search.ErrStatsNotReady) {
			c.Response().Header().Set("Retry-After", "5")
			return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "stats not ready yet"})
		}
		api.Logger.Error("failed to get stats", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to get stats"})
	}

	return c.JSON(http.StatusOK, stats)
}
