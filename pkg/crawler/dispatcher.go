package crawler

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"golang.org/x/time/rate"
)

const pdsBlockThreshold = 10

type CrawlTask struct {
	DID string
	PDS string
}

type PDSState struct {
	URL     string
	Limiter *rate.Limiter
	DIDs    []string
	Cursor  int  // index of next DID to dispatch
	Errors  int  // consecutive errors (for adaptive backoff)
	Blocked bool // true when PDS is unreachable
}

type pdsReport struct {
	PDS     string
	Success bool
}

// Dispatcher runs continuously, receiving pages of work via Feed() and
// dispatching CrawlTasks to a work queue with per-PDS rate limiting.
// Workers report PDS health via ReportResult(); after enough consecutive
// failures a PDS is blocked and its remaining DIDs are skipped.
type Dispatcher struct {
	pdsIndex   map[string]*PDSState
	defaultRPS float64
	feedCh     chan map[string][]string
	reportCh   chan pdsReport
	done       chan struct{}
	remaining  atomic.Int64
	activePDS  atomic.Int32
	logger     *slog.Logger
}

func NewDispatcher(defaultRPS float64, logger *slog.Logger) *Dispatcher {
	return &Dispatcher{
		pdsIndex:   make(map[string]*PDSState),
		defaultRPS: defaultRPS,
		feedCh:     make(chan map[string][]string, 2),
		reportCh:   make(chan pdsReport, 1000),
		done:       make(chan struct{}),
		logger:     logger,
	}
}

// Feed sends a page of work to the dispatcher. It blocks if the dispatcher's
// internal buffer is full (backpressure). Safe to call from another goroutine.
func (d *Dispatcher) Feed(pdsGroups map[string][]string) {
	d.feedCh <- pdsGroups
}

func (d *Dispatcher) Stop() {
	close(d.done)
}

func (d *Dispatcher) Remaining() int64 {
	return d.remaining.Load()
}

func (d *Dispatcher) ActivePDSCount() int32 {
	return d.activePDS.Load()
}

// ReportResult reports a PDS success or failure from a worker goroutine.
// Thread-safe — sends on a buffered channel processed by the dispatcher loop.
func (d *Dispatcher) ReportResult(pds string, success bool) {
	select {
	case d.reportCh <- pdsReport{PDS: pds, Success: success}:
	default:
		// Channel full, drop report. Dispatcher will catch up.
	}
}

// Run dispatches CrawlTasks to the workQueue continuously until Stop() is
// called or the context is cancelled. It receives pages via Feed().
func (d *Dispatcher) Run(ctx context.Context, workQueue chan<- *CrawlTask) error {
	var active []*PDSState

	snapshotTicker := time.NewTicker(30 * time.Second)
	defer snapshotTicker.Stop()

	for {
		d.drain(&active)
		d.processReports(&active)

		select {
		case <-snapshotTicker.C:
			d.emitQueueSnapshot(ctx, active)
		default:
		}

		if len(active) == 0 {
			// No work — block until we get some, or are told to stop.
			select {
			case groups := <-d.feedCh:
				d.ingest(groups, &active)
			case report := <-d.reportCh:
				d.handleReport(report, &active)
			case <-d.done:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		roundDispatched := false
		i := 0
		for i < len(active) {
			pds := active[i]

			// Check if PDS was blocked by error reports.
			if pds.Blocked {
				d.skipBlockedPDS(pds)
				active[i] = active[len(active)-1]
				active = active[:len(active)-1]
				continue
			}

			if pds.Cursor >= len(pds.DIDs) {
				// Exhausted — swap-remove.
				active[i] = active[len(active)-1]
				active = active[:len(active)-1]
				continue
			}

			if !pds.Limiter.Allow() {
				i++
				continue
			}

			did := pds.DIDs[pds.Cursor]
			pds.Cursor++

			select {
			case workQueue <- &CrawlTask{DID: did, PDS: pds.URL}:
				roundDispatched = true
				d.remaining.Add(-1)
				dispatchedTotal.Inc()
			case <-ctx.Done():
				return ctx.Err()
			}
			i++
		}

		d.activePDS.Store(int32(len(active)))
		dispatchRemaining.Set(float64(d.remaining.Load()))
		activePDSGauge.Set(float64(len(active)))

		if !roundDispatched && len(active) > 0 {
			// All active PDSs are rate-limited; brief pause.
			// Also check for new work, reports, or stop signals.
			select {
			case <-time.After(10 * time.Millisecond):
			case groups := <-d.feedCh:
				d.ingest(groups, &active)
			case report := <-d.reportCh:
				d.handleReport(report, &active)
			case <-d.done:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

func (d *Dispatcher) processReports(active *[]*PDSState) {
	for {
		select {
		case report := <-d.reportCh:
			d.handleReport(report, active)
		default:
			return
		}
	}
}

func (d *Dispatcher) handleReport(report pdsReport, _ *[]*PDSState) {
	pds, ok := d.pdsIndex[report.PDS]
	if !ok {
		return
	}

	if report.Success {
		if pds.Errors > 0 {
			pds.Errors = 0
			pds.Limiter.SetLimit(rate.Limit(pdsRate(pds.URL, len(pds.DIDs), d.defaultRPS)))
		}
		return
	}

	pds.Errors++
	if pds.Errors >= pdsBlockThreshold && !pds.Blocked {
		pds.Blocked = true
		d.logger.Warn("blocking PDS due to consecutive failures",
			"pds", pds.URL, "errors", pds.Errors)
		pdsBlockedTotal.Inc()
	} else if pds.Errors%5 == 0 {
		currentRate := float64(pds.Limiter.Limit())
		newRate := currentRate / 2
		if newRate < 0.5 {
			newRate = 0.5
		}
		pds.Limiter.SetLimit(rate.Limit(newRate))
	}
}

func (d *Dispatcher) skipBlockedPDS(pds *PDSState) {
	remaining := len(pds.DIDs) - pds.Cursor
	if remaining > 0 {
		d.logger.Info("skipping remaining DIDs for blocked PDS",
			"pds", pds.URL, "skipped", remaining)
		d.remaining.Add(-int64(remaining))
		pds.Cursor = len(pds.DIDs)
	}
}

// pdsRate returns the appropriate request rate for a PDS based on its URL
// and how many DIDs it has. Bluesky infrastructure PDSs get a fixed 10 RPS;
// small self-hosted PDSs get a low rate to be polite.
func pdsRate(pdsURL string, didCount int, defaultRPS float64) float64 {
	if strings.HasSuffix(pdsURL, ".host.bsky.network") {
		return 10
	}
	switch {
	case didCount < 100:
		return 2
	case didCount < 1000:
		return defaultRPS
	default:
		return 10
	}
}

func (d *Dispatcher) drain(active *[]*PDSState) {
	for {
		select {
		case groups := <-d.feedCh:
			d.ingest(groups, active)
		default:
			return
		}
	}
}

type pdsQueueEntry struct {
	URL       string
	Remaining int
	Errors    int
	Blocked   bool
	RPS       float64
}

func (d *Dispatcher) emitQueueSnapshot(ctx context.Context, active []*PDSState) {
	_, span := tracer.Start(ctx, "dispatcher.queueSnapshot")
	defer span.End()

	entries := make([]pdsQueueEntry, 0, len(active))
	for _, pds := range active {
		remaining := len(pds.DIDs) - pds.Cursor
		if remaining <= 0 {
			continue
		}
		entries = append(entries, pdsQueueEntry{
			URL:       pds.URL,
			Remaining: remaining,
			Errors:    pds.Errors,
			Blocked:   pds.Blocked,
			RPS:       float64(pds.Limiter.Limit()),
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Remaining > entries[j].Remaining
	})

	span.SetAttributes(
		attribute.Int64("queue.total_remaining", d.remaining.Load()),
		attribute.Int("queue.active_pds", len(entries)),
	)

	limit := min(len(entries), 50)
	for i, e := range entries[:limit] {
		prefix := fmt.Sprintf("queue.pds.%d", i)
		span.SetAttributes(
			attribute.String(prefix+".url", e.URL),
			attribute.Int(prefix+".remaining", e.Remaining),
			attribute.Int(prefix+".errors", e.Errors),
			attribute.Bool(prefix+".blocked", e.Blocked),
			attribute.Float64(prefix+".rps", e.RPS),
		)
	}
}

func (d *Dispatcher) ingest(groups map[string][]string, active *[]*PDSState) {
	newDIDs := 0
	for pdsURL, dids := range groups {
		if existing, ok := d.pdsIndex[pdsURL]; ok {
			if existing.Blocked {
				d.logger.Debug("skipping new DIDs for blocked PDS",
					"pds", pdsURL, "count", len(dids))
				continue
			}
			existing.DIDs = append(existing.DIDs, dids...)
			newDIDs += len(dids)
			if !slices.Contains(*active, existing) {
				*active = append(*active, existing)
			}
		} else {
			rps := pdsRate(pdsURL, len(dids), d.defaultRPS)
			burst := max(int(rps), 1)
			state := &PDSState{
				URL:     pdsURL,
				Limiter: rate.NewLimiter(rate.Limit(rps), burst),
				DIDs:    dids,
			}
			d.pdsIndex[pdsURL] = state
			*active = append(*active, state)
			newDIDs += len(dids)
		}
	}
	d.remaining.Add(int64(newDIDs))
	pdsCount.Set(float64(len(d.pdsIndex)))
}
