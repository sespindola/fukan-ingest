ALTER TABLE bgp_events ADD COLUMN prefix_as UInt32 CODEC(LZ4) AFTER origin_as;
ALTER TABLE bgp_events ADD COLUMN prefix_org LowCardinality(String) AFTER prefix_as;
