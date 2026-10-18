package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/jazware/bsky-experiments/pkg/repoarchive"
	"github.com/urfave/cli/v2"
)

func tallyCommand() *cli.Command {
	return &cli.Command{
		Name:  "tally",
		Usage: "Count total records across all .rca segment files (fast, no decompression)",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "input-dir",
				Usage:   "Directory containing .rca segment files",
				Value:   "./data/rca",
				EnvVars: []string{"INPUT_DIR"},
			},
			&cli.BoolFlag{
				Name:  "by-collection",
				Usage: "Show breakdown by collection",
			},
			&cli.IntFlag{
				Name:  "workers",
				Usage: "Parallel segment readers (keep at 1 for HDD, increase for SSD/NVMe)",
				Value: 1,
			},
		},
		Action: func(cctx *cli.Context) error {
			return runTally(cctx)
		},
	}
}

type segmentTally struct {
	path        string
	repos       int
	records     int64
	dids        []string
	collections map[string]int64 // only populated with --by-collection
	err         error
}

func runTally(cctx *cli.Context) error {
	inputDir := cctx.String("input-dir")
	byCollection := cctx.Bool("by-collection")
	workers := cctx.Int("workers")

	segments, err := filepath.Glob(filepath.Join(inputDir, "*.rca"))
	if err != nil {
		return fmt.Errorf("listing segments: %w", err)
	}
	sort.Strings(segments)

	if len(segments) == 0 {
		return fmt.Errorf("no .rca files found in %s", inputDir)
	}

	total := len(segments)
	fmt.Printf("Scanning %d segments in %s ...\n\n", total, inputDir)

	segCh := make(chan string)
	resultCh := make(chan segmentTally, total)
	var wg sync.WaitGroup

	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for segPath := range segCh {
				resultCh <- tallySegment(segPath, byCollection)
			}
		}()
	}

	go func() {
		for _, seg := range segments {
			segCh <- seg
		}
		close(segCh)
	}()

	results := make(map[string]segmentTally, total)
	var runningRecords int64
	for range total {
		r := <-resultCh
		results[r.path] = r
		runningRecords += r.records
		fmt.Fprintf(os.Stderr, "\r  [%d/%d] segments scanned  %s records so far",
			len(results), total, formatCount(runningRecords))
	}
	fmt.Fprintf(os.Stderr, "\r%s\r", strings.Repeat(" ", 72)) // clear progress line

	wg.Wait()
	close(resultCh)

	var totalRepos int
	var totalRecords int64
	allDIDs := make(map[string]int) // DID -> number of segments it appears in
	globalCollections := make(map[string]int64)

	for _, segPath := range segments {
		r := results[segPath]
		if r.err != nil {
			fmt.Printf("  %-40s  ERROR: %v\n", filepath.Base(segPath), r.err)
			continue
		}
		fmt.Printf("  %-40s  repos: %8d  records: %12s\n",
			filepath.Base(segPath), r.repos, formatCount(r.records))
		totalRepos += r.repos
		totalRecords += r.records
		for _, did := range r.dids {
			allDIDs[did]++
		}
		for col, count := range r.collections {
			globalCollections[col] += count
		}
	}

	uniqueDIDs := len(allDIDs)
	var duplicatedDIDs int
	var duplicateRepoInstances int
	for _, count := range allDIDs {
		if count > 1 {
			duplicatedDIDs++
			duplicateRepoInstances += count - 1 // extra appearances beyond the first
		}
	}

	fmt.Println()
	fmt.Println("Totals:")
	fmt.Printf("  Segments:            %d\n", len(segments))
	fmt.Printf("  Repo instances:      %s\n", formatCount(int64(totalRepos)))
	fmt.Printf("  Total records:       %s\n", formatCount(totalRecords))
	fmt.Printf("  Unique DIDs:         %s\n", formatCount(int64(uniqueDIDs)))
	fmt.Printf("  Duplicated DIDs:     %s  (appear in 2+ segments)\n", formatCount(int64(duplicatedDIDs)))
	fmt.Printf("  Duplicate instances: %s  (extra repo copies from re-crawls)\n", formatCount(int64(duplicateRepoInstances)))

	if duplicatedDIDs > 0 {
		fmt.Println()
		fmt.Println("Note: Duplicated DIDs will be deduplicated by ReplacingMergeTree during replay.")
		fmt.Println("The actual row count after OPTIMIZE TABLE FINAL will be less than the total above.")
	}

	if byCollection && len(globalCollections) > 0 {
		fmt.Println()
		fmt.Println("Records by collection:")
		names := make([]string, 0, len(globalCollections))
		for name := range globalCollections {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			fmt.Printf("  %-50s  %12s\n", name, formatCount(globalCollections[name]))
		}
	}

	return nil
}

func tallySegment(segPath string, byCollection bool) segmentTally {
	result := segmentTally{path: segPath}

	reader, err := repoarchive.OpenSegment(segPath)
	if err != nil {
		result.err = err
		return result
	}
	defer reader.Close()

	collections := make(map[string]int64)

	for reader.Next() {
		repo := reader.Repo()
		result.repos++
		result.dids = append(result.dids, repo.DID)

		for _, entry := range repo.TOC() {
			result.records += int64(entry.RecordCount)
			if byCollection {
				collections[entry.Name] += int64(entry.RecordCount)
			}
		}
	}

	if byCollection {
		result.collections = collections
	}

	return result
}

func formatCount(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	s := fmt.Sprintf("%d", n)
	var result []byte
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			result = append(result, ',')
		}
		result = append(result, byte(c))
	}
	return string(result)
}
