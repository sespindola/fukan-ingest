CREATE DATABASE IF NOT EXISTS fukan;

-- Raw telemetry (validation phase: local NVMe, LZ4, 24h TTL)
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
TTL timestamp + INTERVAL 24 HOUR
SETTINGS index_granularity = 8192;

-- Latest position per asset
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
