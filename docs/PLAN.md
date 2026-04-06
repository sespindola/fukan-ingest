# Fukan Ingest — Implementation Plan

> Last updated: 2026-03-12

---

## Phase 0 — Foundation

- ~~Go module, project structure, `go.mod`~~
- ~~`FukanEvent` canonical struct with JSON tags (`internal/model/event.go`)~~
- ~~`AssetType` enum: aircraft, vessel, satellite, bgp_node, news~~
- ~~Coordinate helpers: `ScaleLat`, `ScaleLon`, `ComputeH3` (`internal/coord`)~~
- ~~Validation: asset_id, coordinates, timestamp (`internal/model/validate.go`)~~
- ~~Docker Compose: NATS, ClickHouse, Redis~~
- ~~ClickHouse DDL: `telemetry_raw`, `telemetry_latest`, `telemetry_h3_agg` (`scripts/clickhouse-init.sql`)~~
- ~~Cobra CLI: unified `cmd/fukan-ingest` binary with `worker`, `batcher`, `version` subcommands~~
- ~~Viper config: YAML file + env-var overrides + typed structs (`internal/config`)~~
- ~~Multi-integration support: `integrations` map in config allows multiple providers per feed type~~
- ~~Legacy env-var fallback for backward compatibility~~

## Phase 1 — Core Pipeline

- ~~NATS publisher/subscriber wrappers (`internal/nats`)~~
- ~~ClickHouse native client + columnar batch insert (`internal/clickhouse`)~~
- ~~Redis pub/sub publisher grouped by H3 cell (`internal/redis`)~~
- ~~Batcher: dual-threshold flush (10k events / 2s), exponential retry (`internal/batcher`)~~
- ~~Worker interface + `RunWithReconnect` helper (`internal/worker`)~~

## Phase 2 — First Feed (ADS-B)

- ~~ADS-B HTTP polling worker (`internal/worker/adsb`)~~
- ~~Parser: raw JSON → FukanEvent (hex/icao, alt feet→m, squawk metadata)~~
- ~~Fixture-based parser tests (`testdata/feed_sample.json`)~~
- ~~`cmd/fukan-ingest/worker.go` — Cobra `worker --type adsb` subcommand (replaces `cmd/worker`)~~
- ~~`cmd/fukan-ingest/batcher.go` — Cobra `batcher --type aircraft` subcommand (replaces `cmd/batcher`)~~

## Phase 2.5 — Code Review Fixes

- ~~**CRITICAL:** `DrainAndFlush` drops final batch — rewritten to synchronous final insert with fresh context~~
- ~~**HIGH:** `RunWithReconnect` backoff never resets after long-lived connections — reset if connection lasted > maxBackoff~~
- ~~**HIGH:** `MaxRetryBuffer` declared but never enforced — added `inflight` atomic counter, cap at 10 concurrent retry goroutines~~
- ~~**MEDIUM:** Timer not stopped in `DrainAndFlush` — now explicitly stopped~~
- ~~**MEDIUM:** Validation missing `AssetType` and `Source` checks — added with tests~~

---

## Phase 3 — AIS Feed (Vessels)

- [ ] AIS WebSocket worker (`internal/worker/ais`)
- [ ] Parser: AISStream.io JSON → FukanEvent (MMSI, sog/10→knots, cog/10→degrees, nav_status metadata)
- [ ] Persistent WebSocket with auto-reconnect via `RunWithReconnect`
- [ ] Env var: `AISSTREAM_API_KEY`
- [ ] Fixture-based parser tests
- [ ] Wire into `cmd/worker` switch on `WORKER_TYPE=ais`

## Phase 4 — Satellite Feed (TLE Orbits)

- [ ] TLE fetcher: daily HTTP pull from CelesTrak — primary catalog (`internal/worker/tle/fetcher.go`)
- [ ] planet4589.org fetcher: daily HTTP pull of JSR Satellite Catalog for classified/military objects not in CelesTrak (`internal/worker/tle/planet4589.go`)
- [ ] JSR catalog parser: handle planet4589.org's TSV format (separate from standard TLE parser)
- [ ] SGP4 propagator with regime-aware intervals (`internal/worker/tle/propagator.go`):
  - LEO (< 2,000 km): every 10s
  - MEO (2,000–35,786 km): every 30s
  - GEO (~35,786 km): every 60–300s
  - HEO (elliptical): every 10s
- [ ] Orbit regime classification: derive from orbital period/altitude, enrich metadata `{"regime":"leo"}`
- [ ] Confidence tagging: planet4589.org objects get `{"confidence":"community_derived"}`
- [ ] Maneuver detection: compare daily mean motion / inclination deltas → metadata `{"status":"maneuvering"}`
- [ ] Decay detection: BSTAR drag + perigee < 150km → metadata `{"status":"decaying"}`
- [ ] `cmd/propagator/main.go` entrypoint
- [ ] Env vars: `CELESTRAK_URL`, `PLANET4589_URL`
- [ ] Tests with known TLE fixtures + planet4589.org format fixtures

## Phase 5 — BGP Feed (Internet Routing)

- [ ] BGPStream consumer (`internal/worker/bgp`)
- [ ] GeoIP mapping: ASN → coordinates via MaxMind GeoLite2-ASN (`internal/worker/bgp/geoip.go`)
- [ ] Parser: BGP events → FukanEvent (ASN, hijack/leak detection, prefix metadata)
- [ ] Env vars: `BGPSTREAM_PROJECT`, `MAXMIND_DB_PATH`
- [ ] Tests

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
