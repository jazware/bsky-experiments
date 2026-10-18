package carrepo

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/ipfs/go-cid"
	cbornode "github.com/ipfs/go-ipld-cbor"
)

// ReadCAR reads a CARv1 stream into a root CID and a map of CID→raw block bytes.
// Unlike go-car, it does NOT verify block hashes (no SHA256). We trust PDS-signed
// repos; verification is redundant and accounts for ~30-40% of CAR reading CPU.
func ReadCAR(r io.Reader) (cid.Cid, map[cid.Cid][]byte, error) {
	br := bufio.NewReaderSize(r, 64*1024)

	root, err := readHeader(br)
	if err != nil {
		return cid.Undef, nil, fmt.Errorf("reading CAR header: %w", err)
	}

	blocks := make(map[cid.Cid][]byte, 256)
	for {
		blockLen, err := binary.ReadUvarint(br)
		if err != nil {
			if err == io.EOF {
				break
			}
			return cid.Undef, nil, fmt.Errorf("reading block length: %w", err)
		}

		buf := make([]byte, blockLen)
		if _, err := io.ReadFull(br, buf); err != nil {
			return cid.Undef, nil, fmt.Errorf("reading block data: %w", err)
		}

		cidLen, c, err := cid.CidFromBytes(buf)
		if err != nil {
			return cid.Undef, nil, fmt.Errorf("parsing block CID: %w", err)
		}

		blocks[c] = buf[cidLen:]
	}

	return root, blocks, nil
}

func readHeader(br *bufio.Reader) (cid.Cid, error) {
	headerLen, err := binary.ReadUvarint(br)
	if err != nil {
		return cid.Undef, fmt.Errorf("reading header length: %w", err)
	}

	headerBuf := make([]byte, headerLen)
	if _, err := io.ReadFull(br, headerBuf); err != nil {
		return cid.Undef, fmt.Errorf("reading header bytes: %w", err)
	}

	var header map[string]any
	if err := cbornode.DecodeInto(headerBuf, &header); err != nil {
		return cid.Undef, fmt.Errorf("decoding header CBOR: %w", err)
	}

	rootsRaw, ok := header["roots"]
	if !ok {
		return cid.Undef, fmt.Errorf("header missing 'roots' field")
	}

	roots, ok := rootsRaw.([]any)
	if !ok || len(roots) == 0 {
		return cid.Undef, fmt.Errorf("header 'roots' is not a non-empty array")
	}

	root, ok := roots[0].(cid.Cid)
	if !ok {
		return cid.Undef, fmt.Errorf("header root is not a CID")
	}

	return root, nil
}
