package dashboard

import "embed"

// DistFS holds the Vite build output. Run `just dashboard-build` to
// populate dist/; the committed .gitkeep keeps `go build` working before
// the first frontend build.
//
//go:embed all:dist
var DistFS embed.FS
