package carrepo

import (
	"bytes"
	"fmt"

	"github.com/bluesky-social/indigo/mst"
	"github.com/ipfs/go-cid"
)

// walkLeaves walks all MST leaf entries, calling cb with each record path and
// record CID. Unlike indigo's WalkLeavesFrom, this skips layer detection
// (no SHA256 hashing), MerkleSearchTree object allocation, and nodeEntry
// intermediaries — directly walking decoded NodeData.
func walkLeaves(blocks map[cid.Cid][]byte, root cid.Cid, cb func(key string, val cid.Cid) error) error {
	var reader bytes.Reader
	return walkNode(blocks, root, cb, &reader)
}

func walkNode(blocks map[cid.Cid][]byte, nodeCid cid.Cid, cb func(key string, val cid.Cid) error, reader *bytes.Reader) error {
	raw, ok := blocks[nodeCid]
	if !ok {
		return fmt.Errorf("missing MST node block: %s", nodeCid)
	}

	var nd mst.NodeData
	reader.Reset(raw)
	if err := nd.UnmarshalCBOR(reader); err != nil {
		return fmt.Errorf("decoding MST node: %w", err)
	}

	if nd.Left != nil {
		if err := walkNode(blocks, *nd.Left, cb, reader); err != nil {
			return err
		}
	}

	var lastKey []byte
	for i := range nd.Entries {
		e := &nd.Entries[i]

		// Reconstruct full key: keep first PrefixLen bytes of lastKey, append suffix.
		lastKey = append(lastKey[:e.PrefixLen], e.KeySuffix...)

		if err := cb(string(lastKey), e.Val); err != nil {
			return err
		}

		if e.Tree != nil {
			if err := walkNode(blocks, *e.Tree, cb, reader); err != nil {
				return err
			}
		}
	}

	return nil
}
