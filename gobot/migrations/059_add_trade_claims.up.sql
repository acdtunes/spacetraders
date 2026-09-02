-- MVT trade loop: one durable claim per hull (spec docs/superpowers/specs/2026-09-02-mvt-trade-loop-design.md §3).
CREATE TABLE IF NOT EXISTS trade_claims (
    player_id   BIGINT      NOT NULL,
    hull        VARCHAR(50) NOT NULL,
    system      VARCHAR(20) NOT NULL,
    claimed_at  TIMESTAMPTZ NOT NULL,
    arrived_at  TIMESTAMPTZ,
    era_id      BIGINT,
    PRIMARY KEY (player_id, hull)
);
CREATE INDEX IF NOT EXISTS idx_trade_claims_in_transit
    ON trade_claims (player_id, system)
    WHERE arrived_at IS NULL;
COMMENT ON TABLE trade_claims IS 'MVT trade loop: the system each intra-system hull is working (arrived_at set) or travelling to (arrived_at NULL). A ranking penalty, never a lock.';
