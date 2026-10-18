package ops

import (
	"regexp"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// GoRuntimeMetrics picks the runtime/metrics the Go collector exports on top
// of its memstats defaults: what tells a GC running back to back from a busy
// service. The live heap at or over GOMEMLIMIT makes the GC run cycle after
// cycle and free nothing, its idle mark workers taking every P. roles/monitoring's
// go-runtime-alerts.yml reads these names:
//
//	go_cpu_classes_{gc_total,gc_mark_assist,gc_mark_idle,idle,total,user}_cpu_seconds_total
//	go_gc_cycles_total_gc_cycles_total
//	go_gc_heap_live_bytes, go_gc_heap_goal_bytes
//	go_gc_gomemlimit_bytes, go_gc_gogc_percent, go_sched_gomaxprocs_threads
//
// The CPU classes are the runtime's estimates, comparable only with each
// other: the GC's share of the CPU the process used is gc_total over
// total minus idle.
var GoRuntimeMetrics = regexp.MustCompile(`^/(` +
	`cpu/classes/(gc/total|gc/mark/assist|gc/mark/idle|idle|total|user):cpu-seconds|` +
	`gc/cycles/total:gc-cycles|gc/heap/(live|goal):bytes|gc/gomemlimit:bytes|gc/gogc:percent|` +
	`sched/gomaxprocs:threads)$`)

// GoCollector is client_golang's Go collector with GoRuntimeMetrics added.
func GoCollector() prometheus.Collector {
	return collectors.NewGoCollector(collectors.WithGoCollectorRuntimeMetrics(
		collectors.GoRuntimeMetricsRule{Matcher: GoRuntimeMetrics}))
}

var defaultOnce sync.Once

// UseGoCollector swaps the default registry's Go collector for GoCollector,
// for services that serve promhttp.Handler(). Calls after the first do
// nothing.
func UseGoCollector() {
	defaultOnce.Do(func() {
		prometheus.Unregister(collectors.NewGoCollector())
		prometheus.MustRegister(GoCollector())
	})
}
