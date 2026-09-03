-- 061: lookup indexes for tour_leg_telemetry (sp-73835 follow-up, 2026-09-03).
-- The margin-per-unit dashboard panels and the ledger analyses look up "the hull's most recent buy of
-- this good before this sell"; without an index that is a per-row scan of the hull's whole history
-- (54 s for a 6-hour window on 422k rows). With these two partial indexes the same query runs in
-- ~70 ms (6h) / ~170 ms (24h). CONCURRENTLY: no write lock on the live table. Applied live 22:00Z.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_tlt_hull_good_buy_time
    ON tour_leg_telemetry (player_id, ship_symbol, good, realized_at DESC)
    WHERE is_buy AND realized_units > 0;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_tlt_player_realized
    ON tour_leg_telemetry (player_id, realized_at)
    WHERE realized_units > 0;
