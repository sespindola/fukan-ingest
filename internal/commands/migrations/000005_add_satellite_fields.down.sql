DROP TABLE IF EXISTS satellite_meta;

DROP VIEW IF EXISTS telemetry_latest_flat;
DROP VIEW IF EXISTS telemetry_latest_mv;

ALTER TABLE telemetry_latest
    DROP COLUMN IF EXISTS orbit_regime_state,
    DROP COLUMN IF EXISTS confidence_state,
    DROP COLUMN IF EXISTS tle_epoch_state,
    DROP COLUMN IF EXISTS inclination_state,
    DROP COLUMN IF EXISTS period_minutes_state,
    DROP COLUMN IF EXISTS apogee_km_state,
    DROP COLUMN IF EXISTS perigee_km_state;

ALTER TABLE telemetry_raw
    DROP COLUMN IF EXISTS orbit_regime,
    DROP COLUMN IF EXISTS confidence,
    DROP COLUMN IF EXISTS tle_epoch,
    DROP COLUMN IF EXISTS inclination,
    DROP COLUMN IF EXISTS period_minutes,
    DROP COLUMN IF EXISTS apogee_km,
    DROP COLUMN IF EXISTS perigee_km;

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
    argMaxState(source, event_time)        AS source_state,
    argMaxState(squawk, event_time)        AS squawk_state,
    argMaxState(nav_status, event_time)    AS nav_status_state,
    argMaxState(imo_number, event_time)    AS imo_number_state,
    argMaxState(ship_type, event_time)     AS ship_type_state,
    argMaxState(destination, event_time)   AS destination_state,
    argMaxState(draught, event_time)       AS draught_state,
    argMaxState(rate_of_turn, event_time)  AS rate_of_turn_state
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
    argMaxMerge(source_state)        AS source,
    argMaxMerge(squawk_state)        AS squawk,
    argMaxMerge(nav_status_state)    AS nav_status,
    argMaxMerge(imo_number_state)    AS imo_number,
    argMaxMerge(ship_type_state)     AS ship_type,
    argMaxMerge(destination_state)   AS destination,
    argMaxMerge(draught_state)       AS draught,
    argMaxMerge(rate_of_turn_state)  AS rate_of_turn
FROM telemetry_latest
GROUP BY asset_type, asset_id;
