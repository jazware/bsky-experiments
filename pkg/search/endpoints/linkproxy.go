package endpoints

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bluesky-social/indigo/api/bsky"
	"github.com/bluesky-social/indigo/atproto/syntax"
	"github.com/jazware/bsky-experiments/pkg/search/postcard"
	"github.com/labstack/echo/v4"
)

// Link proxy: bsky.app-shaped URLs on this host serve rich OpenGraph embeds
// to link-preview crawlers (with a rendered screenshot of the post as the
// image) and redirect humans straight to bsky.app. Because hydration uses an
// authenticated AppView session, posts from accounts that opted out of
// logged-out visibility still get full previews.

const (
	cardPNGCacheTTL  = 10 * time.Minute
	cardMaxReplies   = 2
	ogDescriptionMax = 300
	cardPNGCachePfx  = "embed:cardpng:v4:"
	bskyAppHost      = "https://bsky.app"
)

var ogPageTemplate = template.Must(template.New("og").Parse(`<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>{{.Title}}</title>
<link rel="canonical" href="{{.BskyURL}}">
<meta property="og:site_name" content="Bluesky">
<meta property="og:type" content="article">
<meta property="og:url" content="{{.BskyURL}}">
<meta property="og:title" content="{{.Title}}">
<meta property="og:description" content="{{.Description}}">
{{if .ImageURL}}<meta property="og:image" content="{{.ImageURL}}">
{{if .ImageAlt}}<meta property="og:image:alt" content="{{.ImageAlt}}">{{end}}
{{end}}<meta name="twitter:card" content="{{.TwitterCard}}">
<meta name="twitter:title" content="{{.Title}}">
<meta name="twitter:description" content="{{.Description}}">
{{if .ImageURL}}<meta name="twitter:image" content="{{.ImageURL}}">{{end}}
<meta name="theme-color" content="#1083fe">
{{if .OEmbedURL}}<link rel="alternate" type="application/json+oembed" href="{{.OEmbedURL}}" title="{{.Title}}">{{end}}
</head>
<body>
<p>Redirecting to <a href="{{.BskyURL}}">{{.BskyURL}}</a>…</p>
<script>location.replace({{.BskyURL}});</script>
</body>
</html>
`))

type ogPageData struct {
	Title       string
	Description string
	BskyURL     string
	ImageURL    string
	ImageAlt    string
	TwitterCard string
	OEmbedURL   string
}

// publicBaseURL returns the externally-visible base URL for building
// absolute og:image / oEmbed links
func (api *API) publicBaseURL(c echo.Context) string {
	if api.PublicURL != "" {
		return strings.TrimSuffix(api.PublicURL, "/")
	}
	return c.Scheme() + "://" + c.Request().Host
}

// shouldServeEmbed decides between an OG preview page and a redirect
func shouldServeEmbed(c echo.Context) bool {
	if c.QueryParams().Has("embed") {
		return true
	}
	return isCrawlerUA(c.Request().UserAgent())
}

func truncateForOG(s string) string {
	runes := []rune(s)
	if len(runes) <= ogDescriptionMax {
		return s
	}
	return strings.TrimSpace(string(runes[:ogDescriptionMax])) + "…"
}

func actorDisplayName(name *string, handle string) string {
	if name != nil && *name != "" {
		return *name
	}
	return handle
}

// GetPostLinkProxy handles GET /profile/:ident/post/:rkey
func (api *API) GetPostLinkProxy(c echo.Context) error {
	ctx := c.Request().Context()
	ctx, span := tracer.Start(ctx, "GetPostLinkProxy")
	defer span.End()

	ident := c.Param("ident")
	rkey := c.Param("rkey")
	bskyURL := fmt.Sprintf("%s/profile/%s/post/%s", bskyAppHost, ident, rkey)

	if !shouldServeEmbed(c) {
		return c.Redirect(http.StatusFound, bskyURL)
	}

	atURI := fmt.Sprintf("at://%s/app.bsky.feed.post/%s", ident, rkey)
	thread, err := api.Appview.GetPostThread(ctx, atURI)
	if err != nil || thread.Post == nil {
		// Never break the link: fall back to a plain redirect
		api.Logger.Warn("failed to hydrate post for embed", "uri", atURI, "error", err)
		return c.Redirect(http.StatusFound, bskyURL)
	}
	post := thread.Post

	title := "Bluesky"
	avatar := ""
	if post.Author != nil {
		title = fmt.Sprintf("%s (@%s)", actorDisplayName(post.Author.DisplayName, post.Author.Handle), post.Author.Handle)
		if post.Author.Avatar != nil {
			avatar = *post.Author.Avatar
		}
	}

	text := ""
	if post.Record != nil {
		if rec, ok := post.Record.Val.(*bsky.FeedPost); ok {
			text = rec.Text
		}
	}

	stats := fmt.Sprintf("💬 %d   🔁 %d   ❤️ %d",
		derefInt64(post.ReplyCount), derefInt64(post.RepostCount), derefInt64(post.LikeCount))
	description := stats
	if text != "" {
		description = truncateForOG(text) + "\n\n" + stats
	}

	base := api.publicBaseURL(c)
	data := ogPageData{
		Title:       title,
		Description: description,
		BskyURL:     bskyURL,
		TwitterCard: "summary_large_image",
		OEmbedURL:   fmt.Sprintf("%s/oembed?url=%s", base, url.QueryEscape(bskyURL)),
	}

	if api.Renderer != nil {
		// Use the canonical DID form so the card image caches once per post
		did, canonicalRkey := ident, rkey
		if uri, err := syntax.ParseATURI(post.Uri); err == nil {
			did = uri.Authority().String()
			canonicalRkey = uri.RecordKey().String()
		}
		data.ImageURL = fmt.Sprintf("%s/embed/post/%s/%s/card.png", base, did, canonicalRkey)
		data.ImageAlt = truncateForOG(text)
	} else {
		data.ImageURL, data.ImageAlt = staticPostImage(post)
		if data.ImageURL == "" && avatar != "" {
			data.ImageURL = avatar
			data.TwitterCard = "summary"
		}
	}

	return renderOGPage(c, &data)
}

// GetProfileLinkProxy handles GET /profile/:ident
func (api *API) GetProfileLinkProxy(c echo.Context) error {
	ctx := c.Request().Context()
	ctx, span := tracer.Start(ctx, "GetProfileLinkProxy")
	defer span.End()

	ident := c.Param("ident")
	bskyURL := fmt.Sprintf("%s/profile/%s", bskyAppHost, ident)

	if !shouldServeEmbed(c) {
		return c.Redirect(http.StatusFound, bskyURL)
	}

	profile, err := api.Appview.GetProfile(ctx, ident)
	if err != nil {
		api.Logger.Warn("failed to hydrate profile for embed", "actor", ident, "error", err)
		return c.Redirect(http.StatusFound, bskyURL)
	}

	description := fmt.Sprintf("👥 %d followers · %d following · %d posts",
		derefInt64(profile.FollowersCount), derefInt64(profile.FollowsCount), derefInt64(profile.PostsCount))
	if profile.Description != nil && *profile.Description != "" {
		description = truncateForOG(*profile.Description) + "\n\n" + description
	}

	base := api.publicBaseURL(c)
	data := ogPageData{
		Title:       fmt.Sprintf("%s (@%s)", actorDisplayName(profile.DisplayName, profile.Handle), profile.Handle),
		Description: description,
		BskyURL:     bskyURL,
		TwitterCard: "summary",
		OEmbedURL:   fmt.Sprintf("%s/oembed?url=%s", base, url.QueryEscape(bskyURL)),
	}
	if profile.Avatar != nil {
		data.ImageURL = *profile.Avatar
	}

	return renderOGPage(c, &data)
}

// GetPostCardImage handles GET /embed/post/:ident/:rkey/card.png
func (api *API) GetPostCardImage(c echo.Context) error {
	ctx := c.Request().Context()
	ctx, span := tracer.Start(ctx, "GetPostCardImage")
	defer span.End()

	if api.Renderer == nil {
		return c.String(http.StatusNotFound, "card rendering is not available")
	}

	ident := c.Param("ident")
	rkey := c.Param("rkey")
	atURI := fmt.Sprintf("at://%s/app.bsky.feed.post/%s", ident, rkey)

	thread, err := api.Appview.GetPostThread(ctx, atURI)
	if err != nil || thread.Post == nil {
		return c.String(http.StatusNotFound, "post not found")
	}

	cacheKey := cardPNGCachePfx + thread.Post.Uri
	if api.Redis != nil {
		if png, err := api.Redis.Get(ctx, cacheKey).Bytes(); err == nil {
			c.Response().Header().Set("Cache-Control", "public, max-age=600")
			return c.Blob(http.StatusOK, "image/png", png)
		}
	}

	card := postcard.BuildCard(thread, hostname(api.publicBaseURL(c)), cardMaxReplies)
	html, err := postcard.RenderHTML(card)
	if err != nil {
		api.Logger.Error("failed to render card HTML", "uri", atURI, "error", err)
		return c.String(http.StatusInternalServerError, "failed to render card")
	}

	png, err := api.Renderer.RenderPNG(ctx, html)
	if err != nil {
		api.Logger.Error("failed to screenshot card", "uri", atURI, "error", err)
		return c.String(http.StatusInternalServerError, "failed to render card")
	}

	if api.Redis != nil {
		if err := api.Redis.Set(ctx, cacheKey, png, cardPNGCacheTTL).Err(); err != nil {
			api.Logger.Warn("failed to cache card png", "error", err)
		}
	}

	c.Response().Header().Set("Cache-Control", "public, max-age=600")
	return c.Blob(http.StatusOK, "image/png", png)
}

// oEmbedResponse is a minimal oEmbed "link" payload; Discord and Mastodon
// use it to show provider/author lines above the embed
type oEmbedResponse struct {
	Version      string `json:"version"`
	Type         string `json:"type"`
	AuthorName   string `json:"author_name,omitempty"`
	AuthorURL    string `json:"author_url,omitempty"`
	ProviderName string `json:"provider_name"`
	ProviderURL  string `json:"provider_url"`
}

// GetOEmbed handles GET /oembed?url=...
func (api *API) GetOEmbed(c echo.Context) error {
	ctx := c.Request().Context()
	ctx, span := tracer.Start(ctx, "GetOEmbed")
	defer span.End()

	if format := c.QueryParam("format"); format != "" && format != "json" {
		return c.String(http.StatusNotImplemented, "only json is supported")
	}

	resp := oEmbedResponse{
		Version:      "1.0",
		Type:         "link",
		ProviderName: "Bluesky",
		ProviderURL:  bskyAppHost,
	}

	rawURL := c.QueryParam("url")
	if u, err := url.Parse(rawURL); err == nil {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) >= 2 && parts[0] == "profile" {
			if profile, err := api.Appview.GetProfile(ctx, parts[1]); err == nil {
				resp.AuthorName = fmt.Sprintf("%s (@%s)", actorDisplayName(profile.DisplayName, profile.Handle), profile.Handle)
				resp.AuthorURL = fmt.Sprintf("%s/profile/%s", bskyAppHost, profile.Handle)
			}
		}
	}

	return c.JSON(http.StatusOK, resp)
}

func renderOGPage(c echo.Context, data *ogPageData) error {
	var buf bytes.Buffer
	if err := ogPageTemplate.Execute(&buf, data); err != nil {
		return c.Redirect(http.StatusFound, data.BskyURL)
	}
	c.Response().Header().Set("Cache-Control", "public, max-age=300")
	return c.HTMLBlob(http.StatusOK, buf.Bytes())
}

// staticPostImage picks a direct CDN image for posts when no renderer is
// available: first embedded image, else video thumbnail
func staticPostImage(post *bsky.FeedDefs_PostView) (imgURL, alt string) {
	if post.Embed == nil {
		return "", ""
	}
	embed := post.Embed
	if embed.EmbedRecordWithMedia_View != nil && embed.EmbedRecordWithMedia_View.Media != nil {
		m := embed.EmbedRecordWithMedia_View.Media
		if m.EmbedImages_View != nil && len(m.EmbedImages_View.Images) > 0 {
			return m.EmbedImages_View.Images[0].Fullsize, m.EmbedImages_View.Images[0].Alt
		}
		if m.EmbedVideo_View != nil && m.EmbedVideo_View.Thumbnail != nil {
			return *m.EmbedVideo_View.Thumbnail, ""
		}
	}
	if embed.EmbedImages_View != nil && len(embed.EmbedImages_View.Images) > 0 {
		return embed.EmbedImages_View.Images[0].Fullsize, embed.EmbedImages_View.Images[0].Alt
	}
	if embed.EmbedVideo_View != nil && embed.EmbedVideo_View.Thumbnail != nil {
		return *embed.EmbedVideo_View.Thumbnail, ""
	}
	return "", ""
}

func derefInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func hostname(baseURL string) string {
	if u, err := url.Parse(baseURL); err == nil && u.Host != "" {
		return u.Host
	}
	return baseURL
}
