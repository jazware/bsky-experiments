package telemetry

import (
	"flag"
	"net/http"
	"testing"

	"github.com/urfave/cli/v2"
)

func TestStartMetricsEmptyAddrDisablesServer(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.String("metrics-listen-address", "0.0.0.0:8081", "")
	cctx := cli.NewContext(cli.NewApp(), fs, nil)

	StartMetrics(cctx, WithListenAddr(""), WithPath("/metrics-empty-test"))

	req, _ := http.NewRequest(http.MethodGet, "/metrics-empty-test", nil)
	if _, pattern := http.DefaultServeMux.Handler(req); pattern != "" {
		t.Fatalf("metrics handler registered with an empty address: %q", pattern)
	}
}
