package crawler

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"golang.org/x/time/rate"
)

// How a PDS is treated when fetches fail. A 5xx is the repo's failure: PDSes
// answer 5xx for repos they can't serve, so a run of them says nothing about
// the host. Only host signals (429, connection, DNS, TLS, timeouts) slow or
// pause a host, and a paused host keeps its DIDs. The February 2026 crawl
// blocked hosts on 10 straight 5xx and dropped their remaining DIDs, which
// lost 3M repos on 7 bsky.network PDSes.
//
// The exception is a host that has never served a repo this run and isn't
// bsky-operated: 10 different repos in a row failing with 5xx (each counted
// once, after its own retries), or 2 h of host errors, block it, and its
// remaining DIDs are recorded as pds_blocked for a later run.
const (
	hostErrorPauseThreshold = 10
	hostPauseBase           = time.Minute
	hostPauseMax            = 15 * time.Minute
	maxRetryAfter           = 10 * time.Minute
	neverServed5xxRepos     = 10
	neverServedDownAfter    = 2 * time.Hour
	minPDSRPS               = 0.5
)

// maxInFlight caps a PDS's fetches in flight, so hosts that hang until a
// timeout can't hold every worker and stall the rest of the crawl.
func maxInFlight(rps float64) int32 {
	return int32(max(2, int(rps*2)))
}

type CrawlTask struct {
	DID      string
	PDS      string
	Attempts int       // fetches of this DID before this one
	ReadyAt  time.Time // a retry isn't dispatched before this

	state *PDSState // set at dispatch
}

type PDSState struct {
	URL     string
	Bsky    bool
	Limiter *rate.Limiter
	BaseRPS float64
	DIDs    []string
	Cursor  int          // index of next DID to dispatch
	Delayed []*CrawlTask // retries, by ReadyAt

	Served       bool // a repo was fetched from it this run
	Failed5xx    int  // repos in a row whose retries ended in 5xx, while !Served
	HostErrors   int  // consecutive host errors, for backoff
	Pauses       int  // pauses since the last success
	PausedUntil  time.Time
	FailingSince time.Time // first host error since the last success

	Blocked     bool
	BlockReason string

	inFlight atomic.Int32 // dispatched, not yet Done
}

func (p *PDSState) remaining() int {
	return len(p.DIDs) - p.Cursor + len(p.Delayed)
}

type reportKind int

const (
	reportSuccess reportKind = iota
	reportRepo5xx
	reportHostError
	reportRateLimited
)

type pdsReport struct {
	PDS        string
	Kind       reportKind
	RetryAfter time.Duration
	Err        string
}

// Dispatcher runs continuously, receiving pages of work via Feed() and
// dispatching CrawlTasks to a work queue with per-PDS rate limiting.
// Workers report each fetch's result per PDS and requeue retries.
type Dispatcher struct {
	pdsIndex   map[string]*PDSState
	defaultRPS float64
	maxRPS     float64
	feedCh     chan map[string][]string
	reportCh   chan pdsReport
	done       chan struct{}
	remaining  atomic.Int64
	inFlight   atomic.Int64
	activePDS  atomic.Int32
	logger     *slog.Logger

	// Requeues are never dropped, and a worker must not block on the
	// dispatcher while the dispatcher blocks on the work queue.
	requeueMu sync.Mutex
	requeued  []*CrawlTask

	// onBlocked records each DID a blocked PDS leaves undone.
	onBlocked func(task *CrawlTask, reason string)

	now func() time.Time
}

func NewDispatcher(defaultRPS, maxRPS float64, logger *slog.Logger) *Dispatcher {
	return &Dispatcher{
		pdsIndex:   make(map[string]*PDSState),
		defaultRPS: defaultRPS,
		maxRPS:     maxRPS,
		feedCh:     make(chan map[string][]string, 2),
		reportCh:   make(chan pdsReport, 10000),
		done:       make(chan struct{}),
		logger:     logger,
		now:        time.Now,
	}
}

// Feed sends a page of work to the dispatcher. It blocks if the dispatcher's
// internal buffer is full (backpressure). Safe to call from another goroutine.
func (d *Dispatcher) Feed(pdsGroups map[string][]string) {
	// Counted before the send so Idle can't see the page in neither place.
	n := 0
	for _, dids := range pdsGroups {
		n += len(dids)
	}
	d.remaining.Add(int64(n))
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

// Requeue puts a task back on its PDS's queue for another attempt at
// task.ReadyAt. Call it before Done for the same task.
func (d *Dispatcher) Requeue(task *CrawlTask) {
	d.remaining.Add(1)
	d.requeueMu.Lock()
	d.requeued = append(d.requeued, task)
	d.requeueMu.Unlock()
}

// Done marks a dispatched task finished (succeeded, failed or requeued).
func (d *Dispatcher) Done(task *CrawlTask) {
	if task.state != nil {
		task.state.inFlight.Add(-1)
	}
	d.inFlight.Add(-1)
}

// Idle is true when nothing is queued or in flight.
func (d *Dispatcher) Idle() bool {
	return d.remaining.Load() == 0 && d.inFlight.Load() == 0
}

func (d *Dispatcher) ReportSuccess(pds string) {
	d.report(pdsReport{PDS: pds, Kind: reportSuccess})
}

// ReportRepo5xx reports a repo whose retries all ended in 5xx: the repo's
// failure, not the host's, unless the host has never served a repo.
func (d *Dispatcher) ReportRepo5xx(pds, err string) {
	d.report(pdsReport{PDS: pds, Kind: reportRepo5xx, Err: err})
}

// ReportHostError reports a connection, DNS, TLS or timeout failure.
func (d *Dispatcher) ReportHostError(pds, err string) {
	d.report(pdsReport{PDS: pds, Kind: reportHostError, Err: err})
}

// ReportRateLimited reports a 429; the PDS pauses for retryAfter.
func (d *Dispatcher) ReportRateLimited(pds string, retryAfter time.Duration) {
	d.report(pdsReport{PDS: pds, Kind: reportRateLimited, RetryAfter: retryAfter})
}

func (d *Dispatcher) report(r pdsReport) {
	select {
	case d.reportCh <- r:
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
		d.processReports()
		d.drainRequeues(&active)

		select {
		case <-snapshotTicker.C:
			d.emitQueueSnapshot(ctx, active)
		default:
		}

		if len(active) == 0 {
			// No work — block until we get some, or are told to stop.
			// The timeout picks up requeues from in-flight tasks.
			select {
			case groups := <-d.feedCh:
				d.ingest(groups, &active)
			case report := <-d.reportCh:
				d.handleReport(report)
			case <-time.After(50 * time.Millisecond):
			case <-d.done:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		now := d.now()
		roundDispatched := false
		i := 0
		for i < len(active) {
			pds := active[i]

			if pds.Blocked {
				d.skipBlockedPDS(pds)
				active[i] = active[len(active)-1]
				active = active[:len(active)-1]
				continue
			}

			if pds.remaining() == 0 {
				// Exhausted — swap-remove. A requeue brings it back.
				active[i] = active[len(active)-1]
				active = active[:len(active)-1]
				continue
			}

			if now.Before(pds.PausedUntil) || !pds.ready(now) ||
				pds.inFlight.Load() >= maxInFlight(pds.BaseRPS) || !pds.Limiter.Allow() {
				i++
				continue
			}
			task := pds.next(now)
			task.state = pds

			pds.inFlight.Add(1)
			d.inFlight.Add(1)
			select {
			case workQueue <- task:
				roundDispatched = true
				d.remaining.Add(-1)
				dispatchedTotal.Inc()
			case <-ctx.Done():
				pds.inFlight.Add(-1)
				d.inFlight.Add(-1)
				return ctx.Err()
			}
			i++
		}

		d.activePDS.Store(int32(len(active)))
		dispatchRemaining.Set(float64(d.remaining.Load()))
		activePDSGauge.Set(float64(len(active)))

		if !roundDispatched && len(active) > 0 {
			// All active PDSs are rate-limited, paused or waiting on retries.
			select {
			case <-time.After(10 * time.Millisecond):
			case groups := <-d.feedCh:
				d.ingest(groups, &active)
			case report := <-d.reportCh:
				d.handleReport(report)
			case <-d.done:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

// ready is true when next would return a task.
func (p *PDSState) ready(now time.Time) bool {
	return p.Cursor < len(p.DIDs) || (len(p.Delayed) > 0 && !p.Delayed[0].ReadyAt.After(now))
}

// next takes a retry whose time has come, else the next fresh DID.
func (p *PDSState) next(now time.Time) *CrawlTask {
	if len(p.Delayed) > 0 && !p.Delayed[0].ReadyAt.After(now) {
		t := p.Delayed[0]
		p.Delayed = p.Delayed[1:]
		return t
	}
	if p.Cursor < len(p.DIDs) {
		t := &CrawlTask{DID: p.DIDs[p.Cursor], PDS: p.URL}
		p.Cursor++
		return t
	}
	return nil
}

func (p *PDSState) delay(t *CrawlTask) {
	i := sort.Search(len(p.Delayed), func(i int) bool { return p.Delayed[i].ReadyAt.After(t.ReadyAt) })
	p.Delayed = slices.Insert(p.Delayed, i, t)
}

func (d *Dispatcher) drainRequeues(active *[]*PDSState) {
	d.requeueMu.Lock()
	tasks := d.requeued
	d.requeued = nil
	d.requeueMu.Unlock()
	for _, t := range tasks {
		pds, ok := d.pdsIndex[t.PDS]
		if !ok {
			d.remaining.Add(-1)
			continue
		}
		if pds.Blocked {
			d.remaining.Add(-1)
			if d.onBlocked != nil {
				d.onBlocked(t, pds.BlockReason)
			}
			continue
		}
		pds.delay(t)
		if !slices.Contains(*active, pds) {
			*active = append(*active, pds)
		}
	}
}

func (d *Dispatcher) processReports() {
	for {
		select {
		case report := <-d.reportCh:
			d.handleReport(report)
		default:
			return
		}
	}
}

func (d *Dispatcher) handleReport(report pdsReport) {
	pds, ok := d.pdsIndex[report.PDS]
	if !ok || pds.Blocked {
		return
	}
	now := d.now()

	switch report.Kind {
	case reportSuccess:
		pds.Served = true
		pds.Failed5xx = 0
		pds.Pauses = 0
		pds.HostErrors = 0
		pds.FailingSince = time.Time{}
		if float64(pds.Limiter.Limit()) != pds.BaseRPS {
			pds.Limiter.SetLimit(rate.Limit(pds.BaseRPS))
		}

	case reportRepo5xx:
		if pds.Served || pds.Bsky {
			return
		}
		pds.Failed5xx++
		if pds.Failed5xx >= neverServed5xxRepos {
			d.block(pds, fmt.Sprintf("host never served a repo: %d repos in a row failed with 5xx, last %s",
				pds.Failed5xx, report.Err))
		}

	case reportRateLimited:
		until := now.Add(min(report.RetryAfter, maxRetryAfter))
		if until.After(pds.PausedUntil) {
			pds.PausedUntil = until
		}
		d.slowDown(pds)

	case reportHostError:
		if now.Before(pds.PausedUntil) {
			return // in flight when the pause began
		}
		if pds.FailingSince.IsZero() {
			pds.FailingSince = now
		}
		if !pds.Served && !pds.Bsky && now.Sub(pds.FailingSince) >= neverServedDownAfter {
			d.block(pds, fmt.Sprintf("host never served a repo: host errors for %s, last %s",
				now.Sub(pds.FailingSince).Round(time.Minute), report.Err))
			return
		}
		pds.HostErrors++
		if pds.HostErrors%5 == 0 {
			d.slowDown(pds)
		}
		// After a pause one failure is enough to pause again.
		if pds.HostErrors >= hostErrorPauseThreshold || pds.Pauses > 0 {
			pds.HostErrors = 0
			pds.Pauses++
			pause := min(hostPauseBase<<min(pds.Pauses-1, 10), hostPauseMax)
			pds.PausedUntil = now.Add(pause)
			d.logger.Warn("pausing PDS after host errors",
				"pds", pds.URL, "pause", pause, "pauses", pds.Pauses, "last_error", report.Err)
			pdsPausedTotal.Inc()
		}
	}
}

func (d *Dispatcher) slowDown(pds *PDSState) {
	pds.Limiter.SetLimit(rate.Limit(max(float64(pds.Limiter.Limit())/2, minPDSRPS)))
}

func (d *Dispatcher) block(pds *PDSState, reason string) {
	pds.Blocked = true
	pds.BlockReason = reason
	d.logger.Warn("blocking PDS for the rest of the run", "pds", pds.URL, "reason", reason, "remaining", pds.remaining())
	pdsBlockedTotal.Inc()
}

func (d *Dispatcher) skipBlockedPDS(pds *PDSState) {
	remaining := pds.remaining()
	if remaining == 0 {
		return
	}
	d.logger.Info("recording remaining DIDs for blocked PDS", "pds", pds.URL, "skipped", remaining)
	if d.onBlocked != nil {
		for i := pds.Cursor; i < len(pds.DIDs); i++ {
			d.onBlocked(&CrawlTask{DID: pds.DIDs[i], PDS: pds.URL}, pds.BlockReason)
		}
		for _, t := range pds.Delayed {
			d.onBlocked(t, pds.BlockReason)
		}
	}
	d.remaining.Add(-int64(remaining))
	pds.Cursor = len(pds.DIDs)
	pds.Delayed = nil
}

func isBskyHost(pdsURL string) bool {
	return strings.HasSuffix(pdsURL, ".bsky.network")
}

// pdsRate returns the appropriate request rate for a PDS based on its URL
// and how many DIDs it has, capped at maxRPS. Bluesky infrastructure PDSs get
// 10 RPS; small self-hosted PDSs get a low rate to be polite.
func pdsRate(pdsURL string, didCount int, defaultRPS, maxRPS float64) float64 {
	r := 10.0
	if !strings.HasSuffix(pdsURL, ".host.bsky.network") {
		switch {
		case didCount < 100:
			r = 2
		case didCount < 1000:
			r = defaultRPS
		}
	}
	if maxRPS > 0 {
		r = min(r, maxRPS)
	}
	return r
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
		remaining := pds.remaining()
		if remaining <= 0 {
			continue
		}
		entries = append(entries, pdsQueueEntry{
			URL:       pds.URL,
			Remaining: remaining,
			Errors:    pds.HostErrors,
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
	for pdsURL, dids := range groups {
		if existing, ok := d.pdsIndex[pdsURL]; ok {
			if existing.Blocked {
				if d.onBlocked != nil {
					for _, did := range dids {
						d.onBlocked(&CrawlTask{DID: did, PDS: pdsURL}, existing.BlockReason)
					}
				}
				d.remaining.Add(-int64(len(dids)))
				continue
			}
			existing.DIDs = append(existing.DIDs, dids...)
			if !slices.Contains(*active, existing) {
				*active = append(*active, existing)
			}
		} else {
			rps := pdsRate(pdsURL, len(dids), d.defaultRPS, d.maxRPS)
			state := &PDSState{
				URL:     pdsURL,
				Bsky:    isBskyHost(pdsURL),
				Limiter: rate.NewLimiter(rate.Limit(rps), 1),
				BaseRPS: rps,
				DIDs:    dids,
			}
			d.pdsIndex[pdsURL] = state
			*active = append(*active, state)
		}
	}
	pdsCount.Set(float64(len(d.pdsIndex)))
}
