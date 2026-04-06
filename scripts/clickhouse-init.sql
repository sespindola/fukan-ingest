CREATE DATABASE IF NOT EXISTS fukan;

-- Raw telemetry: ordered for viewport/H3/time reads, hourly partitions for clean 24h TTL drops
CREATE TABLE IF NOT EXISTS fukan.telemetry_raw (
    event_time    DateTime64(3)     CODEC(DoubleDelta, LZ4),
    asset_id      String            CODEC(LZ4),
    asset_type    LowCardinality(String),
    lat           Int32             CODEC(DoubleDelta, LZ4),
    lon           Int32             CODEC(DoubleDelta, LZ4),
    alt           Int32             CODEC(DoubleDelta, LZ4),
    speed         Float32           CODEC(Gorilla, LZ4),
    heading       Float32           CODEC(Gorilla, LZ4),
    h3_cell       UInt64            CODEC(LZ4),
    source        LowCardinality(String),
    metadata      String            CODEC(LZ4)
) ENGINE = MergeTree()
PARTITION BY toStartOfHour(event_time)
ORDER BY (asset_type, h3_cell, event_time, asset_id)
TTL event_time + INTERVAL 24 HOUR
SETTINGS
    index_granularity = 8192,
    ttl_only_drop_parts = 1;

-- Latest position per asset: correct-by-design via argMax aggregate states.
-- Never needs FINAL; no duplicate visibility window.
CREATE TABLE IF NOT EXISTS fukan.telemetry_latest (
    asset_type        LowCardinality(String),
    asset_id          String,
    ts_state          AggregateFunction(max, DateTime64(3)),
    lat_state         AggregateFunction(argMax, Int32, DateTime64(3)),
    lon_state         AggregateFunction(argMax, Int32, DateTime64(3)),
    alt_state         AggregateFunction(argMax, Int32, DateTime64(3)),
    speed_state       AggregateFunction(argMax, Float32, DateTime64(3)),
    heading_state     AggregateFunction(argMax, Float32, DateTime64(3)),
    h3_state          AggregateFunction(argMax, UInt64, DateTime64(3)),
    source_state      AggregateFunction(argMax, String, DateTime64(3))
) ENGINE = AggregatingMergeTree()
ORDER BY (asset_type, asset_id);

CREATE MATERIALIZED VIEW IF NOT EXISTS fukan.telemetry_latest_mv
TO fukan.telemetry_latest AS
SELECT
    asset_type,
    asset_id,
    maxState(event_time)                 AS ts_state,
    argMaxState(lat, event_time)         AS lat_state,
    argMaxState(lon, event_time)         AS lon_state,
    argMaxState(alt, event_time)         AS alt_state,
    argMaxState(speed, event_time)       AS speed_state,
    argMaxState(heading, event_time)     AS heading_state,
    argMaxState(h3_cell, event_time)     AS h3_state,
    argMaxState(source, event_time)      AS source_state
FROM fukan.telemetry_raw
GROUP BY asset_type, asset_id;

-- Flat readable view — use this for "where is asset X now" queries
CREATE VIEW IF NOT EXISTS fukan.telemetry_latest_flat AS
SELECT
    asset_type,
    asset_id,
    maxMerge(ts_state)           AS event_time,
    argMaxMerge(lat_state)       AS lat,
    argMaxMerge(lon_state)       AS lon,
    argMaxMerge(alt_state)       AS alt,
    argMaxMerge(speed_state)     AS speed,
    argMaxMerge(heading_state)   AS heading,
    argMaxMerge(h3_state)        AS h3_cell,
    argMaxMerge(source_state)    AS source
FROM fukan.telemetry_latest
GROUP BY asset_type, asset_id;

-- Aircraft metadata reference table (monthly full refresh from OpenSky)
-- ReplacingMergeTree deduplicates by icao24, keeping the row with latest updated_at.
-- No TRUNCATE needed before refresh — just insert and CH handles the rest.
CREATE TABLE IF NOT EXISTS fukan.aircraft_meta (
    icao24              String,
    registration        String       DEFAULT '',
    manufacturer_name   String       DEFAULT '',
    model               String       DEFAULT '',
    typecode            String       DEFAULT '',
    icao_aircraft_type  LowCardinality(String) DEFAULT '',
    operator            String       DEFAULT '',
    operator_callsign   String       DEFAULT '',
    operator_icao       LowCardinality(String) DEFAULT '',
    operator_iata       LowCardinality(String) DEFAULT '',
    owner               String       DEFAULT '',
    built               String       DEFAULT '',
    status              String       DEFAULT '',
    category_desc       String       DEFAULT '',
    image_url           String       DEFAULT '',
    image_attribution   String       DEFAULT '',
    updated_at          DateTime     DEFAULT now()
) ENGINE = ReplacingMergeTree(updated_at)
ORDER BY icao24;

-- 5-minute H3 density buckets for zoomed-out heatmaps
CREATE TABLE IF NOT EXISTS fukan.telemetry_h3_agg (
    time_bucket   DateTime,
    h3_cell       UInt64,
    asset_type    LowCardinality(String),
    cnt           UInt64
) ENGINE = SummingMergeTree()
ORDER BY (asset_type, h3_cell, time_bucket);

CREATE MATERIALIZED VIEW IF NOT EXISTS fukan.telemetry_h3_agg_mv
TO fukan.telemetry_h3_agg AS
SELECT
    toStartOfFiveMinutes(event_time) AS time_bucket,
    h3_cell,
    asset_type,
    count() AS cnt
FROM fukan.telemetry_raw
GROUP BY time_bucket, h3_cell, asset_type;
