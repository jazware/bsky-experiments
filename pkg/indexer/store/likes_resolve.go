package store

import (
	"context"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var unlikeResolutions = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "atproto_unlike_resolutions_total",
	Help: "Unlikes by how their subject was found: batch, lookup or missed",
}, []string{"result"})

const lookupChunk = 10_000

type likeKey struct {
	ActorDID string
	RKey     string
}

type likeRef struct {
	SubjectURI        string
	SubjectDID        string
	SubjectCollection string
	SubjectRKey       string
	CreatedAt         time.Time
}

type likeLookup func(ctx context.Context, keys []likeKey) (map[likeKey]likeRef, error)

// resolveUnlikes fills in the subject of delete rows. A delete event carries
// only the like's (actor, rkey), so it lands on subject_uri = ” and nothing
// downstream (post_like_counts, likes_hourly, likes' own dedup) can match it
// to the like. It takes the like's created_at too, so the delete falls in the
// same hour and month partition as the like.
//
// A like and its unlike in one batch resolve from the batch. A failed lookup
// leaves the rest unresolved, as before.
func resolveUnlikes(ctx context.Context, batch []*LikeBatch, lookup likeLookup) error {
	inBatch := make(map[likeKey]likeRef)
	var pending []*LikeBatch
	for _, l := range batch {
		k := likeKey{l.ActorDID, l.RKey}
		switch {
		case l.Deleted == 0:
			inBatch[k] = likeRef{l.SubjectURI, l.SubjectDID, l.SubjectCollection, l.SubjectRKey, l.CreatedAt}
		case l.SubjectURI == "":
			if ref, ok := inBatch[k]; ok {
				applyRef(l, ref)
				unlikeResolutions.WithLabelValues("batch").Inc()
			} else {
				pending = append(pending, l)
			}
		}
	}
	if len(pending) == 0 {
		return nil
	}

	keys := make([]likeKey, len(pending))
	for i, l := range pending {
		keys[i] = likeKey{l.ActorDID, l.RKey}
	}
	found, err := lookup(ctx, keys)
	if err != nil {
		unlikeResolutions.WithLabelValues("error").Add(float64(len(pending)))
		return err
	}
	for _, l := range pending {
		ref, ok := found[likeKey{l.ActorDID, l.RKey}]
		if !ok {
			unlikeResolutions.WithLabelValues("missed").Inc()
			continue
		}
		applyRef(l, ref)
		unlikeResolutions.WithLabelValues("lookup").Inc()
	}
	return nil
}

func applyRef(l *LikeBatch, ref likeRef) {
	l.SubjectURI = ref.SubjectURI
	l.SubjectDID = ref.SubjectDID
	l.SubjectCollection = ref.SubjectCollection
	l.SubjectRKey = ref.SubjectRKey
	l.CreatedAt = ref.CreatedAt
}

// LookupLikeKeys finds likes by (actor, rkey) in like_keys, which holds the
// last week's. Likes older than that aren't in the result.
func (s *Store) LookupLikeKeys(ctx context.Context, keys []likeKey) (map[likeKey]likeRef, error) {
	found := make(map[likeKey]likeRef, len(keys))
	for start := 0; start < len(keys); start += lookupChunk {
		chunk := keys[start:min(start+lookupChunk, len(keys))]
		actors := make([]string, len(chunk))
		rkeys := make([]string, len(chunk))
		for i, k := range chunk {
			actors[i], rkeys[i] = k.ActorDID, k.RKey
		}

		rows, err := s.DB.Query(ctx, `
			SELECT actor_did, rkey, subject_uri, subject_did, subject_collection, subject_rkey, created_at
			FROM like_keys
			WHERE (actor_did, rkey) IN (
				SELECT t.1, t.2 FROM (SELECT arrayJoin(arrayZip(?, ?)) AS t)
			)
		`, actors, rkeys)
		if err != nil {
			return nil, fmt.Errorf("failed to look up likes: %w", err)
		}
		for rows.Next() {
			var k likeKey
			var ref likeRef
			if err := rows.Scan(&k.ActorDID, &k.RKey, &ref.SubjectURI, &ref.SubjectDID,
				&ref.SubjectCollection, &ref.SubjectRKey, &ref.CreatedAt); err != nil {
				rows.Close()
				return nil, fmt.Errorf("failed to scan like: %w", err)
			}
			found[k] = ref
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return found, nil
}
