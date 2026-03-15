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
│  Retry with exponential backoff          │
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

## Binaries

### `cmd/worker` — ETL Worker

Connects to an external data feed, normalizes events into `FukanEvent`, and publishes to NATS.

**Environment variables:**

| Variable | Default | Description |
|---|---|---|
| `WORKER_TYPE` | `adsb` | Which feed to consume: `adsb`, `ais`, `tle`, `bgp`, `news` |
| `NATS_URL` | `nats://localhost:4222` | NATS server address |
| `ADSB_FEED_URL` | *(required for adsb)* | HTTP endpoint to poll |
| `ADSB_SOURCE` | `adsb_exchange` | Provider identifier written to `Source` field |

```bash
WORKER_TYPE=adsb \
NATS_URL=nats://localhost:4222 \
ADSB_FEED_URL=https://example.com/feed \
  go run ./cmd/worker
```

### `cmd/batcher` — ClickHouse Batcher

Subscribes to NATS, accumulates events in memory, and flushes to ClickHouse in batches. Publishes each batch to Redis for real-time streaming.

**Environment variables:**

| Variable | Default | Description |
|---|---|---|
| `BATCHER_ASSET_TYPE` | `aircraft` | Asset type to consume: `aircraft`, `vessel`, `satellite`, `bgp_node`, `news` |
| `NATS_URL` | `nats://localhost:4222` | NATS server address |
| `CLICKHOUSE_ADDR` | `localhost:9000` | ClickHouse native protocol address |
| `CLICKHOUSE_DATABASE` | `fukan` | Target database |
| `CLICKHOUSE_USER` | `default` | Auth user |
| `CLICKHOUSE_PASSWORD` | *(empty)* | Auth password |
| `REDIS_URL` | `redis://localhost:6379/0` | Redis connection URL |

```bash
BATCHER_ASSET_TYPE=aircraft \
CLICKHOUSE_ADDR=localhost:9000 \
  go run ./cmd/batcher
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
- `ComputeH3(lat, lon float64) uint64` — H3 cell at resolution 7 (~5.16 km²)

H3 is always computed in the worker, never in ClickHouse.

### `internal/nats`

Thin wrappers around `nats.go`:
- **Publisher** — `Publish(subject, event)` serializes to JSON and publishes.
- **Subscriber** — `QueueSubscribe(subject, queue, handler)` for load-balanced consumption.

### `internal/batcher`

Dual-threshold batching engine.

- **Size threshold:** 10,000 events triggers immediate flush.
- **Time threshold:** 2-second `AfterFunc` timer triggers flush if buffer is non-empty.
- **Retry:** Failed inserts retry with exponential backoff (100ms initial, 5s cap). In-flight retry goroutines are capped at `MaxRetryBuffer / MaxBatchSize` (10) to prevent unbounded memory growth.
- **Shutdown:** `DrainAndFlush()` cancels retry loops, waits for in-flight goroutines, then does a synchronous final insert with a 10-second timeout against a fresh context.

### `internal/clickhouse`

- **Client** — `ch-go` native protocol connection (`NewClient`, `Ping`, `Close`).
- **InsertBatch** — Columnar batch insert into `fukan.telemetry_raw`. Builds `proto.Input` with 11 columns using codec-aware types (DoubleDelta for coords/timestamps, Gorilla for speed/heading, LowCardinality for asset_type/source).

### `internal/redis`

- **PublishBatch** — Groups events by H3 cell, publishes JSON to Redis channels `telemetry:{h3_cell}` using a pipeline. Failures are logged but non-fatal.

### `internal/worker`

- **Worker interface** — `Run(ctx) error`, `Name() string`.
- **RunWithReconnect** — Exponential backoff reconnect loop (1s initial, 60s cap). Resets backoff if the connection lasted longer than 60s.

### `internal/worker/adsb`

HTTP-polling ADS-B worker:
1. Polls `ADSB_FEED_URL` every 5 seconds.
2. `ParseFeed` deserializes the JSON response, normalizes each aircraft to `FukanEvent` (ICAO hex → uppercase, feet → meters, compute H3, build squawk metadata).
3. Validates and publishes each event to `fukan.telemetry.aircraft`.

---

## Data Flow Guarantees

| Guarantee | Mechanism |
|---|---|
| Best-effort delivery | NATS core (RAM-only). No persistence or redelivery. |
| Durability | None at broker level; data can be lost on restarts. |
| Load distribution | NATS queue groups: `batcher-{asset_type}` share work. |
| Backpressure | None from broker; batcher flush/retry controls local memory. |
| CH failure behavior | In-process retry with backoff; loss if process exits. |
| Shutdown behavior | Batchers `DrainAndFlush()`; NATS publisher `Drain()` to flush. |

---

## ClickHouse Schema

Three tables defined in `scripts/clickhouse-init.sql`:

1. **`telemetry_raw`** — MergeTree, partitioned by day, ordered by `(asset_type, asset_id, timestamp)`. TTL: 24 hours (dev/validation). Production target: 90 days with tiered storage to Cloudflare R2.
2. **`telemetry_latest`** — ReplacingMergeTree materialized view. Latest position per `(asset_type, asset_id)`.
3. **`telemetry_h3_agg`** — AggregatingMergeTree materialized view. 5-minute bucketed density counts per `(h3_cell, asset_type)` for heatmaps.

---

## Local Development

```bash
# Start infrastructure
docker compose up -d    # NATS :4222, ClickHouse :9000, Redis :6379

# Run worker
WORKER_TYPE=adsb ADSB_FEED_URL=https://... go run ./cmd/worker

# Run batcher
BATCHER_ASSET_TYPE=aircraft go run ./cmd/batcher

# Test
go test ./...

# Verify shutdown behavior
go run ./cmd/batcher &
kill -TERM $!    # should see "final flush" in logs
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
| `github.com/ClickHouse/ch-go` | ClickHouse native protocol client |
| `github.com/nats-io/nats.go` | NATS client |
| `github.com/redis/go-redis/v9` | Redis client |
| `github.com/uber/h3-go/v4` | H3 geospatial indexing |
