package crawler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jazware/bsky-experiments/pkg/crawler/carrepo"
	"github.com/jazware/bsky-experiments/pkg/repoarchive"
	"github.com/klauspost/compress/zstd"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type WorkerPool struct {
	workQueue    chan *CrawlTask
	repoQueue    chan *repoarchive.SerializedRepo
	crawledQueue chan *repoarchive.CrawledRepo
	progress     *Progress
	dispatcher   *Dispatcher
	outcomes     *OutcomeLog
	config       Config
	logger       *slog.Logger
	client       *http.Client
	encoderPool  sync.Pool
	wg           sync.WaitGroup
	cancel       context.CancelFunc
}

func NewWorkerPool(
	parent context.Context,
	numWorkers int,
	repoQueue chan *repoarchive.SerializedRepo,
	crawledQueue chan *repoarchive.CrawledRepo,
	progress *Progress,
	dispatcher *Dispatcher,
	outcomes *OutcomeLog,
	config Config,
	logger *slog.Logger,
) *WorkerPool {
	// Cancelling the crawl aborts fetches in flight, so a shutdown doesn't
	// wait on a hung PDS and the segment gets its index.
	ctx, cancel := context.WithCancel(parent)

	wp := &WorkerPool{
		workQueue:    make(chan *CrawlTask, 1000),
		repoQueue:    repoQueue,
		crawledQueue: crawledQueue,
		progress:     progress,
		dispatcher:   dispatcher,
		outcomes:     outcomes,
		config:       config,
		logger:       logger,
		client: &http.Client{
			Timeout: 20 * time.Minute,
			Transport: otelhttp.NewTransport(&http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				ForceAttemptHTTP2:     true,
				MaxIdleConns:          numWorkers + 10,
				MaxIdleConnsPerHost:   numWorkers,
				MaxConnsPerHost:       numWorkers,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
				DialContext: (&net.Dialer{
					Timeout:   30 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
			}),
		},
		encoderPool: sync.Pool{
			New: func() any {
				enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.EncoderLevel(config.ZstdLevel)))
				if err != nil {
					panic("creating zstd encoder: " + err.Error())
				}
				return enc
			},
		},
		cancel: cancel,
	}

	wp.wg.Add(numWorkers)
	for i := range numWorkers {
		go wp.worker(ctx, i)
	}

	return wp
}

func (wp *WorkerPool) WorkQueue() chan<- *CrawlTask {
	return wp.workQueue
}

func (wp *WorkerPool) Stop() {
	close(wp.workQueue)
	wp.wg.Wait()
}

func (wp *WorkerPool) worker(ctx context.Context, id int) {
	defer wp.wg.Done()
	logger := wp.logger.With("worker", id)

	for task := range wp.workQueue {
		if ctx.Err() != nil {
			return
		}
		wp.processTask(ctx, logger, task)
	}
}

func (wp *WorkerPool) processTask(ctx context.Context, logger *slog.Logger, task *CrawlTask) {
	defer wp.dispatcher.Done(task)
	ctx, span := tracer.Start(ctx, "crawler.processRepo")
	defer span.End()

	span.SetAttributes(
		attribute.String("repo.did", task.DID),
		attribute.String("repo.pds", task.PDS),
	)

	start := time.Now()

	crawled, recordCount, collectionCount, err := wp.fetchAndParseRepo(ctx, logger, task)
	duration := time.Since(start).Seconds()
	repoDurationSeconds.Observe(duration)

	if err != nil {
		wp.handleError(ctx, logger, task, err)
		return
	}

	wp.dispatcher.ReportSuccess(task.PDS)

	span.SetAttributes(
		attribute.String("repo.rev", crawled.Rev),
		attribute.Int("repo.record.count", recordCount),
		attribute.Int("repo.collection.count", collectionCount),
	)

	repoRecords.Observe(float64(recordCount))
	repoCollections.Observe(float64(collectionCount))

	success := Outcome{DID: task.DID, PDS: task.PDS, Result: "success", Records: recordCount,
		Collections: collectionCount, CrawledAt: crawled.CrawledAt, Attempts: task.Attempts + 1}

	if wp.config.Direct {
		reposProcessedTotal.WithLabelValues("success").Inc()
		select {
		case wp.crawledQueue <- crawled:
			wp.progress.MarkCompleted(task.DID)
			wp.outcomes.Record(success)
		case <-ctx.Done():
			return
		}
	} else {
		enc := wp.encoderPool.Get().(*zstd.Encoder)
		sr, err := repoarchive.SerializeRepoBlock(enc, crawled)
		wp.encoderPool.Put(enc)
		if err != nil {
			logger.Error("failed to serialize repo", "did", task.DID, "error", err)
			reposProcessedTotal.WithLabelValues("serialize_error").Inc()
			wp.outcomes.Record(Outcome{DID: task.DID, PDS: task.PDS, Result: "serialize_error",
				Attempts: task.Attempts + 1, Message: err.Error()})
			return
		}

		reposProcessedTotal.WithLabelValues("success").Inc()

		select {
		case wp.repoQueue <- sr:
			wp.progress.MarkCompleted(task.DID)
			wp.outcomes.Record(success)
		case <-ctx.Done():
			return
		}
	}
}

func (wp *WorkerPool) fetchAndParseRepo(ctx context.Context, logger *slog.Logger, task *CrawlTask) (*repoarchive.CrawledRepo, int, int, error) {
	url := fmt.Sprintf("%s/xrpc/com.atproto.sync.getRepo?did=%s", task.PDS, task.DID)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.ipld.car")
	req.Header.Set("User-Agent", "jaz-crawler/1.0")

	// The repo is the PDS's state at some point after this, so rows stamped
	// with it never outrank a firehose event the snapshot doesn't include.
	httpStart := time.Now()
	resp, err := wp.client.Do(req)
	httpDuration := time.Since(httpStart).Seconds()
	httpRequestDurationSeconds.Observe(httpDuration)

	if err != nil {
		return nil, 0, 0, classifyNetworkError(err, task)
	}
	defer resp.Body.Close()

	httpRequestsTotal.WithLabelValues(strconv.Itoa(resp.StatusCode)).Inc()
	trace.SpanFromContext(ctx).SetAttributes(attribute.Int("http.status_code", resp.StatusCode))

	if resp.StatusCode != http.StatusOK {
		return nil, 0, 0, statusError(resp, task, time.Now())
	}

	maxBytes := int64(wp.config.MaxRepoSizeMB) * 1024 * 1024
	body := &countingReader{r: io.LimitReader(resp.Body, maxBytes+1)}

	r, err := carrepo.ParseRepo(body)
	if err != nil {
		// The cap cuts the CAR off mid-block, which reads as a bad CAR.
		if body.n > maxBytes {
			return nil, 0, 0, &RepoError{Category: "too_large", DID: task.DID,
				Err: fmt.Errorf("repo is over --max-repo-size-mb %d", wp.config.MaxRepoSizeMB)}
		}
		return nil, 0, 0, &RepoError{Category: "parse_error", DID: task.DID, Err: fmt.Errorf("reading CAR: %w", err)}
	}

	rev := r.Rev

	collections := make(map[string][]repoarchive.Record, 16)
	recordCount := 0

	err = r.ForEach(func(path string, raw []byte) error {
		parts := strings.SplitN(path, "/", 2)
		if len(parts) != 2 {
			recordsDroppedTotal.WithLabelValues("invalid_path").Inc()
			return nil
		}
		collection := parts[0]
		rkey := parts[1]

		jsonBytes, err := cborToStrippedJSON(raw)
		if err != nil {
			recordsDroppedTotal.WithLabelValues("cbor_decode").Inc()
			logger.Warn("cbor decode failed, skipping record",
				"did", task.DID, "collection", collection, "rkey", rkey, "error", err)
			return nil
		}

		collections[collection] = append(collections[collection], repoarchive.Record{
			RKey: rkey,
			JSON: jsonBytes,
		})
		recordCount++
		return nil
	})
	if err != nil {
		return nil, 0, 0, &RepoError{Category: "parse_error", DID: task.DID, Err: fmt.Errorf("ForEach: %w", err)}
	}

	return &repoarchive.CrawledRepo{
		DID:         task.DID,
		PDS:         task.PDS,
		Rev:         rev,
		CrawledAt:   httpStart,
		Collections: collections,
	}, recordCount, len(collections), nil
}

// classifyNetworkError categorizes a network-level error from http.Client.Do.
// All errors here are PDS-level (no HTTP response was received).
func classifyNetworkError(err error, task *CrawlTask) *RepoError {
	category := "connection_error"
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		category = "dns_error"
	} else if os.IsTimeout(err) || errors.Is(err, context.DeadlineExceeded) {
		category = "timeout"
	}
	return &RepoError{Category: category, DID: task.DID, PDS: task.PDS, Err: err}
}

type RepoError struct {
	Code       int
	Category   string // "not_found", "deactivated", "takendown", "rate_limited", "http_error", "dns_error", "unavailable", "parse_error", "timeout"
	DID        string
	PDS        string
	Err        error
	RetryAfter time.Duration // rate_limited only
	XRPCError  string        // the response body's "error"
	XRPCMsg    string        // and its "message"
}

// statusError classifies a non-200 getRepo response by its status and the
// XRPC error in its body.
func statusError(resp *http.Response, task *CrawlTask, now time.Time) *RepoError {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var x struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &x) != nil {
		x.Message = strings.TrimSpace(string(body))
	}
	e := &RepoError{Code: resp.StatusCode, DID: task.DID, PDS: task.PDS, XRPCError: x.Error, XRPCMsg: x.Message,
		Err: fmt.Errorf("%s: %s: %s", resp.Status, x.Error, x.Message)}

	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		e.Category = "rate_limited"
		e.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"), now)
	case x.Error == "RepoTakendown":
		e.Category = "takendown"
	case x.Error == "RepoDeactivated", x.Error == "RepoSuspended", resp.StatusCode == http.StatusGone:
		e.Category = "deactivated"
	case x.Error == "RepoNotFound", resp.StatusCode == http.StatusNotFound:
		e.Category = "not_found"
	case resp.StatusCode >= 500:
		e.Category = "unavailable"
	case resp.StatusCode == http.StatusBadRequest && strings.Contains(x.Message+x.Error, "NotFound"):
		e.Category = "not_found"
	default:
		e.Category = "http_error"
	}
	return e
}

var (
	repo5xxBackoff   = 30 * time.Second
	hostErrorBackoff = 10 * time.Second
)

const (
	defaultRetryAfter = 10 * time.Second
	// rateLimitAttempts is how often a DID may be fetched while its PDS keeps
	// answering 429: the PDS pauses each time, so this bounds the wait, not load.
	rateLimitAttempts = 10
	repo5xxRetries    = 2
)

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// parseRetryAfter reads a Retry-After header (seconds or an HTTP date).
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return defaultRetryAfter
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return time.Second
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > time.Second {
			return d
		}
		return time.Second
	}
	return defaultRetryAfter
}

// retryDelay says whether a failure is worth another attempt, given how many
// fetches of the DID have been made including this one, and how long to wait.
func retryDelay(category string, attempts, maxRetries int) (time.Duration, bool) {
	switch category {
	case "rate_limited":
		// The PDS pauses for Retry-After; the DID goes when it resumes.
		return 0, attempts < rateLimitAttempts
	case "unavailable":
		// A 5xx is usually the repo's own state; two more tries, spaced out.
		return repo5xxBackoff << (attempts - 1), attempts <= repo5xxRetries
	case "timeout", "connection_error", "dns_error":
		return hostErrorBackoff << (attempts - 1), attempts <= maxRetries
	case "parse_error", "unknown":
		// A body cut off mid-download reads as a bad CAR; one more try.
		return hostErrorBackoff, attempts < 2
	default:
		return 0, false
	}
}

func (e *RepoError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s (did=%s): %v", e.Category, e.DID, e.Err)
	}
	return fmt.Sprintf("%s (did=%s, code=%d)", e.Category, e.DID, e.Code)
}

func (e *RepoError) Unwrap() error {
	return e.Err
}

// isHostError reports whether a failure says something about the host rather
// than the repo. 5xx don't: PDSes return them for repos they can't serve.
func isHostError(category string) bool {
	switch category {
	case "dns_error", "timeout", "connection_error":
		return true
	default:
		return false
	}
}

func (wp *WorkerPool) handleError(ctx context.Context, logger *slog.Logger, task *CrawlTask, err error) {
	if ctx.Err() != nil {
		return // Shutting down, don't report errors.
	}

	repoErr, ok := err.(*RepoError)
	if !ok {
		repoErr = &RepoError{Category: "unknown", DID: task.DID, PDS: task.PDS, Err: err}
	}
	category := repoErr.Category
	errStr := ""
	if repoErr.Err != nil {
		errStr = repoErr.Err.Error()
	}

	reposProcessedTotal.WithLabelValues(category).Inc()

	switch {
	case category == "rate_limited":
		wp.dispatcher.ReportRateLimited(task.PDS, repoErr.RetryAfter)
	case isHostError(category):
		wp.dispatcher.ReportHostError(task.PDS, errStr)
	}

	attempts := task.Attempts + 1
	if delay, ok := retryDelay(category, attempts, wp.config.MaxRetries); ok {
		retriesTotal.WithLabelValues(category).Inc()
		logger.Debug("requeueing repo", "did", task.DID, "pds", task.PDS, "category", category,
			"attempts", attempts, "delay", delay)
		wp.dispatcher.Requeue(&CrawlTask{DID: task.DID, PDS: task.PDS, Attempts: attempts, ReadyAt: time.Now().Add(delay)})
		return
	}

	if category == "unavailable" {
		wp.dispatcher.ReportRepo5xx(task.PDS, errStr)
	}

	switch category {
	case "not_found", "deactivated", "takendown":
		logger.Debug("skipping repo", "did", task.DID, "reason", category)
	default:
		logger.Warn("crawl error", "did", task.DID, "pds", task.PDS, "category", category,
			"attempts", attempts, "error", errStr)
	}
	wp.progress.MarkFailed(task.DID, category)
	msg := repoErr.XRPCMsg
	if msg == "" && repoErr.XRPCError == "" {
		msg = errStr
	}
	wp.outcomes.Record(Outcome{DID: task.DID, PDS: task.PDS, Result: category, Attempts: attempts,
		Status: repoErr.Code, XRPCError: repoErr.XRPCError, Message: msg})
}
