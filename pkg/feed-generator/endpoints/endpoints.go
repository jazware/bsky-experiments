package endpoints

import (
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bits-and-blooms/bloom/v3"
	appbsky "github.com/bluesky-social/indigo/api/bsky"
	"github.com/bluesky-social/indigo/atproto/identity"
	"github.com/bluesky-social/indigo/atproto/syntax"
	"github.com/jazware/bsky-experiments/pkg/auth"
	feedgenerator "github.com/jazware/bsky-experiments/pkg/feed-generator"
	"github.com/jazware/bsky-experiments/pkg/indexer/store"
	"golang.org/x/time/rate"

	"github.com/kwertop/gostatix"
	"github.com/labstack/echo/v4"
	"github.com/whyrusleeping/go-did"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

type DescriptionCacheItem struct {
	Description appbsky.FeedDescribeFeedGenerator_Output
	ExpiresAt   time.Time
}

type Endpoints struct {
	FeedGenerator   *feedgenerator.FeedGenerator
	FeedUsers       map[string][]string
	usersLk         sync.RWMutex
	UniqueSeenUsers *bloom.BloomFilter

	tkLk              sync.Mutex
	TopKUsersAndFeeds *gostatix.TopK

	dir *identity.CacheDirectory

	Store         *store.Store
	BatchInserter *store.BatchInserter

	DescriptionCache    *DescriptionCacheItem
	DescriptionCacheTTL time.Duration
}

type DidResponse struct {
	Context []string      `json:"@context"`
	ID      string        `json:"id"`
	Service []did.Service `json:"service"`
}

// Request types for Echo binding
type GetFeedSkeletonRequest struct {
	Feed   string `query:"feed"`
	Limit  string `query:"limit"`
	Cursor string `query:"cursor"`
}

type FeedUserRequest struct {
	FeedName string `query:"feedName"`
	Handle   string `query:"handle"`
}

type GetFeedMembersRequest struct {
	FeedName string `query:"feedName"`
}

type GetFeedMembersPaginatedRequest struct {
	FeedName string `query:"feedName"`
	Limit    int    `query:"limit"`
	Page     int    `query:"page"`
	Search   string `query:"search"`
}

type FeedMemberResponse struct {
	DID         string `json:"did"`
	Handle      string `json:"handle,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	CreatedAt   string `json:"createdAt"`
}

type GetFeedMembersPaginatedResponse struct {
	Members    []FeedMemberResponse `json:"members"`
	Page       int                  `json:"page"`
	TotalPages int                  `json:"totalPages"`
	TotalCount uint64               `json:"totalCount"`
}

func NewEndpoints(feedGenerator *feedgenerator.FeedGenerator, chStore *store.Store, batchInserter *store.BatchInserter) (*Endpoints, error) {
	uniqueSeenUsers := bloom.NewWithEstimates(1000000, 0.01)

	base := identity.BaseDirectory{
		PLCURL: "https://plc.directory",
		HTTPClient: http.Client{
			Timeout: time.Second * 15,
		},
		PLCLimiter:            rate.NewLimiter(rate.Limit(10), 1),
		TryAuthoritativeDNS:   true,
		SkipDNSDomainSuffixes: []string{".bsky.social"},
	}
	dir := identity.NewCacheDirectory(&base, 250_000, time.Hour*24, time.Minute*2, time.Minute*5)

	newTopK := func() *gostatix.TopK {
		return gostatix.NewTopK(512, 0.0001, 0.999)
	}

	ep := Endpoints{
		FeedGenerator:       feedGenerator,
		UniqueSeenUsers:     uniqueSeenUsers,
		FeedUsers:           map[string][]string{},
		dir:                 dir,
		Store:               chStore,
		BatchInserter:       batchInserter,
		DescriptionCacheTTL: 30 * time.Minute,
		TopKUsersAndFeeds:   newTopK(),
	}

	// Start a routine to periodically flush the top k users and feeds
	go func() {
		t := time.NewTicker(time.Minute * 2)
		for {
			select {
			case <-t.C:
				ep.tkLk.Lock()
				vals := ep.TopKUsersAndFeeds.Values()
				ep.TopKUsersAndFeeds = newTopK()
				ep.tkLk.Unlock()
				topDIDFeedPairs := make([]string, 0, len(vals))
				for _, v := range vals {
					if v.Count > 5 {
						topDIDFeedPairs = append(topDIDFeedPairs, fmt.Sprintf("%s_%d", v.Element, v.Count))
					}
				}
				slog.Info("Top users and feeds", "top", strings.Join(topDIDFeedPairs, ", "))
			}
		}
	}()

	return &ep, nil
}

func (ep *Endpoints) GetWellKnownDID(c echo.Context) error {
	tracer := otel.Tracer("feedgenerator")
	_, span := tracer.Start(c.Request().Context(), "FeedGenerator:Endpoints:GetWellKnownDID")
	defer span.End()

	// Use a custom struct to fix missing omitempty on did.Document
	didResponse := DidResponse{
		Context: ep.FeedGenerator.DIDDocument.Context,
		ID:      ep.FeedGenerator.DIDDocument.ID.String(),
		Service: ep.FeedGenerator.DIDDocument.Service,
	}

	return c.JSON(http.StatusOK, didResponse)
}

func (ep *Endpoints) DescribeFeedGenerator(c echo.Context) error {
	tracer := otel.Tracer("feedgenerator")
	ctx, span := tracer.Start(c.Request().Context(), "FeedGenerator:Endpoints:DescribeFeedGenerator")
	defer span.End()

	if ep.DescriptionCache != nil && ep.DescriptionCache.ExpiresAt.After(time.Now()) {
		span.SetAttributes(attribute.String("cache.hit", "true"))
		return c.JSON(http.StatusOK, ep.DescriptionCache.Description)
	}

	span.SetAttributes(attribute.String("cache.hit", "false"))

	feedDescriptions := []*appbsky.FeedDescribeFeedGenerator_Feed{}

	for _, feed := range ep.FeedGenerator.Feeds {
		newDescriptions, err := feed.Describe(ctx)
		if err != nil {
			span.RecordError(err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}

		for _, newDescription := range newDescriptions {
			description := newDescription
			feedDescriptions = append(feedDescriptions, &description)
		}
	}

	span.SetAttributes(attribute.Int("feeds.length", len(feedDescriptions)))

	feedGeneratorDescription := appbsky.FeedDescribeFeedGenerator_Output{
		Did:   ep.FeedGenerator.FeedActorDID.String(),
		Feeds: feedDescriptions,
	}

	ep.DescriptionCache = &DescriptionCacheItem{
		Description: feedGeneratorDescription,
		ExpiresAt:   time.Now().Add(ep.DescriptionCacheTTL),
	}

	return c.JSON(http.StatusOK, feedGeneratorDescription)
}

func (ep *Endpoints) GetFeedSkeleton(c echo.Context) error {
	// Incoming requests should have a query parameter "feed" that looks like:
	// 		at://did:web:feedsky.jazco.io/app.bsky.feed.generator/feed-name
	// Also a query parameter "limit" that looks like: 50
	// Also a query parameter "cursor" that is either the empty string
	// or the cursor returned from a previous request
	tracer := otel.Tracer("feed-generator")
	ctx, span := tracer.Start(c.Request().Context(), "FeedGenerator:Endpoints:GetFeedSkeleton")
	defer span.End()

	var req GetFeedSkeletonRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	// Get userDID from the request context, which is set by the auth middleware
	userDID, _ := c.Get("actor_did").(string)

	start := time.Now()

	if req.Feed == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "feed query parameter is required"})
	}

	c.Set("feedQuery", req.Feed)
	span.SetAttributes(attribute.String("feed.query", req.Feed))

	feedPrefix := ""
	for _, acceptablePrefix := range ep.FeedGenerator.AcceptableURIPrefixes {
		if strings.HasPrefix(req.Feed, acceptablePrefix) {
			feedPrefix = acceptablePrefix
			break
		}
	}

	if feedPrefix == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "this feed generator does not serve feeds for the given DID"})
	}

	// Get the feed name from the query
	feedName := strings.TrimPrefix(req.Feed, feedPrefix)
	if feedName == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "feed name is required"})
	}

	// Count the user
	ep.ProcessUser(feedName, userDID)

	span.SetAttributes(attribute.String("feed.name", feedName))
	c.Set("feedName", feedName)
	feedRequestCounter.WithLabelValues(feedName).Inc()

	// Get the limit from the query, default to 50, maximum of 250
	limit := int64(50)
	span.SetAttributes(attribute.String("feed.limit.raw", req.Limit))
	if req.Limit != "" {
		parsedLimit, err := strconv.ParseInt(req.Limit, 10, 64)
		if err != nil {
			span.SetAttributes(attribute.Bool("feed.limit.failed_to_parse", true))
			limit = 50
		} else {
			limit = parsedLimit
			if limit > 250 {
				span.SetAttributes(attribute.Bool("feed.limit.clamped", true))
				limit = 250
			}
		}
	}

	span.SetAttributes(attribute.Int64("feed.limit.parsed", limit))

	c.Set("cursor", req.Cursor)

	if ep.FeedGenerator.FeedMap == nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "feed generator has no feeds configured"})
	}

	feed, ok := ep.FeedGenerator.FeedMap[feedName]
	if !ok {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "feed not found"})
	}

	// Get the feed items
	feedItems, newCursor, err := feed.GetPage(ctx, feedName, userDID, limit, req.Cursor)
	if err != nil {
		span.RecordError(err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("failed to get feed items: %s", err.Error())})
	}

	span.SetAttributes(attribute.Int("feed.items.length", len(feedItems)))

	feedRequestLatency.WithLabelValues(feedName).Observe(time.Since(start).Seconds())

	// Track analytics for authenticated users with cursors (real pagination)
	if userDID != "" && req.Cursor != "" && ep.BatchInserter != nil {
		ep.BatchInserter.AddFeedRequestAnalytics(&store.FeedRequestAnalyticsBatch{
			UserDID:        userDID,
			FeedName:       feedName,
			LimitRequested: uint16(limit),
			HasCursor:      1,
			RequestedAt:    time.Now(),
			TimeUS:         time.Now().UnixMicro(),
		})
	}

	return c.JSON(http.StatusOK, appbsky.FeedGetFeedSkeleton_Output{
		Feed:   feedItems,
		Cursor: newCursor,
	})
}

func (ep *Endpoints) ProcessUser(feedName string, userDID string) {
	// Check if the user has ever been seen before
	if !ep.UniqueSeenUsers.TestString(userDID) {
		ep.UniqueSeenUsers.AddString(userDID)
		uniqueFeedUserCounter.Inc()
	}

	ep.tkLk.Lock()
	// Update the top k users and feeds
	key := fmt.Sprintf("%s_%s", userDID, feedName)
	ep.TopKUsersAndFeeds.Insert([]byte(key), 1)
	ep.tkLk.Unlock()

	ep.usersLk.Lock()
	defer ep.usersLk.Unlock()
	// Check if the feed user list exists
	if ep.FeedUsers[feedName] == nil {
		// If not, create the feed user list
		ep.FeedUsers[feedName] = []string{
			userDID,
		}
	} else {
		// Check if the user is already in the list
		if slices.Contains(ep.FeedUsers[feedName], userDID) {
			return
		}

		ep.FeedUsers[feedName] = append(ep.FeedUsers[feedName], userDID)
	}

	feedUserCounter.WithLabelValues(feedName).Inc()
}

//
//
// Additional endpoints below are for private feeds only, not part of a standard Feed Generator implementation
//
//

var binder = echo.DefaultBinder{}

func (ep *Endpoints) AssignUserToFeed(c echo.Context) error {
	tracer := otel.Tracer("feed-generator")
	ctx, span := tracer.Start(c.Request().Context(), "FeedGenerator:Endpoints:AssignUserToFeed")
	defer span.End()

	rawAuthEntity := c.Get("feed.auth.entity")
	if rawAuthEntity == nil {
		span.SetAttributes(attribute.Bool("feed.assign_label.not_authorized", true))
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "not authorized: no user DID in context"})
	}

	// Cast the rawAuthEntity to a FeedAuthEntity
	authEntity, ok := rawAuthEntity.(*auth.FeedAuthEntity)
	if !ok || authEntity == nil {
		span.SetAttributes(attribute.Bool("feed.assign_label.not_authorized", true))
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "not authorized: could not cast auth entity"})
	}

	var req FeedUserRequest
	if err := binder.BindQueryParams(c, &req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	if req.FeedName == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "feedName is required"})
	}

	if authEntity.FeedAlias != req.FeedName {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "not authorized: you are not authorized to assign users to this feed"})
	}

	if req.Handle == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "handle of user to add to feed is required"})
	}

	handle, err := syntax.ParseHandle(req.Handle)
	if err != nil {
		span.SetAttributes(attribute.Bool("feed.assign_label.invalid_handle", true))
		return c.JSON(http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid handle: %s", err.Error())})
	}

	id, err := ep.dir.LookupHandle(ctx, handle)
	if err != nil {
		span.SetAttributes(attribute.Bool("feed.assign_label.handle_not_found", true))
		return c.JSON(http.StatusNotFound, map[string]string{"error": fmt.Sprintf("failed to resolve handle to did: %s", err.Error())})
	}

	err = ep.Store.CreateActorLabel(ctx, id.DID.String(), req.FeedName)
	if err != nil {
		slog.Error("failed to assign label to user", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to assign label"})
	}

	return c.JSON(http.StatusOK, map[string]string{"message": "success"})
}

func (ep *Endpoints) UnassignUserFromFeed(c echo.Context) error {
	tracer := otel.Tracer("feed-generator")
	ctx, span := tracer.Start(c.Request().Context(), "FeedGenerator:Endpoints:UnassignUserFromFeed")
	defer span.End()

	rawAuthEntity := c.Get("feed.auth.entity")
	if rawAuthEntity == nil {
		span.SetAttributes(attribute.Bool("feed.assign_label.not_authorized", true))
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "not authorized: no user DID in context"})
	}

	// Cast the rawAuthEntity to a FeedAuthEntity
	authEntity, ok := rawAuthEntity.(*auth.FeedAuthEntity)
	if !ok || authEntity == nil {
		span.SetAttributes(attribute.Bool("feed.assign_label.not_authorized", true))
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "not authorized: could not cast auth entity"})
	}

	var req FeedUserRequest
	if err := binder.BindQueryParams(c, &req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	if req.FeedName == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "feedName is required"})
	}

	if authEntity.FeedAlias != req.FeedName {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "not authorized: you are not authorized to assign users to this feed"})
	}

	if req.Handle == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "handle of user to remove from feed is required"})
	}

	var userDID string

	// Check if the input is already a DID (starts with "did:")
	if strings.HasPrefix(req.Handle, "did:") {
		userDID = req.Handle
	} else {
		// Parse as handle and resolve to DID
		handle, err := syntax.ParseHandle(req.Handle)
		if err != nil {
			span.SetAttributes(attribute.Bool("feed.assign_label.invalid_handle", true))
			return c.JSON(http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid handle: %s", err.Error())})
		}

		id, err := ep.dir.LookupHandle(ctx, handle)
		if err != nil {
			span.SetAttributes(attribute.Bool("feed.assign_label.handle_not_found", true))
			return c.JSON(http.StatusNotFound, map[string]string{"error": fmt.Sprintf("failed to resolve handle to did: %s", err.Error())})
		}
		userDID = id.DID.String()
	}

	err := ep.Store.DeleteActorLabel(ctx, userDID, req.FeedName)
	if err != nil {
		slog.Error("failed to unassign label from user", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to unassign label"})
	}

	return c.JSON(http.StatusOK, map[string]string{"message": "success"})
}

func (ep *Endpoints) GetFeedMembers(c echo.Context) error {
	tracer := otel.Tracer("feed-generator")
	ctx, span := tracer.Start(c.Request().Context(), "FeedGenerator:Endpoints:GetFeedMembers")
	defer span.End()

	rawAuthEntity := c.Get("feed.auth.entity")
	if rawAuthEntity == nil {
		span.SetAttributes(attribute.Bool("feed.get_members.not_authorized", true))
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "not authorized: no user DID in context"})
	}

	// Cast the rawAuthEntity to a FeedAuthEntity
	authEntity, ok := rawAuthEntity.(*auth.FeedAuthEntity)
	if !ok || authEntity == nil {
		span.SetAttributes(attribute.Bool("feed.get_members.not_authorized", true))
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "not authorized: could not cast auth entity"})
	}

	var req GetFeedMembersRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	if req.FeedName == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "feedName is required"})
	}

	if authEntity.FeedAlias != req.FeedName {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "not authorized: you are not authorized to list the users assigned to this feed"})
	}

	authors, err := ep.Store.ListActorsByLabel(ctx, req.FeedName)
	if err != nil {
		span.SetAttributes(attribute.Bool("feed.get_members.error", true))
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("error getting authors: %s", err.Error())})
	}

	return c.JSON(http.StatusOK, map[string]any{"authors": authors})
}

func (ep *Endpoints) GetFeedMembersPaginated(c echo.Context) error {
	tracer := otel.Tracer("feed-generator")
	ctx, span := tracer.Start(c.Request().Context(), "FeedGenerator:Endpoints:GetFeedMembersPaginated")
	defer span.End()

	rawAuthEntity := c.Get("feed.auth.entity")
	if rawAuthEntity == nil {
		span.SetAttributes(attribute.Bool("feed.get_members.not_authorized", true))
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "not authorized: no user DID in context"})
	}

	authEntity, ok := rawAuthEntity.(*auth.FeedAuthEntity)
	if !ok || authEntity == nil {
		span.SetAttributes(attribute.Bool("feed.get_members.not_authorized", true))
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "not authorized: could not cast auth entity"})
	}

	var req GetFeedMembersPaginatedRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	if req.FeedName == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "feedName is required"})
	}

	feedName := req.FeedName
	if _, after, ok := strings.Cut(feedName, "-"); ok {
		feedName = after
	}

	if authEntity.FeedAlias != feedName {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "not authorized: you are not authorized to list the users assigned to this feed"})
	}

	// Default limit and page
	if req.Limit <= 0 {
		req.Limit = 25
	}
	if req.Limit > 100 {
		req.Limit = 100
	}
	if req.Page <= 0 {
		req.Page = 1
	}

	actors, totalCount, err := ep.Store.ListActorsByLabelPaginated(ctx, feedName, req.Page, req.Limit, req.Search)
	if err != nil {
		span.SetAttributes(attribute.Bool("feed.get_members.error", true))
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("error getting authors: %s", err.Error())})
	}

	members := make([]FeedMemberResponse, len(actors))
	for i, actor := range actors {
		members[i] = FeedMemberResponse{
			DID:         actor.ActorDID,
			Handle:      actor.Handle,
			DisplayName: actor.DisplayName,
			CreatedAt:   actor.CreatedAt.Format(time.RFC3339),
		}
	}

	// Calculate total pages
	totalPages := int(totalCount) / req.Limit
	if int(totalCount)%req.Limit > 0 {
		totalPages++
	}

	return c.JSON(http.StatusOK, GetFeedMembersPaginatedResponse{
		Members:    members,
		Page:       req.Page,
		TotalPages: totalPages,
		TotalCount: totalCount,
	})
}

type FeedMeta struct {
	FeedType  string `json:"feed_type"`
	UserCount int    `json:"user_count"`
}

// Debug endpoints for the Feed Generator Admin Dashboard
type GetFeedsResponse struct {
	Feeds map[string]FeedMeta `json:"feeds"`
}

func (ep *Endpoints) GetFeeds(c echo.Context) error {
	_, span := otel.Tracer("feed-generator").Start(c.Request().Context(), "GetFeeds")
	defer span.End()

	// Get the authenticated feed alias from context
	var allowedAlias string
	if rawAuthEntity := c.Get("feed.auth.entity"); rawAuthEntity != nil {
		if authEntity, ok := rawAuthEntity.(*auth.FeedAuthEntity); ok && authEntity != nil {
			allowedAlias = authEntity.FeedAlias
		}
	}

	feeds := make(map[string]FeedMeta)

	ep.usersLk.RLock()
	for alias, feed := range ep.FeedGenerator.FeedMap {
		// Trim the hyphenated prefix if it exists
		trimmedAlias := alias
		if _, after, ok := strings.Cut(alias, "-"); ok {
			trimmedAlias = after
		}

		feedType := reflect.TypeOf(feed).String()
		feedType = strings.TrimPrefix(feedType, "*")

		// If authenticated, only show the feed the API key has access to
		if allowedAlias != "" && trimmedAlias != allowedAlias {
			continue
		}

		feeds[trimmedAlias] = FeedMeta{
			FeedType:  feedType,
			UserCount: len(ep.FeedUsers[alias]),
		}
	}
	ep.usersLk.RUnlock()

	resp := GetFeedsResponse{
		Feeds: feeds,
	}

	return c.JSON(http.StatusOK, resp)
}
