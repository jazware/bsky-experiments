package indexer

import "github.com/goccy/go-json"

// cidFieldNames are the JSON field names that contain CID values.
// These are stripped from records before storage to improve compression.
// Note: $link (BlobRef) is intentionally kept as blob CIDs are needed.
var cidFieldNames = map[string]bool{
	"cid": true, // StrongRef (likes, reposts, replies)
}

// StripCIDs removes StrongRef CID fields from a JSON record.
// It walks the JSON tree recursively and removes "cid" fields
// (used in likes, reposts, replies). BlobRef $link fields are preserved.
// Returns the modified JSON as a byte slice, or error if parsing fails.
func StripCIDs(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return data, nil
	}

	var parsed any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}

	stripped := stripCIDsRecursive(parsed)
	return json.Marshal(stripped)
}

func stripCIDsRecursive(v any) any {
	switch val := v.(type) {
	case map[string]any:
		result := make(map[string]any, len(val))
		for key, value := range val {
			if cidFieldNames[key] {
				continue
			}
			result[key] = stripCIDsRecursive(value)
		}
		return result

	case []any:
		result := make([]any, len(val))
		for i, item := range val {
			result[i] = stripCIDsRecursive(item)
		}
		return result

	default:
		return val
	}
}
