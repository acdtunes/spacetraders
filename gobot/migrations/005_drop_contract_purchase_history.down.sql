-- Rollback: Recreate contract_purchase_history table
-- This allows rolling back to the purchase history-based rebalancing approach

CREATE TABLE IF NOT EXISTS contract_purchase_history (
    id SERIAL PRIMARY KEY,
    player_id INTEGER NOT NULL,
    system_symbol VARCHAR(255) NOT NULL,
    waypoint_symbol VARCHAR(255) NOT NULL,
    trade_good VARCHAR(255) NOT NULL,
    purchased_at TIMESTAMP NOT NULL,
    contract_id VARCHAR(255) NOT NULL,
    FOREIGN KEY (player_id) REFERENCES players(id) ON UPDATE CASCADE ON DELETE CASCADE
);

-- Create composite index for efficient queries
CREATE INDEX IF NOT EXISTS idx_player_system_time
ON contract_purchase_history (player_id, system_symbol, purchased_at);
