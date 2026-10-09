# ATProto Package

Go services for the ATProto (Bluesky) network: firehose indexing into ClickHouse, a feed generator, a search and stats service, and a network-wide repo crawler.

## Major Projects

### Indexer (`cmd/indexer`, `pkg/indexer`)
Subscribes to Jetstream WebSocket, consumes firehose events, and indexes Bluesky records into ClickHouse. Tracks progress in Redis with cursor recovery for restarts.

It also follows the PLC directory export into `plc_operations` (`pkg/plc`, on by default, `--enable-plc-exporter`), which feeds the `plc_did_state` table the crawler verifies against. An optional profile hydrator (`pkg/profilehydrator`, `--enable-profile-hydrator`) fills `profiles` from the public AppView.

### Feed Generator (`cmd/feedgen`, `pkg/feed-generator`)
HTTP service providing Bluesky-compatible feed endpoints. Implements trending/hot feeds, label-based feeds (private communities), and static feeds. JWT and API key authentication.

It also serves an admin dashboard at `/dashboard/` (a React SPA in `dashboard/`, embedded in the binary) for managing who's in the label-based feeds, backed by the API-key routes under `/api/admin`. `just dashboard-dev` runs it on Vite at http://localhost:3001/dashboard/.

### Search Service (`cmd/search`, `pkg/search`)
HTTP service for search, statistics, and repository maintenance. Provides site-wide analytics, stats caching, and cleanup operations.

The total-users stat comes from `pkg/usercount`, which scrapes `listRepos` on every official Bluesky PDS. The scrape list lives in the Redis hash `usercount:pdslist` (host → `count|pagesize|cursor`), seeded/merged from `PDSHostList` in `pkg/usercount/usercount.go` at startup. `GET /pds` lists the current hosts and counts. `POST /pds {"host": "https://<name>.host.bsky.network"}` (API key) adds an official host at runtime without a restart. The host has to answer a `describeServer` probe, and third-party hosts are rejected.

Also serves link-proxy embeds: bsky.app-shaped URLs (`/profile/:ident`, `/profile/:ident/post/:rkey`) return rich OpenGraph pages to link-preview crawlers and 302 humans to bsky.app. Post previews use a headless-Chromium screenshot of the post + top 2 replies (`/embed/post/:ident/:rkey/card.png`, `pkg/search/postcard`), hydrated via an authenticated AppView session (`pkg/search/appview`, `BSKY_IDENTIFIER`/`BSKY_APP_PASSWORD`) so posts from accounts with logged-out visibility disabled still preview. Append `?embed` to force the OG page in a browser.

### Crawler (`cmd/crawler`, `pkg/crawler`)
A CLI that fetches every repo on the network into `.rca` archive segments (or straight into ClickHouse) and replays them into the `crawl_records` table. `prepare` builds the crawl list from the relay's hosts, `crawl` fetches the repos, and `replay`, `create-mv`, `tally`, `inspect` and `reset` work with the results. See [cmd/crawler/README.md](cmd/crawler/README.md).

## Development Workflow


```bash
# Start services (uses sops for secret decryption)
just common        # Start Redis
just indexer       # Run migrations, then start the indexer
just feedgen       # Build the dashboard, then start the feed generator
just search        # Start search service
just crawler       # Start the crawler container

# Stop services
just indexer-down
just feedgen-down
just search-down
just crawler-down
just common-down
```

The service recipes decrypt `env/<service>.enc.env` with sops and build their images with the monorepo's `packages/` directory as the Docker context. Without those (in the public bsky-experiments repo, say), copy `env/<service>.env.example` to `env/<service>.env` and run the binaries directly:

```bash
just common                     # Redis
set -a; source env/indexer.env; set +a
go run ./cmd/migrate up
go run ./cmd/indexer
```

In the public repo, `build/common/docker-compose.yml` also has a ClickHouse service: `docker compose -f build/common/docker-compose.yml up -d clickhouse`.

### Building

All services use multi-stage Docker builds. Binaries compile with `CGO_ENABLED=0 GOOS=linux`.

```bash
# Build locally
go build -o indexer ./cmd/indexer
go build -o feedgen ./cmd/feedgen
go build -o search ./cmd/search
go build -o crawler ./cmd/crawler
go build -o migrate ./cmd/migrate

# Run tests
go test ./...
```

### Service Ports
These are the host ports the compose files publish. The binaries default to 8080 for the app and 8081 for metrics (`LISTEN_ADDRESS`, `METRICS_LISTEN_ADDRESS`).
- Indexer: 8091 (metrics only)
- Feedgen: 8094 (app), 8095 (metrics)
- Search: 8092 (app), 8093 (metrics)
- Crawler: 8096 (metrics, from `env/crawler.env.example`)
- Redis: 6379

## Data Schemas

ClickHouse schema is managed via migrations in [pkg/migrate/migrations/](pkg/migrate/migrations/), applied by `cmd/migrate`.
`just migrate-dump-schema` writes the full schema to `pkg/migrate/full_schema.sql`.

```bash
# Migration commands
just migrate-up           # Apply pending migrations
just migrate-up-long      # The same with a 1 h read timeout, for large data migrations
just migrate-down         # Rollback last migration
just migrate-version      # Show current version
just migrate-force 12     # Set the version after a failed (dirty) migration
just migrate-create foo   # Create new migration files
just migrate-dump-schema  # Regenerate full schema reference
```

### Core Tables
| Table | Purpose |
|-------|---------|
| `repo_records` | Raw firehose events (repo, collection, rkey, operation, record_json) |
| `posts` | Post content (did, uri, text, langs, parent_uri, root_uri) |
| `follows` | Social graph follow relationships |
| `likes` | Post engagement |
| `reposts` | Post sharing |
| `blocks` | User blocking relationships |
| `actor_labels` | Access control labels for private feeds |
| `daily_stats` | Aggregated daily statistics |
| `api_keys` | API authentication keys |
| `repo_cleanup_jobs` | Maintenance job tracking |
| `plc_operations` / `plc_did_state` | PLC directory operations and each DID's current PDS |
| `profiles` | Profiles filled by the profile hydrator |
| `feed_request_analytics` | Feed requests served by the feed generator |
| `crawl_repos` / `crawl_records` | The crawler's repo list and replayed records |

### Views
- `recent_posts` - Posts from last 72 hours
- `recent_posts_with_score` - Trending posts with engagement scoring
- `following_counts` - User following statistics
- `daily_stats_*` - Materialized views for daily aggregations

## Key Packages

| Package | Location | Purpose |
|---------|----------|---------|
| Store | [pkg/indexer/store/](pkg/indexer/store/) | ClickHouse operations (batch inserts, feeds, labels, auth, stats) |
| Feeds | [pkg/feeds/](pkg/feeds/) | Feed implementations (hot, authorlabel, static) |
| Auth | [pkg/auth/](pkg/auth/) | JWT + API key authentication |
| Endpoints | [pkg/feed-generator/endpoints/](pkg/feed-generator/endpoints/) | Feed REST endpoints |
| Search Endpoints | [pkg/search/endpoints/](pkg/search/endpoints/) | Search/stats REST endpoints |
| PLC | [pkg/plc/](pkg/plc/) | PLC directory export follower |
| Crawler | [pkg/crawler/](pkg/crawler/) | Crawl list, fetch pipeline, ClickHouse writer |
| Repo archive | [pkg/repoarchive/](pkg/repoarchive/) | The `.rca` segment format |
| Secret files | [pkg/secretfile/](pkg/secretfile/) | `<NAME>_FILE` support for secrets |

## Feed Implementations

Located in [pkg/feeds/](pkg/feeds/):

- **Hot** (`hot/feed.go`): "whats-hot", "top-1h", "top-24h" - trending algorithm with Redis caching
- **Author Label** (`authorlabel/feed.go`): "a-mpls", "cl-tqsp" - private/public community feeds based on actor labels
- **Static** (`static/feed.go`): "bangers", "at-bangers", etc. - pinned posts

All feeds implement the interface in [pkg/feed-generator/feed.go](pkg/feed-generator/feed.go).

## Configuration

Each service reads its settings from `env/<service>.env` (indexer, feedgen, search, crawler). `env/<service>.env.example` lists the variables. In the monorepo the real files are sops-encrypted as `env/<service>.enc.env`, which the `just` recipes decrypt.

Key environment variables:
- `WS_URL` - Jetstream WebSocket endpoint (indexer)
- `REDIS_ADDRESS` - Redis connection
- `CLICKHOUSE_ADDRESS`, `CLICKHOUSE_USERNAME`, `CLICKHOUSE_PASSWORD` - ClickHouse connection
- `SERVICE_ENDPOINT`, `FEED_ACTOR_DID` - the feed generator's public URL and the account that publishes its feeds
- `BSKY_IDENTIFIER`, `BSKY_APP_PASSWORD`, `PUBLIC_URL` - search's link-preview session and base URL
- `OTEL_EXPORTER_OTLP_ENDPOINT` - OpenTelemetry endpoint

Secrets (`CLICKHOUSE_PASSWORD`, `MAGIC_HEADER_VAL`, `BSKY_IDENTIFIER`, `BSKY_APP_PASSWORD`) can also come from a file named by `<NAME>_FILE`.

## Dependencies

- ATProto: `github.com/bluesky-social/indigo`, `github.com/bluesky-social/jetstream`
- Database: `github.com/ClickHouse/clickhouse-go/v2`, `github.com/redis/go-redis/v9`
- Web: `github.com/labstack/echo/v4`, `github.com/samber/slog-echo`
- Telemetry: `go.opentelemetry.io/otel` and the shared telemetry module (`packages/telemetry` in the monorepo, `telemetry/` in the public repo)

## Patterns

- **Soft deletes**: ReplacingMergeTree with `deleted UInt8 DEFAULT 0`, queries filter on `deleted = 0`
- **Time versioning**: `time_us` column for ClickHouse deduplication
- **OpenTelemetry**: Each module creates tracer via `otel.Tracer("module-name")`
- **Graceful shutdown**: Signal handling with timeout-based cleanup
