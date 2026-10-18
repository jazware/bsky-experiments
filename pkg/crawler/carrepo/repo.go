package carrepo

import (
	"fmt"
	"io"

	"github.com/ipfs/go-cid"
	cbornode "github.com/ipfs/go-ipld-cbor"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var missingBlocksTotal = promauto.NewCounter(prometheus.CounterOpts{
	Name: "crawler_repo_missing_blocks_total",
	Help: "Records skipped due to missing CID blocks in CAR data.",
})

type Repo struct {
	Rev    string
	root   cid.Cid
	blocks map[cid.Cid][]byte
}

func ParseRepo(r io.Reader) (*Repo, error) {
	rootCid, blocks, err := ReadCAR(r)
	if err != nil {
		return nil, fmt.Errorf("reading CAR: %w", err)
	}

	commitBytes, ok := blocks[rootCid]
	if !ok {
		return nil, fmt.Errorf("commit block not found for root CID: %s", rootCid)
	}

	var commit map[string]any
	if err := cbornode.DecodeInto(commitBytes, &commit); err != nil {
		return nil, fmt.Errorf("decoding signed commit: %w", err)
	}

	rev, _ := commit["rev"].(string)
	if rev == "" {
		return nil, fmt.Errorf("signed commit missing 'rev' field")
	}

	dataCid, ok := commit["data"].(cid.Cid)
	if !ok {
		return nil, fmt.Errorf("signed commit missing 'data' CID field")
	}

	return &Repo{
		Rev:    rev,
		root:   dataCid,
		blocks: blocks,
	}, nil
}

// ForEach iterates all records in the repo, calling cb with the record path
// (e.g. "app.bsky.feed.post/3abc") and the raw CBOR bytes of the record.
// Records with missing blocks are counted via metrics and skipped.
func (r *Repo) ForEach(cb func(path string, raw []byte) error) error {
	return walkLeaves(r.blocks, r.root, func(key string, val cid.Cid) error {
		raw, ok := r.blocks[val]
		if !ok {
			missingBlocksTotal.Inc()
			return nil
		}
		return cb(key, raw)
	})
}
