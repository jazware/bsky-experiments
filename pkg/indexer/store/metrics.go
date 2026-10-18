package store

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

var tracer = otel.Tracer("atproto-indexer-store")

// callerCache memoizes PC -> function name lookups to avoid stack unwinding on every call.
// Key is uintptr (program counter), value is the function name string.
var callerCache sync.Map

var (
	storeOpsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "atproto_store_operations_total",
		Help: "Total number of store operations by operation type, caller, and result",
	}, []string{"operation", "caller", "result"})

	storeOpDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "atproto_store_operation_duration_seconds",
		Help:    "Store operation duration in seconds",
		Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5},
	}, []string{"operation", "caller"})

	batchSize = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "atproto_store_batch_size",
		Help:    "Number of rows in batch insert operations",
		Buckets: []float64{1, 10, 100, 500, 1000, 5000, 10000, 25000, 50000},
	}, []string{"table"})

	batchDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "atproto_store_batch_duration_seconds",
		Help:    "Batch insert duration in seconds",
		Buckets: []float64{.01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
	}, []string{"table"})
)

// callerName extracts the calling function name from the stack.
// Results are memoized by program counter to avoid repeated stack unwinding.
func callerName(skip int) string {
	var pcs [1]uintptr
	n := runtime.Callers(skip+1, pcs[:])
	if n == 0 {
		return "unknown"
	}

	pc := pcs[0]

	if cached, ok := callerCache.Load(pc); ok {
		return cached.(string)
	}

	frames := runtime.CallersFrames(pcs[:])
	frame, _ := frames.Next()

	// Extract just the function name: "(*Store).GetHotPosts" -> "GetHotPosts"
	name := frame.Function
	if idx := strings.LastIndex(name, "."); idx != -1 {
		name = name[idx+1:]
	}

	callerCache.Store(pc, name)
	return name
}

// observe creates a span and returns a done function that records metrics and ends the span.
// The done function accepts optional result attributes to add before closing.
//
// Usage:
//
//	func (s *Store) GetHotPosts(ctx context.Context, limit int) (posts []HotPost, err error) {
//	    ctx, done := observe(ctx, "query", &err, attribute.Int("limit", limit))
//	    defer func() { done(attribute.Int("result_count", len(posts))) }()
//	    // ... or simply: defer done() if no result attributes needed
//	}
func observe(ctx context.Context, operation string, err *error, attrs ...attribute.KeyValue) (context.Context, func(...attribute.KeyValue)) {
	start := time.Now()
	caller := callerName(3)

	baseAttrs := []attribute.KeyValue{
		attribute.String("db.system", "clickhouse"),
		attribute.String("db.operation", operation),
		attribute.String("code.function", caller),
	}
	ctx, span := tracer.Start(ctx, "store."+caller, trace.WithAttributes(append(baseAttrs, attrs...)...))

	return ctx, func(resultAttrs ...attribute.KeyValue) {
		if len(resultAttrs) > 0 {
			span.SetAttributes(resultAttrs...)
		}

		if *err != nil {
			span.RecordError(*err)
		}
		span.End()

		duration := time.Since(start).Seconds()
		storeOpDuration.WithLabelValues(operation, caller).Observe(duration)

		result := "success"
		if *err != nil {
			errStr := (*err).Error()
			if strings.Contains(errStr, "not found") ||
				strings.Contains(errStr, "no rows") {
				result = "not_found"
			} else {
				result = "error"
			}
		}
		storeOpsTotal.WithLabelValues(operation, caller, result).Inc()
	}
}

// observeBatchOp creates a span for batch operations and returns a done function.
//
// Usage:
//
//	func (bi *BatchInserter) insertPostBatch(ctx context.Context, batch []*PostBatch) (err error) {
//	    ctx, done := observeBatchOp(ctx, "posts", len(batch), &err)
//	    defer done()
//	}
func observeBatchOp(ctx context.Context, table string, size int, err *error) (context.Context, func()) {
	start := time.Now()
	caller := callerName(3)

	ctx, span := tracer.Start(ctx, "store."+caller, trace.WithAttributes(
		attribute.String("db.system", "clickhouse"),
		attribute.String("db.operation", "batch_insert"),
		attribute.String("db.table", table),
		attribute.String("code.function", caller),
		attribute.Int("batch.size", size),
	))

	return ctx, func() {
		if *err != nil {
			span.RecordError(*err)
		}
		span.End()

		duration := time.Since(start).Seconds()
		batchSize.WithLabelValues(table).Observe(float64(size))
		batchDuration.WithLabelValues(table).Observe(duration)

		result := "success"
		if *err != nil {
			result = "error"
		}
		storeOpsTotal.WithLabelValues("batch_insert", table, result).Inc()
	}
}
