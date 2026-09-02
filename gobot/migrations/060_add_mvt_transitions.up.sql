-- MVT trade loop: one row per hull state transition (spec §5 telemetry line).
CREATE TABLE IF NOT EXISTS mvt_transitions (
    id               BIGSERIAL PRIMARY KEY,
    player_id        BIGINT           NOT NULL,
    hull             VARCHAR(50)      NOT NULL,
    from_state       VARCHAR(8)       NOT NULL,
    to_state         VARCHAR(8)       NOT NULL,
    system           VARCHAR(20)      NOT NULL,
    yield_here       DOUBLE PRECISION NOT NULL DEFAULT 0,
    best_alternative DOUBLE PRECISION NOT NULL DEFAULT 0,
    travel_cost      DOUBLE PRECISION NOT NULL DEFAULT 0,
    reason           VARCHAR(64)      NOT NULL,
    at               TIMESTAMPTZ      NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_mvt_transitions_player_at ON mvt_transitions (player_id, at DESC);
COMMENT ON TABLE mvt_transitions IS 'MVT trade loop telemetry: hull, from_state, to_state, system, yield_here, best_alternative, travel_cost, reason — read by the replay and dashboards';
