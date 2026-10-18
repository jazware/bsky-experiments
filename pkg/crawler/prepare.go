package crawler

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/prometheus/client_golang/prometheus"
)

type PrepareConfig struct {
	DiscoveryConfig
	ClickHouseConn driver.Conn
	VerifyPDS      bool
	BatchSize      int
	Logger         *slog.Logger
}

const (
	prepareConsumers = 4
	prepareFlushSize = 50000
)

func RunPrepare(ctx context.Context, config PrepareConfig) error {
	logger := config.Logger
	timer := prometheus.NewTimer(prepareDurationSeconds)
	defer timer.ObserveDuration()

	// Pre-load the full DID→PDS mapping into memory so verification is a
	// map lookup instead of a per-batch FINAL query against 47M+ rows.
	var plcState map[string]string
	if config.VerifyPDS {
		var err error
		plcState, err = PreloadPLCState(ctx, config.ClickHouseConn, logger)
		if err != nil {
			return fmt.Errorf("pre-loading PLC state: %w", err)
		}
	}

	discoverer := NewDiscoverer(config.DiscoveryConfig)

	logger.Info("discovering hosts from relay", "relay", config.RelayHost)
	hosts, err := discoverer.ListHosts(ctx)
	if err != nil {
		return fmt.Errorf("listing hosts: %w", err)
	}
	logger.Info("hosts discovered", "count", len(hosts))

	if len(hosts) == 0 {
		return fmt.Errorf("no hosts found from relay")
	}

	var expectedRepos int64
	for _, h := range hosts {
		expectedRepos += h.AccountCount
	}
	prepareExpectedRepos.Set(float64(expectedRepos))
	logger.Info("expected repos from relay", "expected", expectedRepos)

	limit := min(10, len(hosts))
	for _, h := range hosts[:limit] {
		logger.Info("top host", "hostname", h.Hostname, "accounts", h.AccountCount)
	}

	logger.Info("truncating crawl_repos table")
	if err := config.ClickHouseConn.Exec(ctx, "TRUNCATE TABLE crawl_repos"); err != nil {
		return fmt.Errorf("truncating crawl_repos: %w", err)
	}

	repoCh := make(chan []DiscoveredRepo, config.DiscoveryConfig.Workers*2)

	enumDone := make(chan error, 1)
	go func() {
		enumDone <- discoverer.EnumerateRepos(ctx, hosts, repoCh)
	}()

	var stats struct {
		discovered atomic.Int64
		verified   atomic.Int64
		excluded   atomic.Int64
		inserted   atomic.Int64
	}

	var consumerWg sync.WaitGroup
	for range prepareConsumers {
		consumerWg.Add(1)
		go func() {
			defer consumerWg.Done()

			var localBatch []VerifiedRepo

			for batch := range repoCh {
				if ctx.Err() != nil {
					return
				}

				stats.discovered.Add(int64(len(batch)))

				var toInsert []VerifiedRepo
				if plcState != nil {
					verified := VerifyInMemory(batch, plcState)
					stats.verified.Add(int64(len(verified)))
					stats.excluded.Add(int64(len(batch) - len(verified)))
					toInsert = verified
				} else {
					toInsert = make([]VerifiedRepo, len(batch))
					for i, r := range batch {
						toInsert[i] = VerifiedRepo{DiscoveredRepo: r, Verified: false}
					}
				}

				localBatch = append(localBatch, toInsert...)

				if len(localBatch) >= prepareFlushSize {
					if err := insertCrawlRepos(ctx, config.ClickHouseConn, localBatch); err != nil {
						logger.Error("failed to insert crawl_repos", "error", err)
						return
					}
					n := int64(len(localBatch))
					stats.inserted.Add(n)
					prepareInsertedTotal.Add(float64(n))
					logger.Info("prepare progress",
						"discovered", stats.discovered.Load(),
						"inserted", stats.inserted.Load(),
						"excluded", stats.excluded.Load())
					localBatch = localBatch[:0]
				}
			}

			if len(localBatch) > 0 {
				if err := insertCrawlRepos(ctx, config.ClickHouseConn, localBatch); err != nil {
					logger.Error("failed to flush crawl_repos", "error", err)
					return
				}
				stats.inserted.Add(int64(len(localBatch)))
				prepareInsertedTotal.Add(float64(len(localBatch)))
			}
		}()
	}

	consumerWg.Wait()

	if err := <-enumDone; err != nil {
		return fmt.Errorf("enumerating repos: %w", err)
	}

	logger.Info("prepare complete",
		"discovered", stats.discovered.Load(),
		"verified", stats.verified.Load(),
		"excluded", stats.excluded.Load(),
		"inserted", stats.inserted.Load())

	return nil
}

func insertCrawlRepos(ctx context.Context, conn driver.Conn, repos []VerifiedRepo) error {
	batch, err := conn.PrepareBatch(ctx,
		"INSERT INTO crawl_repos (did, pds, head, rev, active, status, verified, time_us)")
	if err != nil {
		return fmt.Errorf("preparing batch: %w", err)
	}

	now := time.Now().UnixMicro()
	for _, r := range repos {
		var active uint8
		if r.Active {
			active = 1
		}
		var verified uint8
		if r.Verified {
			verified = 1
		}
		if err := batch.Append(
			r.DID,
			r.PDS,
			r.Head,
			r.Rev,
			active,
			r.Status,
			verified,
			now,
		); err != nil {
			return fmt.Errorf("appending to batch: %w", err)
		}
	}

	return batch.Send()
}
