package ops

import (
	"runtime/debug"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// The names go-runtime-alerts.yml and the fleet dashboard read.
var gcSeries = []string{
	"go_cpu_classes_gc_total_cpu_seconds_total",
	"go_cpu_classes_gc_mark_assist_cpu_seconds_total",
	"go_cpu_classes_gc_mark_idle_cpu_seconds_total",
	"go_cpu_classes_idle_cpu_seconds_total",
	"go_cpu_classes_total_cpu_seconds_total",
	"go_cpu_classes_user_cpu_seconds_total",
	"go_gc_cycles_total_gc_cycles_total",
	"go_gc_heap_live_bytes",
	"go_gc_heap_goal_bytes",
	"go_gc_gomemlimit_bytes",
	"go_gc_gogc_percent",
	"go_sched_gomaxprocs_threads",
}

func names(t *testing.T, g prometheus.Gatherer) map[string]bool {
	t.Helper()
	fams, err := g.Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, f := range fams {
		out[f.GetName()] = true
	}
	return out
}

func TestRegistryHasTheGCSeries(t *testing.T) {
	got := names(t, NewRegistry())
	for _, n := range append(gcSeries, "go_goroutines", "go_memstats_heap_alloc_bytes", "process_cpu_seconds_total") {
		if !got[n] {
			t.Errorf("no %s", n)
		}
	}
	// Only what's picked: no histograms by size class, no godebug counters.
	for n := range got {
		if strings.HasPrefix(n, "go_gc_heap_allocs_by_size") || strings.HasPrefix(n, "go_godebug_") {
			t.Errorf("unexpected %s", n)
		}
	}
}

func TestGoMemLimitIsExported(t *testing.T) {
	defer debug.SetMemoryLimit(debug.SetMemoryLimit(384 << 20))
	r := prometheus.NewRegistry()
	r.MustRegister(GoCollector())
	fams, err := r.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fams {
		if f.GetName() == "go_gc_gomemlimit_bytes" {
			if v := f.GetMetric()[0].GetGauge().GetValue(); v != 384<<20 {
				t.Fatalf("go_gc_gomemlimit_bytes %v", v)
			}
			return
		}
	}
	t.Fatal("no go_gc_gomemlimit_bytes")
}

func TestUseGoCollectorSwapsTheDefault(t *testing.T) {
	if names(t, prometheus.DefaultGatherer)["go_cpu_classes_gc_total_cpu_seconds_total"] {
		t.Fatal("the default registry had the GC series before UseGoCollector")
	}
	UseGoCollector()
	UseGoCollector()
	got := names(t, prometheus.DefaultGatherer)
	for _, n := range append(gcSeries, "go_goroutines", "go_memstats_heap_alloc_bytes") {
		if !got[n] {
			t.Errorf("default registry: no %s", n)
		}
	}
}
