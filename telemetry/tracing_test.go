package telemetry

import (
	"context"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestSampler(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(newSampler(1.0, map[string]float64{"hot": 0})),
		sdktrace.WithSpanProcessor(rec),
	)
	tr := tp.Tracer("test")
	ctx := context.Background()

	_, hot := tr.Start(ctx, "hot")
	hot.End()

	pctx, parent := tr.Start(ctx, "parent")
	_, child := tr.Start(pctx, "hot") // not a root: follows the parent
	child.End()
	_, quiet := tr.Start(WithoutChildSpans(pctx), "quiet")
	quiet.End()
	parent.End()

	_, orphan := tr.Start(WithoutChildSpans(ctx), "orphan")
	orphan.End()

	var got []string
	for _, s := range rec.Ended() {
		got = append(got, s.Name())
	}
	want := []string{"hot", "parent"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("exported spans = %v, want %v", got, want)
	}
	if got := rec.Ended()[0].Parent().SpanID(); got != parent.SpanContext().SpanID() {
		t.Fatalf("child hot span parent = %s, want %s", got, parent.SpanContext().SpanID())
	}
}
