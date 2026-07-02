-- Strategic-event outbox for the autonomous captain (spec: 2026-07-02-autonomous-captain-design.md)
CREATE TABLE IF NOT EXISTS captain_events (
    id           BIGSERIAL PRIMARY KEY,
    player_id    INTEGER NOT NULL REFERENCES players(id) ON UPDATE CASCADE ON DELETE CASCADE,
    type         VARCHAR(50) NOT NULL,
    ship         VARCHAR(100) NOT NULL DEFAULT '',
    payload      JSONB NOT NULL DEFAULT '{}',
    created_at   TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now(),
    processed_at TIMESTAMP WITH TIME ZONE
);
CREATE INDEX IF NOT EXISTS idx_captain_events_unprocessed
    ON captain_events(player_id, created_at) WHERE processed_at IS NULL;
COMMENT ON TABLE captain_events IS 'Outbox of strategic events consumed by the captain supervisor';
