# Fukan Ingest — Architecture

> Last updated: 2026-04-18

> Go ETL pipeline that consumes external telemetry feeds, normalizes them into a unified schema, publishes via NATS core (RAM-only), batch-inserts into ClickHouse, and broadcasts live updates to anycable-go via Redis pub/sub.

---

## System Overview

Two parallel pipelines share the same infrastructure (NATS, ClickHouse, Redis) but carry different data shapes: **moving-asset telemetry** (aircraft, vessels, satellites — long-lived identities, "latest position per asset" semantics) and **BGP events** (announcements, withdrawals, hijacks, leaks — event-stream semantics, no "latest state per ASN"). Forcing BGP through the telemetry pipeline exploded `telemetry_latest` to ~800k rows and saturated the Redis broadcast path, so BGP owns its own subject, table, and stream prefix.

```
                    External Feeds
        ┌──────────────────────────────────┐
        │ ADS-B  AIS  TLE  │     BGP       │
        └────────┬─────────┴──────┬────────┘
                 ▼                ▼
    ┌──────────────────────┐  ┌──────────────────────┐
    │ Telemetry Workers    │  │ BGP Worker (RIS Live)│
    │  FukanEvent          │  │  BgpEvent            │
    └──────────┬───────────┘  └──────────┬───────────┘
               │ fukan.telemetry.<type>  │ fukan.bgp.events
               ▼                         ▼
            NATS core (RAM-only, queue groups per subject)
               │                         │
               ▼                         ▼
    ┌──────────────────────┐  ┌──────────────────────┐
    │ Batcher (telemetry)  │  │ BGPBatcher           │
    │ CH  : 10k or 2s      │  │ CH  : 10k or 2s      │
    │ Pub : 500 or 50 ms   │  │ Pub : 500 or 50 ms   │
    └────┬───────────┬─────┘  └────┬───────────┬─────┘
         │           │             │           │
         ▼           ▼             ▼           ▼
    ClickHouse   Redis          ClickHouse   Redis
    telemetry_   telemetry:<h3> bgp_events   bgp:<h3>
    raw          (res 2–7)                   (res 3 only)
         │
         ▼ (materialized views)
    telemetry_latest   (AggregatingMergeTree)
    telemetry_h3_agg   (AggregatingMergeTree, 5-min buckets)
```

Both pipelines terminate at anycable-go on the `__anycable__` Redis pub/sub channel; only the embedded `stream` prefix in each envelope distinguishes them. The web layer subscribes to each prefix via a separate AnyCable channel.

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
fukan-ingest batcher --type bgp --config config.yaml   # dedicated BGP pipeline
```

`--type bgp` instantiates a parallel `BGPBatcher` that subscribes to `fukan.bgp.events` (queue group `batcher-bgp`), inserts into `bgp_events`, and publishes on the `bgp:<h3>` stream prefix at resolution 3 only. All other `--type` values use the telemetry batcher against `telemetry_raw` + `telemetry:<h3>` at resolutions 2–7.

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
    batcher.go                     # batcher subcommand (switches on --type bgp)
    refresh.go                     # refresh subcommand
    migrate.go                     # migrate subcommand (up/down/version)
    version.go                     # version subcommand
    migrations/                    # Embedded SQL migration files
      000001_create_telemetry_tables.{up,down}.sql
      000002_create_aircraft_meta.{up,down}.sql
      000003_add_telemetry_fields.{up,down}.sql
      000004_denormalize_metadata.{up,down}.sql
      000005_add_satellite_fields.{up,down}.sql
      000006_add_sat_status.{up,down}.sql
      000007_create_bgp_events.{up,down}.sql

  config/config.go                 # Typed config structs + Validate()
  model/event.go                   # FukanEvent canonical struct (moving assets)
  model/bgp_event.go               # BgpEvent canonical struct (BGP event stream)
  model/validate.go                # Validate + ValidateBgp
  coord/coord.go                   # ScaleLat, ScaleLon, ComputeH3
  nats/nats.go                     # Connect() + PublishJSON()
  clickhouse/clickhouse.go         # Connect() (dial + ping)
  clickhouse/insert.go             # InsertBatch() → telemetry_raw
  clickhouse/bgp_insert.go         # InsertBGPBatch() → bgp_events
  redis/publisher.go               # PublishBatch + PublishBGPBatch (prefix+res configurable)
  oauth2/token.go                  # OAuth2 client credentials (OpenSky)
  signal/signal.go                 # NotifyCtx() shared signal handling
  refresh/aircraft.go              # OpenSky CSV → ClickHouse loader
  refresh/satellite.go             # GCAT satcat.tsv → ClickHouse loader

  worker/worker.go                 # Worker interface + RunWithReconnect
  worker/adsb/                     # OpenSky /states/all → FukanEvent
  worker/ais/                      # aisstream.io WebSocket → FukanEvent
  worker/tle/                      # CelesTrak + classified TLE → SGP4 → FukanEvent
  worker/bgp/                      # RIS Live WebSocket → BgpEvent
    worker.go, parser.go, state.go, geo.go, tables.go
  batcher/batcher.go               # Telemetry batcher (FukanEvent)
  batcher/bgp_batcher.go           # BGP batcher (BgpEvent, parallel struct)
```

---

## Packages

### `internal/model`

Two canonical structs, one per pipeline.

**`FukanEvent`** (moving-asset telemetry) is denormalized — typed feed-specific columns live directly on the struct so ClickHouse can store them as typed columns (sparse LowCardinality / scalar columns are free in columnar storage; `JSONExtract()` on a String `metadata` blob was not). Core fields: `Timestamp`, `AssetID`, `AssetType`, `Callsign`, `Origin`, `Category`, `Lat`/`Lon`/`Alt` (Int32 × 10⁷), `Speed`/`Heading`/`VerticalRate`, `H3Cell`, `Source`. Feed-specific columns: `Squawk` (aircraft); `NavStatus`, `IMONumber`, `ShipType`, `Destination`, `Draught`, `DimA`/`B`/`C`/`D`, `ETA`, `RateOfTurn` (vessel); `OrbitRegime`, `Confidence`, `TLEEpoch`, `Inclination`, `PeriodMinutes`, `ApogeeKm`, `PerigeeKm`, `SatStatus` (satellite). See `internal/model/event.go` for the full struct and JSON tags.

**`BgpEvent`** (routing event stream) is the sibling struct for BGP announcements, withdrawals, hijacks, and leaks. Fields: `Timestamp`, `EventID` (fnv64 hash), `Category`, `Prefix`, `OriginAS`, `ASPath`, `PathCoords` (alternating scaled lat/lon per resolved hop), `Collector`, `Lat`/`Lon`, `H3Cell`, `Source`. No identity overlap with `FukanEvent` — each BGP event is a one-time happening, not a position update, so there is no "latest state per ASN."

`Validate(FukanEvent) error` and `ValidateBgp(BgpEvent) error` both reject: empty id/category/source, null-island `(0,0)`, out-of-range coordinates, non-positive timestamps.

**Schema-drift hazard (from AGENTS.md):** the `json:"..."` tags on both structs are load-bearing — they must match the Rails `Telemetry::ViewportQuery` / `Bgp::ViewportQuery` SELECT aliases and the TypeScript `FukanEvent` / `BgpEvent` interfaces in `fukan-web`. Drift silently breaks the bootstrap/live-event unification.

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

Two parallel types — `Batcher` (telemetry, `FukanEvent`) and `BGPBatcher` (BGP, `BgpEvent`) — that share the same shape but call different ClickHouse insert and Redis publish methods. Kept as parallel structs rather than a Go generic because the ClickHouse insert calls need type-specific column lists and generics over different insert signatures would add more noise than it removes.

Each type exposes a single `HandleMsg(*nats.Msg)` entry point. Incoming events are (1) validated, (2) enqueued on a bounded broadcast channel for live streaming, and (3) appended to the ClickHouse buffer for persistence. The two paths are decoupled so live subscribers see updates within ~50 ms regardless of the ClickHouse buffer state, and neither path spawns a goroutine per event.

**ClickHouse path** (dual-threshold flush):
- **Size threshold:** 10,000 events triggers immediate flush.
- **Time threshold:** 2-second `AfterFunc` timer triggers flush if buffer is non-empty.
- **Retry:** Failed inserts retry with exponential backoff (100ms initial, 5s cap, 5 attempts). In-flight retry goroutines capped at 10 via semaphore. Retries detect `client is closed` errors and reconnect the ClickHouse client in place.
- Telemetry batcher writes to `telemetry_raw` via `clickhouse.InsertBatch`; BGP batcher writes to `bgp_events` via `clickhouse.InsertBGPBatch`.

**Broadcast path** (dedicated `broadcastLoop()` goroutine spawned in `New()` / `NewBGP()`):
- **Channel:** bounded at 50,000 events. On overflow the event is dropped with a warning — broadcasts are non-critical and ClickHouse is the source of truth.
- **Size threshold:** 500 events coalesced into a single Redis pipeline `Exec`.
- **Time threshold:** 50 ms ticker flushes any pending events.
- **Timeout:** Each pipeline `Exec` is bounded by a 5 s context.
- Only one `Exec` is in flight at a time per batcher (single broadcaster), so the Redis connection pool sees a small, steady workload under burst.
- Telemetry batcher calls `redis.PublishBatch` (`telemetry:<h3>`, res 2–7); BGP batcher calls `redis.PublishBGPBatch` (`bgp:<h3>`, res 3 only — see `internal/redis`).

**Shutdown:** `DrainAndFlush()` (1) flushes the ClickHouse buffer synchronously, (2) waits for all in-flight ClickHouse retries, (3) closes the broadcast channel so the broadcaster drains any remainder and returns, (4) blocks on the done channel before allowing the Redis client to close.

### `internal/clickhouse`

Free functions — no wrapper types:
- `Connect(ctx, cfg) (*ch.Client, error)` — dials via native protocol and pings.
- `InsertBatch(ctx, conn, events []FukanEvent) error` — columnar batch insert into `fukan.telemetry_raw`. Uses LowCardinality for `asset_type`, `origin`, `category`, `source`, `nav_status`, `ship_type`, `orbit_regime`, `confidence`, `sat_status`.
- `InsertBGPBatch(ctx, conn, events []BgpEvent) error` — columnar batch insert into `fukan.bgp_events`. Uses LowCardinality for `category`, `collector`, `source`. `as_path` and `path_coords` land as `Array(UInt32)` / `Array(Int32)`.

### `internal/redis`

`Publisher` broadcasts to anycable-go via the Redis pub/sub channel `__anycable__` (the anycable-go default for `broadcast_adapters = ["redis"]`). Each event is wrapped in an `anycableEnvelope` of shape `{"stream": "<prefix>:<h3_hex>", "data": "<event_json>"}` and fanned out across one or more H3 resolutions. The cell id is encoded via h3-go's `Cell.String()` so it matches what the browser's `polygonToCells()` / `cellToParent()` (h3-js) return.

- `PublishBatch(ctx, events []FukanEvent) error` — stream prefix `telemetry:`, fan-out across H3 resolutions **2–7**. The frontend subscribes at its current altitude band without server-side child expansion.
- `PublishBGPBatch(ctx, events []BgpEvent) error` — stream prefix `bgp:`, fan-out at **res 3 only**. BGP event coordinates are imprecise (geolocated from the origin AS's HQ lat/lon, not actual routing infrastructure), so zoom-band-precise subscriptions would be misleading. The frontend always subscribes at res 3 (`cellToParent(cell, 3)` on the viewport cells) regardless of camera altitude. This cuts per-event Redis `PUBLISH` calls 6×.

Both methods coalesce a whole batch into one pipeline `Exec` and are called exclusively from their respective `broadcastLoop()` goroutines (single broadcaster per batcher).

### `internal/signal`

- `NotifyCtx(parent) (context.Context, CancelFunc)` — cancels context on SIGTERM/SIGINT. A second signal forces `os.Exit(1)`. Used by all commands.

### `internal/worker`

- `Worker` interface — `Run(ctx) error`, `Name() string`.
- `RunWithReconnect(ctx, name, fn)` — wraps any `func(ctx) error` with exponential backoff (1s→60s). Resets backoff if the function ran for >60s (long-lived = healthy).

### `internal/worker/adsb`

- `ADSBWorker` struct with functional options (`WithInterval`, `WithAPIKey`, `WithOAuth2`).
- `Run()` delegates to `RunWithReconnect` wrapping an HTTP poll loop.
- `ParseStates(body, source)` — parses OpenSky `/states/all` JSON (positional arrays) into `[]FukanEvent`. Handles null fields, m/s→knots, coordinate scaling, H3 computation. Skips on-ground and no-position aircraft.

### `internal/worker/ais`

- `AISWorker` — persistent WebSocket consumer against `aisstream.io` with auto-reconnect via `RunWithReconnect`.
- Handles `PositionReport`, `StandardClassBPositionReport`, and `ShipStaticData` message types. TrueHeading 511 falls back to COG; NavigationalStatus 0–15 is mapped to a string enum. Static data fills `IMONumber`, `ShipType`, `Destination`, `Draught`, dimensions, and `ETA` on the corresponding MMSI.

### `internal/worker/tle`

- Single `TLEWorker` manages fetching (CelesTrak 2 h, classified TLEs 6 h) and continuous SGP4 propagation in four parallel goroutines — one per orbit regime (LEO 30 s, MEO 60 s, GEO 120 s, HEO 30 s).
- `Confidence` tag: `official` (CelesTrak), `community_derived` (classified), `stale` (TLE epoch > 14 d LEO/HEO, 30 d MEO/GEO).
- `SatStatus` tag: `maneuvering` on mean-motion (> 0.01 rev/day) or inclination (> 0.1°) delta between fetches; `decaying` on perigee < 150 km or BSTAR > 0.01.

### `internal/worker/bgp`

- `BGPWorker` subscribes to `wss://ris-live.ripe.net/v1/ws/` filtered to `UPDATE` messages via `RunWithReconnect`.
- `ParseMessage()` emits a `BgpEvent` per prefix per category: `announcement` (new prefix), `hijack` (origin change on a known prefix), `leak` (Tier-1 appears downstream of non-Tier-1 in the AS path), `withdrawal` (known prefix withdrawn). Plain re-announcements are dropped.
- `PrefixState` keeps a bounded (~500 k entries, LRU-pruned by `lastSeen`) map of prefix → last-seen origin AS, used for hijack detection and withdrawal filtering.
- **Geolocation (two-tier).** The parser resolves an event's primary `(lat, lon)` in this order:
  1. `CityLookup.LookupPrefix(prefix)` — a MaxMind GeoLite2-City MMDB (via `oschwald/geoip2-golang`) loaded in-process at worker startup. Sub-µs per lookup, city-level precision, no per-event network I/O.
  2. `Geo.Lookup(originAS)` — legacy path: an embedded ASN→country seed table (`tables.go`) plus async RIPEstat fallback for unknown ASNs; country-centroid precision.
  3. Both miss → event dropped.
  Withdrawals apply the same precedence with peer-ASN as the fallback ASN. `resolvePathCoords` (per-hop AS path polyline) stays on `Geo` only — MMDB has no native ASN→coord mapping and coarse per-hop coords are acceptable for the path visualization.
- **Prefix-holder org enrichment.** `ASNLookup` — parallel to `CityLookup`, backed by MaxMind **GeoLite2-ASN** (separate free MMDB, ~8.5 MB, `Reader.ASN(ip)`). `LookupPrefix` returns `(asn, org, ok)` for the AS that registered the IP block. Every announcement and withdrawal stamps `PrefixAS` + `PrefixOrg` onto the emitted `BgpEvent`. On hijacks, the MMDB returns the *legitimate holder's* AS+org while `OriginAS` (from RIS Live) holds the announcer — the divergence is the OSINT signal, surfaced in the web detail panel's "Holder vs Announced by" split. A miss leaves the fields at zero values; the event is never dropped for a missing org.
- **MMDB distribution.** Both `CityLookup` and `ASNLookup` read filesystem paths from `geoip.city_db` / `geoip.asn_db` in `config.yaml`. Worker startup fails fast on missing/empty path or unreadable file. Delivery is the operator's concern: developer drops the files locally, prod mounts via ConfigMap/Volume. Both MMDBs come from the same free MaxMind account; license terms prohibit redistribution so the files are not in the repo.
- Publishes to `fukan.bgp.events` (not `fukan.telemetry.bgp`).

### `internal/oauth2`

- `TokenSource` — OAuth2 client credentials flow for OpenSky Network. Caches tokens with 60-second refresh margin. Thread-safe.

### `internal/refresh`

- `Aircraft(ctx, conn, csvURL, token, dryRun)` — downloads OpenSky aircraft database CSV, parses ~600k rows, batch-inserts into `fukan.aircraft_meta` (50k rows per batch). Preserves existing image URLs across refreshes.
- `Satellite(ctx, conn, tsvURL, dryRun)` — downloads GCAT `satcat.tsv`, parses, batch-inserts into `fukan.satellite_meta`.

### `internal/config`

Typed configuration structs with `mapstructure` tags for Viper unmarshalling:
- `Config` (top-level) → `NATSConfig`, `ClickHouseConfig`, `RedisConfig`, `GeoIPConfig`, `OpenSkyConfig`, `Integrations map[string][]IntegrationConfig`
- `GeoIPConfig` → `CityDB` (path to a GeoLite2-City MMDB; consumed by the BGP worker only).
- `IntegrationConfig` → `Name`, `APIURL`, `APIKey`, `Interval`, `ClientID`, `ClientSecret`, `TokenURL`
- `Validate()` — checks required fields (`nats.url`, `clickhouse.addr`, `clickhouse.database`, `redis.url`). `geoip.city_db` is validated at BGP-worker startup rather than in `Validate()` since it's only required for `--type bgp`.

---

## Data Flow Guarantees

| Guarantee | Mechanism |
|---|---|
| Best-effort delivery | NATS core (RAM-only). No persistence or redelivery. |
| Durability | None at broker level; data can be lost on restarts. |
| Load distribution | NATS queue groups: `batcher-{asset_type}` for telemetry, `batcher-bgp` for BGP. |
| Backpressure | None from broker; batcher flush controls local memory. |
| CH failure behavior | Retry with exponential backoff (100ms→5s, 5 attempts, 10 concurrent cap). |
| Live broadcast latency | ≤50 ms from NATS arrival to Redis publish, independent of ClickHouse buffer state. |
| Broadcast overflow | 50k-event bounded channel per batcher; on overflow events are dropped with a warning (ClickHouse path unaffected). |
| Shutdown behavior | Batchers `DrainAndFlush()`: CH flush → CH retry drain → broadcast channel close → broadcast drain. NATS `Drain()` to flush pending. |

---

## ClickHouse Schema

Managed via golang-migrate. Migration files are embedded in the binary at `internal/commands/migrations/`. Apply with `fukan-ingest migrate up`.

**Telemetry tables (moving assets):**

1. **`telemetry_raw`** — MergeTree, partitioned by hour, ordered by `(asset_type, h3_cell, event_time, asset_id)`. TTL: 24 hours (dev/validation). Production target: 90 days with tiered storage. Denormalized across migrations 000003–000006: typed columns per feed (squawk; nav_status / imo_number / ship_type / destination / draught / dim_a/b/c/d / eta / rate_of_turn; orbit_regime / confidence / tle_epoch / inclination / period_minutes / apogee_km / perigee_km / sat_status). No `metadata` JSON blob — `JSONExtract()` on String is expensive at scale; sparse LowCardinality / scalar columns are free.
2. **`telemetry_latest`** — AggregatingMergeTree. Latest position per `(asset_type, asset_id)` via argMax aggregate states over every column in `telemetry_raw`. `telemetry_latest_mv` populates it; `telemetry_latest_flat` is the read-side `VIEW` that applies `argMaxMerge()` / `maxMerge()` so callers can SELECT the flat shape directly.
3. **`telemetry_h3_agg`** — AggregatingMergeTree. 5-minute bucketed density counts per `(h3_cell, asset_type)` for heatmaps.
4. **`aircraft_meta`** — ReplacingMergeTree. Aircraft reference data from OpenSky (ICAO24, registration, operator, images).
5. **`satellite_meta`** — ReplacingMergeTree. GCAT reference data (NORAD ID, name, object type, status, country/operator, launch date, mass, apogee/perigee/inclination).

**BGP table (event stream):**

6. **`bgp_events`** (migrations 000007 + 000008) — MergeTree, partitioned by hour, ordered by `(category, h3_cell, event_time)`. TTL: 24 h. Append-only — no argMax, no "latest per ASN" since BGP events are one-time happenings. Columns: `event_time`, `event_id`, `category`, `prefix`, `origin_as`, `prefix_as`, `prefix_org`, `as_path`, `path_coords`, `collector`, `lat`, `lon`, `h3_cell`, `source`. `prefix_as` + `prefix_org` (added in 000008) carry the registered holder identity from GeoLite2-ASN and may diverge from `origin_as` on hijacks. Queried by the web layer via `Bgp::ViewportQuery` with a 15-minute recency window and `LIMIT 1000`.

BGP never writes to `telemetry_raw` / `telemetry_latest` / `telemetry_h3_agg`. The previous iteration did — every RIS Live event got a unique hashed `asset_id`, producing ~800k rows in `telemetry_latest` (vs ~53k for all other asset types combined) and saturating the broadcast path. The 000007 migration and the split pipeline remove that pressure.

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

# Run batcher (one per asset_type in parallel for full coverage)
go run ./cmd/fukan-ingest batcher --type aircraft --config config.yaml
go run ./cmd/fukan-ingest batcher --type vessel    --config config.yaml
go run ./cmd/fukan-ingest batcher --type satellite --config config.yaml
go run ./cmd/fukan-ingest batcher --type bgp       --config config.yaml   # BGPBatcher

# Refresh reference data
go run ./cmd/fukan-ingest refresh --target airlines   --config config.yaml
go run ./cmd/fukan-ingest refresh --target satellites --config config.yaml

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
