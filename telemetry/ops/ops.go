// Package ops serves a service's operator endpoints, /metrics and
// net/http/pprof, on a listener of their own, apart from the service's API
// and its auth. yeetd scrapes both (packages/yeet/docs/18-profiling.md,
// 19-metrics.md) when a stack declares them in [services.<svc>.scrape].
//
// It imports only client_golang and the standard library, so a service can
// use it without the OpenTelemetry modules package telemetry pulls in.
package ops

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/pprof"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// NewRegistry is a registry with the Go runtime (GoCollector) and process
// collectors, the series every service's dashboards and alerts expect.
func NewRegistry() *prometheus.Registry {
	r := prometheus.NewRegistry()
	r.MustRegister(GoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return r
}

// Handler serves /metrics from g and pprof under /debug/pprof/, and nothing
// else. It has its own mux, so handlers registered on http.DefaultServeMux
// (net/http/pprof's import side effect among them) don't leak onto it, and
// pprof never shows up on the service's own port by way of this package.
func Handler(g prometheus.Gatherer) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(g, promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
	return mux
}

// Server is an http.Server for Handler. It has no WriteTimeout: pprof's CPU
// profile and trace refuse a seconds parameter past the server's write
// timeout, and yeet's captures ask for up to a few minutes.
func Server(addr string, g prometheus.Gatherer) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           Handler(g),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
}

// Serve listens on addr and serves Handler(g) until ctx ends. The listener
// is bound before it returns, so a bad address fails the caller's start;
// errors after that go to onErr (nil drops them).
func Serve(ctx context.Context, addr string, g prometheus.Gatherer, onErr func(error)) (net.Addr, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	srv := Server(addr, g)
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && onErr != nil {
			onErr(err)
		}
	}()
	return ln.Addr(), nil
}
