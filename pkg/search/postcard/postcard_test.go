package postcard

import (
	"strings"
	"testing"

	"github.com/bluesky-social/indigo/api/bsky"
	"github.com/bluesky-social/indigo/lex/util"
)

func ptr[T any](v T) *T { return &v }

func postView(handle, name, text string, likes int64) *bsky.FeedDefs_PostView {
	return &bsky.FeedDefs_PostView{
		Uri: "at://did:plc:" + handle + "/app.bsky.feed.post/3ktest",
		Author: &bsky.ActorDefs_ProfileViewBasic{
			Did:         "did:plc:" + handle,
			Handle:      handle,
			DisplayName: ptr(name),
			Avatar:      ptr("https://cdn.example/" + handle + ".jpg"),
		},
		Record: &util.LexiconTypeDecoder{Val: &bsky.FeedPost{
			Text:      text,
			CreatedAt: "2026-07-01T12:34:56Z",
		}},
		ReplyCount:  ptr(int64(7)),
		RepostCount: ptr(int64(3)),
		LikeCount:   ptr(likes),
	}
}

func threadWithReplies() *bsky.FeedDefs_ThreadViewPost {
	reply := func(handle, name, text string, likes int64) *bsky.FeedDefs_ThreadViewPost_Replies_Elem {
		return &bsky.FeedDefs_ThreadViewPost_Replies_Elem{
			FeedDefs_ThreadViewPost: &bsky.FeedDefs_ThreadViewPost{
				Post: postView(handle, name, text, likes),
			},
		}
	}
	return &bsky.FeedDefs_ThreadViewPost{
		Post: postView("alice.test", "Alice", "Hello world, this is the main post", 42),
		Replies: []*bsky.FeedDefs_ThreadViewPost_Replies_Elem{
			reply("bob.test", "Bob", "low engagement reply", 1),
			reply("carol.test", "Carol", "top reply", 30),
			reply("dan.test", "Dan", "second best reply", 12),
		},
	}
}

func TestBuildCard(t *testing.T) {
	card := BuildCard(threadWithReplies(), "bsky.jazco.dev", 2)

	if card.Post.AuthorName != "Alice" || card.Post.AuthorHandle != "alice.test" {
		t.Errorf("unexpected author: %+v", card.Post)
	}
	if card.Post.Text != "Hello world, this is the main post" {
		t.Errorf("unexpected text: %q", card.Post.Text)
	}
	if card.Post.LikeCount != 42 {
		t.Errorf("unexpected like count: %d", card.Post.LikeCount)
	}
	if card.Post.Timestamp == "" {
		t.Error("expected a formatted timestamp")
	}

	if len(card.Replies) != 2 {
		t.Fatalf("expected 2 replies, got %d", len(card.Replies))
	}
	if card.Replies[0].AuthorName != "Carol" {
		t.Errorf("expected top-liked reply first, got %q", card.Replies[0].AuthorName)
	}
	if card.Replies[1].AuthorName != "Dan" {
		t.Errorf("expected second-most-liked reply, got %q", card.Replies[1].AuthorName)
	}
	// Main post claims 7 replies, 2 shown
	if card.HiddenReplies != 5 {
		t.Errorf("expected 5 hidden replies, got %d", card.HiddenReplies)
	}
}

func TestBuildCardEmbeds(t *testing.T) {
	thread := threadWithReplies()
	thread.Post.Embed = &bsky.FeedDefs_PostView_Embed{
		EmbedImages_View: &bsky.EmbedImages_View{
			Images: []*bsky.EmbedImages_ViewImage{
				{Thumb: "https://cdn.example/img1.jpg", Fullsize: "https://cdn.example/img1-full.jpg", Alt: "first image"},
				{Thumb: "https://cdn.example/img2.jpg", Fullsize: "https://cdn.example/img2-full.jpg"},
			},
		},
	}

	card := BuildCard(thread, "bsky.jazco.dev", 2)
	if len(card.Post.Images) != 2 {
		t.Fatalf("expected 2 images, got %d", len(card.Post.Images))
	}
	if card.Post.Images[0].URL != "https://cdn.example/img1.jpg" || card.Post.Images[0].Alt != "first image" {
		t.Errorf("unexpected first image: %+v", card.Post.Images[0])
	}
}

func TestBuildCardQuote(t *testing.T) {
	thread := threadWithReplies()
	thread.Post.Embed = &bsky.FeedDefs_PostView_Embed{
		EmbedRecord_View: &bsky.EmbedRecord_View{
			Record: &bsky.EmbedRecord_View_Record{
				EmbedRecord_ViewRecord: &bsky.EmbedRecord_ViewRecord{
					Author: &bsky.ActorDefs_ProfileViewBasic{
						Did:         "did:plc:quoted",
						Handle:      "quoted.test",
						DisplayName: ptr("Quoted Author"),
					},
					Value: &util.LexiconTypeDecoder{Val: &bsky.FeedPost{Text: "the quoted post"}},
				},
			},
		},
	}

	card := BuildCard(thread, "bsky.jazco.dev", 2)
	if card.Post.Quote == nil {
		t.Fatal("expected a quote")
	}
	if card.Post.Quote.AuthorName != "Quoted Author" || card.Post.Quote.Text != "the quoted post" {
		t.Errorf("unexpected quote: %+v", card.Post.Quote)
	}
}

func TestRenderHTML(t *testing.T) {
	card := BuildCard(threadWithReplies(), "bsky.jazco.dev", 2)
	html, err := RenderHTML(card)
	if err != nil {
		t.Fatalf("RenderHTML failed: %v", err)
	}

	for _, want := range []string{
		`id="card"`,
		"Alice",
		"@alice.test",
		"Hello world, this is the main post",
		"top reply",
		"@carol.test",
		"preview via bsky.jazco.dev",
		"View 5 more replies on Bluesky",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered HTML missing %q", want)
		}
	}

	// HTML injection from post content must be escaped
	card.Post.Text = `<script>alert("xss")</script>`
	html, err = RenderHTML(card)
	if err != nil {
		t.Fatalf("RenderHTML failed: %v", err)
	}
	if strings.Contains(html, "<script>alert") {
		t.Error("post text was not HTML-escaped")
	}
}

func TestCompactCount(t *testing.T) {
	cases := map[int64]string{
		0:         "0",
		999:       "999",
		1000:      "1K",
		1234:      "1.2K",
		999_999:   "1000K",
		1_500_000: "1.5M",
	}
	for n, want := range cases {
		if got := compactCount(n); got != want {
			t.Errorf("compactCount(%d) = %q, want %q", n, got, want)
		}
	}
}
