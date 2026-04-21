CREATE TABLE IF NOT EXISTS bgp_events (
    event_time    DateTime64(3)   CODEC(DoubleDelta, LZ4),
    event_id      String          CODEC(LZ4),
    category      LowCardinality(String),
    prefix        String          CODEC(LZ4),
    origin_as     UInt32          CODEC(LZ4),
    as_path       Array(UInt32)   CODEC(LZ4),
    path_coords   Array(Int32)    CODEC(LZ4),
    collector     LowCardinality(String),
    lat           Int32           CODEC(DoubleDelta, LZ4),
    lon           Int32           CODEC(DoubleDelta, LZ4),
    h3_cell       UInt64          CODEC(LZ4),
    source        LowCardinality(String)
) ENGINE = MergeTree()
ORDER BY (category, h3_cell, event_time)
PARTITION BY toStartOfHour(event_time)
TTL event_time + INTERVAL 24 HOUR
SETTINGS index_granularity = 8192;
