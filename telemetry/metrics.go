package telemetry

import (
	"log/slog"
	"net/http"
	_ "net/http/pprof"

	"github.com/jazware/bsky-experiments/telemetry/ops"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/urfave/cli/v2"
)

var CLIFlagMetricsListenAddress = &cli.StringFlag{
	Name:    "metrics-listen-address",
	Usage:   "listen endpoint for metrics and pprof",
	Value:   "0.0.0.0:8081",
	EnvVars: []string{"METRICS_LISTEN_ADDRESS"},
}

type Metrics struct {
	listenAddr string
	path       string
}

func StartMetrics(cctx *cli.Context, opts ...MetricsOption) {
	listenAddr := cctx.String("metrics-listen-address")

	logger := slog.Default().With("component", "telemetry")

	m := &Metrics{
		listenAddr: listenAddr,
		path:       "/metrics",
	}

	for _, opt := range opts {
		opt(m)
	}

	// The default registry serves the GC's numbers too (ops.GoRuntimeMetrics),
	// also to services that serve promhttp.Handler() on a port of their own.
	ops.UseGoCollector()

	// Start the metrics server.
	logger.Info("starting metrics server", "address", m.listenAddr, "path", m.path)
	if m.listenAddr != "" {
		metricsServer := http.DefaultServeMux
		metricsServer.Handle(m.path, promhttp.Handler())
		go func() {
			if err := http.ListenAndServe(m.listenAddr, metricsServer); err != nil {
				logger.Error("metrics server failed", "err", err)
			}
		}()
	}
}

type MetricsOption func(*Metrics)

// WithListenAddr sets the listen address for the metrics server.
// Defaults to :8081 or whatever is set in the CLI flag. An empty address
// disables the server, as an empty flag does.
func WithListenAddr(addr string) MetricsOption {
	return func(m *Metrics) {
		m.listenAddr = addr
	}
}

// WithPath sets the path for the metrics server.
// Defaults to "/metrics".
func WithPath(path string) MetricsOption {
	return func(m *Metrics) {
		m.path = path
	}
}
