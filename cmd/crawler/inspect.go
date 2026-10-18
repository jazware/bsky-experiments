package main

import (
	"fmt"
	"sort"
	"time"

	"github.com/jazware/bsky-experiments/pkg/repoarchive"
	"github.com/urfave/cli/v2"
)

func inspectCommand() *cli.Command {
	return &cli.Command{
		Name:      "inspect",
		Usage:     "Inspect an .rca segment file",
		ArgsUsage: "<segment-file>",
		Action: func(cctx *cli.Context) error {
			return runInspect(cctx)
		},
	}
}

func runInspect(cctx *cli.Context) error {
	path := cctx.Args().First()
	if path == "" {
		return fmt.Errorf("segment file path required")
	}

	reader, err := repoarchive.OpenSegment(path)
	if err != nil {
		return fmt.Errorf("opening segment: %w", err)
	}
	defer reader.Close()

	header := reader.Header()
	fmt.Printf("Segment: %s\n", path)
	fmt.Printf("  Version:     %d\n", header.Version)
	fmt.Printf("  Created:     %s\n", time.UnixMicro(header.CreatedAt).Format(time.RFC3339))
	fmt.Printf("  Repo Count:  %d\n", header.RepoCount)
	fmt.Printf("  Index Offset: %d\n", header.IndexOffset)
	fmt.Println()

	index, err := repoarchive.LoadIndex(path)
	if err != nil {
		fmt.Printf("  (index not readable: %v)\n", err)
	} else {
		fmt.Printf("  Index Entries: %d\n", len(index.Entries))
	}

	collectionStats := make(map[string]struct {
		repos   int
		records int
	})
	repoCount := 0

	for reader.Next() {
		repo := reader.Repo()
		repoCount++
		for repo.NextCollection() {
			col := repo.Collection()
			stats := collectionStats[col.Name]
			stats.repos++
			recordCount := 0
			for col.NextRecord() {
				if _, err := col.Record(); err == nil {
					recordCount++
				}
			}
			stats.records += recordCount
			collectionStats[col.Name] = stats
		}
	}

	fmt.Printf("\n  Repos scanned: %d\n", repoCount)
	fmt.Println("  Collections:")

	names := make([]string, 0, len(collectionStats))
	for name := range collectionStats {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		stats := collectionStats[name]
		fmt.Printf("    %-50s  repos: %6d  records: %8d\n", name, stats.repos, stats.records)
	}

	return nil
}
