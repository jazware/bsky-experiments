package telemetry

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/urfave/cli/v2"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.19.0"
	"go.opentelemetry.io/otel/trace"
)

var CLIFlagTracingSampleRatio = &cli.Float64Flag{
	Name:    "tracing-sample-ratio",
	Usage:   "tracing sample ratio (0.0 to 1.0)",
	Value:   1.0,
	EnvVars: []string{"TRACING_SAMPLE_RATIO"},
}

var CLIFlagTracingRootSampleRatios = &cli.StringSliceFlag{
	Name:    "tracing-root-sample-ratios",
	Usage:   "per-span-name sample ratios for root spans, e.g. HandleStreamEvent=0.01 (overrides tracing-sample-ratio for those roots)",
	EnvVars: []string{"TRACING_ROOT_SAMPLE_RATIOS"},
}

var CLIFlagServiceName = &cli.StringFlag{
	Name:    "service-name",
	Usage:   "service name for tracing",
	Value:   "service",
	EnvVars: []string{"SERVICE_NAME"},
}

type Tracing struct {
	serviceName  string
	sampleRatio  float64
	rootRatios   map[string]float64
	exporter     sdktrace.SpanExporter
	provider     *sdktrace.TracerProvider
	shutdownFunc func(context.Context) error
}

func StartTracing(cctx *cli.Context, opts ...TracingOption) (func(context.Context) error, error) {
	logger := slog.Default().With("component", "telemetry")
	ctx := context.Background()

	t := &Tracing{
		serviceName: cctx.String("service-name"),
		sampleRatio: cctx.Float64("tracing-sample-ratio"),
	}

	for _, opt := range opts {
		opt(t)
	}

	for _, kv := range cctx.StringSlice(CLIFlagTracingRootSampleRatios.Name) {
		name, ratio, ok := strings.Cut(kv, "=")
		r, err := strconv.ParseFloat(ratio, 64)
		if !ok || err != nil {
			return nil, fmt.Errorf("invalid %s entry %q (want name=ratio)", CLIFlagTracingRootSampleRatios.Name, kv)
		}
		WithRootSpanRatio(name, r)(t)
	}

	if t.exporter == nil {
		client := otlptracehttp.NewClient()
		exporter, err := otlptrace.New(ctx, client)
		if err != nil {
			return nil, fmt.Errorf("creating OTLP trace exporter: %w", err)
		}
		t.exporter = exporter
	}

	t.provider = newTraceProvider(t.exporter, t.serviceName, newSampler(t.sampleRatio, t.rootRatios))
	otel.SetTracerProvider(t.provider)

	t.shutdownFunc = t.provider.Shutdown

	logger.Info("started tracing", "service", t.serviceName, "sample_ratio", t.sampleRatio, "root_sample_ratios", t.rootRatios)

	return t.shutdownFunc, nil
}

func newTraceProvider(exp sdktrace.SpanExporter, serviceName string, sampler sdktrace.Sampler) *sdktrace.TracerProvider {
	// Ensure default SDK resources and the required service name are set.
	r := resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(serviceName),
	)

	return sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sampler),
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(r),
	)
}

// newSampler is ParentBased so that child spans inherit their parent's
// decision: without it, library code (e.g. indigo) that creates spans via the
// global tracer gets sampled independently, generating millions of unwanted
// spans. Root spans named in rootRatios use their own ratio, so a per-event
// root on a hot path can be thinned without losing rarer roots.
func newSampler(ratio float64, rootRatios map[string]float64) sdktrace.Sampler {
	root := &rootSampler{
		def:    sdktrace.TraceIDRatioBased(ratio),
		byName: make(map[string]sdktrace.Sampler, len(rootRatios)),
	}
	for name, r := range rootRatios {
		root.byName[name] = sdktrace.TraceIDRatioBased(r)
	}
	return sdktrace.ParentBased(root)
}

type rootSampler struct {
	def    sdktrace.Sampler
	byName map[string]sdktrace.Sampler
}

func (s *rootSampler) ShouldSample(p sdktrace.SamplingParameters) sdktrace.SamplingResult {
	if named, ok := s.byName[p.Name]; ok {
		return named.ShouldSample(p)
	}
	return s.def.ShouldSample(p)
}

func (s *rootSampler) Description() string {
	return fmt.Sprintf("RootSampler{default:%s,byName:%d}", s.def.Description(), len(s.byName))
}

// WithoutChildSpans returns ctx with an unsampled current span, so spans
// started under it (by the ParentBased sampler) are dropped. For loops into
// library code that opens a span per item, such as indigo's per-record
// repo.GetRecord: with no traced parent each of those is its own root trace.
// The trace ID of ctx's span is kept so logs still correlate.
func WithoutChildSpans(ctx context.Context) context.Context {
	sc := trace.SpanContextFromContext(ctx)
	cfg := trace.SpanContextConfig{TraceID: sc.TraceID(), SpanID: sc.SpanID()}
	if !sc.IsValid() {
		_, _ = rand.Read(cfg.TraceID[:])
		_, _ = rand.Read(cfg.SpanID[:])
	}
	return trace.ContextWithSpanContext(ctx, trace.NewSpanContext(cfg))
}

// TracingOption is a functional option for configuring the Tracing.
type TracingOption func(*Tracing)

// WithServiceName sets the service name for tracing.
// Defaults to "service" or whatever is set in the CLI flag.
func WithServiceName(name string) TracingOption {
	return func(t *Tracing) {
		t.serviceName = name
	}
}

// WithSampleRatio sets the sample ratio for tracing.
// Defaults to 1.0 or whatever is set in the CLI flag.
func WithSampleRatio(ratio float64) TracingOption {
	return func(t *Tracing) {
		t.sampleRatio = ratio
	}
}

// WithRootSpanRatio sets the sample ratio for root spans with this name.
// Entries from the tracing-root-sample-ratios flag take precedence.
func WithRootSpanRatio(name string, ratio float64) TracingOption {
	return func(t *Tracing) {
		if t.rootRatios == nil {
			t.rootRatios = map[string]float64{}
		}
		t.rootRatios[name] = ratio
	}
}

// WithExporter sets a custom span exporter for tracing.
func WithExporter(exporter sdktrace.SpanExporter) TracingOption {
	return func(t *Tracing) {
		t.exporter = exporter
	}
}
