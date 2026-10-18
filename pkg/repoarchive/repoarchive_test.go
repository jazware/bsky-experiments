package repoarchive

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()

	// Create a writer with a small segment size to test finalization.
	writer, err := NewSegmentWriter(dir, WithSegmentSize(1024*1024)) // 1MB
	if err != nil {
		t.Fatal(err)
	}

	// Write some repos.
	repos := []*CrawledRepo{
		{
			DID:       "did:plc:aaa111",
			PDS:       "https://pds1.example.com",
			Rev:       "rev1",
			CrawledAt: time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC),
			Collections: map[string][]Record{
				"app.bsky.feed.post": {
					{RKey: "3abc", JSON: []byte(`{"text":"hello world","$type":"app.bsky.feed.post"}`)},
					{RKey: "3def", JSON: []byte(`{"text":"second post","$type":"app.bsky.feed.post"}`)},
				},
				"app.bsky.graph.follow": {
					{RKey: "3ghi", JSON: []byte(`{"subject":"did:plc:bbb222","$type":"app.bsky.graph.follow"}`)},
				},
			},
		},
		{
			DID:       "did:plc:bbb222",
			PDS:       "https://pds2.example.com",
			Rev:       "rev2",
			CrawledAt: time.Date(2025, 1, 15, 11, 0, 0, 0, time.UTC),
			Collections: map[string][]Record{
				"app.bsky.feed.post": {
					{RKey: "3xyz", JSON: []byte(`{"text":"from bbb","$type":"app.bsky.feed.post"}`)},
				},
				"app.bsky.feed.like": {
					{RKey: "3lk1", JSON: []byte(`{"subject":{"uri":"at://did:plc:aaa111/app.bsky.feed.post/3abc"}}`)},
				},
			},
		},
	}

	for _, repo := range repos {
		if err := writer.WriteRepo(repo); err != nil {
			t.Fatalf("WriteRepo: %v", err)
		}
	}

	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if writer.TotalRepos() != 2 {
		t.Errorf("TotalRepos: got %d, want 2", writer.TotalRepos())
	}

	// Read back.
	segPath := filepath.Join(dir, "segment_0001.rca")
	reader, err := OpenSegment(segPath)
	if err != nil {
		t.Fatalf("OpenSegment: %v", err)
	}
	defer reader.Close()

	header := reader.Header()
	if header.RepoCount != 2 {
		t.Errorf("header.RepoCount: got %d, want 2", header.RepoCount)
	}
	if header.Version != Version {
		t.Errorf("header.Version: got %d, want %d", header.Version, Version)
	}

	readRepos := 0
	for reader.Next() {
		repo := reader.Repo()
		readRepos++

		switch repo.DID {
		case "did:plc:aaa111":
			if repo.PDS != "https://pds1.example.com" {
				t.Errorf("repo.PDS: got %q, want %q", repo.PDS, "https://pds1.example.com")
			}
			if repo.Rev != "rev1" {
				t.Errorf("repo.Rev: got %q, want %q", repo.Rev, "rev1")
			}
			collCount := 0
			for repo.NextCollection() {
				col := repo.Collection()
				collCount++
				recCount := 0
				for col.NextRecord() {
					_, err := col.Record()
					if err != nil {
						t.Fatalf("Record: %v", err)
					}
					recCount++
				}
				switch col.Name {
				case "app.bsky.feed.post":
					if recCount != 2 {
						t.Errorf("post records: got %d, want 2", recCount)
					}
				case "app.bsky.graph.follow":
					if recCount != 1 {
						t.Errorf("follow records: got %d, want 1", recCount)
					}
				default:
					t.Errorf("unexpected collection: %s", col.Name)
				}
			}
			if collCount != 2 {
				t.Errorf("collection count for aaa: got %d, want 2", collCount)
			}

		case "did:plc:bbb222":
			if repo.PDS != "https://pds2.example.com" {
				t.Errorf("repo.PDS: got %q", repo.PDS)
			}

		default:
			t.Errorf("unexpected DID: %s", repo.DID)
		}
	}

	if readRepos != 2 {
		t.Errorf("read repos: got %d, want 2", readRepos)
	}
}

func TestCollectionFilter(t *testing.T) {
	dir := t.TempDir()

	writer, err := NewSegmentWriter(dir)
	if err != nil {
		t.Fatal(err)
	}

	repo := &CrawledRepo{
		DID:       "did:plc:filter-test",
		PDS:       "https://pds.example.com",
		Rev:       "rev1",
		CrawledAt: time.Now(),
		Collections: map[string][]Record{
			"app.bsky.feed.post": {
				{RKey: "post1", JSON: []byte(`{"text":"hello"}`)},
			},
			"app.bsky.feed.like": {
				{RKey: "like1", JSON: []byte(`{"subject":{}}`)},
			},
			"app.bsky.graph.follow": {
				{RKey: "follow1", JSON: []byte(`{"subject":"did:plc:xxx"}`)},
			},
		},
	}

	if err := writer.WriteRepo(repo); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	// Read with filter: only posts.
	segPath := filepath.Join(dir, "segment_0001.rca")
	reader, err := OpenSegment(segPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	reader.SetCollectionFilter("app.bsky.feed.post")

	if !reader.Next() {
		t.Fatal("expected a repo")
	}

	collNames := []string{}
	r := reader.Repo()
	for r.NextCollection() {
		col := r.Collection()
		collNames = append(collNames, col.Name)
		for col.NextRecord() {
			rec, err := col.Record()
			if err != nil {
				t.Fatal(err)
			}
			if rec.RKey != "post1" {
				t.Errorf("unexpected rkey: %s", rec.RKey)
			}
		}
	}

	if len(collNames) != 1 {
		t.Errorf("expected 1 filtered collection, got %d: %v", len(collNames), collNames)
	}
	if collNames[0] != "app.bsky.feed.post" {
		t.Errorf("expected app.bsky.feed.post, got %s", collNames[0])
	}
}

func TestIndex(t *testing.T) {
	dir := t.TempDir()

	writer, err := NewSegmentWriter(dir)
	if err != nil {
		t.Fatal(err)
	}

	dids := []string{"did:plc:zzz", "did:plc:aaa", "did:plc:mmm"}
	for _, did := range dids {
		if err := writer.WriteRepo(&CrawledRepo{
			DID:       did,
			PDS:       "https://pds.example.com",
			CrawledAt: time.Now(),
			Collections: map[string][]Record{
				"app.bsky.feed.post": {
					{RKey: "r1", JSON: []byte(`{}`)},
				},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	segPath := filepath.Join(dir, "segment_0001.rca")
	index, err := LoadIndex(segPath)
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}

	if len(index.Entries) != 3 {
		t.Fatalf("expected 3 index entries, got %d", len(index.Entries))
	}

	// Index should be sorted.
	if index.Entries[0].DID != "did:plc:aaa" {
		t.Errorf("first entry: got %q, want did:plc:aaa", index.Entries[0].DID)
	}

	// Binary search.
	entry, ok := index.FindDID("did:plc:mmm")
	if !ok {
		t.Fatal("FindDID: not found")
	}
	if entry.DID != "did:plc:mmm" {
		t.Errorf("FindDID: got %q", entry.DID)
	}
	if entry.Size == 0 {
		t.Error("FindDID: size is 0")
	}

	// Not found.
	_, ok = index.FindDID("did:plc:nonexistent")
	if ok {
		t.Error("FindDID: expected not found")
	}
}

func TestSegmentRotation(t *testing.T) {
	dir := t.TempDir()

	// Use a very small segment size to force rotation.
	writer, err := NewSegmentWriter(dir, WithSegmentSize(500))
	if err != nil {
		t.Fatal(err)
	}

	// Write repos until we get multiple segments.
	for i := range 20 {
		if err := writer.WriteRepo(&CrawledRepo{
			DID:       "did:plc:" + string(rune('a'+i)),
			PDS:       "https://pds.example.com",
			CrawledAt: time.Now(),
			Collections: map[string][]Record{
				"app.bsky.feed.post": {
					{RKey: "r1", JSON: []byte(`{"text":"hello world this is a longer message to force segment rotation"}`)},
				},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	// Check that multiple segments were created.
	files, err := filepath.Glob(filepath.Join(dir, "segment_*.rca"))
	if err != nil {
		t.Fatal(err)
	}

	if len(files) < 2 {
		t.Errorf("expected multiple segments, got %d", len(files))
	}

	// Verify each segment is a valid .rca file.
	totalRepos := 0
	for _, f := range files {
		reader, err := OpenSegment(f)
		if err != nil {
			t.Fatalf("OpenSegment(%s): %v", f, err)
		}
		for reader.Next() {
			totalRepos++
			repo := reader.Repo()
			for repo.NextCollection() {
				col := repo.Collection()
				for col.NextRecord() {
					if _, err := col.Record(); err != nil {
						t.Fatalf("Record: %v", err)
					}
				}
			}
		}
		reader.Close()
	}

	if totalRepos != 20 {
		t.Errorf("total repos across segments: got %d, want 20", totalRepos)
	}
}

func TestEmptyRepo(t *testing.T) {
	dir := t.TempDir()

	writer, err := NewSegmentWriter(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Write a repo with no collections.
	if err := writer.WriteRepo(&CrawledRepo{
		DID:         "did:plc:empty",
		PDS:         "https://pds.example.com",
		CrawledAt:   time.Now(),
		Collections: map[string][]Record{},
	}); err != nil {
		t.Fatal(err)
	}

	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	// Read back.
	segPath := filepath.Join(dir, "segment_0001.rca")
	reader, err := OpenSegment(segPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	if !reader.Next() {
		t.Fatal("expected one repo")
	}
	repo := reader.Repo()
	if repo.DID != "did:plc:empty" {
		t.Errorf("DID: got %q", repo.DID)
	}
	if repo.NextCollection() {
		t.Error("expected no collections")
	}
}

func TestFileStructure(t *testing.T) {
	dir := t.TempDir()

	writer, err := NewSegmentWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteRepo(&CrawledRepo{
		DID:       "did:plc:test",
		PDS:       "https://pds.example.com",
		CrawledAt: time.Now(),
		Collections: map[string][]Record{
			"test": {{RKey: "r1", JSON: []byte(`{}`)}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	segPath := filepath.Join(dir, "segment_0001.rca")
	fi, err := os.Stat(segPath)
	if err != nil {
		t.Fatal(err)
	}

	// File should have at least header + some data + index + footer.
	minSize := int64(FileHeaderSize + FileFooterSize + 10)
	if fi.Size() < minSize {
		t.Errorf("file too small: %d < %d", fi.Size(), minSize)
	}
}
