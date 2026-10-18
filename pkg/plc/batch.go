package plc

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

const (
	MaxBatchSize     = 10_000
	FlushInterval    = 5 * time.Second
	DefaultQueueSize = 50_000
)

type BatchInserter struct {
	db     driver.Conn
	logger *slog.Logger

	queue chan *PLCBatch

	wg     sync.WaitGroup
	ctx    context.Context
	cancel context.CancelFunc
}

func NewBatchInserter(db driver.Conn, logger *slog.Logger) *BatchInserter {
	ctx, cancel := context.WithCancel(context.Background())

	bi := &BatchInserter{
		db:     db,
		logger: logger,
		queue:  make(chan *PLCBatch, DefaultQueueSize),
		ctx:    ctx,
		cancel: cancel,
	}

	bi.wg.Add(1)
	go bi.batchWorker()

	return bi
}

func (bi *BatchInserter) Add(op *PLCBatch) {
	bi.queue <- op
}

func (bi *BatchInserter) batchWorker() {
	defer bi.wg.Done()

	ticker := time.NewTicker(FlushInterval)
	defer ticker.Stop()

	var batch []*PLCBatch

	flush := func() {
		if len(batch) == 0 {
			return
		}

		if err := bi.insertBatch(bi.ctx, batch); err != nil {
			bi.logger.Error("failed to insert PLC batch", "error", err, "batch_size", len(batch))
		} else {
			bi.logger.Debug("inserted PLC batch", "batch_size", len(batch))
			batchesInsertedCounter.Inc()
		}
		batch = batch[:0]
	}

	for {
		select {
		case <-bi.ctx.Done():
			flush()
			return
		case <-ticker.C:
			flush()
		case op := <-bi.queue:
			batch = append(batch, op)
			if len(batch) >= MaxBatchSize {
				flush()
			}
		}
	}
}

func (bi *BatchInserter) insertBatch(ctx context.Context, batch []*PLCBatch) error {
	if len(batch) == 0 {
		return nil
	}

	batchInsert, err := bi.db.PrepareBatch(ctx, "INSERT INTO plc_operations (did, operation_type, created_at, handle, pds, operation_json, time_us)")
	if err != nil {
		return fmt.Errorf("failed to prepare batch: %w", err)
	}

	for _, op := range batch {
		if err := batchInsert.Append(
			op.DID,
			op.OperationType,
			op.CreatedAt,
			op.Handle,
			op.PDS,
			op.OperationJSON,
			op.TimeUS,
		); err != nil {
			return fmt.Errorf("failed to append to batch: %w", err)
		}
	}

	if err := batchInsert.Send(); err != nil {
		return fmt.Errorf("failed to send batch: %w", err)
	}

	return nil
}

func (bi *BatchInserter) Shutdown() {
	bi.cancel()
	bi.wg.Wait()
}

func (bi *BatchInserter) QueueLen() int {
	return len(bi.queue)
}
