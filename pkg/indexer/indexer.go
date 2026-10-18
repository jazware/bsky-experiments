package indexer

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/araddon/dateparse"
	"github.com/bluesky-social/indigo/api/bsky"
	"github.com/bluesky-social/jetstream/pkg/models"
	"github.com/goccy/go-json"
	"github.com/jazware/bsky-experiments/pkg/indexer/store"
	"github.com/redis/go-redis/v9"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

type Indexer struct {
	SocketURL string
	Progress  *Progress
	Logger    *slog.Logger

	RedisClient *redis.Client
	ProgressKey string

	Store         *store.Store
	BatchInserter *store.BatchInserter
}

type Progress struct {
	LastSeq            int64     `json:"last_seq"`
	LastSeqProcessedAt time.Time `json:"last_seq_processed_at"`
	lk                 sync.RWMutex
}

func (p *Progress) Update(seq int64, processedAt time.Time) {
	p.lk.Lock()
	defer p.lk.Unlock()
	p.LastSeq = seq
	p.LastSeqProcessedAt = processedAt
}

func (p *Progress) Get() (int64, time.Time) {
	p.lk.RLock()
	defer p.lk.RUnlock()
	return p.LastSeq, p.LastSeqProcessedAt
}

var tracer = otel.Tracer("indexer")

func NewIndexer(
	ctx context.Context,
	logger *slog.Logger,
	redisClient *redis.Client,
	redisPrefix string,
	chStore *store.Store,
	socketURL string,
) (*Indexer, error) {
	batchInserter := store.NewBatchInserter(chStore, logger)

	i := &Indexer{
		SocketURL: socketURL,
		Progress: &Progress{
			LastSeq: -1,
		},
		Logger:        logger,
		RedisClient:   redisClient,
		ProgressKey:   fmt.Sprintf("%s:progress", redisPrefix),
		Store:         chStore,
		BatchInserter: batchInserter,
	}

	err := i.ReadCursor(context.Background())
	if err != nil {
		if !strings.Contains(err.Error(), "redis: nil") {
			return nil, fmt.Errorf("failed to read cursor from redis: %+v", err)
		}
		logger.Warn("cursor not found in redis, starting from live")
	}

	return i, nil
}

func (i *Indexer) WriteCursor(ctx context.Context) error {
	ctx, span := tracer.Start(ctx, "WriteCursor")
	defer span.End()

	seq, processedAt := i.Progress.Get()
	p := Progress{
		LastSeq:            seq,
		LastSeqProcessedAt: processedAt,
	}
	data, err := json.Marshal(&p)
	if err != nil {
		return fmt.Errorf("failed to marshal cursor JSON: %+v", err)
	}

	err = i.RedisClient.Set(ctx, i.ProgressKey, data, 0).Err()
	if err != nil {
		return fmt.Errorf("failed to write cursor to redis: %+v", err)
	}

	return nil
}

func (i *Indexer) ReadCursor(ctx context.Context) error {
	ctx, span := tracer.Start(ctx, "ReadCursor")
	defer span.End()

	data, err := i.RedisClient.Get(ctx, i.ProgressKey).Bytes()
	if err != nil {
		return fmt.Errorf("failed to read cursor from redis: %+v", err)
	}

	err = json.Unmarshal(data, i.Progress)
	if err != nil {
		return fmt.Errorf("failed to unmarshal cursor JSON: %+v", err)
	}

	return nil
}

func (i *Indexer) Shutdown() error {
	i.BatchInserter.Shutdown()
	return i.Store.Close()
}

func (i *Indexer) OnEvent(ctx context.Context, evt *models.Event) error {
	ctx, span := tracer.Start(ctx, "HandleStreamEvent")
	defer span.End()

	if evt.Identity != nil {
		now := time.Now()
		i.Progress.Update(evt.TimeUS, now)
		eventsProcessedCounter.WithLabelValues("repo_identity", i.SocketURL).Inc()
		return nil
	}

	if evt.Commit != nil {
		err := i.OnCommit(ctx, evt)
		if err != nil {
			return fmt.Errorf("failed to process commit: %+v", err)
		}
	}

	return nil
}

func (i *Indexer) OnCommit(ctx context.Context, evt *models.Event) error {
	ctx, span := tracer.Start(ctx, "OnCommit")
	defer span.End()

	processedAt := time.Now()
	i.Progress.Update(evt.TimeUS, processedAt)

	if evt.Commit == nil {
		i.Logger.Error("got commit with empty 'commit' field on it", "repo", evt.Did, "seq", evt.TimeUS)
		return nil
	}

	log := i.Logger.With("repo", evt.Did, "seq", evt.TimeUS, "action", evt.Commit.Operation, "collection", evt.Commit.Collection)

	evtCreatedAt := time.UnixMicro(evt.TimeUS)

	span.SetAttributes(attribute.String("repo", evt.Did))
	span.SetAttributes(attribute.String("collection", evt.Commit.Collection))
	span.SetAttributes(attribute.String("rkey", evt.Commit.RKey))
	span.SetAttributes(attribute.Int64("seq", evt.TimeUS))
	span.SetAttributes(attribute.String("event.kind", evt.Commit.Operation))

	opsProcessedCounter.WithLabelValues(evt.Commit.Operation, evt.Commit.Collection, i.SocketURL).Inc()

	switch evt.Commit.Operation {
	case models.CommitOperationCreate:
		err := i.HandleCreateRecord(ctx, evt.Did, evt.Commit.Collection, evt.Commit.RKey, evt.Commit.Record, evtCreatedAt, evt.TimeUS)
		if err != nil {
			log.Error("failed to handle create record", "error", err)
		}
	case models.CommitOperationUpdate:
		err := i.HandleUpdateRecord(ctx, evt.Did, evt.Commit.Collection, evt.Commit.RKey, evt.Commit.Record, evtCreatedAt, evt.TimeUS)
		if err != nil {
			log.Error("failed to handle update record", "error", err)
		}
	case models.CommitOperationDelete:
		err := i.HandleDeleteRecord(ctx, evt.Did, evt.Commit.Collection, evt.Commit.RKey, evtCreatedAt, evt.TimeUS)
		if err != nil {
			log.Error("failed to handle delete record", "error", err)
		}
	}

	return nil
}

func (i *Indexer) HandleCreateRecord(
	ctx context.Context,
	repo string,
	collection string,
	rkey string,
	rec json.RawMessage,
	eventTime time.Time,
	timeUS int64,
) error {
	ctx, span := tracer.Start(ctx, "HandleCreateRecord")
	defer span.End()

	recordsProcessedCounter.WithLabelValues("create", collection, i.SocketURL).Inc()

	strippedJSON, err := StripCIDs(rec)
	if err != nil {
		i.Logger.Warn("failed to strip CIDs from record", "error", err, "collection", collection, "rkey", rkey)
		strippedJSON = rec
	}

	i.BatchInserter.AddRecord(&store.RecordBatch{
		Repo:       repo,
		Collection: collection,
		RKey:       rkey,
		Operation:  "create",
		RecordJSON: string(strippedJSON),
		CreatedAt:  eventTime,
		TimeUS:     timeUS,
	})

	switch collection {
	case "app.bsky.graph.follow":
		var follow bsky.GraphFollow
		if err := json.Unmarshal(rec, &follow); err != nil {
			return fmt.Errorf("failed to unmarshal follow: %w", err)
		}

		createdAt, err := dateparse.ParseAny(follow.CreatedAt)
		if err != nil {
			createdAt = eventTime
		}

		i.BatchInserter.AddFollow(&store.FollowBatch{
			ActorDID:  repo,
			TargetDID: follow.Subject,
			RKey:      rkey,
			CreatedAt: createdAt,
			Deleted:   0,
			TimeUS:    timeUS,
		})

	case "app.bsky.feed.like":
		var like bsky.FeedLike
		if err := json.Unmarshal(rec, &like); err != nil {
			return fmt.Errorf("failed to unmarshal like: %w", err)
		}

		createdAt, err := dateparse.ParseAny(like.CreatedAt)
		if err != nil {
			createdAt = eventTime
		}

		if like.Subject != nil {
			uri, err := ParseURI(like.Subject.Uri)
			if err != nil {
				return fmt.Errorf("failed to parse subject URI: %w", err)
			}

			i.BatchInserter.AddLike(&store.LikeBatch{
				ActorDID:          repo,
				SubjectURI:        like.Subject.Uri,
				SubjectDID:        uri.Did,
				SubjectCollection: uri.Collection,
				SubjectRKey:       uri.RKey,
				RKey:              rkey,
				CreatedAt:         createdAt,
				Deleted:           0,
				TimeUS:            timeUS,
			})
		}

	case "app.bsky.feed.repost":
		var repost bsky.FeedRepost
		if err := json.Unmarshal(rec, &repost); err != nil {
			return fmt.Errorf("failed to unmarshal repost: %w", err)
		}

		createdAt, err := dateparse.ParseAny(repost.CreatedAt)
		if err != nil {
			createdAt = eventTime
		}

		if repost.Subject != nil {
			uri, err := ParseURI(repost.Subject.Uri)
			if err != nil {
				return fmt.Errorf("failed to parse subject URI: %w", err)
			}

			i.BatchInserter.AddRepost(&store.RepostBatch{
				ActorDID:          repo,
				SubjectURI:        repost.Subject.Uri,
				SubjectDID:        uri.Did,
				SubjectCollection: uri.Collection,
				SubjectRKey:       uri.RKey,
				RKey:              rkey,
				CreatedAt:         createdAt,
				Deleted:           0,
				TimeUS:            timeUS,
			})
		}

	case "app.bsky.graph.block":
		var block bsky.GraphBlock
		if err := json.Unmarshal(rec, &block); err != nil {
			return fmt.Errorf("failed to unmarshal block: %w", err)
		}

		createdAt, err := dateparse.ParseAny(block.CreatedAt)
		if err != nil {
			createdAt = eventTime
		}

		i.BatchInserter.AddBlock(&store.BlockBatch{
			ActorDID:  repo,
			TargetDID: block.Subject,
			RKey:      rkey,
			CreatedAt: createdAt,
			Deleted:   0,
			TimeUS:    timeUS,
		})

	case "app.bsky.feed.post":
		var post bsky.FeedPost
		if err := json.Unmarshal(rec, &post); err != nil {
			return fmt.Errorf("failed to unmarshal post: %w", err)
		}

		createdAt, err := dateparse.ParseAny(post.CreatedAt)
		if err != nil {
			createdAt = eventTime
		}

		text := post.Text

		langs := []string{}
		if post.Langs != nil {
			langs = post.Langs
		}

		hasEmbeddedMedia := uint8(0)
		if post.Embed != nil {
			if post.Embed.EmbedImages != nil || post.Embed.EmbedVideo != nil {
				hasEmbeddedMedia = 1
			}
			if post.Embed.EmbedRecordWithMedia != nil && post.Embed.EmbedRecordWithMedia.Media != nil {
				if post.Embed.EmbedRecordWithMedia.Media.EmbedImages != nil || post.Embed.EmbedRecordWithMedia.Media.EmbedVideo != nil {
					hasEmbeddedMedia = 1
				}
			}
		}

		parentURI := ""
		rootURI := ""
		if post.Reply != nil {
			if post.Reply.Parent != nil {
				parentURI = post.Reply.Parent.Uri
			}
			if post.Reply.Root != nil {
				rootURI = post.Reply.Root.Uri
			}
		}

		uri := fmt.Sprintf("at://%s/app.bsky.feed.post/%s", repo, rkey)

		i.BatchInserter.AddPost(&store.PostBatch{
			DID:              repo,
			RKey:             rkey,
			URI:              uri,
			CreatedAt:        createdAt,
			Text:             text,
			Langs:            langs,
			HasEmbeddedMedia: hasEmbeddedMedia,
			ParentURI:        parentURI,
			RootURI:          rootURI,
			Deleted:          0,
			TimeUS:           timeUS,
		})
	}

	return nil
}

func (i *Indexer) HandleUpdateRecord(
	ctx context.Context,
	repo string,
	collection string,
	rkey string,
	rec json.RawMessage,
	eventTime time.Time,
	timeUS int64,
) error {
	ctx, span := tracer.Start(ctx, "HandleUpdateRecord")
	defer span.End()

	recordsProcessedCounter.WithLabelValues("update", collection, i.SocketURL).Inc()

	strippedJSON, err := StripCIDs(rec)
	if err != nil {
		i.Logger.Warn("failed to strip CIDs from record", "error", err, "collection", collection, "rkey", rkey)
		strippedJSON = rec
	}

	i.BatchInserter.AddRecord(&store.RecordBatch{
		Repo:       repo,
		Collection: collection,
		RKey:       rkey,
		Operation:  "update",
		RecordJSON: string(strippedJSON),
		CreatedAt:  eventTime,
		TimeUS:     timeUS,
	})

	return nil
}

func (i *Indexer) HandleDeleteRecord(
	ctx context.Context,
	repo string,
	collection string,
	rkey string,
	eventTime time.Time,
	timeUS int64,
) error {
	ctx, span := tracer.Start(ctx, "HandleDeleteRecord")
	defer span.End()

	recordsProcessedCounter.WithLabelValues("delete", collection, i.SocketURL).Inc()

	i.BatchInserter.AddRecord(&store.RecordBatch{
		Repo:       repo,
		Collection: collection,
		RKey:       rkey,
		Operation:  "delete",
		RecordJSON: "",
		CreatedAt:  eventTime,
		TimeUS:     timeUS,
	})

	// Handle specialized tables - mark as deleted using ReplacingMergeTree
	switch collection {
	case "app.bsky.graph.follow":
		i.BatchInserter.AddFollow(&store.FollowBatch{
			ActorDID:  repo,
			TargetDID: "", // Will be replaced by ReplacingMergeTree
			RKey:      rkey,
			CreatedAt: eventTime,
			Deleted:   1,
			TimeUS:    timeUS,
		})

	case "app.bsky.feed.like":
		i.BatchInserter.AddLike(&store.LikeBatch{
			ActorDID:          repo,
			SubjectURI:        "",
			SubjectDID:        "",
			SubjectCollection: "",
			SubjectRKey:       "",
			RKey:              rkey,
			CreatedAt:         eventTime,
			Deleted:           1,
			TimeUS:            timeUS,
		})

	case "app.bsky.feed.repost":
		i.BatchInserter.AddRepost(&store.RepostBatch{
			ActorDID:          repo,
			SubjectURI:        "",
			SubjectDID:        "",
			SubjectCollection: "",
			SubjectRKey:       "",
			RKey:              rkey,
			CreatedAt:         eventTime,
			Deleted:           1,
			TimeUS:            timeUS,
		})

	case "app.bsky.graph.block":
		i.BatchInserter.AddBlock(&store.BlockBatch{
			ActorDID:  repo,
			TargetDID: "",
			RKey:      rkey,
			CreatedAt: eventTime,
			Deleted:   1,
			TimeUS:    timeUS,
		})

	case "app.bsky.feed.post":
		uri := fmt.Sprintf("at://%s/app.bsky.feed.post/%s", repo, rkey)
		i.BatchInserter.AddPost(&store.PostBatch{
			DID:              repo,
			RKey:             rkey,
			URI:              uri,
			CreatedAt:        eventTime,
			Text:             "",
			Langs:            []string{},
			HasEmbeddedMedia: 0,
			ParentURI:        "",
			RootURI:          "",
			Deleted:          1,
			TimeUS:           timeUS,
		})
	}

	return nil
}
