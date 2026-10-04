package endpoints

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
)

// officialPDSSuffix restricts runtime additions to Bluesky-operated PDS
// instances; third-party hosts must go through the checked-in default list.
const officialPDSSuffix = ".host.bsky.network"

type addPDSRequest struct {
	Host string `json:"host"`
}

// GetPDSList returns the usercount scrape list with per-host user counts.
func (api *API) GetPDSList(c echo.Context) error {
	hosts := api.SearchService.UserCount.Hosts()
	return c.JSON(http.StatusOK, map[string]any{
		"count": len(hosts),
		"hosts": hosts,
	})
}

// AddPDSToList adds an official Bluesky PDS to the usercount scrape list at
// runtime, persisting it to redis. The host must be a *.host.bsky.network
// instance and must answer com.atproto.server.describeServer before it is
// accepted, so junk hostnames can't be injected.
func (api *API) AddPDSToList(c echo.Context) error {
	ctx, span := tracer.Start(c.Request().Context(), "AddPDSToList")
	defer span.End()

	var req addPDSRequest
	if err := c.Bind(&req); err != nil || strings.TrimSpace(req.Host) == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error": `body must be {"host": "https://<name>.host.bsky.network"}`,
		})
	}

	raw := strings.TrimRight(strings.TrimSpace(req.Host), "/")
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}

	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != u.Hostname() ||
		u.Path != "" || u.RawQuery != "" || u.User != nil ||
		!strings.HasSuffix(u.Hostname(), officialPDSSuffix) {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error": "host must be an official https://<name>" + officialPDSSuffix + " instance",
		})
	}
	host := "https://" + u.Hostname()

	if err := probePDS(ctx, host); err != nil {
		api.Logger.Warn("PDS probe failed", "host", host, "error", err)
		return c.JSON(http.StatusBadGateway, map[string]string{
			"error": "host did not answer describeServer: " + err.Error(),
		})
	}

	added, err := api.SearchService.UserCount.AddPDS(ctx, host)
	if err != nil {
		api.Logger.Error("failed to add PDS to scrape list", "host", host, "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to persist PDS"})
	}

	if added {
		api.Logger.Info("added PDS to scrape list", "host", host)
	}

	return c.JSON(http.StatusOK, map[string]any{
		"host":  host,
		"added": added,
	})
}

// probePDS verifies the host is a live PDS by calling describeServer.
func probePDS(ctx context.Context, host string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, host+"/xrpc/com.atproto.server.describeServer", nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return echo.NewHTTPError(resp.StatusCode, "unexpected status from describeServer")
	}

	var desc struct {
		DID string `json:"did"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&desc); err != nil || desc.DID == "" {
		return echo.NewHTTPError(http.StatusBadGateway, "response is not a PDS describeServer document")
	}

	return nil
}
