# Crawler

CLI tool that crawls every ATProto repo from every PDS on the network and archives them into `.rca` (Repo Archive) segment files, or writes the records straight into ClickHouse. `prepare` builds the list of repos to crawl from the relay's hosts and checks each repo's PDS against the PLC directory state in ClickHouse (`plc_did_state`, which the indexer's PLC exporter fills).

## Commands

| Command | Description | Key flags |
|---------|-------------|-----------|
| `prepare` | Lists the relay's hosts (`listHosts`), enumerates each host's repos with `listRepos`, verifies them and writes the crawl list to `crawl_repos` (truncated first) | `--relay-host` (`https://bsky.network`), `--discovery-workers` (50), `--discovery-rps` (5), `--list-repos-limit` (1000), `--skip-pds`, `--verify` (on) |
| `crawl` | Full network crawl of the repos in `crawl_repos`, or of a DID file | `--output-dir` (`./data/rca`), `--workers` (50), `--default-pds-rps` (5), `--max-pds-rps` (cap), `--max-retries` (3), `--dids-from`, `--outcomes`, `--segment-size` (2GB), `--zstd-level` (3), `--page-size` (50000), `--skip-pds`, `--direct` |
| `replay` | Replay `.rca` archives into the ClickHouse `crawl_records` table | `--input-dir` (`./data/rca`), `--collections` (filter), `--workers` (4), `--truncate`, `--append` |
| `create-mv` | Create the `crawl_records` stats tables and materialized views, and backfill them from existing rows | |
| `inspect` | Print segment header, index, and per-collection stats | `<segment-file>` positional arg, `--repos` (one did, pds, records, crawled_at_us line per repo) |
| `tally` | Count records across all segments without decompressing | `--input-dir` (`./data/rca`), `--by-collection`, `--workers` (1, raise it on SSDs) |
| `reset` | Delete the `.rca` files in a directory | `--output-dir` |

`prepare`, `crawl`, `replay` and `create-mv` take the `--clickhouse-address`, `--clickhouse-username` and `--clickhouse-password` flags (or `CLICKHOUSE_*` env vars). Every flag also has an env var (`OUTPUT_DIR`, `WORKERS` and so on, see `env/crawler.env.example`).

`crawl --dids-from FILE` crawls the `did<TAB>pds` lines of a file instead of `crawl_repos` and needs no ClickHouse (it writes segments only). `--outcomes FILE` appends one line per DID with its final result: `did, pds, result, records, collections, crawled_at_us, attempts, status, xrpc_error, message`. Every DID it was given gets exactly one line, so failures can be grepped out and fed back with `--dids-from`.

`replay` without `--append` prepares the table for a bulk load: it kills queries that mention `crawl_records`, stops merges, drops the skip indexes and ends with `OPTIMIZE TABLE crawl_records FINAL`. That's for loading an empty table. To add a recrawl to the live table use `replay --append`, which only INSERTs. `replay` exits non-zero if a segment fails partway.

Rows get `crawled_at` and `time_us` = the time the getRepo request started (so they never outrank a firehose event the snapshot doesn't include), `indexed_at` = insert time. `crawl_records` is `ReplacingMergeTree(time_us)` on `(collection, repo, rkey)` partitioned by month of `crawled_at`, so a repo replayed twice in one month collapses on merge but a repo crawled in two months keeps both copies: check `crawl_records_by_repo` before recrawling a repo that has rows.

With `--verify`, a `did:plc` repo whose PDS doesn't match `plc_did_state` is dropped, and one missing from it is kept as unverified (it could be a new account). `did:web` repos are checked by fetching their DID document.

## Architecture

4-stage concurrent pipeline:

```
ClickHouse ──page──▶ Coordinator ──groups──▶ Dispatcher ──tasks──▶ Workers ──repos──▶ Writer
(crawl_repos)         (crawler.go)          (dispatcher.go)       (worker.go)        (writerLoop)
```

### 1. Coordinator (`pkg/crawler/crawler.go`)

Pages DIDs from `crawl_repos FINAL` ordered by DID, groups them by PDS URL into `map[string][]string`, drops junk PDS entries (localhost, example domains) and hosts that don't resolve in DNS, and feeds pages to the dispatcher. It applies backpressure by waiting while the dispatcher has more than 10x the page size left. When fewer than 50 PDSs are active, it keeps fetching anyway, up to 10M undispatched DIDs, so a few large PDSs don't starve the workers.

### 2. Dispatcher (`pkg/crawler/dispatcher.go`)

Receives pages via `Feed()`, maintains per-PDS state with token-bucket rate limiting (`golang.org/x/time/rate`). Adaptive rate selection based on PDS size:
- `*.host.bsky.network`: 10 RPS
- `<100 DIDs`: 2 RPS (small self-hosted)
- `<1000 DIDs`: default RPS (5)
- `>=1000 DIDs`: 10 RPS

`--max-pds-rps` caps all of these. A PDS also has at most 2x its rate in fetches in flight (at least 2), so hosts that hang until a timeout can't hold every worker and stall the others. A SIGINT aborts fetches in flight and finalizes the open segment; DIDs that were in flight get no outcome line, so resume from `--dids-from` minus the DIDs in `--outcomes`.

Failures are the repo's unless they say something about the host:
- A 5xx is the repo's failure (PDSes answer 5xx for repos they can't serve). It's retried twice, 30 s and 60 s later, then recorded with its status and XRPC `error`/`message`. It never slows or pauses the host.
- A 429 pauses the host for its `Retry-After` (default 10 s, at most 10 min) and halves its rate; the repo goes again when the host resumes, up to 10 attempts.
- Connection, DNS, TLS and timeout errors are host errors: the repo is retried `--max-retries` times (10 s, 20 s, 40 s), every 5 halve the host's rate (floor 0.5 RPS), and 10 in a row pause the host for 1 min, doubling to 15 min; after a pause one more failure pauses it again. A paused host keeps its DIDs.
- A success restores the host's rate and clears its pauses.
- The only block: a host that isn't `*.bsky.network` and hasn't served a repo this run, after 10 different repos in a row fail with 5xx (each counted once, after its retries) or 2 h of host errors. Its remaining DIDs are recorded as `pds_blocked` with the reason.

The February 2026 crawl blocked a PDS on 10 consecutive 5xx and dropped its remaining DIDs, and didn't retry 429s or 5xx; that left 3.0M repos on 7 bsky.network PDSes, and 48k rate-limited repos, out of `crawl_records`.

### 3. Worker Pool (`pkg/crawler/worker.go`)

50 concurrent goroutines (configurable). Each worker:
1. HTTP GET `{pds}/xrpc/com.atproto.sync.getRepo?did={did}` with `Accept: application/vnd.ipld.car`
2. Body capped at `--max-repo-size-mb` (100) via `io.LimitReader`; a repo over it is recorded as `too_large`, not retried. Before this, a repo over the cap read as a truncated CAR and was recorded as `parse_error`, as most likely were the February 2026 crawl's 1,576. They're the most active accounts: recrawl them with a bigger cap and few workers, since a repo is parsed in memory
3. Custom CAR parser (`pkg/crawler/carrepo/`) that skips SHA256 block verification for ~30-40% CPU savings
4. MST walk extracts all records, CBOR decoded to JSON with CID links stripped (`cborjson.go`)
5. Groups records by collection. In archive mode the worker serializes and zstd-compresses the repo block itself and sends it to the writer. With `--direct` it sends the records to the ClickHouse writer (`chwriter.go`).

Error classification, from the status and the XRPC `error` in the body: `not_found`, `deactivated`, `takendown` (per repo, final), `unavailable` (5xx, per repo, retried), `http_error` (other 4xx), `rate_limited` (429), and the host errors `dns_error`, `timeout`, `connection_error`.

### 4. Segment Writer (`writerLoop` in `crawler.go`, `pkg/repoarchive/writer.go`)

Receives pre-compressed repo blocks from a buffered channel (cap 100) and appends them to the current `.rca` segment, so compression runs in parallel and the file I/O stays sequential. Rotates to a new segment file when current exceeds target size (default 2GB) and logs progress every 30s. Segment files named `segment_NNNN.rca`.

## Key Packages

| Package | Purpose |
|---------|---------|
| `pkg/crawler/prepare.go` | `prepare`: host discovery, repo enumeration, verification, `crawl_repos` inserts |
| `pkg/crawler/discovery.go` | Relay `listHosts` and per-PDS `listRepos` |
| `pkg/crawler/verify.go` | PDS checks against `plc_did_state` and `did:web` documents |
| `pkg/crawler/crawler.go` | Orchestrator, page loop, backpressure, writer goroutine |
| `pkg/crawler/dispatcher.go` | Per-PDS rate limiting, adaptive backoff, blocking |
| `pkg/crawler/worker.go` | HTTP fetch, CAR parse, error classification, `RepoError` type |
| `pkg/crawler/chwriter.go` | `--direct` writes to `crawl_records` |
| `pkg/crawler/cborjson.go` | CBOR-to-JSON with CID link stripping |
| `pkg/crawler/progress.go` | In-memory progress (cursor, completed set, failure counts) |
| `pkg/crawler/metrics.go` | Prometheus counters/histograms |
| `pkg/crawler/carrepo/car.go` | CARv1 reader without SHA256 verification |
| `pkg/crawler/carrepo/repo.go` | Commit parsing, `ForEach` record iteration |
| `pkg/crawler/carrepo/mst.go` | Direct MST node walking with prefix-compressed key reconstruction |
| `pkg/repoarchive/` | `.rca` binary format: types, encoding, writer, reader, index |

## RCA Binary Format

Repo Archive (`.rca`) is a binary format for storing crawled ATProto repositories in segment files.

### File Layout

```
[FileHeader 64B] [Repo Block]... [Index Section] [FileFooter 32B]
```

### FileHeader (64 bytes)

| Offset | Size | Type | Field | Notes |
|--------|------|------|-------|-------|
| 0 | 8 | `[8]byte` | Magic | `REPOARCH` |
| 8 | 2 | `uint16` | Version | `1` |
| 10 | 2 | `uint16` | Flags | Reserved (0) |
| 12 | 8 | `int64` | CreatedAt | Unix microseconds |
| 20 | 4 | `uint32` | RepoCount | Set at finalize |
| 24 | 8 | `int64` | IndexOffset | Byte offset of index section, set at finalize |
| 32 | 32 | `[32]byte` | Reserved | Zero-filled padding |

### Repo Block

Each repo block is self-contained and skippable via `TotalBlockSize`:

```
[TotalBlockSize uint32]
[DID            uint16-prefixed string]
[PDS            uint16-prefixed string]
[Rev            uint16-prefixed string]
[CrawledAt      int64 (unix microseconds)]
[CollectionCount uint16]
[CRC32          uint32 (of TOC + data blocks)]
[Collection TOC entries...]
[Compressed data blocks...]
```

**Collection TOC Entry:**
```
[Name           uint16-prefixed string]
[RecordCount    uint32]
[CompressedSize uint32]
```

**Compressed Data Block** (one per collection, zstd-compressed):

Inside each decompressed block, records are sequential:
```
[RKey  uint16-prefixed string]
[JSON  uint32-prefixed bytes]
... (repeated RecordCount times)
```

The CRC32 covers everything after the CRC32 field (all TOC entries + all compressed data blocks).

### Index Section

Sorted by DID for binary search. Written after all repo blocks, before the footer.

```
[EntryCount  uint32]
[IndexEntry]...
```

**IndexEntry:**
```
[DID    uint16-prefixed string]
[Offset int64  (byte offset of repo block)]
[Size   uint32 (byte size of repo block)]
[CRC32  uint32 (CRC32 of entire repo block bytes)]
```

### FileFooter (32 bytes)

| Offset | Size | Type | Field | Notes |
|--------|------|------|-------|-------|
| 0 | 8 | `int64` | IndexOffset | Byte offset of index section |
| 8 | 4 | `uint32` | IndexSize | Byte size of index section |
| 12 | 4 | `uint32` | RepoCount | Total repos in segment |
| 16 | 4 | `uint32` | CRC32 | CRC32 of index section bytes |
| 20 | 4 | `[4]byte` | Reserved | Padding |
| 24 | 8 | `[8]byte` | Magic | `REPOARCH` |

### Encoding Rules

- All multi-byte integers: **little-endian**
- Checksums: **CRC32-IEEE** (`crc32.ChecksumIEEE`)
- Compression: **zstd** (default level 3, configurable 1-19)
- Length-prefixed strings: `uint16` prefix for short strings (DID, PDS, Rev, rkey, collection name), `uint32` prefix for JSON record data
- Collections sorted alphabetically within each repo block

## Progress

Progress lives in memory (`pkg/crawler/progress.go`), so a restarted crawl starts over from the beginning of `crawl_repos`. It tracks the page cursor, the current segment number, the set of DIDs crawled or permanently failed, and a counter per failure category (not_found, deactivated, takendown, rate_limited, parse_error, http_error, dns_error, unavailable, connection_error, timeout, unknown).

Permanent failures (not_found, deactivated, takendown, parse_error) are also added to the completed set, so `FilterPage` needs one lookup per DID.

## Prometheus Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `crawler_repos_processed_total` | counter (label: `result`) | Repos by result category |
| `crawler_repo_duration_seconds` | histogram | Fetch + parse time per repo |
| `crawler_repo_records` | histogram | Records per repo |
| `crawler_repo_collections` | histogram | Collections per repo |
| `crawler_repo_compressed_bytes` | histogram | Compressed bytes per repo |
| `crawler_http_requests_total` | counter (label: `status_code`) | HTTP requests to PDSs |
| `crawler_http_request_duration_seconds` | histogram | HTTP latency |
| `crawler_segments_finalized_total` | counter | Completed segment files |
| `crawler_bytes_written_total` | counter | Total bytes to disk |
| `crawler_dids_processed_total` | counter | Running DID count |
| `crawler_pages_fetched_total` | counter | ClickHouse pages fetched |
| `crawler_page_dids` | histogram | DIDs per page (post-filter) |
| `crawler_pds_count` | gauge | Distinct PDSs tracked |
| `crawler_dispatched_total` | counter | DIDs dispatched to workers |
| `crawler_dispatch_remaining` | gauge | Undispatched DIDs |
| `crawler_pds_blocked_total` | counter | PDSs blocked |
| `crawler_active_pds` | gauge | PDSs with remaining work |
| `crawler_records_dropped_total` | counter (label: `reason`) | Records dropped while parsing a repo |
| `crawler_hosts_discovered_total` | counter | PDS hosts found from the relay (`prepare`) |
| `crawler_discovery_repos_total` | counter | Repos found with `listRepos` |
| `crawler_discovery_pages_total` | counter | `listRepos` pages fetched |
| `crawler_discovery_errors_total` | counter (label: `error_type`) | Discovery errors |
| `crawler_verification_total` | counter (label: `result`) | PDS verification results |
| `crawler_prepare_inserted_total` | counter | Rows inserted into `crawl_repos` |
| `crawler_prepare_expected_repos` | gauge | Repos the relay's host account counts add up to |
| `crawler_prepare_hosts_remaining` | gauge | Hosts left to enumerate |
| `crawler_prepare_duration_seconds` | histogram | Total `prepare` duration |

`replay` has its own `replay_*` metrics (records, batches, segments, parse errors, batch size).
