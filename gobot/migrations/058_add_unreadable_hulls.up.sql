-- Open episodes of a hull the SpaceTraders API will not serialise, and the bounds the
-- automatic repair is held to.
--
-- The composite GET /my/ships/<symbol> returns a server error while every sub-resource
-- still answers; that narrows the corruption to one field the parts do not cover, and fuel
-- is the only such field a client can write. Writing it re-serialises the record.
--
-- The row exists so the ATTEMPT BOUND survives a daemon restart (RULINGS #2): the repair
-- spends credits, so a bound that resets on restart would let a fix that provably does not
-- apply run forever. escalated_at set means the repair has given up and an operator is
-- needed; the row is deleted outright the moment the hull reads again.
--
-- GORM AutoMigrate at daemon boot also creates this table, but boot AutoMigrate is
-- best-effort and non-fatal, so this migration is the durable baseline a write can depend
-- on without risking SQLSTATE 42P01 (undefined_table).
--
-- No players foreign key, mirroring the other operational-state rows: player_id is a plain
-- scoped column.

CREATE TABLE IF NOT EXISTS unreadable_hulls (
    player_id       BIGINT      NOT NULL,
    ship_symbol     VARCHAR(50) NOT NULL,
    first_seen_at   TIMESTAMPTZ NOT NULL,
    last_seen_at    TIMESTAMPTZ NOT NULL,
    attempts        INTEGER     NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL,
    escalated_at    TIMESTAMPTZ,
    last_outcome    VARCHAR(64),
    last_reason     TEXT,
    PRIMARY KEY (player_id, ship_symbol)
);

-- The sweep's only query: the open, non-escalated episodes whose backoff has expired.
CREATE INDEX IF NOT EXISTS idx_unreadable_hulls_due
    ON unreadable_hulls (player_id, next_attempt_at)
    WHERE escalated_at IS NULL;

COMMENT ON TABLE unreadable_hulls IS 'Hulls the API will not serialise, with the automatic repair''s attempt bound and backoff — deleted when the hull reads again';
