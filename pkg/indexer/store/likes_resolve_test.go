package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestResolveUnlikes(t *testing.T) {
	t0 := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	ref := likeRef{"at://did:plc:a/app.bsky.feed.post/p", "did:plc:a", "app.bsky.feed.post", "p", t0}
	unlike := func(actor, rkey string) *LikeBatch {
		return &LikeBatch{ActorDID: actor, RKey: rkey, Deleted: 1, CreatedAt: t0.Add(48 * time.Hour)}
	}

	t.Run("lookup and batch", func(t *testing.T) {
		var asked []likeKey
		lookup := func(_ context.Context, keys []likeKey) (map[likeKey]likeRef, error) {
			asked = keys
			return map[likeKey]likeRef{{"u1", "r1"}: ref}, nil
		}
		batch := []*LikeBatch{
			{ActorDID: "u2", RKey: "r2", SubjectURI: "at://x/y/z", SubjectDID: "x", SubjectCollection: "y", SubjectRKey: "z", CreatedAt: t0},
			unlike("u1", "r1"),
			unlike("u2", "r2"),
			unlike("u3", "r3"),
		}
		if err := resolveUnlikes(context.Background(), batch, lookup); err != nil {
			t.Fatal(err)
		}
		if len(asked) != 2 {
			t.Fatalf("looked up %v, want only u1 and u3", asked)
		}
		if got := batch[1]; got.SubjectURI != ref.SubjectURI || got.SubjectDID != "did:plc:a" || !got.CreatedAt.Equal(t0) {
			t.Errorf("lookup hit not filled: %+v", got)
		}
		if got := batch[2]; got.SubjectURI != "at://x/y/z" || !got.CreatedAt.Equal(t0) {
			t.Errorf("in-batch hit not filled: %+v", got)
		}
		if got := batch[3]; got.SubjectURI != "" || !got.CreatedAt.Equal(t0.Add(48*time.Hour)) {
			t.Errorf("miss was changed: %+v", got)
		}
	})

	t.Run("no deletes, no lookup", func(t *testing.T) {
		lookup := func(context.Context, []likeKey) (map[likeKey]likeRef, error) {
			t.Fatal("lookup called")
			return nil, nil
		}
		if err := resolveUnlikes(context.Background(), []*LikeBatch{{ActorDID: "u", RKey: "r", SubjectURI: "s"}}, lookup); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("lookup error leaves unlikes blank", func(t *testing.T) {
		lookup := func(context.Context, []likeKey) (map[likeKey]likeRef, error) {
			return nil, errors.New("down")
		}
		batch := []*LikeBatch{unlike("u1", "r1")}
		if err := resolveUnlikes(context.Background(), batch, lookup); err == nil {
			t.Fatal("want the error")
		}
		if batch[0].SubjectURI != "" {
			t.Errorf("filled on error: %+v", batch[0])
		}
	})
}
