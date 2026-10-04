package endpoints

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jazware/bsky-experiments/pkg/search/appview"
	"github.com/labstack/echo/v4"
)

const threadFixture = `{
	"thread": {
		"$type": "app.bsky.feed.defs#threadViewPost",
		"post": {
			"$type": "app.bsky.feed.defs#postView",
			"uri": "at://did:plc:abc123/app.bsky.feed.post/3kxyz",
			"cid": "bafyreib000000000000000000000000000000000000000000000000000",
			"author": {
				"did": "did:plc:abc123",
				"handle": "alice.test",
				"displayName": "Alice",
				"avatar": "https://cdn.example/avatar.jpg"
			},
			"record": {
				"$type": "app.bsky.feed.post",
				"text": "Hello world from the fixture post",
				"createdAt": "2026-07-01T12:34:56Z"
			},
			"indexedAt": "2026-07-01T12:34:57Z",
			"replyCount": 3,
			"repostCount": 2,
			"likeCount": 40
		},
		"replies": [
			{
				"$type": "app.bsky.feed.defs#threadViewPost",
				"post": {
					"$type": "app.bsky.feed.defs#postView",
					"uri": "at://did:plc:def456/app.bsky.feed.post/3kreply",
					"cid": "bafyreib000000000000000000000000000000000000000000000000001",
					"author": {"did": "did:plc:def456", "handle": "bob.test", "displayName": "Bob"},
					"record": {"$type": "app.bsky.feed.post", "text": "nice post", "createdAt": "2026-07-01T13:00:00Z"},
					"indexedAt": "2026-07-01T13:00:01Z",
					"likeCount": 5
				}
			}
		]
	}
}`

const profileFixture = `{
	"did": "did:plc:abc123",
	"handle": "alice.test",
	"displayName": "Alice",
	"description": "I write tests",
	"avatar": "https://cdn.example/avatar.jpg",
	"followersCount": 100,
	"followsCount": 50,
	"postsCount": 200
}`

func newTestAPI(t *testing.T) *API {
	t.Helper()

	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/xrpc/app.bsky.feed.getPostThread":
			if !strings.Contains(r.URL.Query().Get("uri"), "3kxyz") {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":"NotFound","message":"post not found"}`))
				return
			}
			w.Write([]byte(threadFixture))
		case "/xrpc/app.bsky.actor.getProfile":
			w.Write([]byte(profileFixture))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"MethodNotImplemented"}`))
		}
	}))
	t.Cleanup(stub.Close)

	logger := slog.New(slog.DiscardHandler)
	appviewClient := appview.NewClient(logger, stub.URL, "", "", nil, time.Minute)

	api, err := NewAPI(logger, nil, nil, "", appviewClient, nil, nil, "https://embed.test")
	if err != nil {
		t.Fatalf("NewAPI failed: %v", err)
	}
	return api
}

func doRequest(t *testing.T, path, ua string, handler func(echo.Context) error, pathParams map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	var names, values []string
	for k, v := range pathParams {
		names = append(names, k)
		values = append(values, v)
	}
	c.SetParamNames(names...)
	c.SetParamValues(values...)
	if err := handler(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	return rec
}

const chromeUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

func TestGetPostLinkProxyRedirectsHumans(t *testing.T) {
	api := newTestAPI(t)
	rec := doRequest(t, "/profile/alice.test/post/3kxyz", chromeUA, api.GetPostLinkProxy,
		map[string]string{"ident": "alice.test", "rkey": "3kxyz"})

	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "https://bsky.app/profile/alice.test/post/3kxyz" {
		t.Errorf("unexpected redirect location: %q", loc)
	}
}

func TestGetPostLinkProxyServesOGToCrawlers(t *testing.T) {
	api := newTestAPI(t)
	rec := doRequest(t, "/profile/alice.test/post/3kxyz", "Discordbot/2.0", api.GetPostLinkProxy,
		map[string]string{"ident": "alice.test", "rkey": "3kxyz"})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`og:title" content="Alice (@alice.test)"`,
		"Hello world from the fixture post",
		"💬 3",
		"❤️ 40",
		`og:url" content="https://bsky.app/profile/alice.test/post/3kxyz"`,
		"/oembed?url=",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("OG page missing %q\nbody:\n%s", want, body)
		}
	}

	// No renderer configured: og:image falls back to the author avatar
	if !strings.Contains(body, `og:image" content="https://cdn.example/avatar.jpg"`) {
		t.Errorf("expected avatar fallback og:image, body:\n%s", body)
	}
}

func TestGetPostLinkProxyFallsBackToRedirectOnError(t *testing.T) {
	api := newTestAPI(t)
	rec := doRequest(t, "/profile/alice.test/post/unknown", "Discordbot/2.0", api.GetPostLinkProxy,
		map[string]string{"ident": "alice.test", "rkey": "unknown"})

	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302 fallback, got %d", rec.Code)
	}
}

func TestGetPostLinkProxyEmbedParamForcesOGPage(t *testing.T) {
	api := newTestAPI(t)
	rec := doRequest(t, "/profile/alice.test/post/3kxyz?embed=1", chromeUA, api.GetPostLinkProxy,
		map[string]string{"ident": "alice.test", "rkey": "3kxyz"})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with ?embed, got %d", rec.Code)
	}
}

func TestGetProfileLinkProxy(t *testing.T) {
	api := newTestAPI(t)

	rec := doRequest(t, "/profile/alice.test", chromeUA, api.GetProfileLinkProxy,
		map[string]string{"ident": "alice.test"})
	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302 for humans, got %d", rec.Code)
	}

	rec = doRequest(t, "/profile/alice.test", "Slackbot-LinkExpanding 1.0", api.GetProfileLinkProxy,
		map[string]string{"ident": "alice.test"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for crawlers, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`og:title" content="Alice (@alice.test)"`,
		"I write tests",
		"100 followers",
		`og:image" content="https://cdn.example/avatar.jpg"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("profile OG page missing %q\nbody:\n%s", want, body)
		}
	}
}

func TestGetPostCardImageWithoutRenderer(t *testing.T) {
	api := newTestAPI(t)
	rec := doRequest(t, "/embed/post/did:plc:abc123/3kxyz/card.png", "Discordbot/2.0", api.GetPostCardImage,
		map[string]string{"ident": "did:plc:abc123", "rkey": "3kxyz"})

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when renderer unavailable, got %d", rec.Code)
	}
}

func TestGetOEmbed(t *testing.T) {
	api := newTestAPI(t)
	rec := doRequest(t, "/oembed?url=https%3A%2F%2Fbsky.app%2Fprofile%2Falice.test%2Fpost%2F3kxyz", "Discordbot/2.0", api.GetOEmbed, nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"author_name":"Alice (@alice.test)"`) {
		t.Errorf("oEmbed missing author_name, body: %s", body)
	}
	if !strings.Contains(body, `"provider_name":"Bluesky"`) {
		t.Errorf("oEmbed missing provider_name, body: %s", body)
	}
}
