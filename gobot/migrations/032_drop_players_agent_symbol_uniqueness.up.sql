-- The Admiral reuses the same agent symbol across universe eras (bead sp-81qa),
-- so players.agent_symbol can no longer enforce global uniqueness: a second
-- registration of an existing symbol must be allowed to insert its own player row.
ALTER TABLE players DROP CONSTRAINT IF EXISTS uni_players_agent_symbol;
CREATE INDEX IF NOT EXISTS idx_players_agent_symbol ON players (agent_symbol);
