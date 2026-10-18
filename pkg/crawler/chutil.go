package crawler

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

func SetupBulkClickHouse(address, username, password string) (driver.Conn, error) {
	opts := &clickhouse.Options{
		Addr: []string{address},
		Settings: clickhouse.Settings{
			"insert_deduplicate": 0, // skip block-level dedup, ReplacingMergeTree handles it
			"optimize_on_insert": 0, // don't merge inline, let background merges handle it
			"max_insert_threads": 4, // parallel block processing server-side
			"insert_quorum":      0, // no quorum waiting (single-node)
		},
	}
	if username != "" {
		opts.Auth = clickhouse.Auth{
			Username: username,
			Password: password,
		}
	}
	conn, err := clickhouse.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("opening clickhouse: %w", err)
	}
	if err := conn.Ping(context.Background()); err != nil {
		return nil, fmt.Errorf("pinging clickhouse: %w", err)
	}
	return conn, nil
}

// killStuckQueries kills any queries on crawl_records that might be holding the
// table metadata lock (stale ALTERs from previous crashed runs, etc.).
func killStuckQueries(ctx context.Context, conn driver.Conn, logger *slog.Logger) {
	rows, err := conn.Query(ctx,
		"SELECT query_id, query, elapsed FROM system.processes WHERE query LIKE '%crawl_records%' AND query NOT LIKE '%system.processes%'")
	if err != nil {
		logger.Warn("failed to query stuck processes", "error", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var qid, q string
		var elapsed float64
		if err := rows.Scan(&qid, &q, &elapsed); err != nil {
			continue
		}
		logger.Info("killing stuck query on crawl_records", "query_id", qid, "elapsed_s", int(elapsed), "query", q)
		_ = conn.Exec(ctx, "KILL QUERY WHERE query_id = $1", qid)
	}

	// Give ClickHouse a moment to release locks.
	time.Sleep(2 * time.Second)
}

func execWithRetry(ctx context.Context, conn driver.Conn, stmt string, logger *slog.Logger) error {
	const maxRetries = 3
	for attempt := range maxRetries {
		if err := conn.Exec(ctx, stmt); err != nil {
			if strings.Contains(err.Error(), "code: 473") && attempt < maxRetries-1 {
				logger.Warn("lock timeout, retrying", "stmt", stmt, "attempt", attempt+1)
				time.Sleep(5 * time.Second)
				continue
			}
			return err
		}
		return nil
	}
	return nil
}

// PrepareTableForBulkLoad stops merges and drops secondary indexes on crawl_records
// to maximize insert throughput during bulk loading.
func PrepareTableForBulkLoad(ctx context.Context, conn driver.Conn, truncate bool, logger *slog.Logger) error {
	killStuckQueries(ctx, conn, logger)

	// Truncate first (if requested) to clear all parts before altering settings,
	// otherwise in-progress merges on a large table can block ALTER.
	var stmts []string
	if truncate {
		stmts = append(stmts, "TRUNCATE TABLE crawl_records SETTINGS max_table_size_to_drop = 0")
	}
	stmts = append(stmts,
		"SYSTEM STOP MERGES crawl_records",
		"KILL MUTATION WHERE database = 'default' AND table = 'crawl_records'",
		"ALTER TABLE crawl_records MODIFY SETTING parts_to_delay_insert = 100000, parts_to_throw_insert = 100000",
		"ALTER TABLE crawl_records MODIFY COLUMN IF EXISTS record_json String CODEC(LZ4)",
		"ALTER TABLE crawl_records DROP INDEX IF EXISTS idx_repo",
		"ALTER TABLE crawl_records DROP INDEX IF EXISTS idx_collection",
		"ALTER TABLE crawl_records DROP INDEX IF EXISTS idx_crawled_at",
	)
	for _, stmt := range stmts {
		logger.Info("bulk load prep", "stmt", stmt)
		if err := execWithRetry(ctx, conn, stmt, logger); err != nil {
			return fmt.Errorf("exec %q: %w", stmt, err)
		}
	}
	return nil
}

// FinalizeTableAfterBulkLoad re-adds secondary indexes and resumes merges.
func FinalizeTableAfterBulkLoad(ctx context.Context, conn driver.Conn, logger *slog.Logger) error {
	stmts := []string{
		"ALTER TABLE crawl_records MODIFY COLUMN IF EXISTS record_json String CODEC(ZSTD(3))",
		"SYSTEM START MERGES crawl_records",
		"OPTIMIZE TABLE crawl_records FINAL",
		"ALTER TABLE crawl_records ADD INDEX IF NOT EXISTS idx_repo repo TYPE bloom_filter(0.01) GRANULARITY 1",
		"ALTER TABLE crawl_records ADD INDEX IF NOT EXISTS idx_collection collection TYPE bloom_filter(0.01) GRANULARITY 1",
		"ALTER TABLE crawl_records ADD INDEX IF NOT EXISTS idx_crawled_at crawled_at TYPE minmax GRANULARITY 1",
		"ALTER TABLE crawl_records MODIFY SETTING parts_to_delay_insert = 150, parts_to_throw_insert = 300",
	}
	for _, stmt := range stmts {
		logger.Info("bulk load finalize", "stmt", stmt)
		if err := execWithRetry(ctx, conn, stmt, logger); err != nil {
			return fmt.Errorf("exec %q: %w", stmt, err)
		}
	}
	return nil
}
