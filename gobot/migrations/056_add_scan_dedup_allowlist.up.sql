-- sp-7q61f: the scan-dedup A/B test's live, operator-settable ship-symbol
-- allowlist. Default empty: a ship not in this table always takes the
-- unconditional fresh live scan (today's behavior, unchanged). The operator
-- populates it live via `spacetraders trade-route scan-dedup add/remove` — no
-- daemon restart, no redeploy (PLAYBOOK #10, RULINGS #19).
--
-- Deliberately its OWN table rather than a per-ship tag column: it is scoped to
-- an explicit allowlist (not a dedicated_fleet-style claim filter), so arming a
-- ship never pulls it out of trade_fleet_coordinator's normal claim pool.
--
-- GORM AutoMigrate at daemon boot also creates this table (models are the
-- source of truth, AutoMigrate is additive), but boot AutoMigrate is
-- best-effort and NON-FATAL, so this migration is the durable baseline a write
-- can depend on without risking SQLSTATE 42P01 (undefined_table).
--
-- No players foreign key, mirroring the other operational-state rows (spend
-- reservations, scout posts): player_id is a plain scoped column.

CREATE TABLE IF NOT EXISTS scan_dedup_allowlist (
    ship_symbol VARCHAR(50) NOT NULL,
    player_id   BIGINT      NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (player_id, ship_symbol)
);

COMMENT ON TABLE scan_dedup_allowlist IS 'Live, operator-settable ship-symbol allowlist for the sp-7q61f scan-dedup A/B test — default empty, no restart to change';
