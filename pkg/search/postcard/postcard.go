// Package postcard renders Bluesky posts as self-contained preview cards:
// it builds a display model from an AppView thread, templates it into HTML,
// and screenshots it with headless Chromium for use as an OpenGraph image.
package postcard

import (
	"sort"
	"time"

	"github.com/bluesky-social/indigo/api/bsky"
)

// Image is a displayable image in a card
type Image struct {
	URL string
	Alt string
}

// External is a link-card embed
type External struct {
	URL         string
	Host        string
	Title       string
	Description string
	Thumb       string
}

// Quote is an embedded (quoted) post
type Quote struct {
	AuthorName   string
	AuthorHandle string
	Avatar       string
	Text         string
	Image        string
}

// Post is the display model for a single post in a card
type Post struct {
	AuthorName   string
	AuthorHandle string
	Avatar       string
	Text         string
	Timestamp    string
	Images       []Image
	Video        *Image
	External     *External
	Quote        *Quote
	ReplyCount   int64
	RepostCount  int64
	LikeCount    int64
}

// Card is the full display model: the main post plus its top replies
type Card struct {
	Post          Post
	Replies       []Post
	HiddenReplies int64
	Host          string
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func displayName(name *string, handle string) string {
	if name != nil && *name != "" {
		return *name
	}
	return handle
}

func formatTimestamp(raw string) string {
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return ""
	}
	return t.UTC().Format("Jan 2, 2006 · 3:04 PM")
}

// postFromView converts an AppView PostView into the display model
func postFromView(view *bsky.FeedDefs_PostView) Post {
	p := Post{
		ReplyCount:  deref(view.ReplyCount),
		RepostCount: deref(view.RepostCount),
		LikeCount:   deref(view.LikeCount),
	}

	if view.Author != nil {
		p.AuthorName = displayName(view.Author.DisplayName, view.Author.Handle)
		p.AuthorHandle = view.Author.Handle
		p.Avatar = deref(view.Author.Avatar)
	}

	if view.Record != nil {
		if rec, ok := view.Record.Val.(*bsky.FeedPost); ok {
			p.Text = rec.Text
			p.Timestamp = formatTimestamp(rec.CreatedAt)
		}
	}

	if view.Embed != nil {
		applyEmbed(&p, view.Embed)
	}

	return p
}

func applyEmbed(p *Post, embed *bsky.FeedDefs_PostView_Embed) {
	switch {
	case embed.EmbedImages_View != nil:
		for _, img := range embed.EmbedImages_View.Images {
			p.Images = append(p.Images, Image{URL: img.Thumb, Alt: img.Alt})
		}
	case embed.EmbedVideo_View != nil:
		if embed.EmbedVideo_View.Thumbnail != nil {
			p.Video = &Image{URL: *embed.EmbedVideo_View.Thumbnail, Alt: deref(embed.EmbedVideo_View.Alt)}
		}
	case embed.EmbedExternal_View != nil:
		p.External = externalFromView(embed.EmbedExternal_View)
	case embed.EmbedRecord_View != nil:
		p.Quote = quoteFromView(embed.EmbedRecord_View)
	case embed.EmbedRecordWithMedia_View != nil:
		rwm := embed.EmbedRecordWithMedia_View
		if rwm.Media != nil {
			switch {
			case rwm.Media.EmbedImages_View != nil:
				for _, img := range rwm.Media.EmbedImages_View.Images {
					p.Images = append(p.Images, Image{URL: img.Thumb, Alt: img.Alt})
				}
			case rwm.Media.EmbedVideo_View != nil:
				if rwm.Media.EmbedVideo_View.Thumbnail != nil {
					p.Video = &Image{URL: *rwm.Media.EmbedVideo_View.Thumbnail, Alt: deref(rwm.Media.EmbedVideo_View.Alt)}
				}
			case rwm.Media.EmbedExternal_View != nil:
				p.External = externalFromView(rwm.Media.EmbedExternal_View)
			}
		}
		if rwm.Record != nil {
			p.Quote = quoteFromView(rwm.Record)
		}
	}
}

func externalFromView(view *bsky.EmbedExternal_View) *External {
	if view.External == nil {
		return nil
	}
	return &External{
		URL:         view.External.Uri,
		Host:        hostOf(view.External.Uri),
		Title:       view.External.Title,
		Description: view.External.Description,
		Thumb:       deref(view.External.Thumb),
	}
}

func quoteFromView(view *bsky.EmbedRecord_View) *Quote {
	if view.Record == nil || view.Record.EmbedRecord_ViewRecord == nil {
		return nil
	}
	vr := view.Record.EmbedRecord_ViewRecord

	q := &Quote{}
	if vr.Author != nil {
		q.AuthorName = displayName(vr.Author.DisplayName, vr.Author.Handle)
		q.AuthorHandle = vr.Author.Handle
		q.Avatar = deref(vr.Author.Avatar)
	}
	if vr.Value != nil {
		if rec, ok := vr.Value.Val.(*bsky.FeedPost); ok {
			q.Text = rec.Text
		}
	}
	for _, e := range vr.Embeds {
		if e.EmbedImages_View != nil && len(e.EmbedImages_View.Images) > 0 {
			q.Image = e.EmbedImages_View.Images[0].Thumb
			break
		}
	}
	return q
}

func hostOf(rawURL string) string {
	// tolerate bad URLs: just show the raw string trimmed of scheme
	s := rawURL
	for _, prefix := range []string{"https://", "http://"} {
		if len(s) > len(prefix) && s[:len(prefix)] == prefix {
			s = s[len(prefix):]
			break
		}
	}
	for i := 0; i < len(s); i++ {
		if s[i] == '/' || s[i] == '?' {
			return s[:i]
		}
	}
	return s
}

// BuildCard builds a card display model from a hydrated thread view.
// The top maxReplies replies by like count are included.
func BuildCard(thread *bsky.FeedDefs_ThreadViewPost, host string, maxReplies int) *Card {
	card := &Card{
		Post: postFromView(thread.Post),
		Host: host,
	}

	var replies []*bsky.FeedDefs_PostView
	for _, r := range thread.Replies {
		if r.FeedDefs_ThreadViewPost != nil && r.FeedDefs_ThreadViewPost.Post != nil {
			replies = append(replies, r.FeedDefs_ThreadViewPost.Post)
		}
	}
	sort.SliceStable(replies, func(i, j int) bool {
		return deref(replies[i].LikeCount) > deref(replies[j].LikeCount)
	})

	if len(replies) > maxReplies {
		replies = replies[:maxReplies]
	}
	for _, r := range replies {
		card.Replies = append(card.Replies, postFromView(r))
	}

	if card.Post.ReplyCount > int64(len(card.Replies)) {
		card.HiddenReplies = card.Post.ReplyCount - int64(len(card.Replies))
	}

	return card
}
