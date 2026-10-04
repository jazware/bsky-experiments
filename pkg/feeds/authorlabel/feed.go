package authorlabel

import (
	"context"
	"fmt"
	"time"

	appbsky "github.com/bluesky-social/indigo/api/bsky"
	"github.com/jazware/bsky-experiments/pkg/indexer/store"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

type Feed struct {
	FeedActorDID string
	Store        *store.Store
	Labels       map[string]LabelFeed
}

type LabelFeed struct {
	Label   string
	Private bool
	GetPage func(ctx context.Context, userDID string, limit int64, cursor string) ([]*appbsky.FeedDefs_SkeletonFeedPost, *string, error)
}

// Explainer shown to logged-out users and users not on the label list:
// https://bsky.app/profile/amityf.bsky.social/post/3mrv36uo4pc2a
var unauthorizedResponse = []*appbsky.FeedDefs_SkeletonFeedPost{
	{Post: "at://did:plc:wksnvmuo52yo3a32hkxdq4kb/app.bsky.feed.post/3mrv36uo4pc2a"},
}

type NotFoundError struct {
	error
}

func NewFeed(ctx context.Context, feedActorDID string, chStore *store.Store) (*Feed, []string, error) {
	alf := Feed{
		FeedActorDID: feedActorDID,
		Store:        chStore,
	}

	labelFeeds := map[string]LabelFeed{
		"a-mpls": {
			Label:   "mpls",
			Private: true,
			GetPage: alf.GetMPLSPage,
		},
		"cl-tqsp": {
			Label:   "tqsp",
			Private: false,
			GetPage: alf.GetTQSPPage,
		},
	}

	labels := make([]string, 0, len(labelFeeds))
	for name := range labelFeeds {
		labels = append(labels, name)
	}

	alf.Labels = labelFeeds
	return &alf, labels, nil
}

var tracer = otel.Tracer("author-label-feed")

func (f *Feed) GetPage(ctx context.Context, feed string, userDID string, limit int64, cursor string) ([]*appbsky.FeedDefs_SkeletonFeedPost, *string, error) {
	ctx, span := tracer.Start(ctx, "GetPage")
	defer span.End()

	span.SetAttributes(
		attribute.String("feed", feed),
		attribute.String("actor.did", userDID),
		attribute.Int64("limit", limit),
		attribute.String("cursor", cursor),
	)

	if userDID == "" {
		span.SetAttributes(attribute.Bool("actor.not_authorized", true))
		return unauthorizedResponse, nil, nil
	}

	labelFeed, ok := f.Labels[feed]
	if !ok {
		span.SetAttributes(attribute.String("label.invalid", feed))
		return nil, nil, NotFoundError{fmt.Errorf("feed not found")}
	}

	if labelFeed.Private {
		// Ensure the user is assigned to the label before letting them view the feed
		authorized, err := f.Store.ActorHasLabel(ctx, userDID, labelFeed.Label)
		if err != nil {
			span.SetAttributes(attribute.String("label.lookup_error", err.Error()))
			return nil, nil, fmt.Errorf("error getting labels for actor: %w", err)
		}

		if !authorized {
			span.SetAttributes(attribute.Bool("actor.not_authorized", true))
			return unauthorizedResponse, nil, nil
		}
	}

	posts, newCursor, err := labelFeed.GetPage(ctx, userDID, limit, cursor)
	if err != nil {
		span.SetAttributes(attribute.String("error", err.Error()))
		return nil, nil, fmt.Errorf("error getting posts for feed (%s): %w", feed, err)
	}

	return posts, newCursor, nil
}

func (f *Feed) Describe(ctx context.Context) ([]appbsky.FeedDescribeFeedGenerator_Feed, error) {
	feeds := []appbsky.FeedDescribeFeedGenerator_Feed{}
	for name := range f.Labels {
		feeds = append(feeds, appbsky.FeedDescribeFeedGenerator_Feed{
			Uri: "at://" + f.FeedActorDID + "/app.bsky.feed.generator/" + name,
		})
	}

	return feeds, nil
}

func (f *Feed) GetMPLSPage(ctx context.Context, userDID string, limit int64, cursor string) ([]*appbsky.FeedDefs_SkeletonFeedPost, *string, error) {
	ctx, span := tracer.Start(ctx, "GetMPLSPage")
	defer span.End()

	if userDID == "" {
		return unauthorizedResponse, nil, nil
	}

	var cursorTime time.Time
	if cursor == "" {
		cursorTime = time.Now().Add(time.Hour)
	} else {
		var err error
		cursorTime, err = time.Parse(time.RFC3339Nano, cursor)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid cursor: %w", err)
		}
	}

	posts, err := f.Store.ListMPLS(ctx, cursorTime, int(limit))
	if err != nil {
		return nil, nil, fmt.Errorf("error getting posts from DB for feed (%s): %w", "mpls", err)
	}

	feedPosts := []*appbsky.FeedDefs_SkeletonFeedPost{}
	newCursor := ""
	for _, post := range posts {
		postAtURL := fmt.Sprintf("at://%s/app.bsky.feed.post/%s", post.ActorDID, post.Rkey)
		feedPosts = append(feedPosts, &appbsky.FeedDefs_SkeletonFeedPost{
			Post: postAtURL,
		})
		newCursor = post.IndexedAt.Format(time.RFC3339Nano)
	}

	if int64(len(posts)) < limit {
		return feedPosts, nil, nil
	}

	return feedPosts, &newCursor, nil
}

func (f *Feed) GetTQSPPage(ctx context.Context, userDID string, limit int64, cursor string) ([]*appbsky.FeedDefs_SkeletonFeedPost, *string, error) {
	ctx, span := tracer.Start(ctx, "GetTQSPPage")
	defer span.End()

	if cursor == "" {
		cursor = "~"
	}

	posts, err := f.Store.ListTQSP(ctx, cursor, int(limit))
	if err != nil {
		return nil, nil, fmt.Errorf("error getting posts from DB for feed (%s): %w", "tqsp", err)
	}

	feedPosts := []*appbsky.FeedDefs_SkeletonFeedPost{}
	newCursor := ""
	for _, post := range posts {
		postAtURL := fmt.Sprintf("at://%s/app.bsky.feed.post/%s", post.ActorDID, post.Rkey)
		feedPosts = append(feedPosts, &appbsky.FeedDefs_SkeletonFeedPost{
			Post: postAtURL,
		})
		newCursor = post.Rkey
	}

	if int64(len(posts)) < limit {
		return feedPosts, nil, nil
	}

	return feedPosts, &newCursor, nil
}
