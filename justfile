# Decrypt the indexer environment file and bring up the indexer service
indexer:
    #!/usr/bin/env bash
    set -euo pipefail
    echo "Decrypting indexer.enc.env..."
    sops decrypt env/indexer.enc.env > env/indexer.env
    echo "Running migrations..."
    set -a; source env/indexer.env; set +a
    go run ./cmd/migrate up
    echo "Dumping schema..."
    go run ./cmd/migrate dump-schema -o pkg/migrate/full_schema.sql
    echo "Starting indexer service with Docker Compose..."
    export GIT_COMMIT=$(git rev-parse --short HEAD)
    export BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    docker compose -f build/indexer/docker-compose.yml up --build -d

indexer-down:
    @echo "Stopping indexer service with Docker Compose..."
    docker compose -f build/indexer/docker-compose.yml down

# Decrypt the feedgen environment file and bring up the feedgen service
feedgen:
    #!/usr/bin/env bash
    set -euo pipefail
    echo "Building dashboard..."
    just dashboard-build
    echo "Decrypting feedgen.enc.env..."
    sops decrypt env/feedgen.enc.env > env/feedgen.env
    echo "Starting feedgen service with Docker Compose..."
    export GIT_COMMIT=$(git rev-parse --short HEAD)
    export BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    docker compose -f build/feedgen/docker-compose.yml up --build -d

feedgen-down:
    @echo "Stopping feedgen service with Docker Compose..."
    docker compose -f build/feedgen/docker-compose.yml down

search:
    #!/usr/bin/env bash
    set -euo pipefail
    echo "Decrypting search.enc.env..."
    sops decrypt env/search.enc.env > env/search.env
    echo "Starting search service with Docker Compose..."
    export GIT_COMMIT=$(git rev-parse --short HEAD)
    export BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    docker compose -f build/search/docker-compose.yml up --build -d

search-down:
    @echo "Stopping search service with Docker Compose..."
    docker compose -f build/search/docker-compose.yml down

crawler:
    #!/usr/bin/env bash
    set -euo pipefail
    echo "Decrypting crawler.enc.env..."
    sops decrypt env/crawler.enc.env > env/crawler.env
    set -a; source env/crawler.env; set +a
    echo "Starting crawler service with Docker Compose..."
    export GIT_COMMIT=$(git rev-parse --short HEAD)
    export BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    docker compose -f build/crawler/docker-compose.yml up --build -d

crawler-down:
    @echo "Stopping crawler service with Docker Compose..."
    docker compose -f build/crawler/docker-compose.yml down

crawler-prepare:
    #!/usr/bin/env bash
    set -euo pipefail
    echo "Decrypting crawler.enc.env..."
    sops decrypt env/crawler.enc.env > env/crawler.env
    set -a; source env/crawler.env; set +a
    echo "Building crawl list from relay..."
    go run ./cmd/crawler prepare

crawler-replay input_dir="" workers="6" *collections="":
    #!/usr/bin/env bash
    set -euo pipefail
    echo "Decrypting crawler.enc.env..."
    sops decrypt env/crawler.enc.env > env/crawler.env
    set -a; source env/crawler.env; set +a
    input_dir="{{input_dir}}"
    input_dir="${input_dir:-${CRAWLER_DATA_DIR:-data/crawler}}"
    args="--input-dir $input_dir --workers {{workers}} --truncate"
    if [[ -n "{{collections}}" ]]; then
        args="$args --collections {{collections}}"
    fi
    echo "Replaying segments from $input_dir with {{workers}} workers..."
    go run ./cmd/crawler replay $args

crawler-tally input_dir="" *collections="":
    #!/usr/bin/env bash
    set -euo pipefail
    echo "Decrypting crawler.enc.env..."
    sops decrypt env/crawler.enc.env > env/crawler.env
    set -a; source env/crawler.env; set +a
    input_dir="{{input_dir}}"
    input_dir="${input_dir:-${CRAWLER_DATA_DIR:-data/crawler}}"
    args="--input-dir $input_dir --by-collection"
    echo "Tallying records across segments in $input_dir..."
    go run ./cmd/crawler tally $args

crawler-create-mv:
    #!/usr/bin/env bash
    set -euo pipefail
    echo "Decrypting crawler.enc.env..."
    sops decrypt env/crawler.enc.env > env/crawler.env
    set -a; source env/crawler.env; set +a
    echo "Creating materialized views and backfilling..."
    go run ./cmd/crawler create-mv

crawler-reset:
    #!/usr/bin/env bash
    set -euo pipefail
    echo "Decrypting crawler.enc.env..."
    sops decrypt env/crawler.enc.env > env/crawler.env
    set -a; source env/crawler.env; set +a
    echo "Clearing crawl data and Redis state..."
    go run ./cmd/crawler reset --output-dir "${CRAWLER_DATA_DIR:-data/crawler}"

# Bring up Redis and other common services
common:
    @echo "Starting Redis and other common services with Docker Compose..."
    docker compose -f build/common/docker-compose.yml up --build -d redis

common-down:
    @echo "Stopping Redis and other common services with Docker Compose..."
    docker compose -f build/common/docker-compose.yml down redis

# ============================================================================
# ClickHouse Migrations
# ============================================================================

# Run pending ClickHouse migrations (uses env/indexer.env for credentials)
migrate-up:
    #!/usr/bin/env bash
    set -euo pipefail
    if [[ ! -f env/indexer.env ]]; then
        echo "Decrypting indexer.enc.env..."
        sops decrypt env/indexer.enc.env > env/indexer.env
    fi
    set -a; source env/indexer.env; set +a
    go run ./cmd/migrate up

# Run pending ClickHouse migrations with extended timeout (for large data migrations)
# timeout is in seconds, default 3600 (1 hour)
migrate-up-long timeout="3600":
    #!/usr/bin/env bash
    set -euo pipefail
    if [[ ! -f env/indexer.env ]]; then
        echo "Decrypting indexer.enc.env..."
        sops decrypt env/indexer.enc.env > env/indexer.env
    fi
    set -a; source env/indexer.env; set +a
    echo "Running migrations with {{timeout}}s read timeout..."
    go run ./cmd/migrate --read-timeout={{timeout}} up

# Rollback N migrations (default: 1)
migrate-down steps="1":
    #!/usr/bin/env bash
    set -euo pipefail
    if [[ ! -f env/indexer.env ]]; then
        echo "Decrypting indexer.enc.env..."
        sops decrypt env/indexer.enc.env > env/indexer.env
    fi
    set -a; source env/indexer.env; set +a
    go run ./cmd/migrate down {{steps}}

# Show current migration version
migrate-version:
    #!/usr/bin/env bash
    set -euo pipefail
    if [[ ! -f env/indexer.env ]]; then
        echo "Decrypting indexer.enc.env..."
        sops decrypt env/indexer.enc.env > env/indexer.env
    fi
    set -a; source env/indexer.env; set +a
    go run ./cmd/migrate version

# Force set migration version (use to fix dirty state)
migrate-force version:
    #!/usr/bin/env bash
    set -euo pipefail
    if [[ ! -f env/indexer.env ]]; then
        echo "Decrypting indexer.enc.env..."
        sops decrypt env/indexer.enc.env > env/indexer.env
    fi
    set -a; source env/indexer.env; set +a
    go run ./cmd/migrate force {{version}}

# Create a new migration file
migrate-create name:
    #!/usr/bin/env bash
    set -euo pipefail
    dir="pkg/migrate/migrations"
    mkdir -p "$dir"
    # Find the next version number
    last=$(ls "$dir"/*.up.sql 2>/dev/null | sed 's/.*\///' | sort -n | tail -1 | cut -d'_' -f1 || echo "000000")
    next=$(printf "%06d" $((10#$last + 1)))
    touch "$dir/${next}_{{name}}.up.sql" "$dir/${next}_{{name}}.down.sql"
    echo "Created $dir/${next}_{{name}}.{up,down}.sql"
    echo "Remember to run 'just migrate-dump-schema' after applying migrations"

# Dump current schema from ClickHouse to reference file (requires running database)
migrate-dump-schema:
    #!/usr/bin/env bash
    set -euo pipefail
    if [[ ! -f env/indexer.env ]]; then
        echo "Decrypting indexer.enc.env..."
        sops decrypt env/indexer.enc.env > env/indexer.env
    fi
    set -a; source env/indexer.env; set +a
    go run ./cmd/migrate dump-schema -o pkg/migrate/full_schema.sql

# ============================================================================
# Dashboard
# ============================================================================

# Build the dashboard (React SPA)
dashboard-build:
    #!/usr/bin/env bash
    set -euo pipefail
    cd dashboard
    echo "Installing dependencies..."
    pnpm install
    echo "Building dashboard..."
    pnpm build
    touch dist/.gitkeep
    echo "Dashboard built successfully"

# Start the dashboard development server
dashboard-dev:
    #!/usr/bin/env bash
    set -euo pipefail
    cd dashboard
    echo "Installing dependencies..."
    pnpm install
    echo "Starting dev server at http://localhost:3001/dashboard/"
    pnpm dev
