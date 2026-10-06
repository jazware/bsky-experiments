# atproto services deployment

The indexer, feed generator, search service and their shared Redis run on the devbox. yeet deploys
them, one stack each, in `deploy/yeet/stacks/`:

| Stack | Compose project | Container | Ports | Gate |
|---|---|---|---|---|
| `atproto-redis` | `common` | `atproto-redis` | host network, 6379 | none (see below) |
| `atproto-indexer` | `indexer` | `atproto-indexer` | 8091 (metrics, pprof, `/healthz`) | `/healthz` on 8091, 5 passes in a row |
| `atproto-feedgen` | `feedgen` | `feedgen` | 8094 (app), 8095 (metrics) | `/healthz` on 8094 |
| `atproto-search` | `search` | `bsky-search` | 8092 (app), 8093 (metrics) | `/healthz` on 8092 |

The ports bind on every interface, as the hand-run compose files had them. feedsky.jazco.io and
bsky-search.jazco.io route to 8094 and 8092 from outside this repo (no tunnel config here names
them).

## Images

Built on the Mac and pushed to GHCR, the way boards and leadsheet are:

```bash
cd packages/atproto
just docker-push              # all three: ghcr.io/jazware/mono/atproto-<service>:<12-char commit>, plus :main when HEAD is on main
just docker-push search       # one of indexer, feedgen, search
```

It builds the committed tree only (`git archive HEAD` of packages/atproto, version and telemetry,
the Dockerfiles' `packages/` context) for linux/amd64. The Go stages cross-compile on the Mac's own
platform. search's runtime stage runs apt for Chromium under emulation, so if that fails with "exec
format error", OrbStack lost Rosetta: `orb stop && orb start`.

## Deploying

```bash
export YEET_HQ=https://yeet.jazco.dev     # or https://monitoring.goat-alpha.ts.net:8443
yeet plan atproto-indexer                 # from origin/main; --commit <branch> for a pushed branch
yeet deploy atproto-indexer               # resolves :main to its digest, recreates what changed, gates on /healthz
yeet bundles atproto-indexer              # then `yeet rollback atproto-indexer --to <bundle>`
```

The stacks pin `:main` by digest at plan time, so push the images before deploying. ClickHouse
migrations aren't part of a deploy: when a release adds one, run `just migrate-up` from the Mac
first (it also needs the indexer's sops file).

The non-secret settings (ClickHouse and Redis addresses, `SERVICE_NAME`, the feed DID, `WS_URL` and
so on) are `[vars]` in each stack.toml, copied from the `.enc.env` files. Change both when one
changes, since `just indexer` and friends still read the `.enc.env` files.

### Adoption

Each stack renders the hand-run project's names: the same project, container name, ports, default
network and, for Redis, the `/data/redis` bind mount. The first plan from `yeet-atproto`:

- `atproto-redis`: `FILES_ONLY`. compose.yaml is added and the container is left running. The
  template uses the tag (`images.redis.ref`), not the digest, because a pinned image string changes
  compose's config hash, and the stack has no `[services.redis]` table, because any table gives
  the service an input hash the live container doesn't carry. Either would recreate Redis. Its
  data would survive that (AOF on the bind mount), but every other service would lose Redis for a
  few seconds. Add a tcp gate on 127.0.0.1:6379 the next time Redis is recreated anyway.
- `atproto-indexer`, `atproto-feedgen`, `atproto-search`: `RECREATE`. The image moves from the
  locally built one to the ghcr digest, the ClickHouse password (and search's other secrets) moves
  to a mounted file, and yeet's input hash label is new. None of them keeps data in the
  container: the indexer's cursor is in Redis, written every five seconds, so a recreate replays a
  few seconds of Jetstream.

search's compose service is called `indexer`, as in `build/search/docker-compose.yml`. The live
container is labelled with that service name, and renaming it would make compose create a second
`bsky-search` next to the old one.

A first deploy has no earlier bundle to roll back to: a failed gate holds the deploy (`yeet
approve`), and the new container keeps running.

### Break-glass: `just indexer`, `just feedgen`, `just search`, `just common`

The hand-run compose files still work, with the secrets as env vars from the `.enc.env` files:

```bash
cd ~/jazware/mono/packages/atproto
git pull && just indexer     # decrypts env/indexer.enc.env, runs migrations, builds and starts it
```

They take over the containers yeet started (same project and container names), so the next `yeet
plan` shows those services changing back. `just indexer`, `just feedgen` and `just search` build
the image on the devbox instead of pulling the ghcr one.

## Secrets

`env/indexer.enc.env`, `env/feedgen.enc.env` and `env/search.enc.env` have their own rule in
`/.sops.yaml` that adds yeet-hq's age key, so hq can decrypt them (crawler's doesn't). yeet writes
each secret to a root-owned 0400 file in `/run/yeet/atproto-<service>/secrets/` (tmpfs), mounted at
`/run/atproto`. The images have no `USER`, so root reads them.

Every secret the services read also works as a file, named by the same variable with `_FILE`
(`pkg/secretfile`):

| Variable | Read by |
|---|---|
| `CLICKHOUSE_PASSWORD_FILE` | indexer, feedgen, search |
| `MAGIC_HEADER_VAL_FILE` | search |
| `BSKY_IDENTIFIER_FILE` | search |
| `BSKY_APP_PASSWORD_FILE` | search |

One trailing newline is dropped. Setting both `NAME` and `NAME_FILE` (or the flag and `NAME_FILE`)
fails startup, as does a missing, unreadable or empty file. The errors name the variable and path,
never the value. An empty `NAME` counts as unset. Plain env vars still work, which is what the
`build/*/docker-compose.yml` files use. Nothing reads `MAGIC_HEADER_KEY` (indexer and search) or
the indexer's `MAGIC_HEADER_VAL`, so the stacks leave them out.

## Health

- indexer: `/healthz` on the metrics listener answers 200 with `{"last_seq", "last_event_age_s"}`
  when an event was processed in the last 30 s, else 503. The cursor loaded from Redis carries the
  previous run's time, so it's 503 after a restart until Jetstream delivers again. The indexer
  still exits by itself after 60 s without events, and `restart: always` brings it back.
- feedgen and search: `/healthz` answers 200 once the router is up, which is after ClickHouse and
  Redis connected. It's left out of the request log.

## Monitoring

Alloy on the devbox scrapes 8091, 8093 and 8095 (metrics and Go profiles) as job
`atproto-services`. Each app stack declares its own target as `[[contribute.alloy_scrape]]`, and
yeet's `alloy` stack renders the union, so changing one reloads Alloy on the devbox.
