# AGENTS.md — Fukan Ingestion Framework

> AI coding assistant context file for the Go ETL/ingestion pipeline.
> Read fully before generating any code.
> Last updated: 2026-04-12
>
> For the most granular (and continuously updated) reference on the
> pipeline's current shape, see `docs/ARCHITECTURE.md` and `docs/PLAN.md`.
> This file captures the invariants, non-goals, and narrative explanation
> of *why* things are the way they are — those docs capture the *what*.

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
- ❌ JSON blobs in String columns for structured data → Typed ClickHouse columns (ClickHouse is columnar; sparse columns cost nothing, `JSONExtract()` on String is expensive at scale)

### Data Ownership Rule

```
This pipeline OWNS all writes to ClickHouse, with one explicit exception:

  EXCEPTION — aircraft_meta image enrichment:
  The web layer (Rails) may write image_url, image_attribution, and
  updated_at on aircraft_meta rows. This happens lazily, on-click,
  via Aircraft::FetchImage in fukan-web because the upstream provider
  (Planespotters) is rate-limited and cannot be bulk-pre-fetched by
  ingest. ALL OTHER aircraft_meta columns (registration, manufacturer,
  model, operator, etc.) remain ingest-owned and MUST NOT be written
  from Rails.

The web layer reads all other ClickHouse tables but NEVER writes to them.
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
//
// The struct is DENORMALIZED — prior versions used a single `Metadata string`
// JSON blob for type-specific fields, but migration 000004 replaced that with
// typed ClickHouse columns because JSONExtract() on a String column is
// expensive at scale and sparse columnar storage is free. Add new fields
// here, add a matching typed column to telemetry_raw via a new migration,
// add the argMax state + flat-view projection to telemetry_latest, and
// update the Go insert batch columns in internal/clickhouse/insert.go.
type FukanEvent struct {
    Timestamp    int64     `json:"ts"`       // Unix epoch milliseconds
    AssetID      string    `json:"id"`       // ICAO hex, MMSI, NORAD ID, ASN, or event hash
    AssetType    AssetType `json:"type"`     // aircraft, vessel, satellite, bgp_node, news
    Callsign     string    `json:"callsign"` // flight callsign, vessel name, satellite designator
    Origin       string    `json:"origin"`   // origin country, city, airport, port
    Category     string    `json:"cat"`      // wake class, vessel type, satellite regime
    Lat          int32     `json:"lat"`      // latitude * 10_000_000
    Lon          int32     `json:"lon"`      // longitude * 10_000_000
    Alt          int32     `json:"alt"`      // meters above sea level (0 for surface/network)
    Speed        float32   `json:"spd"`      // knots (aircraft/vessel) or 0
    Heading      float32   `json:"hdg"`      // degrees (0-360) or 0
    VerticalRate float32   `json:"vr"`       // m/s, positive = climbing (aircraft)
    H3Cell       uint64    `json:"h3"`       // pre-computed H3 index at resolution 7
    Source       string    `json:"src"`      // provider identifier (e.g. "adsb_exchange")

    // Aircraft-specific
    Squawk string `json:"squawk"` // transponder squawk code

    // Vessel-specific (AIS)
    NavStatus   string  `json:"nav_status"`   // under_way, at_anchor, moored, ...
    IMONumber   uint32  `json:"imo_number"`
    ShipType    string  `json:"ship_type"`    // cargo, tanker, passenger, ...
    Destination string  `json:"destination"`  // reported destination port
    Draught     float32 `json:"draught"`      // meters
    DimA        uint16  `json:"dim_a"`        // meters, bow → AIS reference
    DimB        uint16  `json:"dim_b"`        // meters, stern → AIS reference
    DimC        uint16  `json:"dim_c"`        // meters, port → AIS reference
    DimD        uint16  `json:"dim_d"`        // meters, starboard → AIS reference
    ETA         string  `json:"eta"`          // estimated time of arrival (AIS format)
    RateOfTurn  float32 `json:"rate_of_turn"` // degrees/minute

    // Satellite-specific (TLE-derived, set by the TLE worker)
    OrbitRegime   string  `json:"orbit_regime"`   // 'leo' | 'meo' | 'geo' | 'heo'
    Confidence    string  `json:"confidence"`     // 'official' | 'community_derived' | 'stale'
    TLEEpoch      int64   `json:"tle_epoch"`      // Unix ms of the TLE used to propagate
    Inclination   float32 `json:"inclination"`    // degrees
    PeriodMinutes float32 `json:"period_minutes"`
    ApogeeKm      float32 `json:"apogee_km"`
    PerigeeKm     float32 `json:"perigee_km"`
    SatStatus     string  `json:"sat_status"`     // 'maneuvering' | 'decaying' | ''
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
│   └── fukan-ingest/            # Thin entrypoint — ~15 lines, calls commands.Execute()
│       └── main.go
├── config.example.yaml          # Reference YAML config (Viper)
├── internal/
│   ├── commands/                # All subcommand logic (moved here in Phase 2.6)
│   │   ├── root.go              # Cobra root command, config loading via context
│   │   ├── worker.go            # `worker --type <feed>` subcommand
│   │   ├── batcher.go           # `batcher --type <asset>` subcommand
│   │   ├── refresh.go           # `refresh --target <target>` subcommand
│   │   ├── migrate.go           # `migrate up|down|version` subcommand
│   │   ├── version.go           # `version` subcommand
│   │   └── migrations/          # Embedded SQL migration files (golang-migrate)
│   │       ├── 000001_create_telemetry_tables.{up,down}.sql
│   │       ├── 000002_create_aircraft_meta.{up,down}.sql
│   │       ├── 000003_add_telemetry_fields.{up,down}.sql
│   │       ├── 000004_denormalize_metadata.{up,down}.sql
│   │       ├── 000005_add_satellite_fields.{up,down}.sql
│   │       └── 000006_add_sat_status.{up,down}.sql
│   ├── config/
│   │   └── config.go            # Typed config structs (mapstructure tags for Viper)
│   ├── model/
│   │   ├── event.go             # FukanEvent struct (canonical schema)
│   │   └── validate.go          # Event validation rules
│   ├── coord/
│   │   └── coord.go             # ScaleLat/ScaleLon/ComputeH3
│   ├── nats/
│   │   └── nats.go              # Connect() + PublishJSON() free functions
│   │                            # (wrapper types deleted in Phase 2.6)
│   ├── clickhouse/
│   │   ├── clickhouse.go        # Connect() free function
│   │   └── insert.go            # InsertBatch() columnar insert
│   ├── redis/
│   │   └── publisher.go         # AnyCable envelope publisher (H3 res 2–7 fan-out)
│   ├── oauth2/
│   │   └── token.go             # OAuth2 client credentials (OpenSky)
│   ├── signal/
│   │   └── signal.go            # NotifyCtx() shared signal handling
│   ├── refresh/
│   │   ├── aircraft.go          # OpenSky aircraft DB CSV → aircraft_meta
│   │   ├── satellite.go         # GCAT satcat.tsv → satellite_meta
│   │   └── discover.go          # Refresh target discovery
│   ├── worker/
│   │   ├── worker.go            # Worker interface + RunWithReconnect
│   │   ├── adsb/
│   │   │   ├── worker.go        # ADS-B HTTP polling worker
│   │   │   ├── parser.go        # OpenSky JSON → FukanEvent
│   │   │   └── parser_test.go
│   │   ├── ais/
│   │   │   ├── worker.go        # AIS WebSocket consumer (aisstream.io)
│   │   │   ├── parser.go        # Position + static messages → FukanEvent
│   │   │   └── parser_test.go
│   │   └── tle/
│   │       ├── worker.go        # CelesTrak + classified TLE fetch + SGP4 propagation
│   │       ├── fetcher.go       # OMM JSON downloader
│   │       ├── regime.go        # Orbit regime classification + OrbitalParams
│   │       ├── regime_test.go
│   │       ├── fetcher_test.go
│   │       └── propagator_test.go
│   └── batcher/
│       └── batcher.go           # Dual-path: ClickHouse flush + broadcastLoop goroutine
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
fukan.telemetry.news         # Geolocated news events
fukan.telemetry.>            # Wildcard (moving-asset telemetry)
fukan.bgp.events             # BGP routing events (event-stream, dedicated subject)
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

The batcher runs two independent pipelines sharing a single NATS message
handler. Each incoming event is (1) validated, (2) enqueued on a bounded
broadcast channel for live streaming, and (3) appended to the ClickHouse
buffer for persistence. The two paths are decoupled so live subscribers see
updates within ~50 ms regardless of the ClickHouse buffer state, and neither
path spawns a goroutine per event.

### ClickHouse path — dual-threshold flush to `telemetry_raw`

| Threshold | Value | Behavior |
|---|---|---|
| Size | `MaxBatchSize = 10_000` events | Immediate flush |
| Time | `MaxFlushLatency = 2s` AfterFunc | Flush if non-empty |
| Retry | Exponential, 100ms → 5s, 5 attempts | Bounded to 10 concurrent via `retrySem` |

`client is closed` errors trigger an in-place reconnect of the ClickHouse
client before the retry proceeds. On exhausted retries the batch is dropped
with an error log — NATS core does not persist messages, so there is no
redelivery.

### Broadcast path — dedicated `broadcastLoop()` goroutine

Spawned in `New()` and fed by `broadcastCh`, a bounded channel:

| Constant | Value | Meaning |
|---|---|---|
| `broadcastBufferSize` | 50,000 events | Channel capacity; overflow drops with warning |
| `broadcastBatchSize` | 500 events | Flush trigger (size) |
| `broadcastFlushInterval` | 50 ms | Flush trigger (time) |
| `broadcastPublishTimeout` | 5 s | Per-pipeline-Exec context deadline |

Only one Redis pipeline `Exec` is in flight at a time (single broadcaster),
so the Redis connection pool sees a small, steady workload under burst.
Broadcast-channel overflow drops events with a warning — broadcasts are
non-critical and ClickHouse is the source of truth for persistence.

### Shutdown sequence — `DrainAndFlush()`

1. Flush the ClickHouse buffer synchronously under the mutex.
2. Wait for all in-flight ClickHouse retry goroutines (`retryWg.Wait()`).
3. Close `broadcastCh` so the broadcaster drains any remainder and returns.
4. Block on `broadcastDone` before the caller closes the Redis client.

```go
// internal/batcher/batcher.go — shape
type Batcher struct {
    ch            *ch.Client           // bare ch-go client, no wrapper
    chCfg         config.ClickHouseConfig
    redis         *redis.Publisher
    buf           []model.FukanEvent
    mu            sync.Mutex
    timer         *time.Timer
    retrySem      chan struct{}
    retryWg       sync.WaitGroup
    broadcastCh   chan model.FukanEvent
    broadcastDone chan struct{}
}

func (b *Batcher) HandleMsg(msg *nats.Msg) {
    var event model.FukanEvent
    if err := json.Unmarshal(msg.Data, &event); err != nil { return }
    if err := model.Validate(event); err != nil { return }

    // Broadcast path — non-blocking enqueue, drop on overflow
    select {
    case b.broadcastCh <- event:
    default:
        slog.Warn("broadcast buffer full, dropping event", "asset_id", event.AssetID)
    }

    // ClickHouse path — buffer + size/time flush
    b.mu.Lock()
    defer b.mu.Unlock()
    b.buf = append(b.buf, event)
    if len(b.buf) >= MaxBatchSize {
        b.flushLocked()
    } else if b.timer == nil {
        b.timer = time.AfterFunc(MaxFlushLatency, func() {
            b.mu.Lock()
            defer b.mu.Unlock()
            b.flushLocked()
        })
    }
}
```

### Redis Publishing Pattern (AnyCable envelope)

The publisher broadcasts to **anycable-go** on the fixed Redis channel
`__anycable__` (anycable-go's default for `broadcast_adapters = ["redis"]`).
Each event is wrapped in an `anycableEnvelope` matching what anycable-go
relays straight to the client's ActionCable subscription:

```go
type anycableEnvelope struct {
    Stream string `json:"stream"` // "telemetry:<h3_hex>"
    Data   string `json:"data"`   // JSON-encoded FukanEvent
}
```

Each event fans out across H3 resolutions **2–7** so a browser zoomed to
continental view (res 3) and one zoomed to ground level (res 7) both
receive matching broadcasts without requiring server-side child expansion
on every subscribe. Cell ids are encoded via h3-go `Cell.String()` so they
match h3-js `polygonToCells()` output in the browser.

```go
// internal/redis/publisher.go — core loop
var broadcastResolutions = []int{2, 3, 4, 5, 6, 7}

func (p *Publisher) PublishBatch(ctx context.Context, events []model.FukanEvent) error {
    pipe := p.client.Pipeline()
    for _, e := range events {
        data, _ := json.Marshal(e)
        cell := h3.Cell(e.H3Cell)
        for _, res := range broadcastResolutions {
            streamCell := cell
            if res != 7 {
                parent, _ := cell.Parent(res)
                streamCell = parent
            }
            envelope, _ := json.Marshal(anycableEnvelope{
                Stream: "telemetry:" + streamCell.String(),
                Data:   string(data),
            })
            pipe.Publish(ctx, "__anycable__", envelope)
        }
    }
    _, err := pipe.Exec(ctx)
    return err
}
```

Called exclusively from `batcher.broadcastLoop()` — never per-event, never
from multiple goroutines concurrently.

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

**Field routing note:** all feed-specific fields below go into *typed
`FukanEvent` fields and typed ClickHouse columns*, not into the removed
`Metadata` JSON blob. See `internal/model/event.go` for the full canonical
field list.

### ADS-B (Aircraft) — IMPLEMENTED

| Source field    | FukanEvent field | Transformation                          |
|-----------------|---------------------|-----------------------------------------|
| `hex` / `icao`  | `AssetID`           | Uppercase hex string                    |
| `callsign`      | `Callsign`          | Trimmed string                          |
| `lat`           | `Lat`               | `int32(lat * 10_000_000)`              |
| `lon`           | `Lon`               | `int32(lon * 10_000_000)`              |
| `alt_baro`      | `Alt`               | Feet → meters: `int32(alt * 0.3048)`   |
| `gs`            | `Speed`             | Ground speed in knots (keep as-is)      |
| `track`         | `Heading`           | Degrees (keep as-is)                    |
| `vrate`         | `VerticalRate`      | m/s, positive = climbing                |
| `squawk`        | `Squawk`            | Transponder code as string              |
| `now`           | `Timestamp`         | Unix epoch → milliseconds               |
|                 | `AssetType`         | Always `"aircraft"`                     |
|                 | `H3Cell`            | Computed from lat/lon at res 7          |

**Provider:** OpenSky Network `/states/all` (OAuth2 client credentials —
see `internal/oauth2/token.go`).
**Connection:** HTTP polling with exponential backoff via `RunWithReconnect`.
**Enrichment:** aircraft metadata (registration, manufacturer, operator)
refreshed from OpenSky's aircraft database CSV (~600k rows) via
`fukan-ingest refresh --target airlines` into `fukan.aircraft_meta`.

### AIS (Vessels) — IMPLEMENTED (Phase 3)

| Source field       | FukanEvent field | Transformation                                      |
|--------------------|---------------------|-----------------------------------------------------|
| `mmsi`             | `AssetID`           | 9-digit string                                      |
| `name`             | `Callsign`          | Vessel name                                         |
| `latitude`         | `Lat`               | `int32(lat * 10_000_000)`                          |
| `longitude`        | `Lon`               | `int32(lon * 10_000_000)`                          |
| `sog`              | `Speed`             | Knots                                               |
| `cog`              | `Heading`           | Degrees (TrueHeading 511 → COG fallback)           |
| `navigational_status` | `NavStatus`      | 0-15 mapped to `under_way`, `at_anchor`, `moored`, ... |
| `rate_of_turn`     | `RateOfTurn`        | Degrees/minute                                      |
| `imo_number`       | `IMONumber`         | From `ShipStaticData` message                       |
| `ship_type`        | `ShipType`          | Code → `cargo`, `tanker`, `passenger`, ...          |
| `destination`      | `Destination`       | From `ShipStaticData`                               |
| `draught`          | `Draught`           | Meters                                              |
| `dim_a/b/c/d`      | `DimA/B/C/D`        | Bow/stern/port/starboard reference distances (m)    |
| `eta`              | `ETA`               | AIS ETA string                                      |
| `timestamp`        | `Timestamp`         | Unix epoch → milliseconds                           |
|                    | `AssetType`         | Always `"vessel"`                                   |
|                    | `Alt`               | Always `0`                                          |
|                    | `H3Cell`            | Computed from lat/lon at res 7                      |

**Provider:** aisstream.io (`wss://stream.aisstream.io/v0/stream`) with
global bounding box subscription. Handles `PositionReport`,
`StandardClassBPositionReport`, and `ShipStaticData` message types.
**Connection:** Persistent WebSocket via `github.com/coder/websocket` with
auto-reconnect through `RunWithReconnect`.
**API key:** `integrations.ais[].api_key` in `config.yaml`.

### Satellites (TLE Orbits) — IMPLEMENTED (Phase 4)

| Source              | FukanEvent field | Transformation                              |
|---------------------|---------------------|---------------------------------------------|
| NORAD catalog ID    | `AssetID`           | NORAD ID as string                          |
| OMM `OBJECT_NAME`   | `Callsign`          | Satellite designator (e.g. `STARLINK-30042`) |
| SGP4 output         | `Lat`               | `int32(lat * 10_000_000)`                  |
| SGP4 output         | `Lon`               | `int32(lon * 10_000_000)`                  |
| SGP4 output         | `Alt`               | Kilometers → meters: `int32(alt * 1000)`   |
| Propagation time    | `Timestamp`         | Propagation step time in ms                 |
| TLE epoch           | `TLEEpoch`          | Unix ms                                     |
| Computed            | `OrbitRegime`       | `leo` \| `meo` \| `geo` \| `heo`           |
| Computed            | `Inclination`       | Degrees (from TLE)                          |
| Computed            | `PeriodMinutes`     | From mean motion                            |
| Computed            | `ApogeeKm`          | From semi-major axis + eccentricity         |
| Computed            | `PerigeeKm`         | From semi-major axis + eccentricity         |
| Tag                 | `Confidence`        | `official` \| `community_derived` \| `stale` |
| Tag                 | `SatStatus`         | `maneuvering` \| `decaying` \| `""`         |
|                     | `AssetType`         | Always `"satellite"`                        |
|                     | `Speed`             | `0` (orbital velocity is regime-obvious)    |
|                     | `Heading`           | `0` (not meaningful for orbits)             |
|                     | `H3Cell`            | Computed from sub-satellite point           |

**Sources:**
- **CelesTrak** (GP JSON, HTTP fetch every 2h) — primary catalog, ~15k
  active objects. Returns 403 if no data updated since last fetch, which
  `RunWithReconnect` handles cleanly.
- **Classified TLEs** (McCants / Molczan community lists) — ~500 classified
  or military objects not in the official catalog. Tagged with
  `Confidence: community_derived`.

**Propagation loop:** Single `tle` worker manages fetching + propagation.
Four goroutines run in parallel, one per orbit regime, at regime-appropriate
cadence:

| Regime | Interval | Why |
|---|---|---|
| LEO (< 2,000 km) | 30 s | Fast ground-track motion |
| MEO (2,000–35,786 km) | 60 s | Slower apparent motion |
| GEO (~35,786 km) | 120 s | Near-stationary in ECEF |
| HEO (eccentric) | 30 s | Fast perigee passes |

Aggregate event rate ~455 events/s across all regimes.

**Staleness:** TLEs older than 14 days (LEO/HEO) or 30 days (MEO/GEO) are
tagged `Confidence: stale` — SGP4 accuracy degrades rapidly past the
epoch's validity window.

**Maneuver detection:** Compares mean motion (>0.01 rev/day delta) and
inclination (>0.1° delta) between TLE fetch cycles. On a match the new
entry is tagged `SatStatus: maneuvering`.

**Decay detection:** Checks perigee < 150 km or BSTAR > 0.01 at propagation
time. On a match the event is tagged `SatStatus: decaying`.

**Go library:** `github.com/akhenakh/sgp4` (Apache 2.0) — accepts OMM JSON
directly via `TLE.FindPositionAtTime()` + `ToGeodetic()`.

**Metadata enrichment:** GCAT (`planet4589.org/space/gcat/tsv/cat/satcat.tsv`)
loaded into `fukan.satellite_meta` via `fukan-ingest refresh --target satellites`.
See `internal/refresh/satellite.go` — the loader provides name, owner,
country, launch date, mass, apogee/perigee/inclination, object type, status.

### BGP (Internet Routing) — IMPLEMENTED (split pipeline, see ARCHITECTURE.md)

BGP events do **not** share the `FukanEvent` / `telemetry_raw` / `telemetry_latest` pipeline. They flow through a dedicated pipeline with their own model, subject, batcher, ClickHouse table, and Redis stream prefix:

| Stage        | Telemetry (aircraft/vessel/satellite) | BGP                             |
|--------------|---------------------------------------|---------------------------------|
| Go struct    | `model.FukanEvent`                    | `model.BgpEvent`                |
| NATS subject | `fukan.telemetry.<type>`              | `fukan.bgp.events`              |
| Batcher      | `batcher.Batcher`                     | `batcher.BGPBatcher`            |
| CH table     | `telemetry_raw` + `telemetry_latest`  | `bgp_events` (append-only)      |
| Redis prefix | `telemetry:<h3>` (res 2–7)            | `bgp:<h3>` (res 3 only)         |

`BgpEvent` fields: `Timestamp`, `EventID`, `Category` (announcement / hijack / leak / withdrawal), `Prefix`, `OriginAS` (announcer from RIS Live), `PrefixAS` + `PrefixOrg` (registered holder from GeoLite2-ASN; diverges from `OriginAS` on hijacks), `ASPath`, `PathCoords`, `Collector`, `Lat`, `Lon`, `H3Cell`, `Source`.

**Source:** RIPE NCC RIS Live websocket (`wss://ris-live.ripe.net/v1/ws/`), no API key.
**Geolocation (two-tier):**
1. MaxMind **GeoLite2-City** MMDB (via `oschwald/geoip2-golang`) keyed on the prefix's network address — city-level precision, sub-µs in-process lookups.
2. Fallback: ASN seed table + async RIPEstat (`internal/worker/bgp/geo.go`) — country-centroid precision.
**Org enrichment:** MaxMind **GeoLite2-ASN** MMDB (separate free file, ~8.5 MB) resolves the prefix to its registered holder AS + org. On hijacks this diverges from `OriginAS` (announcer from RIS Live) — the gap is the OSINT signal. See `ASNLookup` in `internal/worker/bgp/asn.go`.
MMDB paths are set in `config.yaml` under `geoip.city_db` and `geoip.asn_db` (no env vars); BGP worker fails fast if either is missing. Per-hop `path_coords` uses the ASN-centroid path only.

### News (Geolocated Events) — PLANNED (PLAN.md Phase 6)

| Source              | FukanEvent field | Transformation                        |
|---------------------|---------------------|---------------------------------------|
| GDELT event ID      | `AssetID`           | GDELT GlobalEventID or hash           |
| GDELT ActionGeo     | `Lat/Lon`           | Pre-geolocated by GDELT               |
| Publication time    | `Timestamp`         | Unix epoch → milliseconds             |
|                     | `AssetType`         | Always `"news"`                       |

News-specific fields (headline, URL, tone) will land as typed columns
on a dedicated news table or shared fields on `telemetry_raw` — TBD
during implementation.

**Source:** GDELT Project (free, pre-geolocated global news, 15-minute update cycle).
**Storage rule:** Headline + source URL + sentiment score only. **Never store full article text** (copyright).

---

## ClickHouse Schema

### Table Definitions

```sql
-- scripts/clickhouse-init.sql

-- Raw telemetry (full history)
-- Canonical DDL lives in internal/commands/migrations/ and is applied via
-- `fukan-ingest migrate up` (golang-migrate with embed.FS). The schema
-- shown here is the MergeTree/AggregatingMergeTree shape after all
-- migrations through 000006. Feed-specific fields (squawk, nav_status,
-- imo_number, ship_type, destination, draught, dim_a/b/c/d, eta,
-- rate_of_turn, orbit_regime, confidence, tle_epoch, inclination,
-- period_minutes, apogee_km, perigee_km, sat_status) all live as typed
-- columns — see migrations 000003, 000004, 000005, 000006 for the
-- column-by-column additions.

CREATE TABLE IF NOT EXISTS fukan.telemetry_raw (
    event_time    DateTime64(3)   CODEC(DoubleDelta, LZ4),
    asset_id      String          CODEC(LZ4),
    asset_type    LowCardinality(String),
    callsign      String          CODEC(LZ4),
    origin        LowCardinality(String),
    category      LowCardinality(String),
    lat           Int32           CODEC(DoubleDelta, LZ4),
    lon           Int32           CODEC(DoubleDelta, LZ4),
    alt           Int32           CODEC(DoubleDelta, LZ4),
    speed         Float32         CODEC(Gorilla, LZ4),
    heading       Float32         CODEC(Gorilla, LZ4),
    vertical_rate Float32         CODEC(Gorilla, LZ4),
    h3_cell       UInt64          CODEC(LZ4),
    source        LowCardinality(String),
    -- ...plus the feed-specific typed columns listed above.
    -- NO `metadata String` — removed in migration 000004 (denormalization).
) ENGINE = MergeTree()
ORDER BY (asset_type, h3_cell, event_time, asset_id)
PARTITION BY toStartOfHour(event_time)
TTL event_time + INTERVAL 2 DAY
        MODIFY CODEC(DoubleDelta, ZSTD(3)),
    event_time + INTERVAL 90 DAY
        TO VOLUME 'cold'
SETTINGS index_granularity = 8192;

-- Latest position per asset — AggregatingMergeTree, populated via
-- telemetry_latest_mv which projects argMaxState(column, event_time)
-- for every field. telemetry_latest_flat is the read-side view that
-- applies argMaxMerge() so Rails can SELECT the flat shape.
CREATE TABLE IF NOT EXISTS fukan.telemetry_latest (
    asset_type         LowCardinality(String),
    asset_id           String,
    ts_state           AggregateFunction(max, DateTime64(3)),
    callsign_state     AggregateFunction(argMax, String, DateTime64(3)),
    lat_state          AggregateFunction(argMax, Int32, DateTime64(3)),
    lon_state          AggregateFunction(argMax, Int32, DateTime64(3)),
    alt_state          AggregateFunction(argMax, Int32, DateTime64(3)),
    -- ...argMax state columns for every field in telemetry_raw.
    -- See migrations 000003, 000005, 000006 for the full list.
) ENGINE = AggregatingMergeTree()
ORDER BY (asset_type, asset_id);

CREATE MATERIALIZED VIEW IF NOT EXISTS fukan.telemetry_latest_mv
TO fukan.telemetry_latest AS
SELECT
    asset_type,
    asset_id,
    maxState(event_time)                AS ts_state,
    argMaxState(callsign, event_time)   AS callsign_state,
    argMaxState(lat, event_time)        AS lat_state,
    -- ...one argMaxState per column.
FROM fukan.telemetry_raw
GROUP BY asset_type, asset_id;

-- Flat read-side view — argMaxMerge() for each state column so queries
-- can SELECT the current-state shape without thinking about aggregates.
CREATE VIEW IF NOT EXISTS fukan.telemetry_latest_flat AS
SELECT
    asset_type,
    asset_id,
    maxMerge(ts_state)                  AS event_time,
    argMaxMerge(callsign_state)         AS callsign,
    argMaxMerge(lat_state)              AS lat,
    -- ...argMaxMerge per column.
FROM fukan.telemetry_latest
GROUP BY asset_type, asset_id;

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
// internal/worker/worker.go

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

# GeoIP — configured via config.yaml (geoip.city_db), not env vars.
# GeoLite2-City MMDB; BGP worker fails fast on missing/unreadable file.
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
16. **No JSON blobs in String columns** — use typed ClickHouse columns for all structured data; columnar storage makes sparse columns free

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
