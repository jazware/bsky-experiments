// Package appview provides an authenticated Bluesky AppView client used to
// hydrate posts, threads, and profiles for link-preview embeds. Requests are
// authenticated with an app password so content hidden from logged-out
// viewers (the "discourage public visibility" setting) is still returned.
package appview

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	comatproto "github.com/bluesky-social/indigo/api/atproto"
	"github.com/bluesky-social/indigo/api/bsky"
	"github.com/bluesky-social/indigo/xrpc"
	"github.com/goccy/go-json"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
)

var tracer = otel.Tracer("appview-client")

const (
	// PublicAPIHost serves unauthenticated requests when no credentials are configured
	PublicAPIHost = "https://public.api.bsky.app"
	// DefaultPDSHost is the entryway that proxies authenticated requests to the AppView
	DefaultPDSHost = "https://bsky.social"

	userAgent = "jaz-link-embed/1.0"
)

// Client is a Bluesky AppView client with optional app-password auth and
// Redis-backed response caching.
type Client struct {
	logger   *slog.Logger
	xrpcc    *xrpc.Client
	cache    *redis.Client
	cacheTTL time.Duration

	identifier string
	password   string

	authLk sync.Mutex
}

// NewClient creates an AppView client. If identifier and password are empty,
// requests are made unauthenticated against the public AppView (or pdsHost,
// when it is set to something other than the default entryway).
func NewClient(logger *slog.Logger, pdsHost, identifier, password string, cache *redis.Client, cacheTTL time.Duration) *Client {
	ua := userAgent
	host := pdsHost
	if host == "" {
		host = DefaultPDSHost
	}
	if identifier == "" && host == DefaultPDSHost {
		// the entryway needs auth; anonymous requests go to the public AppView
		host = PublicAPIHost
	}

	return &Client{
		logger: logger.With("component", "appview"),
		xrpcc: &xrpc.Client{
			Host:      host,
			UserAgent: &ua,
		},
		cache:      cache,
		cacheTTL:   cacheTTL,
		identifier: identifier,
		password:   password,
	}
}

// Authenticated reports whether the client has credentials configured
func (c *Client) Authenticated() bool {
	return c.identifier != ""
}

// ensureSession makes sure we hold a valid access token (no-op when unauthenticated)
func (c *Client) ensureSession(ctx context.Context) error {
	if !c.Authenticated() {
		return nil
	}

	c.authLk.Lock()
	defer c.authLk.Unlock()
	if c.xrpcc.Auth != nil {
		return nil
	}
	return c.createSession(ctx)
}

// createSession must be called with authLk held
func (c *Client) createSession(ctx context.Context) error {
	ctx, span := tracer.Start(ctx, "CreateSession")
	defer span.End()

	out, err := comatproto.ServerCreateSession(ctx, c.xrpcc, &comatproto.ServerCreateSession_Input{
		Identifier: c.identifier,
		Password:   c.password,
	})
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}

	c.xrpcc.Auth = &xrpc.AuthInfo{
		AccessJwt:  out.AccessJwt,
		RefreshJwt: out.RefreshJwt,
		Handle:     out.Handle,
		Did:        out.Did,
	}
	c.logger.Info("created appview session", "did", out.Did)
	return nil
}

// refreshSession refreshes the access token, falling back to a fresh login
func (c *Client) refreshSession(ctx context.Context) error {
	c.authLk.Lock()
	defer c.authLk.Unlock()

	if c.xrpcc.Auth == nil {
		return c.createSession(ctx)
	}

	ctx, span := tracer.Start(ctx, "RefreshSession")
	defer span.End()

	// refreshSession authenticates with the refresh token in the bearer slot
	refreshClient := *c.xrpcc
	refreshClient.Auth = &xrpc.AuthInfo{AccessJwt: c.xrpcc.Auth.RefreshJwt}

	out, err := comatproto.ServerRefreshSession(ctx, &refreshClient)
	if err != nil {
		c.logger.Warn("session refresh failed, attempting fresh login", "error", err)
		c.xrpcc.Auth = nil
		return c.createSession(ctx)
	}

	c.xrpcc.Auth = &xrpc.AuthInfo{
		AccessJwt:  out.AccessJwt,
		RefreshJwt: out.RefreshJwt,
		Handle:     out.Handle,
		Did:        out.Did,
	}
	return nil
}

// isAuthError reports whether the error is an expired/invalid token error
func isAuthError(err error) bool {
	var xerr *xrpc.Error
	if !errors.As(err, &xerr) {
		return false
	}
	if xerr.StatusCode != 400 && xerr.StatusCode != 401 {
		return false
	}
	var xrpcErr *xrpc.XRPCError
	if errors.As(xerr.Wrapped, &xrpcErr) {
		switch xrpcErr.ErrStr {
		case "ExpiredToken", "InvalidToken", "AuthenticationRequired":
			return true
		}
	}
	return strings.Contains(err.Error(), "ExpiredToken")
}

// do runs an XRPC call, refreshing the session and retrying once on auth errors
func (c *Client) do(ctx context.Context, call func() error) error {
	if err := c.ensureSession(ctx); err != nil {
		return err
	}

	err := call()
	if err != nil && c.Authenticated() && isAuthError(err) {
		if rerr := c.refreshSession(ctx); rerr != nil {
			return fmt.Errorf("failed to refresh session: %w", rerr)
		}
		err = call()
	}
	return err
}

// GetPostThread fetches a post and one level of replies by AT-URI
func (c *Client) GetPostThread(ctx context.Context, atURI string) (*bsky.FeedDefs_ThreadViewPost, error) {
	ctx, span := tracer.Start(ctx, "GetPostThread")
	defer span.End()

	cacheKey := "embed:thread:v1:" + atURI
	if cached, ok := c.cacheGet(ctx, cacheKey); ok {
		var thread bsky.FeedGetPostThread_Output_Thread
		if err := json.Unmarshal(cached, &thread); err == nil && thread.FeedDefs_ThreadViewPost != nil {
			return thread.FeedDefs_ThreadViewPost, nil
		}
	}

	var out *bsky.FeedGetPostThread_Output
	err := c.do(ctx, func() error {
		var err error
		out, err = bsky.FeedGetPostThread(ctx, c.xrpcc, 1, 0, atURI)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get post thread: %w", err)
	}

	if out.Thread == nil || out.Thread.FeedDefs_ThreadViewPost == nil {
		return nil, fmt.Errorf("post not found or not viewable: %s", atURI)
	}

	if data, err := json.Marshal(out.Thread); err == nil {
		c.cacheSet(ctx, cacheKey, data)
	}

	return out.Thread.FeedDefs_ThreadViewPost, nil
}

// GetProfile fetches a profile by handle or DID
func (c *Client) GetProfile(ctx context.Context, actor string) (*bsky.ActorDefs_ProfileViewDetailed, error) {
	ctx, span := tracer.Start(ctx, "GetProfile")
	defer span.End()

	cacheKey := "embed:profile:v1:" + actor
	if cached, ok := c.cacheGet(ctx, cacheKey); ok {
		var profile bsky.ActorDefs_ProfileViewDetailed
		if err := json.Unmarshal(cached, &profile); err == nil {
			return &profile, nil
		}
	}

	var profile *bsky.ActorDefs_ProfileViewDetailed
	err := c.do(ctx, func() error {
		var err error
		profile, err = bsky.ActorGetProfile(ctx, c.xrpcc, actor)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get profile: %w", err)
	}

	if data, err := json.Marshal(profile); err == nil {
		c.cacheSet(ctx, cacheKey, data)
	}

	return profile, nil
}

func (c *Client) cacheGet(ctx context.Context, key string) ([]byte, bool) {
	if c.cache == nil {
		return nil, false
	}
	data, err := c.cache.Get(ctx, key).Bytes()
	if err != nil {
		return nil, false
	}
	return data, true
}

func (c *Client) cacheSet(ctx context.Context, key string, data []byte) {
	if c.cache == nil {
		return
	}
	if err := c.cache.Set(ctx, key, data, c.cacheTTL).Err(); err != nil {
		c.logger.Warn("failed to cache appview response", "key", key, "error", err)
	}
}
