CREATE TABLE IF NOT EXISTS eras (
    era_id              SERIAL PRIMARY KEY,
    name                TEXT UNIQUE NOT NULL,
    agent_symbol        TEXT NOT NULL,
    faction             TEXT,
    player_id           INTEGER NOT NULL,
    universe_reset_date DATE,
    registered_at       TIMESTAMP WITH TIME ZONE,
    closed_at           TIMESTAMP WITH TIME ZONE,
    final_credits       BIGINT,
    notes               TEXT
);
COMMENT ON TABLE eras IS 'One row per universe era; player_id is the partition key to all history';

ALTER TABLE waypoints ADD COLUMN IF NOT EXISTS era_id INTEGER NULL;
