package crawler

import (
	"log/slog"
	"sync"
	"sync/atomic"
)

// Progress tracks crawl progress in memory using lock-free primitives.
//
// Concurrency model:
//   - completed: sync.Map — 50 workers write disjoint DIDs, main goroutine reads during FilterPage
//   - failedCounts: pre-initialized atomic counters — workers increment, no reads on hot path
//   - cursor: only touched by the main goroutine — no synchronization needed
//   - segmentNum: atomic.Int32 — writer goroutine writes, main reads at startup
type Progress struct {
	completed    sync.Map
	failedCounts map[string]*atomic.Int64
	cursor       string
	segmentNum   atomic.Int32
	logger       *slog.Logger
}

func NewProgress(logger *slog.Logger) *Progress {
	categories := []string{
		"not_found", "deactivated", "takendown", "rate_limited",
		"parse_error", "http_error", "dns_error", "unavailable",
		"connection_error", "timeout", "unknown",
	}
	counts := make(map[string]*atomic.Int64, len(categories))
	for _, c := range categories {
		counts[c] = &atomic.Int64{}
	}

	p := &Progress{
		failedCounts: counts,
		logger:       logger.With("component", "progress"),
	}
	p.segmentNum.Store(1)
	return p
}

func (p *Progress) MarkCompleted(did string) {
	p.completed.Store(did, struct{}{})
	didsProcessedTotal.Inc()
}

// MarkFailed increments the failure counter for the given category.
// Permanent failures are also added to the completed set so FilterPage skips them.
func (p *Progress) MarkFailed(did string, category string) {
	if counter, ok := p.failedCounts[category]; ok {
		counter.Add(1)
	}

	switch category {
	case "not_found", "deactivated", "takendown", "parse_error":
		p.completed.Store(did, struct{}{})
	}

	didsProcessedTotal.Inc()
}

// FilterPage removes already-completed DIDs from the pdsGroups map in place.
// Returns the count of DIDs filtered out.
func (p *Progress) FilterPage(pdsGroups map[string][]string) int {
	filtered := 0
	for pds, dids := range pdsGroups {
		var remaining []string
		for _, did := range dids {
			if _, ok := p.completed.Load(did); ok {
				filtered++
			} else {
				remaining = append(remaining, did)
			}
		}
		if len(remaining) == 0 {
			delete(pdsGroups, pds)
		} else {
			pdsGroups[pds] = remaining
		}
	}
	return filtered
}

func (p *Progress) GetCursor() string {
	return p.cursor
}

func (p *Progress) SetCursor(cursor string) {
	p.cursor = cursor
}

func (p *Progress) ClearCursor() {
	p.cursor = ""
}

func (p *Progress) GetSegmentNum() int {
	return int(p.segmentNum.Load())
}

func (p *Progress) SetSegmentNum(n int) {
	p.segmentNum.Store(int32(n))
}
