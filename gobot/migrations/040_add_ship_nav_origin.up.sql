-- sp-vp9k: persist nav route origin + departure for IN_TRANSIT ships.
--
-- ship_dto.go historically dropped nav.route.origin and nav.route.departureTime,
-- keeping only waypointSymbol + arrival, so no DB consumer could compute exact
-- transit progress (the visualizer Contract Ops tab fell back to client-side
-- origin memory: approximate departure +/- poll interval, cold-start blind).
-- These additive columns carry where an IN_TRANSIT ship departed from (waypoint
-- symbol + coordinates) and when, populated from the API nav.route on every ship
-- sync. Empty/zero and NULL respectively for a ship that is not in transit.
--
-- Additive, no constraints: GORM AutoMigrate at daemon boot also adds these
-- columns (models are the source of truth, AutoMigrate is additive), so a daemon
-- redeploy is sufficient to land them on the live DB. But per the column-drift
-- gate's rationale (boot AutoMigrate is best-effort and NON-FATAL -- it "logs
-- loudly and continues on the existing schema"), this migration is the durable,
-- auditable record so the columns never depend on AutoMigrate having succeeded.
-- Mirrors the ships location_symbol/x/y + arrival_time column shapes added in
-- migration 026. Idempotent via IF NOT EXISTS -- safe to re-run and safe to apply
-- whether or not AutoMigrate already added the columns.
ALTER TABLE ships ADD COLUMN IF NOT EXISTS origin_symbol VARCHAR(64);
ALTER TABLE ships ADD COLUMN IF NOT EXISTS origin_x DOUBLE PRECISION DEFAULT 0;
ALTER TABLE ships ADD COLUMN IF NOT EXISTS origin_y DOUBLE PRECISION DEFAULT 0;
ALTER TABLE ships ADD COLUMN IF NOT EXISTS departure_time TIMESTAMPTZ NULL;

-- Partial index for the transit-progress query pattern (all ships currently in a
-- transit that has a known departure), mirroring migration 026's arrival_time
-- partial index. Cheap: only IN_TRANSIT rows carry a non-NULL departure_time.
CREATE INDEX IF NOT EXISTS idx_ships_departure_time ON ships(departure_time)
    WHERE departure_time IS NOT NULL;

COMMENT ON COLUMN ships.origin_symbol IS 'Waypoint an IN_TRANSIT ship departed from; empty when not in transit (sp-vp9k)';
COMMENT ON COLUMN ships.departure_time IS 'When the current transit departed; NULL when not in transit (sp-vp9k)';
