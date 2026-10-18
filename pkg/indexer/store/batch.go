package store

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const (
	MaxBatchSize     = 50_000
	FlushInterval    = 5 * time.Second
	DefaultQueueSize = 100_000
)

type RecordBatch struct {
	Repo       string
	Collection string
	RKey       string
	Operation  string
	RecordJSON string
	CreatedAt  time.Time
	TimeUS     int64
}

type FollowBatch struct {
	ActorDID  string
	TargetDID string
	RKey      string
	CreatedAt time.Time
	Deleted   uint8
	TimeUS    int64
}

type LikeBatch struct {
	ActorDID          string
	SubjectURI        string
	SubjectDID        string
	SubjectCollection string
	SubjectRKey       string
	RKey              string
	CreatedAt         time.Time
	Deleted           uint8
	TimeUS            int64
}

type RepostBatch struct {
	ActorDID          string
	SubjectURI        string
	SubjectDID        string
	SubjectCollection string
	SubjectRKey       string
	RKey              string
	CreatedAt         time.Time
	Deleted           uint8
	TimeUS            int64
}

type BlockBatch struct {
	ActorDID  string
	TargetDID string
	RKey      string
	CreatedAt time.Time
	Deleted   uint8
	TimeUS    int64
}

type PostBatch struct {
	DID              string
	RKey             string
	URI              string
	CreatedAt        time.Time
	Text             string
	Langs            []string
	HasEmbeddedMedia uint8
	ParentURI        string
	RootURI          string
	Deleted          uint8
	TimeUS           int64
}

type FeedRequestAnalyticsBatch struct {
	UserDID        string
	FeedName       string
	LimitRequested uint16
	HasCursor      uint8
	RequestedAt    time.Time
	TimeUS         int64
}

type BatchInserter struct {
	store  *Store
	logger *slog.Logger

	recordQueue    chan *RecordBatch
	followQueue    chan *FollowBatch
	likeQueue      chan *LikeBatch
	repostQueue    chan *RepostBatch
	blockQueue     chan *BlockBatch
	postQueue      chan *PostBatch
	analyticsQueue chan *FeedRequestAnalyticsBatch

	wg     sync.WaitGroup
	ctx    context.Context
	cancel context.CancelFunc
}

func NewBatchInserter(store *Store, logger *slog.Logger) *BatchInserter {
	ctx, cancel := context.WithCancel(context.Background())

	bi := &BatchInserter{
		store:          store,
		logger:         logger,
		recordQueue:    make(chan *RecordBatch, DefaultQueueSize),
		followQueue:    make(chan *FollowBatch, DefaultQueueSize),
		likeQueue:      make(chan *LikeBatch, DefaultQueueSize),
		repostQueue:    make(chan *RepostBatch, DefaultQueueSize),
		blockQueue:     make(chan *BlockBatch, DefaultQueueSize),
		postQueue:      make(chan *PostBatch, DefaultQueueSize),
		analyticsQueue: make(chan *FeedRequestAnalyticsBatch, DefaultQueueSize),
		ctx:            ctx,
		cancel:         cancel,
	}

	bi.wg.Add(7)
	go bi.recordBatchWorker()
	go bi.followBatchWorker()
	go bi.likeBatchWorker()
	go bi.repostBatchWorker()
	go bi.blockBatchWorker()
	go bi.postBatchWorker()
	go bi.analyticsBatchWorker()

	return bi
}

func (bi *BatchInserter) AddRecord(record *RecordBatch) {
	bi.recordQueue <- record
}

func (bi *BatchInserter) AddFollow(follow *FollowBatch) {
	bi.followQueue <- follow
}

func (bi *BatchInserter) AddLike(like *LikeBatch) {
	bi.likeQueue <- like
}

func (bi *BatchInserter) AddRepost(repost *RepostBatch) {
	bi.repostQueue <- repost
}

func (bi *BatchInserter) AddBlock(block *BlockBatch) {
	bi.blockQueue <- block
}

func (bi *BatchInserter) AddPost(post *PostBatch) {
	bi.postQueue <- post
}

func (bi *BatchInserter) AddFeedRequestAnalytics(analytics *FeedRequestAnalyticsBatch) {
	bi.analyticsQueue <- analytics
}

func (bi *BatchInserter) recordBatchWorker() {
	defer bi.wg.Done()

	ticker := time.NewTicker(FlushInterval)
	defer ticker.Stop()

	var batch []*RecordBatch

	flush := func() {
		if len(batch) == 0 {
			return
		}

		if err := bi.insertRecordBatch(bi.ctx, batch); err != nil {
			bi.logger.Error("failed to insert record batch", "error", err, "batch_size", len(batch))
		} else {
			bi.logger.Debug("inserted record batch", "batch_size", len(batch))
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
		case record := <-bi.recordQueue:
			batch = append(batch, record)
			if len(batch) >= MaxBatchSize {
				flush()
			}
		}
	}
}

func (bi *BatchInserter) followBatchWorker() {
	defer bi.wg.Done()

	ticker := time.NewTicker(FlushInterval)
	defer ticker.Stop()

	var batch []*FollowBatch

	flush := func() {
		if len(batch) == 0 {
			return
		}

		if err := bi.insertFollowBatch(bi.ctx, batch); err != nil {
			bi.logger.Error("failed to insert follow batch", "error", err, "batch_size", len(batch))
		} else {
			bi.logger.Debug("inserted follow batch", "batch_size", len(batch))
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
		case follow := <-bi.followQueue:
			batch = append(batch, follow)
			if len(batch) >= MaxBatchSize {
				flush()
			}
		}
	}
}

func (bi *BatchInserter) likeBatchWorker() {
	defer bi.wg.Done()

	ticker := time.NewTicker(FlushInterval)
	defer ticker.Stop()

	var batch []*LikeBatch

	flush := func() {
		if len(batch) == 0 {
			return
		}

		if err := bi.insertLikeBatch(bi.ctx, batch); err != nil {
			bi.logger.Error("failed to insert like batch", "error", err, "batch_size", len(batch))
		} else {
			bi.logger.Debug("inserted like batch", "batch_size", len(batch))
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
		case like := <-bi.likeQueue:
			batch = append(batch, like)
			if len(batch) >= MaxBatchSize {
				flush()
			}
		}
	}
}

func (bi *BatchInserter) repostBatchWorker() {
	defer bi.wg.Done()

	ticker := time.NewTicker(FlushInterval)
	defer ticker.Stop()

	var batch []*RepostBatch

	flush := func() {
		if len(batch) == 0 {
			return
		}

		if err := bi.insertRepostBatch(bi.ctx, batch); err != nil {
			bi.logger.Error("failed to insert repost batch", "error", err, "batch_size", len(batch))
		} else {
			bi.logger.Debug("inserted repost batch", "batch_size", len(batch))
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
		case repost := <-bi.repostQueue:
			batch = append(batch, repost)
			if len(batch) >= MaxBatchSize {
				flush()
			}
		}
	}
}

func (bi *BatchInserter) blockBatchWorker() {
	defer bi.wg.Done()

	ticker := time.NewTicker(FlushInterval)
	defer ticker.Stop()

	var batch []*BlockBatch

	flush := func() {
		if len(batch) == 0 {
			return
		}

		if err := bi.insertBlockBatch(bi.ctx, batch); err != nil {
			bi.logger.Error("failed to insert block batch", "error", err, "batch_size", len(batch))
		} else {
			bi.logger.Debug("inserted block batch", "batch_size", len(batch))
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
		case block := <-bi.blockQueue:
			batch = append(batch, block)
			if len(batch) >= MaxBatchSize {
				flush()
			}
		}
	}
}

func (bi *BatchInserter) insertRecordBatch(ctx context.Context, batch []*RecordBatch) (err error) {
	if len(batch) == 0 {
		return nil
	}

	ctx, done := observeBatchOp(ctx, "repo_records", len(batch), &err)
	defer done()

	batchInsert, err := bi.store.DB.PrepareBatch(ctx, "INSERT INTO repo_records (repo, collection, rkey, operation, record_json, created_at, time_us)")
	if err != nil {
		return fmt.Errorf("failed to prepare batch: %w", err)
	}

	for _, record := range batch {
		if err := batchInsert.Append(
			record.Repo,
			record.Collection,
			record.RKey,
			record.Operation,
			record.RecordJSON,
			record.CreatedAt,
			record.TimeUS,
		); err != nil {
			return fmt.Errorf("failed to append to batch: %w", err)
		}
	}

	return batchInsert.Send()
}

func (bi *BatchInserter) insertFollowBatch(ctx context.Context, batch []*FollowBatch) (err error) {
	if len(batch) == 0 {
		return nil
	}

	ctx, done := observeBatchOp(ctx, "follows", len(batch), &err)
	defer done()

	batchInsert, err := bi.store.DB.PrepareBatch(ctx, "INSERT INTO follows (actor_did, target_did, rkey, created_at, deleted, time_us)")
	if err != nil {
		return fmt.Errorf("failed to prepare batch: %w", err)
	}

	for _, follow := range batch {
		if err := batchInsert.Append(
			follow.ActorDID,
			follow.TargetDID,
			follow.RKey,
			follow.CreatedAt,
			follow.Deleted,
			follow.TimeUS,
		); err != nil {
			return fmt.Errorf("failed to append to batch: %w", err)
		}
	}

	return batchInsert.Send()
}

func (bi *BatchInserter) insertLikeBatch(ctx context.Context, batch []*LikeBatch) (err error) {
	if len(batch) == 0 {
		return nil
	}

	ctx, done := observeBatchOp(ctx, "likes", len(batch), &err)
	defer done()

	batchInsert, err := bi.store.DB.PrepareBatch(ctx, "INSERT INTO likes (actor_did, subject_uri, subject_did, subject_collection, subject_rkey, rkey, created_at, deleted, time_us)")
	if err != nil {
		return fmt.Errorf("failed to prepare batch: %w", err)
	}

	for _, like := range batch {
		if err := batchInsert.Append(
			like.ActorDID,
			like.SubjectURI,
			like.SubjectDID,
			like.SubjectCollection,
			like.SubjectRKey,
			like.RKey,
			like.CreatedAt,
			like.Deleted,
			like.TimeUS,
		); err != nil {
			return fmt.Errorf("failed to append to batch: %w", err)
		}
	}

	return batchInsert.Send()
}

func (bi *BatchInserter) insertRepostBatch(ctx context.Context, batch []*RepostBatch) (err error) {
	if len(batch) == 0 {
		return nil
	}

	ctx, done := observeBatchOp(ctx, "reposts", len(batch), &err)
	defer done()

	batchInsert, err := bi.store.DB.PrepareBatch(ctx, "INSERT INTO reposts (actor_did, subject_uri, subject_did, subject_collection, subject_rkey, rkey, created_at, deleted, time_us)")
	if err != nil {
		return fmt.Errorf("failed to prepare batch: %w", err)
	}

	for _, repost := range batch {
		if err := batchInsert.Append(
			repost.ActorDID,
			repost.SubjectURI,
			repost.SubjectDID,
			repost.SubjectCollection,
			repost.SubjectRKey,
			repost.RKey,
			repost.CreatedAt,
			repost.Deleted,
			repost.TimeUS,
		); err != nil {
			return fmt.Errorf("failed to append to batch: %w", err)
		}
	}

	return batchInsert.Send()
}

func (bi *BatchInserter) insertBlockBatch(ctx context.Context, batch []*BlockBatch) (err error) {
	if len(batch) == 0 {
		return nil
	}

	ctx, done := observeBatchOp(ctx, "blocks", len(batch), &err)
	defer done()

	batchInsert, err := bi.store.DB.PrepareBatch(ctx, "INSERT INTO blocks (actor_did, target_did, rkey, created_at, deleted, time_us)")
	if err != nil {
		return fmt.Errorf("failed to prepare batch: %w", err)
	}

	for _, block := range batch {
		if err := batchInsert.Append(
			block.ActorDID,
			block.TargetDID,
			block.RKey,
			block.CreatedAt,
			block.Deleted,
			block.TimeUS,
		); err != nil {
			return fmt.Errorf("failed to append to batch: %w", err)
		}
	}

	return batchInsert.Send()
}

func (bi *BatchInserter) postBatchWorker() {
	defer bi.wg.Done()

	ticker := time.NewTicker(FlushInterval)
	defer ticker.Stop()

	var batch []*PostBatch

	flush := func() {
		if len(batch) == 0 {
			return
		}

		if err := bi.insertPostBatch(bi.ctx, batch); err != nil {
			bi.logger.Error("failed to insert post batch", "error", err, "batch_size", len(batch))
		} else {
			bi.logger.Debug("inserted post batch", "batch_size", len(batch))
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
		case post := <-bi.postQueue:
			batch = append(batch, post)
			if len(batch) >= MaxBatchSize {
				flush()
			}
		}
	}
}

func (bi *BatchInserter) insertPostBatch(ctx context.Context, batch []*PostBatch) (err error) {
	if len(batch) == 0 {
		return nil
	}

	ctx, done := observeBatchOp(ctx, "posts", len(batch), &err)
	defer done()

	batchInsert, err := bi.store.DB.PrepareBatch(ctx, "INSERT INTO posts (did, rkey, uri, created_at, text, langs, has_embedded_media, parent_uri, root_uri, deleted, time_us)")
	if err != nil {
		return fmt.Errorf("failed to prepare batch: %w", err)
	}

	for _, post := range batch {
		if err := batchInsert.Append(
			post.DID,
			post.RKey,
			post.URI,
			post.CreatedAt,
			post.Text,
			post.Langs,
			post.HasEmbeddedMedia,
			post.ParentURI,
			post.RootURI,
			post.Deleted,
			post.TimeUS,
		); err != nil {
			return fmt.Errorf("failed to append to batch: %w", err)
		}
	}

	return batchInsert.Send()
}

func (bi *BatchInserter) analyticsBatchWorker() {
	defer bi.wg.Done()

	ticker := time.NewTicker(FlushInterval)
	defer ticker.Stop()

	var batch []*FeedRequestAnalyticsBatch

	flush := func() {
		if len(batch) == 0 {
			return
		}

		if err := bi.insertAnalyticsBatch(bi.ctx, batch); err != nil {
			bi.logger.Error("failed to insert analytics batch", "error", err, "batch_size", len(batch))
		} else {
			bi.logger.Debug("inserted analytics batch", "batch_size", len(batch))
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
		case analytics := <-bi.analyticsQueue:
			batch = append(batch, analytics)
			if len(batch) >= MaxBatchSize {
				flush()
			}
		}
	}
}

func (bi *BatchInserter) insertAnalyticsBatch(ctx context.Context, batch []*FeedRequestAnalyticsBatch) (err error) {
	if len(batch) == 0 {
		return nil
	}

	ctx, done := observeBatchOp(ctx, "feed_request_analytics", len(batch), &err)
	defer done()

	batchInsert, err := bi.store.DB.PrepareBatch(ctx, "INSERT INTO feed_request_analytics (user_did, feed_name, limit_requested, has_cursor, requested_at, time_us)")
	if err != nil {
		return fmt.Errorf("failed to prepare batch: %w", err)
	}

	for _, analytics := range batch {
		if err := batchInsert.Append(
			analytics.UserDID,
			analytics.FeedName,
			analytics.LimitRequested,
			analytics.HasCursor,
			analytics.RequestedAt,
			analytics.TimeUS,
		); err != nil {
			return fmt.Errorf("failed to append to batch: %w", err)
		}
	}

	return batchInsert.Send()
}

func (bi *BatchInserter) Shutdown() {
	bi.cancel()
	bi.wg.Wait()
}
