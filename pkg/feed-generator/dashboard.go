package feedgenerator

import (
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/jazware/bsky-experiments/dashboard"
	"github.com/labstack/echo/v4"
)

type DashboardHandler struct {
	distFS     fs.FS
	fileServer http.Handler
}

func NewDashboardHandler() *DashboardHandler {
	distFS, err := fs.Sub(dashboard.DistFS, "dist")
	if err != nil {
		panic("failed to get dist subdirectory from embedded FS: " + err.Error())
	}

	return &DashboardHandler{
		distFS:     distFS,
		fileServer: http.FileServer(http.FS(distFS)),
	}
}

func (h *DashboardHandler) ServeHTTP(c echo.Context) error {
	reqPath := c.Param("*")
	if reqPath == "" {
		reqPath = "index.html"
	}

	f, err := h.distFS.Open(reqPath)
	if err == nil {
		f.Close()
		contentType := getMimeType(reqPath)
		if contentType != "" {
			c.Response().Header().Set("Content-Type", contentType)
		}
		http.StripPrefix("/dashboard/", h.fileServer).ServeHTTP(c.Response(), c.Request())
		return nil
	}

	indexData, err := fs.ReadFile(h.distFS, "index.html")
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "Dashboard UI not found")
	}

	return c.HTMLBlob(http.StatusOK, indexData)
}

func getMimeType(filePath string) string {
	ext := strings.ToLower(path.Ext(filePath))
	switch ext {
	case ".html":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js":
		return "application/javascript; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".ico":
		return "image/x-icon"
	case ".woff":
		return "font/woff"
	case ".woff2":
		return "font/woff2"
	default:
		return ""
	}
}
