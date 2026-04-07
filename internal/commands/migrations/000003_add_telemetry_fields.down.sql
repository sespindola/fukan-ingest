DROP VIEW IF EXISTS telemetry_latest_flat;
DROP VIEW IF EXISTS telemetry_latest_mv;

ALTER TABLE telemetry_latest
    DROP COLUMN IF EXISTS callsign_state,
    DROP COLUMN IF EXISTS origin_state,
    DROP COLUMN IF EXISTS category_state,
    DROP COLUMN IF EXISTS vertical_rate_state;

ALTER TABLE telemetry_raw
    DROP COLUMN IF EXISTS callsign,
    DROP COLUMN IF EXISTS origin,
    DROP COLUMN IF EXISTS category,
    DROP COLUMN IF EXISTS vertical_rate;

CREATE MATERIALIZED VIEW IF NOT EXISTS telemetry_latest_mv
TO telemetry_latest AS
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
FROM telemetry_raw
GROUP BY asset_type, asset_id;

CREATE VIEW IF NOT EXISTS telemetry_latest_flat AS
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
FROM telemetry_latest
GROUP BY asset_type, asset_id;
