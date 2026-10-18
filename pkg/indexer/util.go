package indexer

import (
	"fmt"
	"strings"
)

type URI struct {
	Did        string
	Collection string
	RKey       string
}

func ParseURI(uri string) (*URI, error) {
	// URI format: at://did:plc:xxx/collection/rkey
	if !strings.HasPrefix(uri, "at://") {
		return nil, fmt.Errorf("invalid URI: must start with at://")
	}

	uri = strings.TrimPrefix(uri, "at://")
	parts := strings.Split(uri, "/")

	if len(parts) < 3 {
		return nil, fmt.Errorf("invalid URI: must have at least 3 parts")
	}

	return &URI{
		Did:        parts[0],
		Collection: parts[1],
		RKey:       parts[2],
	}, nil
}
