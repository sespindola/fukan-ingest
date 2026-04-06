# AGENTS.md — Fukan Ingestion Framework

> AI coding assistant context file for the Go ETL/ingestion pipeline.
> Read fully before generating any code.
> Last updated: 2026-03-05

---

## Conflict Resolution

If any guidance in this file conflicts with the invariants or non-goals listed below, **the invariants and non-goals win**. When in doubt: simpler is right, complexity needs justification.

---

## TL;DR — Critical Invariants

**Read this section even if you read nothing else.**

### What Fukan Is

A real-time, open-source global OSINT platform that collates telemetry feeds (ADS-B, AIS, satellite orbits, BGP routing, geolocated news) onto an interactive 3D globe. Think "Palantir for everyone" — democratized intelligence visualization.

### What This Repo Is

The **data ingestion pipeline** — Go workers that consume external data feeds, normalize events into a unified schema, publish over NATS core (RAM-only, no persistence), batch-insert into ClickHouse, and publish live updates to Redis for real-time streaming. This repo does NOT handle the web UI — that is a separate Rails + React codebase (see `fukan-web`).

### Core Loop (everything serves this)

```
External feed (ADS-B, AIS, TLE, BGP, News)
  → Go ETL worker (parse, normalize, compute H3)
  → NATS (core, RAM-only)
  → Go batcher (accumulate, flush)
  → ClickHouse (batch insert via native protocol)
  → Redis pub/sub (publish for real-time WebSocket streaming)
```

### Technical Non-Goals (never use these)

- ❌ TypeScript/Node.js/Bun for any ingestion component → Go only
- ❌ Kafka/Redpanda → NATS (core)
- ❌ ClickHouse HTTP interface for inserts → Native TCP protocol via ch-go
- ❌ PostgreSQL for telemetry storage → ClickHouse only
- ❌ Float64 for coordinates → Int32 scaled by 10_000_000
- ❌ Bloom filters for dedup → In-memory time-windowed hashmap
- ❌ Complex ORM or query builder → Raw SQL for ClickHouse DDL, ch-go for inserts
- ❌ Storing PII from social/news feeds → Aggregate data only
- ❌ Storing full article text from news → Headline + source URL + sentiment only
- ❌ Processing satellite imagery → Orbital position data only (TLE → SGP4)

### Data Ownership Rule

```
This pipeline OWNS all writes to ClickHouse.
The web layer (Rails) reads from ClickHouse but NEVER writes.
This pipeline publishes to Redis pub/sub for real-time streaming.
The web layer (AnyCable) subscribes to Redis but this pipeline NEVER reads from it.
```

### Single Source of Truth

```
v1: ONE provider per feed category. No dedup needed.
v2: Multiple providers per category. In-memory hashmap dedup in batcher.
```

---

## Architecture Overview

### Pipeline Topology

```
┌───────────────────────────────────────────────────────────────────────┐
│                        ETL WORKERS (Go)                               │
│                                                                       │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐ │
│  │ ADS-B Worker │  │ AIS Worker  │  │ TLE Worker  │  │ BGP Worker  │ │
│  │ (WebSocket/  │  │ (WebSocket) │  │ (HTTP poll) │  │ (BGPStream) │ │
│  │  HTTP poll)  │  │             │  │ + SGP4 prop │  │             │ │
│  └──────┬───────┘  └──────┬──────┘  └──────┬──────┘  └──────┬──────┘ │
│         │                 │                │                │         │
│  ┌──────┴───────┐  ┌──────┘                │         ┌──────┘         │
│  │              ▼  ▼                       ▼         ▼               │
│  │         NATS (core)                                               │
│  │    fukan.telemetry.{type} (queue group: batcher-{type})           │
│  │              │                                                    │
│  │    ┌─────────┴──────────┐                                         │
│  │    ▼                    ▼                                         │
│  │  Batcher (aircraft)   Batcher (vessel)   Batcher (satellite) ... │
│  │    │                    │                    │                     │
│  │    ▼                    ▼                    ▼                     │
│  │         ClickHouse (batch insert, native protocol)                │
│  │              │                                                    │
│  │              ▼                                                    │
│  │         Redis pub/sub (publish live positions by H3 cell)         │
│  │              │                                                    │
│  │              ▼                                                    │
│  │    fukan-web (AnyCable subscribes, streams to browsers)        │
│  │                                                                   │
│  │  ┌─────────────┐                                                  │
│  │  │ News Worker  │  (GDELT poll → same pipeline)                   │
│  │  └─────────────┘                                                  │
│  │                                                                   │
└──┴───────────────────────────────────────────────────────────────────┘
```

### Data Flow Guarantees

| Guarantee                   | Mechanism                                          |
|-----------------------------|----------------------------------------------------|
| Best-effort delivery        | NATS core; in-memory only; no persistence/redelivery |
| Ordering per asset          | Not required — telemetry is idempotent by timestamp |
| Durability                  | None at broker; data can be lost on restarts        |
| Backpressure                | None at broker; batcher flush/retry bounds memory   |
| CH failure behavior         | In-process retry with backoff; loss if process exits |

---

## Unified Event Schema

All feeds normalize to this struct before entering NATS:

```go
// internal/model/event.go
package model

type AssetType string

const (
    AssetAircraft  AssetType = "aircraft"
    AssetVessel    AssetType = "vessel"
    AssetSatellite AssetType = "satellite"
    AssetBGPNode   AssetType = "bgp_node"
    AssetNews      AssetType = "news"
)

// FukanEvent is the canonical normalized event.
// Every ETL worker MUST produce this struct. No exceptions.
type FukanEvent struct {
    Timestamp int64     `json:"ts"`    // Unix epoch milliseconds
    AssetID   string    `json:"id"`    // ICAO hex, MMSI, NORAD ID, ASN, or event hash
    AssetType AssetType `json:"type"`  // aircraft, vessel, satellite, bgp_node, news
    Lat       int32     `json:"lat"`   // latitude * 10_000_000
    Lon       int32     `json:"lon"`   // longitude * 10_000_000
    Alt       int32     `json:"alt"`   // meters above sea level (0 for surface/network)
    Speed     float32   `json:"spd"`   // knots (aircraft/vessel) or 0
    Heading   float32   `json:"hdg"`   // degrees (0-360) or 0
    H3Cell    uint64    `json:"h3"`    // pre-computed H3 index at resolution 7
    Source    string    `json:"src"`   // provider identifier (e.g. "adsb_exchange")
    Metadata  string    `json:"meta"`  // JSON blob, type-specific (squawk, nav_status, etc.)
}
```

### Coordinate Encoding Rule

```go
// ✅ CORRECT — store as Int32 scaled by 10_000_000
lat := int32(rawLat * 10_000_000)   // 51.5074° → 515074000
lon := int32(rawLon * 10_000_000)   // -0.1278° → -1278000

// ❌ WRONG — never store as float64 in ClickHouse
lat := rawLat  // wastes space, compresses poorly
```

**Why:** Int32 gives centimeter precision. ClickHouse compresses sequential integers (DoubleDelta codec) far better than floats. This cuts storage by 40-60% for coordinate columns.

### H3 Computation Rule

```go
// H3 cell MUST be computed in the ETL worker, not in ClickHouse.
// This avoids ClickHouse CPU overhead on every insert.
import "github.com/uber/h3-go/v4"

cell := h3.LatLngToCell(h3.LatLng{Lat: rawLat, Lng: rawLon}, 7)
event.H3Cell = uint64(cell)
```

**Resolution 7:** ~5.16 km² per cell. Balances granularity for viewport queries against cardinality for aggregation. Do not change without updating the web layer's viewport logic.

---

## Project Structure

```
fukan-ingest/
├── cmd/
│   └── fukan-ingest/            # Unified CLI binary (Cobra)
│       ├── main.go              # Entry point — executes rootCmd
│       ├── root.go              # Root command, Viper config init
│       ├── version.go           # `fukan-ingest version` subcommand
│       ├── worker.go            # `fukan-ingest worker --type <feed>` subcommand
│       └── batcher.go           # `fukan-ingest batcher --type <asset>` subcommand
├── config.example.yaml          # Reference YAML config (Viper)
├── internal/
│   ├── config/
│   │   └── config.go           # Typed config structs (mapstructure tags for Viper)
│   ├── model/
│   │   └── event.go            # FukanEvent struct (canonical schema)
│   ├── worker/
│   │   ├── adsb/
│   │   │   ├── worker.go       # ADS-B feed consumer
│   │   │   ├── parser.go       # Raw → FukanEvent normalization
│   │   │   └── parser_test.go
│   │   ├── ais/
│   │   │   ├── worker.go       # AIS WebSocket consumer
│   │   │   ├── parser.go
│   │   │   └── parser_test.go
│   │   ├── tle/
│   │   │   ├── fetcher.go      # CelesTrak TLE downloader
│   │   │   ├── propagator.go   # SGP4 → lat/lon/alt
│   │   │   └── propagator_test.go
│   │   ├── bgp/
│   │   │   ├── worker.go       # BGPStream consumer
│   │   │   ├── parser.go
│   │   │   ├── geoip.go        # ASN/prefix → coordinates via MaxMind
│   │   │   └── parser_test.go
│   │   └── news/
│   │       ├── worker.go       # GDELT poller
│   │       ├── parser.go
│   │       └── parser_test.go
│   ├── batcher/
│   │   ├── batcher.go          # NATS consumer → ClickHouse batch inserter
│   │   ├── batcher_test.go
│   │   ├── dedup.go            # Time-windowed hashmap dedup (v2)
│   │   └── dedup_test.go
│   ├── clickhouse/
│   │   ├── client.go           # ch-go native protocol wrapper
│   │   ├── schema.go           # DDL for table creation / migrations
│   │   └── insert.go           # Batch insert logic
│   ├── nats/
│   │   ├── publisher.go        # NATS core publisher wrapper
│   │   └── consumer.go         # NATS core subscriber wrapper
│   └── redis/
│       └── publisher.go        # Pub/sub publisher for live streaming
├── deploy/
│   ├── k8s/                    # Kubernetes manifests
│   │   ├── worker-deployment.yaml
│   │   ├── batcher-deployment.yaml
│   │   ├── nats-statefulset.yaml
│   │   └── clickhouse-statefulset.yaml
│   └── docker/
│       └── Dockerfile          # Multi-stage Go build
├── scripts/
│   ├── clickhouse-init.sql     # ClickHouse DDL (tables, views, TTLs)
│   └── seed-tle.sh             # Initial TLE catalog fetch
├── go.mod
├── go.sum
└── AGENTS.md                   # This file
```

---

## NATS Core Configuration

### Subject Hierarchy

```
fukan.telemetry.aircraft     # ADS-B normalized events
fukan.telemetry.vessel       # AIS normalized events
fukan.telemetry.satellite    # Propagated TLE positions
fukan.telemetry.bgp          # BGP routing events
fukan.telemetry.news         # Geolocated news events
fukan.telemetry.>            # Wildcard (all telemetry)
```

### Queue Groups

- Use one queue group per asset type for batchers: `batcher-{asset_type}`.
- NATS core distributes messages among subscribers in the same group.
- There is no persistence or redelivery; messages are transient.

### Publisher/Subscriber Notes

- Publishers should call `Drain()` on shutdown to flush pending messages.
- Subscribers can call `Drain()` to stop receiving before exiting.

---

## Future: JetStream (optional)

If at-least-once delivery and durability are needed later, migrate to NATS JetStream:

- Stream: `PANOPTIS_TELEMETRY` with subjects `fukan.telemetry.>` and file storage.
- Consumers: one durable per batcher (e.g., `batcher-aircraft`), `AckExplicit`, `MaxAckPending`, `AckWait`.
- Publisher: use JetStream context (`js.Publish`).
- Subscriber: use JetStream subscribe APIs; batcher must ACK only after successful ClickHouse insert.
- Guarantees: at-least-once with redelivery; broker backpressure via `MaxAckPending`.

Operational note: provision disk for JetStream and monitor stream size/retention.

---

## Batcher Logic

### Flush Strategy

```go
// internal/batcher/batcher.go

const (
    MaxBatchSize    = 10_000        // rows per ClickHouse insert
    MaxFlushLatency = 2 * time.Second // max time before forced flush
)

// Batcher accumulates events and flushes on whichever threshold hits first.
// With NATS core there are no ACKs; messages are in-memory and transient.
type Batcher struct {
    buffer []model.FukanEvent
    mu     sync.Mutex
    ch     *clickhouse.Client
    redis  *redis.Publisher
    timer  *time.Timer
}

func (b *Batcher) Add(event model.FukanEvent) {
    b.mu.Lock()
    defer b.mu.Unlock()

    b.buffer = append(b.buffer, event)

    if len(b.buffer) >= MaxBatchSize {
        b.flush()
    }
}

func (b *Batcher) flush() {
    if len(b.buffer) == 0 {
        return
    }

    batch := b.buffer
    b.buffer = make([]model.FukanEvent, 0, MaxBatchSize)

    // 1. Insert into ClickHouse (native protocol, columnar batch)
    if err := b.ch.InsertBatch(batch); err != nil {
        // Retry in-process with backoff; NATS core does not persist messages.
        log.Error("clickhouse insert failed", "err", err, "count", len(batch))
        return
    }

    // 2. Publish to Redis for live streaming (grouped by H3 cell)
    if err := b.redis.PublishBatch(batch); err != nil {
        // Log but don't block — Redis failure is non-fatal for persistence
        log.Warn("redis publish failed", "err", err)
    }

    // 3. No ACK step with NATS core
}
```

### Redis Publishing Pattern

```go
// internal/redis/publisher.go

// Publish events grouped by H3 cell so AnyCable can fan out per viewport.
// Redis channel: "telemetry:{h3_cell}"
func (p *Publisher) PublishBatch(events []model.FukanEvent) error {
    pipe := p.client.Pipeline()
    for _, e := range events {
        channel := fmt.Sprintf("telemetry:%d", e.H3Cell)
        data, _ := json.Marshal(e)
        pipe.Publish(context.Background(), channel, data)
    }
    _, err := pipe.Exec(context.Background())
    return err
}
```

---

## Deduplication (v2, Multiple Providers)

### Strategy: In-Memory Time-Windowed HashMap

```go
// internal/batcher/dedup.go

// DedupWindow maintains a set of recently seen event fingerprints.
// NOT a bloom filter — no false positives, deterministic behavior.
type DedupWindow struct {
    seen   map[uint64]int64  // hash → expiry timestamp
    mu     sync.RWMutex
    window time.Duration     // 60 seconds
}

// IsDuplicate returns true if this exact (asset_id, timestamp) was seen recently.
func (d *DedupWindow) IsDuplicate(event model.FukanEvent) bool {
    hash := fnv64(event.AssetID, event.Timestamp)

    d.mu.RLock()
    _, exists := d.seen[hash]
    d.mu.RUnlock()

    if exists {
        return true
    }

    d.mu.Lock()
    d.seen[hash] = time.Now().Add(d.window).UnixMilli()
    d.mu.Unlock()

    return false
}

// Sweep removes expired entries. Run in a goroutine every 30 seconds.
func (d *DedupWindow) Sweep() {
    now := time.Now().UnixMilli()
    d.mu.Lock()
    for hash, expiry := range d.seen {
        if now > expiry {
            delete(d.seen, hash)
        }
    }
    d.mu.Unlock()
}

// Memory footprint: ~150MB at 100k msg/sec with 60s window
// (6M entries × 24 bytes per map entry)
```

**Why not bloom filters:** False positives silently drop real telemetry. For an OSINT platform, completeness matters more than saving 100MB of RAM.

---

## Data Feed Specifications

### ADS-B (Aircraft)

| Source field    | FukanEvent field | Transformation                          |
|-----------------|---------------------|-----------------------------------------|
| `hex` / `icao`  | `AssetID`           | Uppercase hex string                    |
| `lat`           | `Lat`               | `int32(lat * 10_000_000)`              |
| `lon`           | `Lon`               | `int32(lon * 10_000_000)`              |
| `alt_baro`      | `Alt`               | Feet → meters: `int32(alt * 0.3048)`   |
| `gs`            | `Speed`             | Ground speed in knots (keep as-is)      |
| `track`         | `Heading`           | Degrees (keep as-is)                    |
| `squawk`        | `Metadata`          | JSON: `{"squawk":"7700"}`               |
| `now`           | `Timestamp`         | Unix epoch → milliseconds               |
|                 | `AssetType`         | Always `"aircraft"`                     |
|                 | `H3Cell`            | Computed from lat/lon at res 7          |

**Initial provider:** ADS-B Exchange or equivalent open feed.
**Connection:** HTTP polling or WebSocket depending on provider.

### AIS (Vessels)

| Source field    | FukanEvent field | Transformation                          |
|-----------------|---------------------|-----------------------------------------|
| `mmsi`          | `AssetID`           | 9-digit string                          |
| `latitude`      | `Lat`               | `int32(lat * 10_000_000)`              |
| `longitude`     | `Lon`               | `int32(lon * 10_000_000)`              |
| `sog`           | `Speed`             | Tenths of knot → knots: `sog / 10.0`   |
| `cog`           | `Heading`           | Tenths of degree → degrees: `cog / 10.0`|
| `status`        | `Metadata`          | JSON: `{"nav_status":"under_way"}`      |
| `timestamp`     | `Timestamp`         | Unix epoch → milliseconds               |
|                 | `AssetType`         | Always `"vessel"`                       |
|                 | `Alt`               | Always `0`                              |
|                 | `H3Cell`            | Computed from lat/lon at res 7          |

**Initial provider:** AISStream.io (WebSocket, clean JSON).
**Connection:** Persistent WebSocket with auto-reconnect.

### Satellites (TLE Orbits)

| Source              | FukanEvent field | Transformation                        |
|---------------------|---------------------|---------------------------------------|
| TLE catalog number  | `AssetID`           | 5-digit NORAD ID as string            |
| SGP4 output         | `Lat`               | `int32(lat * 10_000_000)`            |
| SGP4 output         | `Lon`               | `int32(lon * 10_000_000)`            |
| SGP4 output         | `Alt`               | Kilometers → meters: `int32(alt * 1000)` |
| Propagation time    | `Timestamp`         | Propagation step time in ms           |
|                     | `AssetType`         | Always `"satellite"`                  |
|                     | `Speed`             | `0` (not meaningful for orbits)       |
|                     | `Heading`           | `0` (not meaningful for orbits)       |
|                     | `H3Cell`            | Computed from sub-satellite point     |

**Sources:**
- CelesTrak (public catalog, daily HTTP fetch of GP data) — primary source for all unclassified objects
- planet4589.org / JSR Satellite Catalog (Jonathan McDowell) — supplemental source for classified/military objects not in the official US Space Command catalog. Community-derived from independent observations. Replaces the retired McCant's classified list. Note: uses its own catalog format (not standard TLE), parser must handle separately.

**Propagation pipeline:**
```
1. Fetch TLEs from CelesTrak every 24 hours (primary catalog)
2. Fetch supplemental classified object data from planet4589.org (daily)
3. Store raw TLE lines in memory (or local file cache)
4. Run SGP4 propagation at regime-appropriate intervals:
   - LEO (< 2,000 km): every 10 seconds
   - MEO (2,000–35,786 km): every 30 seconds
   - GEO (~35,786 km): every 60–300 seconds (barely moves)
   - HEO (elliptical): every 10 seconds (fast-moving perigee)
5. Emit FukanEvent for each computed position
6. Enrich metadata with orbit regime: {"regime":"leo"}, {"regime":"geo"}, etc.
7. For planet4589.org objects, tag confidence: {"confidence":"community_derived"}
8. Maneuver detection: compare daily TLE mean motion / inclination
   - If delta exceeds threshold → set metadata: {"status":"maneuvering"}
9. Decay detection: monitor BSTAR drag term + perigee altitude
   - If perigee < 150km → set metadata: {"status":"decaying"}
```

**Go library:** `github.com/shanehandley/go-satellite` for SGP4.

### BGP (Internet Routing)

| Source              | FukanEvent field | Transformation                        |
|---------------------|---------------------|---------------------------------------|
| ASN                 | `AssetID`           | `"AS12345"` format                    |
| MaxMind GeoIP       | `Lat/Lon`           | Mapped from ASN owner → coords        |
| BGPStream event     | `Metadata`          | JSON: `{"event_type":"hijack","prefix":"1.2.3.0/24"}` |
| Event time          | `Timestamp`         | Unix epoch → milliseconds             |
|                     | `AssetType`         | Always `"bgp_node"`                   |
|                     | `Alt`               | Always `0`                            |
|                     | `Speed`             | Always `0`                            |
|                     | `H3Cell`            | Computed from GeoIP coordinates       |

**Source:** CAIDA BGPStream (free, open for tools).
**GeoIP:** MaxMind GeoLite2-ASN database (free, updated weekly).

### News (Geolocated Events)

| Source              | FukanEvent field | Transformation                        |
|---------------------|---------------------|---------------------------------------|
| GDELT event ID      | `AssetID`           | GDELT GlobalEventID or hash           |
| GDELT ActionGeo     | `Lat/Lon`           | Pre-geolocated by GDELT               |
| GDELT               | `Metadata`          | JSON: `{"headline":"...","url":"...","tone":-2.5}` |
| Publication time     | `Timestamp`         | Unix epoch → milliseconds             |
|                     | `AssetType`         | Always `"news"`                       |
|                     | `Alt`               | Always `0`                            |
|                     | `Speed`             | Always `0`                            |
|                     | `H3Cell`            | Computed from GDELT coordinates       |

**Source:** GDELT Project (free, pre-geolocated global news, 15-minute update cycle).
**Storage rule:** Headline + source URL + sentiment score only. **Never store full article text** (copyright).

---

## ClickHouse Schema

### Table Definitions

```sql
-- scripts/clickhouse-init.sql

-- Raw telemetry (full history)
CREATE TABLE IF NOT EXISTS fukan.telemetry_raw (
    timestamp     DateTime        CODEC(DoubleDelta, LZ4),
    asset_id      String          CODEC(LZ4),
    asset_type    LowCardinality(String),
    lat           Int32           CODEC(DoubleDelta, LZ4),
    lon           Int32           CODEC(DoubleDelta, LZ4),
    alt           Int32           CODEC(DoubleDelta, LZ4),
    speed         Float32         CODEC(Gorilla, LZ4),
    heading       Float32         CODEC(Gorilla, LZ4),
    h3_cell       UInt64          CODEC(LZ4),
    source        LowCardinality(String),
    metadata      String          CODEC(LZ4)
) ENGINE = MergeTree()
ORDER BY (asset_type, asset_id, timestamp)
PARTITION BY toYYYYMMDD(timestamp)
TTL timestamp + INTERVAL 2 DAY
        MODIFY CODEC(DoubleDelta, ZSTD(3)),
    timestamp + INTERVAL 90 DAY
        TO VOLUME 'cold'
SETTINGS index_granularity = 8192;

-- Latest position per asset (materialized view)
CREATE TABLE IF NOT EXISTS fukan.telemetry_latest (
    timestamp     DateTime,
    asset_id      String,
    asset_type    LowCardinality(String),
    lat           Int32,
    lon           Int32,
    alt           Int32,
    speed         Float32,
    heading       Float32,
    h3_cell       UInt64,
    source        LowCardinality(String),
    metadata      String
) ENGINE = ReplacingMergeTree(timestamp)
ORDER BY (asset_type, asset_id);

CREATE MATERIALIZED VIEW IF NOT EXISTS fukan.telemetry_latest_mv
TO fukan.telemetry_latest AS
SELECT * FROM fukan.telemetry_raw;

-- H3 density aggregation (for zoomed-out heatmaps)
CREATE TABLE IF NOT EXISTS fukan.telemetry_h3_agg (
    time_bucket   DateTime,
    h3_cell       UInt64,
    asset_type    LowCardinality(String),
    count         AggregateFunction(count, UInt64)
) ENGINE = AggregatingMergeTree()
ORDER BY (asset_type, h3_cell, time_bucket);

CREATE MATERIALIZED VIEW IF NOT EXISTS fukan.telemetry_h3_agg_mv
TO fukan.telemetry_h3_agg AS
SELECT
    toStartOfFiveMinutes(timestamp) AS time_bucket,
    h3_cell,
    asset_type,
    countState() AS count
FROM fukan.telemetry_raw
GROUP BY time_bucket, h3_cell, asset_type;
```

### Storage Policy

```xml
<!-- ClickHouse storage config for tiered storage -->
<storage_configuration>
  <disks>
    <nvme>
      <path>/var/lib/clickhouse/</path>
    </nvme>
    <s3_cold>
      <type>s3</type>
      <endpoint>https://fsn1.your-objectstorage.com/fukan-cold/</endpoint>
      <access_key_id>xxx</access_key_id>
      <secret_access_key>xxx</secret_access_key>
    </s3_cold>
  </disks>
  <policies>
    <tiered>
      <volumes>
        <hot><disk>nvme</disk></hot>
        <cold><disk>s3_cold</disk></cold>
      </volumes>
    </tiered>
  </policies>
</storage_configuration>
```

### Compression Strategy

| Tier         | Age         | Codec               | Purpose                          |
|--------------|-------------|----------------------|----------------------------------|
| Hot          | 0-48 hours  | LZ4                  | Fast decompression for live map  |
| Cold (NVMe)  | 2-90 days   | ZSTD(3)              | High compression, acceptable CPU |
| Cold (S3)    | 90+ days    | ZSTD(3)              | Archive, infrequent access       |

### Column Codec Rationale

| Column      | Codec           | Why                                            |
|-------------|-----------------|------------------------------------------------|
| `timestamp` | DoubleDelta     | Sequential timestamps, regular intervals       |
| `lat/lon`   | DoubleDelta     | Slowly changing integers (assets move smoothly) |
| `alt`       | DoubleDelta     | Same reasoning as coordinates                  |
| `speed`     | Gorilla         | Slowly changing floats                          |
| `heading`   | Gorilla         | Slowly changing floats                          |
| `asset_type`| LowCardinality  | Only 5 possible values, 1-byte pointer         |
| `source`    | LowCardinality  | Small set of provider names                    |

---

## ETL Worker Pattern

### Base Structure

```go
// internal/worker/base.go

// Worker is the interface all feed workers implement.
type Worker interface {
    // Run connects to the feed and starts consuming. Blocks until ctx is cancelled.
    Run(ctx context.Context) error
    // Name returns the worker identifier for logging.
    Name() string
}

// All workers follow this lifecycle:
// 1. Connect to external feed (WebSocket, HTTP, stream)
// 2. Parse provider-specific format into FukanEvent
// 3. Compute H3 cell
// 4. Publish to NATS subject (core)
// 5. Handle reconnection on failure (exponential backoff)
// 6. Respect context cancellation for graceful shutdown
```

### Example: AIS Worker

```go
// internal/worker/ais/worker.go

type AISWorker struct {
    nats      *nats.Publisher
    apiKey    string
    reconnect backoff.BackOff
}

func (w *AISWorker) Run(ctx context.Context) error {
    for {
        err := w.consume(ctx)
        if ctx.Err() != nil {
            return nil // graceful shutdown
        }
        wait := w.reconnect.NextBackOff()
        log.Warn("AIS connection lost, reconnecting", "err", err, "wait", wait)
        time.Sleep(wait)
    }
}

func (w *AISWorker) consume(ctx context.Context) error {
    conn, _, err := websocket.Dial(ctx, "wss://stream.aisstream.io/v0/stream", nil)
    if err != nil {
        return err
    }
    defer conn.Close(websocket.StatusNormalClosure, "")

    // Send subscription message
    sub := AISSubscription{APIKey: w.apiKey, BoundingBoxes: allOceans}
    conn.Write(ctx, websocket.MessageText, marshal(sub))

    for {
        _, data, err := conn.Read(ctx)
        if err != nil {
            return err
        }

        event, err := ParseAISMessage(data)
        if err != nil {
            log.Debug("skipping unparseable AIS message", "err", err)
            continue
        }

        if err := w.nats.Publish("fukan.telemetry.vessel", event); err != nil {
            return fmt.Errorf("nats publish: %w", err)
        }
    }
}
```

### Error Handling Doctrine

| Situation                        | Approach                                    |
|----------------------------------|---------------------------------------------|
| Feed connection lost             | Exponential backoff reconnect, log warning  |
| Unparseable message              | Skip message, log at debug level            |
| NATS publish failure             | Return error, trigger reconnect loop        |
| ClickHouse insert failure        | Retry in-process with backoff; may lose on exit |
| Redis publish failure            | Log warning, don't block (non-fatal)        |
| Invalid coordinates (0,0 / NaN)  | Skip event, log at debug level              |
| Context cancelled                | Return nil, graceful shutdown               |

### Validation Rules

```go
// Every ETL worker MUST validate before publishing to NATS:

func Validate(e model.FukanEvent) error {
    if e.AssetID == "" {
        return errors.New("empty asset_id")
    }
    if e.Lat == 0 && e.Lon == 0 {
        return errors.New("null island coordinates (0,0)")
    }
    if e.Lat < -900_000_000 || e.Lat > 900_000_000 {
        return errors.New("latitude out of range")
    }
    if e.Lon < -1_800_000_000 || e.Lon > 1_800_000_000 {
        return errors.New("longitude out of range")
    }
    if e.Timestamp <= 0 {
        return errors.New("invalid timestamp")
    }
    return nil
}
```

---

## Deployment (k3s on Hetzner)

### Container Layout

```
AX42 (dedicated, local NVMe)
├── k3s server node
├── ClickHouse (pinned, hostPath PV on NVMe)
└── Label: node-role=storage

CPX31 x1-2 (cloud instances)
├── k3s agent nodes
├── Go ETL workers (1 pod per feed type)
├── Go batchers (1 pod per asset type)
├── NATS (single node, core; in-memory only)
└── Label: node-role=compute
```

### Dockerfile

```dockerfile
# deploy/docker/Dockerfile
FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .

# Build unified binary (Cobra CLI — worker/batcher/version are subcommands)
RUN CGO_ENABLED=0 GOOS=linux go build -o /fukan-ingest ./cmd/fukan-ingest

FROM alpine:3.19
RUN apk add --no-cache ca-certificates
COPY --from=builder /fukan-ingest /usr/local/bin/fukan-ingest
```

### k3s Critical Notes

- **ClickHouse PV:** Use `hostPath` on NVMe, NOT Hetzner CSI (network volumes lack IOPS for merges)
- **Pin ClickHouse pod** to the AX42 node via `nodeSelector: { node-role: storage }`
- **NATS:** Stateless core server; no persistence. Size for concurrent traffic and memory.
- **All inter-service traffic** through Hetzner vSwitch (private network)
- **Public egress** only for ETL workers connecting to external feeds

---

## Configuration (Viper + Cobra)

The CLI uses **Cobra** for subcommands and **Viper** for configuration. Configuration is resolved in this priority order (highest wins):

1. CLI flags (e.g. `--type adsb`)
2. Environment variables (e.g. `NATS_URL`, `CLICKHOUSE_ADDR`)
3. YAML config file (`--config path` or `./config.yaml` or `/etc/fukan-ingest/config.yaml`)
4. Built-in defaults

See `config.example.yaml` for the full YAML schema. Typed config structs live in `internal/config/config.go`.

### CLI Usage

```bash
# Worker (--type is required)
fukan-ingest worker --type adsb --config config.yaml

# Batcher (--type defaults to "aircraft")
fukan-ingest batcher --type vessel --config config.yaml

# Version
fukan-ingest version
```

### YAML Config Structure

```yaml
nats:
  url: nats://localhost:4222
clickhouse:
  addr: localhost:9000
  database: fukan
  user: default
  password: ""
redis:
  url: redis://localhost:6379/0
integrations:
  adsb:
    - name: adsb_exchange
      api_url: http://feed-url
      api_key: KEY
      interval: 2   # seconds, 0 = worker default
```

The `integrations` map allows **multiple providers per feed type**, each spawned as a separate worker goroutine.

### Environment Variables (Legacy / Override)

Environment variables override YAML values. The mapping uses `_` as separator (e.g. `NATS_URL` → `nats.url`):

```bash
# NATS
NATS_URL=nats://nats:4222

# ClickHouse (native protocol for inserts)
CLICKHOUSE_ADDR=clickhouse:9000
CLICKHOUSE_DATABASE=fukan
CLICKHOUSE_USER=default
CLICKHOUSE_PASSWORD=xxx

# Redis (pub/sub for live streaming)
REDIS_URL=redis://redis:6379/0

# Feed API keys (legacy fallback when no YAML integrations configured)
ADSB_FEED_URL=xxx
ADSB_SOURCE=adsb_exchange
AISSTREAM_API_KEY=xxx
CELESTRAK_URL=https://celestrak.org/NORAD/elements/gp.php
PLANET4589_URL=https://planet4589.org/space/gcat/data/cat/satcat.tsv
BGPSTREAM_PROJECT=ris-live

# GeoIP
MAXMIND_DB_PATH=/data/GeoLite2-ASN.mmdb
```

---

## Testing Strategy

```go
// Parser tests: fixture-based, test every feed format edge case
// Use real JSON/binary samples captured from actual feeds
// Store fixtures in testdata/ directories

// Batcher tests:
// - Flush on count threshold
// - Flush on time threshold
// - No ACK semantics with NATS core; messages are transient
// - CH insert retry with backoff; verify bounded goroutines
// - Redis failure doesn't block pipeline

// Dedup tests:
// - Identical (asset_id, timestamp) detected as duplicate
// - Different timestamps for same asset NOT deduplicated
// - Window expiry removes old entries
// - Concurrent access safety

// Integration tests:
// - Full pipeline: publish to NATS → batcher → ClickHouse
// - Use testcontainers for NATS and ClickHouse
// - Verify row counts and data correctness

// Do NOT mock ClickHouse inserts in integration tests.
// Use a real ClickHouse instance via testcontainers.
```

---

## Don't Do These Things

1. **No TypeScript/Node.js** — Go for everything in this repo
2. **No Kafka/Redpanda** — Use NATS (core now; JetStream optional later)
3. **No ClickHouse HTTP interface for inserts** — native TCP protocol (ch-go)
4. **No Float64 for coordinates** — Int32 * 10_000_000
5. **No bloom filters for dedup** — in-memory hashmap (no false positives)
6. **No writing to PostgreSQL** — this pipeline doesn't touch the web database
7. **No reading from Redis** — this pipeline only publishes to Redis
8. **No satellite imagery processing** — TLE orbital positions only
9. **No PII or full article text** — aggregated/summary data only
10. **No complex ORMs** — raw SQL for DDL, ch-go for inserts
11. **No H3 computation in ClickHouse** — compute in Go worker before insert
12. **No skipping validation** — every event checked before NATS publish
13. **No broker-level delivery guarantees** — NATS core is best-effort
14. **No ignoring context cancellation** — graceful shutdown on SIGTERM
15. **No Hetzner Cloud Volumes for ClickHouse** — local NVMe only

---

## Verification Checklist

Before marking any task complete:

- [ ] All events normalize to FukanEvent struct exactly
- [ ] Coordinates stored as Int32 * 10_000_000
- [ ] H3 cell computed in Go worker, not in ClickHouse
- [ ] NATS publisher `Drain()` called on shutdown (best-effort flush)
- [ ] Redis publish failure doesn't block pipeline
- [ ] Validation rejects null-island (0,0) and out-of-range coordinates
- [ ] Graceful shutdown on context cancellation (SIGTERM)
- [ ] Worker reconnects on feed disconnection with backoff
- [ ] Tests written (unit + integration with testcontainers)
- [ ] ClickHouse DDL changes reflected in scripts/clickhouse-init.sql
- [ ] Environment variables documented if new ones added

---

## Quick Reference

```bash
# Build (single unified binary)
go build ./cmd/fukan-ingest

# Test
go test ./...                              # All tests
go test ./internal/worker/adsb/...         # ADS-B parser tests
go test ./internal/batcher/...             # Batcher tests
go test -tags integration ./...            # Integration tests (needs Docker)

# Run locally (Cobra subcommands + Viper config)
./fukan-ingest worker --type adsb --config config.yaml
./fukan-ingest batcher --type aircraft --config config.yaml
./fukan-ingest version

# Legacy env-var mode still works (Viper env bindings)
ADSB_FEED_URL=http://feed NATS_URL=nats://localhost:4222 ./fukan-ingest worker --type adsb

# Docker
docker build -t fukan-ingest -f deploy/docker/Dockerfile .
```

---

## Questions?

1. Check `scripts/clickhouse-init.sql` for ClickHouse schema
2. Check `internal/model/event.go` for the canonical event struct
3. Check existing parser implementations for normalization patterns
4. Check `testdata/` directories for feed format examples
5. When in doubt, keep it simple
6. Ask the developer before adding new feed types or changing the event schema

**Remember: This pipeline is the nervous system of Fukan. Reliability and correctness matter more than cleverness. Simple, tested, observable.**
