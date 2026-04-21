# Fukan Ingest — Implementation Plan

> Last updated: 2026-04-18

---

## Phase 0 — Foundation

- ~~Go module, project structure, `go.mod`~~
- ~~`FukanEvent` canonical struct with JSON tags (`internal/model/event.go`)~~
- ~~`AssetType` enum: aircraft, vessel, satellite, bgp_node, news~~
- ~~Coordinate helpers: `ScaleLat`, `ScaleLon`, `ComputeH3` (`internal/coord`)~~
- ~~Validation: asset_id, asset_type, source, coordinates, timestamp (`internal/model/validate.go`)~~
- ~~Docker Compose: NATS, ClickHouse, Redis (`compose.yaml`)~~
- ~~ClickHouse DDL via golang-migrate (`internal/commands/migrations/`)~~
- ~~Cobra CLI: unified `cmd/fukan-ingest` binary with `worker`, `batcher`, `refresh`, `migrate`, `version` subcommands~~
- ~~Viper config: YAML file + env-var overrides + typed structs + validation (`internal/config`)~~
- ~~Multi-integration support: `integrations` map in config allows multiple providers per feed type~~
- ~~Legacy env-var fallback for backward compatibility~~

## Phase 1 — Core Pipeline

- ~~NATS helpers: `Connect()` + `PublishJSON()` free functions (`internal/nats`)~~
- ~~ClickHouse native client: `Connect()` + columnar `InsertBatch()` free functions (`internal/clickhouse`)~~
- ~~Redis pub/sub publisher grouped by H3 cell (`internal/redis`)~~
- ~~Batcher: dual-threshold flush (10k events / 2s) (`internal/batcher`)~~
- ~~Batcher: exponential retry on ClickHouse insert failure (100ms→5s, 5 attempts, 10 concurrent cap)~~
- ~~Worker interface (`internal/worker`)~~
- ~~`RunWithReconnect` helper with exponential backoff (1s initial, 60s cap, reset after 60s healthy)~~
- ~~Shared signal handling: `NotifyCtx()` with double-signal force exit (`internal/signal`)~~
- ~~Config validation: required fields fail fast at startup (`Config.Validate()`)~~

## Phase 2 — First Feed (ADS-B)

- ~~ADS-B HTTP polling worker (`internal/worker/adsb`) with RunWithReconnect~~
- ~~Parser: OpenSky JSON → FukanEvent (`internal/worker/adsb/parser.go`)~~
- ~~Fixture-based parser tests (`internal/worker/adsb/testdata/opensky_response.json`)~~
- ~~`worker --type adsb` subcommand wiring (`internal/commands/worker.go`)~~
- ~~`batcher --type aircraft` subcommand wiring (`internal/commands/batcher.go`)~~

## Phase 2.5 — Code Review Fixes

- ~~**MEDIUM:** Timer not stopped in `DrainAndFlush` — now explicitly stopped~~
- ~~**MEDIUM:** Validation missing `AssetType` and `Source` checks — added with tests~~
- ~~**HIGH:** `RunWithReconnect` backoff resets after 60s healthy connection~~
- ~~**HIGH:** `MaxRetryBuffer` enforced via semaphore (10 concurrent cap)~~
- ~~**CRITICAL:** `DrainAndFlush` — synchronous final insert with fresh context~~

## Phase 2.6 — Architecture Refactor (2026-04-06)

- ~~Thin `cmd/`: `main.go` is ~15 lines, calls `commands.Execute()`~~
- ~~All command logic moved to `internal/commands/` (root, worker, batcher, refresh, version)~~
- ~~Global `var cfg` eliminated — config passed via `context.WithValue` from `PersistentPreRunE`~~
- ~~NATS wrappers deleted (premature `Publisher`/`Subscriber` interfaces) → two free functions~~
- ~~ClickHouse `Client` wrapper deleted (exposed inner via `Conn()`) → `Connect()` free function~~
- ~~`InsertBatch` changed from method to free function taking `*ch.Client`~~
- ~~Signal handling deduplicated — one `signal.NotifyCtx()` replaces 3 copy-pasted variants~~
- ~~`ComputeH3` returns `(uint64, error)` instead of silently returning 0~~
- ~~`redis.PublishBatch` returns `error` instead of swallowing pipeline failures~~
- ~~Missing packages implemented: `internal/worker` (interface), `internal/worker/adsb` (stub), `internal/batcher`~~

## Phase 2.7 — Live Broadcast Pipeline (2026-04-11)

- ~~FukanEvent extended with `Callsign`, `Origin`, `Category`, `VerticalRate` fields for richer client-side rendering~~
- ~~Migration `000003_add_telemetry_fields`: adds `callsign` / `origin` / `category` / `vertical_rate` columns to `telemetry_raw` and matching `AggregateFunction(argMax, ...)` states to `telemetry_latest`; recreates `telemetry_latest_mv` / `telemetry_latest_flat`~~
- ~~ADS-B parser populates the new fields from OpenSky `/states/all`~~
- ~~Redis publisher rewritten to emit AnyCable broadcast envelopes (`{"stream":"telemetry:<h3_hex>","data":"<event_json>"}`) on the `__anycable__` channel~~
- ~~Multi-resolution H3 fan-out (res 2–7) so the frontend can subscribe at any zoom band without server-side child expansion; cell ids use h3-go `Cell.String()` to match h3-js~~
- ~~Batcher grows a dedicated broadcast goroutine (`broadcastLoop`) fed by a bounded 50k-event channel, decoupled from the ClickHouse flush~~
- ~~Broadcast dual-threshold flush: 500 events or 50 ms, single in-flight Redis pipeline `Exec` with 5 s timeout~~
- ~~`DrainAndFlush` sequence updated: CH flush → CH retry drain → close `broadcastCh` → await `broadcastDone`~~

---

## Phase 2.8 — Schema Denormalization (2026-04-12)

- ~~Migration `000004_denormalize_metadata`: replaces `metadata` JSON blob with 12 typed columns on `telemetry_raw` (`squawk`, `nav_status`, `imo_number`, `ship_type`, `destination`, `draught`, `dim_a/b/c/d`, `eta`, `rate_of_turn`)~~
- ~~`metadata` column dropped — ClickHouse columnar storage makes sparse typed columns free; `JSONExtract()` on a String column is expensive at scale~~
- ~~7 new `argMax` state columns on `telemetry_latest`; `telemetry_latest_mv` / `telemetry_latest_flat` recreated with all new fields~~
- ~~`FukanEvent` struct: `Metadata string` replaced with typed Go fields (`Squawk`, `NavStatus`, `IMONumber`, `ShipType`, `Destination`, `Draught`, `DimA/B/C/D`, `ETA`, `RateOfTurn`)~~
- ~~`InsertBatch` updated: 12 new typed columnar encoders replace single `colMeta`~~
- ~~ADS-B parser: squawk set directly on `event.Squawk` instead of `json.Marshal` to metadata blob~~
- ~~ADS-B parser tests updated to assert `Squawk` field~~

## Phase 3 — AIS Feed (Vessels) (2026-04-12)

- ~~AIS WebSocket worker (`internal/worker/ais/worker.go`): persistent connection to `wss://stream.aisstream.io/v0/stream` via `github.com/coder/websocket`, global bounding box, auto-reconnect via `RunWithReconnect`~~
- ~~Parser (`internal/worker/ais/parser.go`): handles `PositionReport`, `StandardClassBPositionReport`, and `ShipStaticData` message types~~
- ~~Position reports: MMSI as AssetID, Sog as knots, TrueHeading with 511→Cog fallback, NavigationalStatus mapped to string (0-15), RateOfTurn~~
- ~~Ship static data: IMO number, ship type code mapped to category (cargo, tanker, passenger, etc.), destination, draught, dimensions (A/B/C/D), ETA~~
- ~~API key via `integrations.ais[].api_key` in config YAML~~
- ~~Fixture-based parser tests: 8 tests covering all message types, heading fallback, edge cases~~
- ~~Wired into `worker --type ais` + `batcher --type vessel` (batcher already generic)~~
- ~~`config.example.yaml` updated with AIS integration section~~

## Phase 3.5 — Satellite Schema (Migration 000005) (2026-04-12)

- ~~Migration `000005_add_satellite_fields`: adds `orbit_regime`, `confidence`, `tle_epoch`, `inclination`, `period_minutes`, `apogee_km`, `perigee_km` columns to `telemetry_raw`~~
- ~~7 new `argMax` state columns on `telemetry_latest`; MV + flat view recreated~~
- ~~`satellite_meta` reference table (ReplacingMergeTree, keyed by norad_cat_id) for GCAT enrichment data~~
- ~~`FukanEvent` struct extended with satellite fields; `InsertBatch` updated with new typed columns~~

## Phase 4 — Satellite Feed (TLE Orbits) (2026-04-12)

- ~~Three data sources: CelesTrak GP JSON (~15k active), McCants/Molczan classified TLEs (~500), GCAT satellite catalog (metadata enrichment)~~
- ~~SGP4 propagation via `github.com/akhenakh/sgp4` (Apache 2.0, OMM JSON support, `FindPositionAtTime()` + `ToGeodetic()`)~~
- ~~Single `worker --type tle` manages fetching (2h CelesTrak, 6h classified) + continuous propagation~~
- ~~Regime-aware propagation intervals: LEO 30s, MEO 60s, GEO 120s, HEO 30s (~455 events/sec total)~~
- ~~Orbit regime classification from semi-major axis (Kepler's third law from mean motion)~~
- ~~Confidence tagging: `official` (CelesTrak), `community_derived` (classified TLEs), `stale` (epoch > 14d LEO / 30d MEO+GEO)~~
- ~~`refresh --target satellites` loads GCAT `satcat.tsv` into `satellite_meta` table (TSV parser, 50k batch inserts)~~
- ~~12 tests: regime classification (LEO/MEO/GEO/HEO), orbital params, fetcher (HTTP mock + rate limit), TLE batch parsing, SGP4 propagation (ISS position/altitude), staleness detection, classified source tagging~~
- ~~Wired into `worker --type tle` + `batcher --type satellite`; `config.example.yaml` updated~~
- ~~Maneuver detection: compares mean motion (>0.01 rev/day delta) and inclination (>0.1° delta) between TLE fetch cycles; tags `sat_status = "maneuvering"`~~
- ~~Decay detection: checks perigee < 150 km or BSTAR > 0.01 at propagation time; tags `sat_status = "decaying"`~~
- ~~Migration `000006_add_sat_status`: adds `sat_status` LowCardinality(String) column to `telemetry_raw` + argMax state to `telemetry_latest`; recreates MV + flat view~~
- ~~5 new tests: maneuver detection (no-change, period shift, plane change), decay detection (healthy, low perigee), propagateOne with decaying/maneuvering status~~

## Phase 5 — BGP Feed (Internet Routing) (2026-04-16)

Design diverged from the original plan. BGPStream + MaxMind were both dropped in favor of RIPE RIS Live (WebSocket, no API key) + an embedded ASN seed table with background RIPEstat enrichment. More importantly, BGP events turned out to be fundamentally different from moving-asset telemetry: each RIS Live event is a one-time happening (announcement/withdrawal/hijack/leak), not a position update. Forcing them through `FukanEvent` + `telemetry_raw` + `telemetry_latest` exploded the `argMax`-keyed latest table to ~800k rows and saturated the Redis broadcast path. Phase 5 therefore ships a **split pipeline**: BGP owns its own event struct, NATS subject, ClickHouse table, and Redis stream prefix.

- ~~`internal/worker/bgp` — RIS Live WebSocket consumer (`worker.go`) via `coder/websocket`, subscribes to `UPDATE` messages, reconnect via `RunWithReconnect`~~
- ~~`parser.go` — flattens AS_SET elements, detects leaks (Tier-1 downstream of non-Tier-1) and hijacks (origin change on known prefix), emits `BgpEvent` per prefix per category~~
- ~~`state.go` — bounded (~500k entries, LRU-pruned by lastSeen) prefix → origin-AS map for hijack detection and withdrawal filtering~~
- ~~`geo.go` + `tables.go` — embedded ASN → (lat, lon) seed table with background RIPEstat fetches for unknown ASNs; `resolvePathCoords` skips unresolved hops rather than emitting null-island coordinates~~
- ~~12 parser tests covering announcement / hijack / leak / withdrawal / AS_SET / unresolved-ASN paths~~
- ~~`model.BgpEvent` struct (`internal/model/bgp_event.go`) — parallel to `FukanEvent`, fields: `ts`, `id`, `cat`, `prefix`, `origin_as`, `as_path`, `path_coords`, `collector`, `lat`, `lon`, `h3`, `src`~~
- ~~`ValidateBgp()` — null-island, out-of-range, empty id / category / source checks~~
- ~~Dedicated NATS subject `fukan.bgp.events` (not `fukan.telemetry.bgp`) with queue group `batcher-bgp`~~
- ~~Migration `000007_create_bgp_events` — append-only MergeTree, ORDER BY `(category, h3_cell, event_time)`, PARTITION BY hour, 24 h TTL. Replaced staged migrations 000007/000008 (never committed) that had put BGP columns on `telemetry_raw` + argMax states on `telemetry_latest`~~
- ~~`clickhouse.InsertBGPBatch` — columnar insert into `bgp_events`; BGP fields removed from `InsertBatch` (telemetry path)~~
- ~~`batcher.BGPBatcher` — parallel struct to `Batcher`, same dual-threshold flush (10k / 2s CH, 500 / 50 ms broadcast), calls `InsertBGPBatch` + `PublishBGPBatch`~~
- ~~`redis.PublishBGPBatch` — stream prefix `bgp:<h3>`, fan-out at **res 3 only** (6× reduction in Redis PUBLISH calls per event vs the telemetry res 2–7 set). BGP event coordinates are imprecise enough that zoom-band-precise subscriptions would be misleading; frontend always subscribes at res 3 via `cellToParent(cell, 3)`~~
- ~~`commands/batcher.go` switches on `--type bgp` to instantiate `BGPBatcher` against `fukan.bgp.events`~~
- ~~Config: `integrations.bgp[]` in `config.example.yaml`, single RIS Live entry (no API key required)~~

## Phase 5.1 — GeoIP Enrichment (✅ 2026-04-17)

Country-centroid precision (Geo seed + RIPEstat) was too coarse: all events from a given origin AS plotted to a single point, and fresh ASNs were dropped on first sight. Replaced with an in-process MaxMind GeoLite2-City MMDB via `oschwald/geoip2-golang`:

- ~~`internal/worker/bgp/city.go` — `CityLookup` wrapper: `NewCityLookup(path)` opens the MMDB (fail-fast on missing file), `LookupPrefix(prefix)` parses CIDR via `net/netip`, extracts the network address, and returns `(lat, lon, ok)` from `Reader.City(ip)`. Guards against null-island zero coords.~~
- ~~Two-tier resolution in parser: MMDB on the prefix → existing `Geo.Lookup(originAS)` ASN-centroid fallback → drop. Applied symmetrically to announcements (path-based fallback) and withdrawals (peer-ASN fallback).~~
- ~~`path_coords` stays on `Geo` only — MMDB has no ASN→coord mapping; per-hop coarse coords are acceptable for the path polyline.~~
- ~~Config: `geoip.city_db` string in `config.yaml`; no env vars. Delivery is the operator's concern (local path in dev, ConfigMap mount in prod). Licensing + redistribution terms keep the MMDB out of the repo.~~
- ~~BGP worker opens the MMDB once in `commands/worker.go` for the `--type bgp` branch; `defer Close()` on command exit. Other worker types are untouched.~~
- ~~Tests: `parser_test.go` gains a `fakeCity` fixture and three precedence cases (MMDB wins / MMDB miss → Geo fallback / both miss → drop / withdrawal MMDB precedence). `city_test.go` is env-gated on `FUKAN_TEST_GEOIP_DB` so CI stays green without shipping a 70 MB blob.~~

## Phase 5.2 — BGP Org Enrichment (✅ 2026-04-18)

Detail panel showed `AS15169` as a raw number — users had to run a second lookup to know whose prefix they were looking at. Added prefix-holder org enrichment via MaxMind's GeoLite2-ASN MMDB (separate free file, ~8.5 MB, same `oschwald/geoip2-golang` library):

- ~~`internal/worker/bgp/asn.go` — `ASNLookup` wrapper parallel to `CityLookup`. `LookupPrefix(prefix)` parses CIDR → netIP → `Reader.ASN(ip)` → `(asn, org, ok)`. Zero ASN or empty org returns `ok=false`.~~
- ~~Two new fields on `BgpEvent`: `PrefixAS` (uint32) and `PrefixOrg` (string). Stamped at parse time on both announcements and withdrawals. A miss leaves zero values; never drops the event.~~
- ~~Key UX insight: MaxMind is IP-keyed, so on hijacks it returns the *legitimate holder's* AS+org while `OriginAS` (from RIS Live) holds the announcer. The divergence is the OSINT signal — surfaced in `BgpDetailPanel.tsx` with a dedicated red warning strip when `prefix_as !== origin_as` on hijack/leak events.~~
- ~~Migration 000008 adds `prefix_as UInt32` + `prefix_org LowCardinality(String)` to `bgp_events`. ADD COLUMN is metadata-only in MergeTree; 24 h TTL cycles legacy rows out naturally.~~
- ~~Config: `geoip.asn_db` alongside `geoip.city_db`. Worker opens both MMDBs at startup, fails fast on either missing. `defer Close()` in command cleanup.~~
- ~~Schema-drift trio touched: `model.BgpEvent` (Go) + `Bgp::ViewportQuery` SELECT aliases (Rails) + `BgpEvent` TS interface. Schema-drift hazard comments extended on all three. `Bgp::CachedViewportQuery` cache-key prefix bumped to `bgp:v2:` so 3-second caches can't serve stale-shape payloads.~~
- ~~Tests: `parser_test.go` fakeASN fixture with four cases (stamping hit / miss leaves empty / hijack divergence / withdrawal stamping). `asn_test.go` env-gated on `FUKAN_TEST_GEOIP_ASN_DB`.~~

## Phase 6 — News Feed (GDELT)

- [ ] GDELT poller: 15-minute CSV/JSON fetch (`internal/worker/news`)
- [ ] Parser: GDELT events → FukanEvent (EventID, ActionGeo coords, headline + URL + tone metadata)
- [ ] No full article text — headline + source URL + sentiment only
- [ ] Tests

## Phase 7 — Deduplication (v2)

- [ ] Time-windowed hashmap dedup in batcher (`internal/batcher/dedup.go`)
- [ ] Key: `fnv64(asset_id, timestamp)`, 60s window
- [ ] Sweep goroutine every 30s to evict expired entries
- [ ] No bloom filters — zero false positives required
- [ ] Memory budget: ~150MB at 100k msg/s with 60s window
- [ ] Enable when multiple providers per feed category are added

## Phase 8 — Hardening

- [ ] Integration tests with testcontainers (NATS + ClickHouse end-to-end)
- [ ] Graceful shutdown integration test: publish → SIGTERM → verify all rows in ClickHouse
- [ ] Metrics: Prometheus counters for events_received, events_flushed, flush_errors, retry_count, buffer_size
- [ ] Health endpoint for k8s liveness/readiness probes
- [ ] Structured logging audit (ensure consistent slog attributes across all packages)
- [ ] Make `MaxBatchSize` and `MaxFlushLatency` configurable via env vars (`BATCH_SIZE`, `BATCH_FLUSH_INTERVAL`)

## Phase 9 — Deployment

- [ ] Multi-stage Dockerfile (`deploy/docker/Dockerfile`)
- [ ] Kubernetes manifests: worker Deployment, batcher Deployment, NATS StatefulSet, ClickHouse StatefulSet
- [ ] ClickHouse pinned to AX42 node (hostPath PV on NVMe), compute pods on CPX31 nodes
- [ ] Tiered ClickHouse storage: hot (NVMe/LZ4) → cold (S3/ZSTD) at 2 days
- [ ] CI pipeline: `go test`, `go vet`, `golangci-lint`, container build + push
