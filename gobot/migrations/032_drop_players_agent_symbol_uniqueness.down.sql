-- Rollback assumes no duplicate agent_symbol rows exist; will fail otherwise.
DROP INDEX IF EXISTS idx_players_agent_symbol;
ALTER TABLE players ADD CONSTRAINT uni_players_agent_symbol UNIQUE (agent_symbol);
