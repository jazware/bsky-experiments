package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/jazware/bsky-experiments/pkg/crawler"
	"github.com/jazware/bsky-experiments/pkg/repoarchive"
	"github.com/jazware/bsky-experiments/telemetry"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/urfave/cli/v2"
)

func replayCommand() *cli.Command {
	flags := append(commonFlags(),
		&cli.StringFlag{
			Name:    "input-dir",
			Usage:   "Directory containing .rca segment files",
			Value:   "./data/rca",
			EnvVars: []string{"INPUT_DIR"},
		},
		&cli.StringSliceFlag{
			Name:    "collections",
			Usage:   "Collections to replay (comma-separated). Empty means all.",
			EnvVars: []string{"COLLECTIONS"},
		},
		&cli.IntFlag{
			Name:    "workers",
			Usage:   "Number of concurrent segment replay workers",
			Value:   4,
			EnvVars: []string{"REPLAY_WORKERS"},
		},
		&cli.BoolFlag{
			Name:  "truncate",
			Usage: "Truncate crawl_records before replaying",
		},
	)

	return &cli.Command{
		Name:  "replay",
		Usage: "Replay .rca archives into ClickHouse crawl_records table",
		Flags: flags,
		Action: func(cctx *cli.Context) error {
			return runReplay(cctx)
		},
	}
}

func setupReplayClickHouse(cctx *cli.Context) (driver.Conn, error) {
	return crawler.SetupBulkClickHouse(
		cctx.String("clickhouse-address"),
		cctx.String("clickhouse-username"),
		cctx.String("clickhouse-password"),
	)
}

var (
	replayRecordsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "replay_records_total",
		Help: "Total records sent to ClickHouse during replay.",
	})

	replayBatchesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "replay_batches_total",
		Help: "Batches sent to ClickHouse during replay.",
	}, []string{"result"})

	replaySegmentsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "replay_segments_total",
		Help: "Segments processed during replay.",
	}, []string{"result"})

	replayRecordParseErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "replay_record_parse_errors_total",
		Help: "Records skipped due to parse errors during replay.",
	})

	replayBatchSize = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "replay_batch_size",
		Help:    "Records per batch sent to ClickHouse.",
		Buckets: []float64{1000, 10000, 50000, 100000, 250000, 500000},
	})
)

func runReplay(cctx *cli.Context) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-signals
		cancel()
	}()

	logger := telemetry.StartLogger(cctx)
	telemetry.StartMetrics(cctx)

	chConn, err := setupReplayClickHouse(cctx)
	if err != nil {
		return fmt.Errorf("clickhouse setup: %w", err)
	}

	inputDir := cctx.String("input-dir")
	collections := cctx.StringSlice("collections")
	workers := cctx.Int("workers")

	segments, err := filepath.Glob(filepath.Join(inputDir, "*.rca"))
	if err != nil {
		return fmt.Errorf("listing segments: %w", err)
	}
	sort.Strings(segments)

	if len(segments) == 0 {
		return fmt.Errorf("no .rca files found in %s", inputDir)
	}

	if err := crawler.PrepareTableForBulkLoad(ctx, chConn, cctx.Bool("truncate"), logger); err != nil {
		return fmt.Errorf("preparing table: %w", err)
	}
	// Always restore indexes and merges, even on error/signal.
	defer func() {
		logger.Info("finalizing table after bulk load...")
		bgCtx := context.Background()
		if err := crawler.FinalizeTableAfterBulkLoad(bgCtx, chConn, logger); err != nil {
			logger.Error("failed to finalize table", "error", err)
		}
	}()

	logger.Info("replaying segments", "count", len(segments), "workers", workers, "collections", collections)

	segCh := make(chan string)
	var totalRecords atomic.Int64
	var wg sync.WaitGroup

	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for segPath := range segCh {
				logger.Info("replaying segment", "path", segPath)
				count, err := replaySegment(ctx, chConn, segPath, collections, logger)
				if err != nil {
					replaySegmentsTotal.WithLabelValues("error").Inc()
					logger.Error("failed to replay segment", "path", segPath, "error", err)
					continue
				}
				replaySegmentsTotal.WithLabelValues("success").Inc()
				totalRecords.Add(int64(count))
				logger.Info("segment replayed", "path", segPath, "records", count)
			}
		}()
	}

	for _, segPath := range segments {
		if ctx.Err() != nil {
			break
		}
		segCh <- segPath
	}
	close(segCh)
	wg.Wait()

	logger.Info("replay complete", "total_records", totalRecords.Load())
	return nil
}

type replayBatch struct {
	repos       []string
	collections []string
	rkeys       []string
	recordJSONs []string
	crawledAts  []time.Time
	timeUSs     []int64
}

func sendReplayBatch(ctx context.Context, db driver.Conn, b replayBatch) error {
	if len(b.repos) == 0 {
		return nil
	}
	batchInsert, err := db.PrepareBatch(ctx,
		"INSERT INTO crawl_records (repo, collection, rkey, record_json, crawled_at, time_us)")
	if err != nil {
		return fmt.Errorf("preparing batch: %w", err)
	}
	for i := range b.repos {
		if err := batchInsert.Append(
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
	if err := batchInsert.Send(); err != nil {
		replayBatchesTotal.WithLabelValues("error").Inc()
		return fmt.Errorf("sending batch: %w", err)
	}
	n := len(b.repos)
	replayBatchesTotal.WithLabelValues("success").Inc()
	replayBatchSize.Observe(float64(n))
	replayRecordsTotal.Add(float64(n))
	return nil
}

func replaySegment(ctx context.Context, db driver.Conn, segPath string, collections []string, logger *slog.Logger) (int, error) {
	reader, err := repoarchive.OpenSegment(segPath)
	if err != nil {
		return 0, err
	}
	defer reader.Close()

	if len(collections) > 0 {
		reader.SetCollectionFilter(collections...)
	}

	const batchSize = 500_000

	// Pipeline: reader fills batches, sender goroutine ships them to ClickHouse.
	// Buffer of 1 lets the reader prepare the next batch while the current one sends.
	sendCh := make(chan replayBatch, 1)
	var sendErr error
	var sent atomic.Int64
	sendDone := make(chan struct{})

	go func() {
		defer close(sendDone)
		for b := range sendCh {
			if err := sendReplayBatch(ctx, db, b); err != nil {
				sendErr = err
				for range sendCh {
				}
				return
			}
			sent.Add(int64(len(b.repos)))
		}
	}()

	current := replayBatch{}
	for reader.Next() {
		if ctx.Err() != nil {
			break
		}
		select {
		case <-sendDone:
			return int(sent.Load()), sendErr
		default:
		}

		repo := reader.Repo()
		for repo.NextCollection() {
			col := repo.Collection()
			for col.NextRecord() {
				rec, err := col.Record()
				if err != nil {
					replayRecordParseErrors.Inc()
					logger.Warn("skipping record", "did", repo.DID, "collection", col.Name, "error", err)
					continue
				}
				current.repos = append(current.repos, repo.DID)
				current.collections = append(current.collections, col.Name)
				current.rkeys = append(current.rkeys, rec.RKey)
				current.recordJSONs = append(current.recordJSONs, string(rec.JSON))
				current.crawledAts = append(current.crawledAts, repo.CrawledAt)
				current.timeUSs = append(current.timeUSs, repo.CrawledAt.UnixMicro())

				if len(current.repos) >= batchSize {
					sendCh <- current
					current = replayBatch{}
				}
			}
		}
	}

	if len(current.repos) > 0 {
		sendCh <- current
	}
	close(sendCh)
	<-sendDone

	return int(sent.Load()), sendErr
}
