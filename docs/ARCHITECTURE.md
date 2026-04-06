# Fukan Ingest — Architecture

> Go ETL pipeline that consumes external telemetry feeds, normalizes them into a unified schema, publishes via NATS core (RAM-only), batch-inserts into ClickHouse, and publishes live updates to Redis.

---

## System Overview

```
External Feeds (ADS-B, AIS, TLE, BGP, News)
        │
        ▼
┌──────────────────────────────────────────┐
│ ETL Workers (one per feed type)          │
│  Parse → Normalize → Compute H3         │
│  Publish to NATS (core)                  │
└──────────────────┬───────────────────────┘
                   │ fukan.telemetry.{asset_type}
                   ▼
          NATS (core)
          Queue group: batcher-{asset_type}
          (in-memory, no persistence)
                   │
                   ▼
┌──────────────────────────────────────────┐
│ Batchers (one per asset type)            │
│  Accumulate 10k events OR 2s timeout     │
└─────────┬────────────────┬───────────────┘
          │                │
          ▼                ▼
    ClickHouse         Redis pub/sub
    (native proto)     telemetry:{h3_cell}
    telemetry_raw      (non-fatal)
          │
          ▼ (materialized views)
    telemetry_latest   (ReplacingMergeTree)
    telemetry_h3_agg   (AggregatingMergeTree, 5-min buckets)
```

---

## CLI (Cobra + Viper)

A single unified binary `cmd/fukan-ingest` uses **Cobra** for subcommands and **Viper** for layered configuration (CLI flags > env vars > YAML file > defaults).

The entrypoint (`cmd/fukan-ingest/main.go`) is ~15 lines — it sets up structured logging and calls `commands.Execute()`. All command logic lives in `internal/commands/`.

Configuration is loaded via `--config <path>` flag or auto-discovered at `./config.yaml` / `/etc/fukan-ingest/config.yaml`. Typed config structs live in `internal/config/config.go` with `mapstructure` tags for Viper unmarshalling. Required fields are validated at startup via `Config.Validate()`. Legacy environment variables (e.g. `NATS_URL`, `CLICKHOUSE_ADDR`) are explicitly bound and continue to work as overrides.

Config is passed to subcommands via `context.WithValue` from `PersistentPreRunE` — no global mutable state.

### Subcommands

#### `fukan-ingest worker --type <feed>`

Connects to external data feeds, normalizes events into `FukanEvent`, and publishes to NATS. Reads the `integrations.<feed>` section from config to spawn one worker goroutine per configured provider.

```bash
# Via config file
fukan-ingest worker --type adsb --config config.yaml

# Via env vars (legacy fallback — creates a single integration from ADSB_FEED_URL)
ADSB_FEED_URL=https://example.com/feed NATS_URL=nats://localhost:4222 \
  fukan-ingest worker --type adsb
```

#### `fukan-ingest batcher --type <asset>`

Subscribes to NATS, accumulates events in memory, and flushes to ClickHouse in batches. Publishes each batch to Redis for real-time streaming.

```bash
fukan-ingest batcher --type aircraft --config config.yaml
```

#### `fukan-ingest refresh --target <target>`

Refreshes reference data from external sources. Currently supports `--target airlines` to download the OpenSky aircraft database CSV (~600k rows) and load into ClickHouse. Supports `--dry-run` to parse without writing.

```bash
fukan-ingest refresh --target airlines --config config.yaml
```

#### `fukan-ingest migrate [up|down|version]`

Manages ClickHouse schema migrations. Migration SQL files are embedded in the binary via `embed.FS`.

```bash
fukan-ingest migrate up                   # apply all pending migrations
fukan-ingest migrate down                 # revert all migrations
fukan-ingest migrate down -n 1            # revert last migration
fukan-ingest migrate version              # print current version
```

#### `fukan-ingest version`

Prints version, commit, and build date (set via `-ldflags` at build time).

### Configuration Reference

See `config.example.yaml` for the full YAML schema. Key sections:

| YAML key | Env var override | Default | Description |
|---|---|---|---|
| `nats.url` | `NATS_URL` | `nats://localhost:4222` | NATS server address |
| `clickhouse.addr` | `CLICKHOUSE_ADDR` | `localhost:9000` | ClickHouse native protocol address |
| `clickhouse.database` | `CLICKHOUSE_DATABASE` | `fukan` | Target database |
| `clickhouse.user` | `CLICKHOUSE_USER` | `default` | Auth user |
| `clickhouse.password` | `CLICKHOUSE_PASSWORD` | *(empty)* | Auth password |
| `redis.url` | `REDIS_URL` | `redis://localhost:6379/0` | Redis connection URL |
| `integrations.<feed>[]` | — | — | List of providers per feed type |

Each integration entry has: `name`, `api_url`, `api_key`, `interval` (seconds, 0 = worker default), `client_id`, `client_secret`, `token_url`.

---

## Project Structure

```
cmd/fukan-ingest/
  main.go                          # Entry point (~15 lines)

internal/
  commands/
    root.go                        # Root command, config loading via context
    worker.go                      # worker subcommand
    batcher.go                     # batcher subcommand
    refresh.go                     # refresh subcommand
    migrate.go                     # migrate subcommand (up/down/version)
    version.go                     # version subcommand
    migrations/                    # Embedded SQL migration files
      000001_create_telemetry_tables.{up,down}.sql
      000002_create_aircraft_meta.{up,down}.sql

  config/config.go                 # Typed config structs + Validate()
  model/event.go                   # FukanEvent canonical struct
  model/validate.go                # Event validation rules
  coord/coord.go                   # ScaleLat, ScaleLon, ComputeH3
  nats/nats.go                     # Connect() + PublishJSON()
  clickhouse/clickhouse.go         # Connect() (dial + ping)
  clickhouse/insert.go             # InsertBatch() columnar insert
  redis/publisher.go               # H3-grouped pub/sub publisher
  oauth2/token.go                  # OAuth2 client credentials (OpenSky)
  signal/signal.go                 # NotifyCtx() shared signal handling
  refresh/aircraft.go              # OpenSky CSV → ClickHouse loader

  worker/worker.go                 # Worker interface + RunWithReconnect
  worker/adsb/worker.go            # ADS-B HTTP polling worker
  worker/adsb/parser.go            # OpenSky JSON → FukanEvent
  batcher/batcher.go               # Dual-threshold batch accumulator
```

---

## Packages

### `internal/model`

Canonical `FukanEvent` struct and validation.

```go
type FukanEvent struct {
    Timestamp int64     `json:"ts"`    // Unix epoch milliseconds
    AssetID   string    `json:"id"`    // ICAO hex, MMSI, NORAD ID, ASN, or event hash
    AssetType AssetType `json:"type"`  // aircraft|vessel|satellite|bgp_node|news
    Lat       int32     `json:"lat"`   // latitude  * 10_000_000  (Int32, NOT float)
    Lon       int32     `json:"lon"`   // longitude * 10_000_000
    Alt       int32     `json:"alt"`   // meters above sea level
    Speed     float32   `json:"spd"`   // knots
    Heading   float32   `json:"hdg"`   // degrees 0-360
    H3Cell    uint64    `json:"h3"`    // H3 index at resolution 7
    Source    string    `json:"src"`   // provider identifier
    Metadata  string    `json:"meta"`  // JSON blob, type-specific
}
```

Validation rejects: empty `AssetID`/`AssetType`/`Source`, null-island `(0,0)`, out-of-range coordinates, non-positive timestamps.

### `internal/coord`

- `ScaleLat(float64) int32` / `ScaleLon(float64) int32` — multiply by 10,000,000
- `ComputeH3(lat, lon float64) (uint64, error)` — H3 cell at resolution 7 (~5.16 km²)

H3 is always computed in the worker, never in ClickHouse.

### `internal/nats`

Two free functions — no wrappers, no interfaces:
- `Connect(url) (*nats.Conn, error)` — dials NATS, returns the bare connection.
- `PublishJSON(nc, subject, v) error` — marshals to JSON and publishes.

Callers use `*nats.Conn` directly for `QueueSubscribe`, `Drain`, `Close`.

### `internal/batcher`

Dual-threshold batching engine with retry.

- **Size threshold:** 10,000 events triggers immediate flush.
- **Time threshold:** 2-second `AfterFunc` timer triggers flush if buffer is non-empty.
- **Retry:** Failed inserts retry with exponential backoff (100ms initial, 5s cap, 5 attempts). In-flight retry goroutines capped at 10 via semaphore.
- **Shutdown:** `DrainAndFlush()` flushes remaining buffer, then waits for all in-flight retries to complete.

### `internal/clickhouse`

Two free functions — no wrapper types:
- `Connect(ctx, cfg) (*ch.Client, error)` — dials via native protocol and pings. Returns bare `*ch.Client`.
- `InsertBatch(ctx, conn, events) error` — columnar batch insert into `fukan.telemetry_raw`. Uses LowCardinality for `asset_type` and `source`.

### `internal/redis`

- `Publisher` struct with `PublishBatch(ctx, events) error` — groups events by H3 cell, publishes JSON to Redis channels `telemetry:{h3_cell}` using a pipeline. Per-event marshal failures are logged and skipped; pipeline exec errors are returned to the caller.

### `internal/signal`

- `NotifyCtx(parent) (context.Context, CancelFunc)` — cancels context on SIGTERM/SIGINT. A second signal forces `os.Exit(1)`. Used by all commands.

### `internal/worker`

- `Worker` interface — `Run(ctx) error`, `Name() string`.
- `RunWithReconnect(ctx, name, fn)` — wraps any `func(ctx) error` with exponential backoff (1s→60s). Resets backoff if the function ran for >60s (long-lived = healthy).

### `internal/worker/adsb`

- `ADSBWorker` struct with functional options (`WithInterval`, `WithAPIKey`, `WithOAuth2`).
- `Run()` delegates to `RunWithReconnect` wrapping an HTTP poll loop.
- `ParseStates(body, source)` — parses OpenSky `/states/all` JSON (positional arrays) into `[]FukanEvent`. Handles null fields, m/s→knots, coordinate scaling, H3 computation. Skips on-ground and no-position aircraft.

### `internal/oauth2`

- `TokenSource` — OAuth2 client credentials flow for OpenSky Network. Caches tokens with 60-second refresh margin. Thread-safe.

### `internal/refresh`

- `Aircraft(ctx, conn, csvURL, token, dryRun)` — downloads OpenSky aircraft database CSV, parses ~600k rows, batch-inserts into `fukan.aircraft_meta` (50k rows per batch). Preserves existing image URLs across refreshes.

### `internal/config`

Typed configuration structs with `mapstructure` tags for Viper unmarshalling:
- `Config` (top-level) → `NATSConfig`, `ClickHouseConfig`, `RedisConfig`, `OpenSkyConfig`, `Integrations map[string][]IntegrationConfig`
- `IntegrationConfig` → `Name`, `APIURL`, `APIKey`, `Interval`, `ClientID`, `ClientSecret`, `TokenURL`
- `Validate()` — checks required fields (`nats.url`, `clickhouse.addr`, `clickhouse.database`, `redis.url`).

---

## Data Flow Guarantees

| Guarantee | Mechanism |
|---|---|
| Best-effort delivery | NATS core (RAM-only). No persistence or redelivery. |
| Durability | None at broker level; data can be lost on restarts. |
| Load distribution | NATS queue groups: `batcher-{asset_type}` share work. |
| Backpressure | None from broker; batcher flush controls local memory. |
| CH failure behavior | Retry with exponential backoff (100ms→5s, 5 attempts, 10 concurrent cap). |
| Shutdown behavior | Batchers `DrainAndFlush()`; NATS `Drain()` to flush pending. |

---

## ClickHouse Schema

Managed via golang-migrate. Migration files are embedded in the binary at `internal/commands/migrations/`. Apply with `fukan-ingest migrate up`.

1. **`telemetry_raw`** — MergeTree, partitioned by hour, ordered by `(asset_type, h3_cell, event_time, asset_id)`. TTL: 24 hours (dev/validation). Production target: 90 days with tiered storage.
2. **`telemetry_latest`** — AggregatingMergeTree. Latest position per `(asset_type, asset_id)` via argMax aggregate states.
3. **`telemetry_h3_agg`** — SummingMergeTree. 5-minute bucketed density counts per `(h3_cell, asset_type)` for heatmaps.
4. **`aircraft_meta`** — ReplacingMergeTree. Aircraft reference data from OpenSky (ICAO24, registration, operator, images).

---

## Local Development

```bash
# Start infrastructure
docker compose up -d

# Apply ClickHouse migrations
go run ./cmd/fukan-ingest migrate up

# Run worker
go run ./cmd/fukan-ingest worker --type adsb --config config.yaml

# Or with legacy env vars
ADSB_FEED_URL=https://... NATS_URL=nats://localhost:4222 \
  go run ./cmd/fukan-ingest worker --type adsb

# Run batcher
go run ./cmd/fukan-ingest batcher --type aircraft --config config.yaml

# Refresh aircraft metadata
go run ./cmd/fukan-ingest refresh --target airlines --config config.yaml

# Test
go test ./internal/...

# Build
go build ./cmd/fukan-ingest
```

---

## Future: JetStream (optional)

If durability and at-least-once delivery are required later, migrate to NATS JetStream:

- Stream: `PANOPTIS_TELEMETRY` with subjects `fukan.telemetry.>` and file storage.
- Consumers: one durable per batcher, e.g. `batcher-aircraft`, `FilterSubject` per asset, `AckExplicit`, `MaxAckPending` for backpressure, `AckWait` for redelivery.
- Publisher: use JetStream context (`js.Publish`) instead of core publish.
- Subscriber: use JetStream subscription APIs; batcher must ACK only after successful ClickHouse insert.
- Guarantees: at-least-once delivery, redelivery on failure/shutdown, broker-level backpressure.

Operational impact: allocate disk for JetStream, monitor stream sizes, and tune `MaxAckPending` to match ClickHouse throughput.

---

## Dependencies

| Package | Purpose |
|---|---|
| `github.com/spf13/cobra` | CLI framework — subcommands, flags, help |
| `github.com/spf13/viper` | Configuration — YAML files, env vars, defaults |
| `github.com/ClickHouse/ch-go` | ClickHouse native protocol client |
| `github.com/nats-io/nats.go` | NATS client |
| `github.com/redis/go-redis/v9` | Redis client |
| `github.com/uber/h3-go/v4` | H3 geospatial indexing |
| `golang.org/x/sync/errgroup` | Concurrent worker goroutine management |
| `github.com/golang-migrate/migrate/v4` | ClickHouse schema migrations |
