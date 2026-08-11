-- Era-scope the system graph cache. Keyed by system_symbol alone, a graph cached in a CLOSED
-- era was served as current topology after a universe reset: gateFromGraph resolved a gate
-- waypoint that no longer exists, routing navigated to it, and the server answered 4201
-- forever while the placement retried and the hull stayed counted against the probe cap.

ALTER TABLE system_graphs ADD COLUMN IF NOT EXISTS era_id INTEGER;

-- Existing rows predate the stamp. NULL reads as the pre-backfill transition window, the same
-- convention waypoints/gate_edges use, so a row nobody has rewritten stays readable.
CREATE INDEX IF NOT EXISTS idx_system_graphs_era ON system_graphs (era_id);
