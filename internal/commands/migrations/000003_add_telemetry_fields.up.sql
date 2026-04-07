ALTER TABLE telemetry_raw
    ADD COLUMN IF NOT EXISTS callsign      String        CODEC(LZ4)        AFTER asset_type,
    ADD COLUMN IF NOT EXISTS origin        LowCardinality(String)          AFTER callsign,
    ADD COLUMN IF NOT EXISTS category      LowCardinality(String)          AFTER origin,
    ADD COLUMN IF NOT EXISTS vertical_rate Float32       CODEC(Gorilla, LZ4) AFTER heading;

DROP VIEW IF EXISTS telemetry_latest_flat;
DROP VIEW IF EXISTS telemetry_latest_mv;

ALTER TABLE telemetry_latest
    ADD COLUMN IF NOT EXISTS callsign_state      AggregateFunction(argMax, String, DateTime64(3))              AFTER asset_id,
    ADD COLUMN IF NOT EXISTS origin_state         AggregateFunction(argMax, String, DateTime64(3))              AFTER callsign_state,
    ADD COLUMN IF NOT EXISTS category_state       AggregateFunction(argMax, String, DateTime64(3))              AFTER origin_state,
    ADD COLUMN IF NOT EXISTS vertical_rate_state  AggregateFunction(argMax, Float32, DateTime64(3))             AFTER heading_state;

CREATE MATERIALIZED VIEW IF NOT EXISTS telemetry_latest_mv
TO telemetry_latest AS
SELECT
    asset_type,
    asset_id,
    argMaxState(callsign, event_time)      AS callsign_state,
    argMaxState(origin, event_time)        AS origin_state,
    argMaxState(category, event_time)      AS category_state,
    maxState(event_time)                   AS ts_state,
    argMaxState(lat, event_time)           AS lat_state,
    argMaxState(lon, event_time)           AS lon_state,
    argMaxState(alt, event_time)           AS alt_state,
    argMaxState(speed, event_time)         AS speed_state,
    argMaxState(heading, event_time)       AS heading_state,
    argMaxState(vertical_rate, event_time) AS vertical_rate_state,
    argMaxState(h3_cell, event_time)       AS h3_state,
    argMaxState(source, event_time)        AS source_state
FROM telemetry_raw
GROUP BY asset_type, asset_id;

CREATE VIEW IF NOT EXISTS telemetry_latest_flat AS
SELECT
    asset_type,
    asset_id,
    argMaxMerge(callsign_state)      AS callsign,
    argMaxMerge(origin_state)        AS origin,
    argMaxMerge(category_state)      AS category,
    maxMerge(ts_state)               AS event_time,
    argMaxMerge(lat_state)           AS lat,
    argMaxMerge(lon_state)           AS lon,
    argMaxMerge(alt_state)           AS alt,
    argMaxMerge(speed_state)         AS speed,
    argMaxMerge(heading_state)       AS heading,
    argMaxMerge(vertical_rate_state) AS vertical_rate,
    argMaxMerge(h3_state)            AS h3_cell,
    argMaxMerge(source_state)        AS source
FROM telemetry_latest
GROUP BY asset_type, asset_id;
