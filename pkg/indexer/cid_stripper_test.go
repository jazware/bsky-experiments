package indexer

import (
	"testing"

	"github.com/goccy/go-json"
)

func TestStripCIDs_Like(t *testing.T) {
	input := `{
		"$type": "app.bsky.feed.like",
		"createdAt": "2024-01-15T10:30:00.000Z",
		"subject": {
			"cid": "bafyreig2fjxi3a2u2na6zxpqvr2u4w7i7ksm2k3j5p6q7r8s9t0u1v2w3x4",
			"uri": "at://did:plc:abc123/app.bsky.feed.post/xyz789"
		}
	}`

	result, err := StripCIDs([]byte(input))
	if err != nil {
		t.Fatalf("StripCIDs failed: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("Failed to parse result: %v", err)
	}

	subject := parsed["subject"].(map[string]any)
	if _, exists := subject["cid"]; exists {
		t.Error("subject.cid should be removed")
	}
	if subject["uri"] != "at://did:plc:abc123/app.bsky.feed.post/xyz789" {
		t.Error("subject.uri should be preserved")
	}
	if parsed["$type"] != "app.bsky.feed.like" {
		t.Error("$type should be preserved")
	}
}

func TestStripCIDs_PostWithReply(t *testing.T) {
	input := `{
		"$type": "app.bsky.feed.post",
		"text": "Hello world",
		"createdAt": "2024-01-15T10:30:00.000Z",
		"reply": {
			"parent": {
				"cid": "bafyreig2fjxi3a2u2na6zxpqvr2u4w7i7ksm2k3j5p6q7r8s9t0u1v2w3x4",
				"uri": "at://did:plc:parent/app.bsky.feed.post/abc"
			},
			"root": {
				"cid": "bafyreig2fjxi3a2u2na6zxpqvr2u4w7i7ksm2k3j5p6q7r8s9t0u1v2w3x5",
				"uri": "at://did:plc:root/app.bsky.feed.post/def"
			}
		}
	}`

	result, err := StripCIDs([]byte(input))
	if err != nil {
		t.Fatalf("StripCIDs failed: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("Failed to parse result: %v", err)
	}

	reply := parsed["reply"].(map[string]any)
	parent := reply["parent"].(map[string]any)
	root := reply["root"].(map[string]any)

	if _, exists := parent["cid"]; exists {
		t.Error("reply.parent.cid should be removed")
	}
	if _, exists := root["cid"]; exists {
		t.Error("reply.root.cid should be removed")
	}
	if parent["uri"] == nil {
		t.Error("reply.parent.uri should be preserved")
	}
	if root["uri"] == nil {
		t.Error("reply.root.uri should be preserved")
	}
	if parsed["text"] != "Hello world" {
		t.Error("text should be preserved")
	}
}

func TestStripCIDs_ProfileWithBlobRefs(t *testing.T) {
	input := `{
		"$type": "app.bsky.actor.profile",
		"displayName": "Test User",
		"description": "A test profile",
		"avatar": {
			"$type": "blob",
			"ref": {
				"$link": "bafkreibh7bgqhqsz6w3l5nz6l7gvl2d7jz5m2k3n4o5p6q7r8s9t0u1v2w3"
			},
			"mimeType": "image/jpeg",
			"size": 12345
		},
		"banner": {
			"$type": "blob",
			"ref": {
				"$link": "bafkreibh7bgqhqsz6w3l5nz6l7gvl2d7jz5m2k3n4o5p6q7r8s9t0u1v2w4"
			},
			"mimeType": "image/jpeg",
			"size": 54321
		}
	}`

	result, err := StripCIDs([]byte(input))
	if err != nil {
		t.Fatalf("StripCIDs failed: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("Failed to parse result: %v", err)
	}

	avatar := parsed["avatar"].(map[string]any)
	avatarRef := avatar["ref"].(map[string]any)
	if _, exists := avatarRef["$link"]; !exists {
		t.Error("avatar.ref.$link should be preserved (blob CIDs are kept)")
	}
	if avatar["mimeType"] != "image/jpeg" {
		t.Error("avatar.mimeType should be preserved")
	}

	banner := parsed["banner"].(map[string]any)
	bannerRef := banner["ref"].(map[string]any)
	if _, exists := bannerRef["$link"]; !exists {
		t.Error("banner.ref.$link should be preserved (blob CIDs are kept)")
	}

	if parsed["displayName"] != "Test User" {
		t.Error("displayName should be preserved")
	}
}

func TestStripCIDs_PostWithEmbeddedImages(t *testing.T) {
	input := `{
		"$type": "app.bsky.feed.post",
		"text": "Check out this image",
		"createdAt": "2024-01-15T10:30:00.000Z",
		"embed": {
			"$type": "app.bsky.embed.images",
			"images": [
				{
					"alt": "A nice picture",
					"image": {
						"$type": "blob",
						"ref": {
							"$link": "bafkreibh7bgqhqsz6w3l5nz6l7gvl2d7jz5m2k3n4o5p6q7r8s9t0u1v2w5"
						},
						"mimeType": "image/png",
						"size": 98765
					}
				},
				{
					"alt": "Another picture",
					"image": {
						"$type": "blob",
						"ref": {
							"$link": "bafkreibh7bgqhqsz6w3l5nz6l7gvl2d7jz5m2k3n4o5p6q7r8s9t0u1v2w6"
						},
						"mimeType": "image/jpeg",
						"size": 54321
					}
				}
			]
		}
	}`

	result, err := StripCIDs([]byte(input))
	if err != nil {
		t.Fatalf("StripCIDs failed: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("Failed to parse result: %v", err)
	}

	embed := parsed["embed"].(map[string]any)
	images := embed["images"].([]any)

	for i, img := range images {
		image := img.(map[string]any)
		imageBlob := image["image"].(map[string]any)
		ref := imageBlob["ref"].(map[string]any)
		if _, exists := ref["$link"]; !exists {
			t.Errorf("embed.images[%d].image.ref.$link should be preserved (blob CIDs are kept)", i)
		}
		if image["alt"] == nil {
			t.Errorf("embed.images[%d].alt should be preserved", i)
		}
	}
}

func TestStripCIDs_EmptyInput(t *testing.T) {
	result, err := StripCIDs([]byte{})
	if err != nil {
		t.Fatalf("StripCIDs failed: %v", err)
	}
	if len(result) != 0 {
		t.Error("Empty input should return empty output")
	}
}

func TestStripCIDs_MalformedJSON(t *testing.T) {
	_, err := StripCIDs([]byte(`{invalid json`))
	if err == nil {
		t.Error("Expected error for malformed JSON")
	}
}

func TestStripCIDs_NoCIDs(t *testing.T) {
	input := `{"$type":"app.bsky.graph.follow","subject":"did:plc:abc123","createdAt":"2024-01-15T10:30:00.000Z"}`

	result, err := StripCIDs([]byte(input))
	if err != nil {
		t.Fatalf("StripCIDs failed: %v", err)
	}

	var inputParsed, resultParsed any
	if err := json.Unmarshal([]byte(input), &inputParsed); err != nil {
		t.Fatalf("Failed to parse input: %v", err)
	}
	if err := json.Unmarshal(result, &resultParsed); err != nil {
		t.Fatalf("Failed to parse result: %v", err)
	}

	inputBytes, err := json.Marshal(inputParsed)
	if err != nil {
		t.Fatalf("Failed to marshal input: %v", err)
	}
	resultBytes, err := json.Marshal(resultParsed)
	if err != nil {
		t.Fatalf("Failed to marshal result: %v", err)
	}

	if string(inputBytes) != string(resultBytes) {
		t.Errorf("Record without CIDs should be unchanged\nInput:  %s\nOutput: %s", inputBytes, resultBytes)
	}
}

func TestStripCIDs_QuotedRecord(t *testing.T) {
	// Post quoting another post
	input := `{
		"$type": "app.bsky.feed.post",
		"text": "Look at this post",
		"createdAt": "2024-01-15T10:30:00.000Z",
		"embed": {
			"$type": "app.bsky.embed.record",
			"record": {
				"cid": "bafyreig2fjxi3a2u2na6zxpqvr2u4w7i7ksm2k3j5p6q7r8s9t0u1v2w3x4",
				"uri": "at://did:plc:quoted/app.bsky.feed.post/abc"
			}
		}
	}`

	result, err := StripCIDs([]byte(input))
	if err != nil {
		t.Fatalf("StripCIDs failed: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("Failed to parse result: %v", err)
	}

	embed := parsed["embed"].(map[string]any)
	record := embed["record"].(map[string]any)

	if _, exists := record["cid"]; exists {
		t.Error("embed.record.cid should be removed")
	}
	if record["uri"] == nil {
		t.Error("embed.record.uri should be preserved")
	}
}

// Benchmarks

func BenchmarkStripCIDs_Like(b *testing.B) {
	input := []byte(`{"$type":"app.bsky.feed.like","createdAt":"2024-01-15T10:30:00.000Z","subject":{"cid":"bafyreig2fjxi3a2u2na6zxpqvr2u4w7i7ksm2k3j5p6q7r8s9t0u1v2w3x4","uri":"at://did:plc:abc123/app.bsky.feed.post/xyz789"}}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		StripCIDs(input)
	}
}

func BenchmarkStripCIDs_Profile(b *testing.B) {
	input := []byte(`{"$type":"app.bsky.actor.profile","displayName":"Test User","description":"A test profile","avatar":{"$type":"blob","ref":{"$link":"bafkreibh7bgqhqsz6w3l5nz6l7gvl2d7jz5m2k3n4o5p6q7r8s9t0u1v2w3"},"mimeType":"image/jpeg","size":12345},"banner":{"$type":"blob","ref":{"$link":"bafkreibh7bgqhqsz6w3l5nz6l7gvl2d7jz5m2k3n4o5p6q7r8s9t0u1v2w4"},"mimeType":"image/jpeg","size":54321}}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		StripCIDs(input)
	}
}

func BenchmarkStripCIDs_PostWithReply(b *testing.B) {
	input := []byte(`{"$type":"app.bsky.feed.post","text":"Hello world","createdAt":"2024-01-15T10:30:00.000Z","reply":{"parent":{"cid":"bafyreig2fjxi3a2u2na6zxpqvr2u4w7i7ksm2k3j5p6q7r8s9t0u1v2w3x4","uri":"at://did:plc:parent/app.bsky.feed.post/abc"},"root":{"cid":"bafyreig2fjxi3a2u2na6zxpqvr2u4w7i7ksm2k3j5p6q7r8s9t0u1v2w3x5","uri":"at://did:plc:root/app.bsky.feed.post/def"}}}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		StripCIDs(input)
	}
}

func BenchmarkStripCIDs_NoCIDs(b *testing.B) {
	input := []byte(`{"$type":"app.bsky.graph.follow","subject":"did:plc:abc123","createdAt":"2024-01-15T10:30:00.000Z"}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		StripCIDs(input)
	}
}
