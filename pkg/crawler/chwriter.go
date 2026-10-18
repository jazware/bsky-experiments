package crawler

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/jazware/bsky-experiments/pkg/repoarchive"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	chWriterRecordsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "crawler_chwriter_records_total",
		Help: "Total records sent to ClickHouse in direct mode.",
	})

	chWriterBatchesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "crawler_chwriter_batches_total",
		Help: "Batches sent to ClickHouse in direct mode.",
	}, []string{"result"})

	chWriterBatchSizeHist = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "crawler_chwriter_batch_size",
		Help:    "Records per batch sent to ClickHouse in direct mode.",
		Buckets: []float64{1000, 10000, 50000, 100000, 250000, 500000},
	})
)

const (
	chBatchLimit    = 500_000
	chFlushInterval = 10 * time.Second
	chLogInterval   = 30 * time.Second
)

type chBatch struct {
	repos       []string
	collections []string
	rkeys       []string
	recordJSONs []string
	crawledAts  []time.Time
	timeUSs     []int64
}

var chBatchPool = sync.Pool{
	New: func() any {
		return &chBatch{
			repos:       make([]string, 0, chBatchLimit),
			collections: make([]string, 0, chBatchLimit),
			rkeys:       make([]string, 0, chBatchLimit),
			recordJSONs: make([]string, 0, chBatchLimit),
			crawledAts:  make([]time.Time, 0, chBatchLimit),
			timeUSs:     make([]int64, 0, chBatchLimit),
		}
	},
}

func (b *chBatch) len() int {
	return len(b.repos)
}

func (b *chBatch) reset() {
	b.repos = b.repos[:0]
	b.collections = b.collections[:0]
	b.rkeys = b.rkeys[:0]
	b.recordJSONs = b.recordJSONs[:0]
	b.crawledAts = b.crawledAts[:0]
	b.timeUSs = b.timeUSs[:0]
}

func (b *chBatch) append(repo, collection, rkey, recordJSON string, crawledAt time.Time) {
	b.repos = append(b.repos, repo)
	b.collections = append(b.collections, collection)
	b.rkeys = append(b.rkeys, rkey)
	b.recordJSONs = append(b.recordJSONs, recordJSON)
	b.crawledAts = append(b.crawledAts, crawledAt)
	b.timeUSs = append(b.timeUSs, crawledAt.UnixMicro())
}

type CHWriter struct {
	conn   driver.Conn
	input  <-chan *repoarchive.CrawledRepo
	logger *slog.Logger

	sent atomic.Int64
	err  error
	done chan struct{}
}

func NewCHWriter(conn driver.Conn, input <-chan *repoarchive.CrawledRepo, logger *slog.Logger) *CHWriter {
	return &CHWriter{
		conn:   conn,
		input:  input,
		logger: logger.With("component", "chwriter"),
		done:   make(chan struct{}),
	}
}

func (w *CHWriter) Run(ctx context.Context) {
	defer close(w.done)

	// Pipeline: accumulator fills batches, sender goroutine ships them.
	// Buffer of 1 lets the next batch accumulate while the current one sends.
	sendCh := make(chan *chBatch, 1)
	sendDone := make(chan struct{})

	go func() {
		defer close(sendDone)
		for b := range sendCh {
			if err := w.sendBatch(ctx, b); err != nil {
				w.err = err
				for range sendCh {
				}
				return
			}
			b.reset()
			chBatchPool.Put(b)
		}
	}()

	current := chBatchPool.Get().(*chBatch)
	flushTicker := time.NewTicker(chFlushInterval)
	defer flushTicker.Stop()
	logTicker := time.NewTicker(chLogInterval)
	defer logTicker.Stop()

	flush := func() {
		if current.len() == 0 {
			return
		}
		select {
		case <-sendDone:
			return
		default:
		}
		sendCh <- current
		current = chBatchPool.Get().(*chBatch)
	}

	for {
		select {
		case repo, ok := <-w.input:
			if !ok {
				flush()
				close(sendCh)
				<-sendDone
				return
			}
			w.flattenRepo(repo, current)
			if current.len() >= chBatchLimit {
				flush()
			}
		case <-flushTicker.C:
			flush()
		case <-logTicker.C:
			w.logger.Info("chwriter progress", "sent", w.sent.Load())
		case <-ctx.Done():
			for repo := range w.input {
				w.flattenRepo(repo, current)
				if current.len() >= chBatchLimit {
					flush()
				}
			}
			flush()
			close(sendCh)
			<-sendDone
			return
		}
	}
}

func (w *CHWriter) Wait() error {
	<-w.done
	return w.err
}

func (w *CHWriter) Sent() int64 {
	return w.sent.Load()
}

func (w *CHWriter) flattenRepo(repo *repoarchive.CrawledRepo, b *chBatch) {
	for collection, records := range repo.Collections {
		for _, rec := range records {
			b.append(repo.DID, collection, rec.RKey, bytesToString(rec.JSON), repo.CrawledAt)
		}
	}
}

// bytesToString is safe only while b is not modified afterwards.
func bytesToString(b []byte) string {
	return unsafe.String(unsafe.SliceData(b), len(b))
}

func (w *CHWriter) sendBatch(ctx context.Context, b *chBatch) error {
	if b.len() == 0 {
		return nil
	}
	batch, err := w.conn.PrepareBatch(ctx,
		"INSERT INTO crawl_records (repo, collection, rkey, record_json, crawled_at, time_us)")
	if err != nil {
		return fmt.Errorf("preparing batch: %w", err)
	}
	for i := range b.repos {
		if err := batch.Append(
			b.repos[i],
			b.collections[i],
			b.rkeys[i],
			b.recordJSONs[i],
			b.crawledAts[i],
			b.timeUSs[i],
		); err != nil {
			return fmt.Errorf("appending to batch: %w", err)
		}
	}
	if err := batch.Send(); err != nil {
		chWriterBatchesTotal.WithLabelValues("error").Inc()
		return fmt.Errorf("sending batch: %w", err)
	}
	n := b.len()
	chWriterBatchesTotal.WithLabelValues("success").Inc()
	chWriterBatchSizeHist.Observe(float64(n))
	chWriterRecordsTotal.Add(float64(n))
	w.sent.Add(int64(n))
	return nil
}
