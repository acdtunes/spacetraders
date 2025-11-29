-- Create gas_operations table for gas extraction operations
CREATE TABLE IF NOT EXISTS gas_operations (
    id TEXT NOT NULL,
    player_id INT NOT NULL,
    gas_giant TEXT NOT NULL,
    status TEXT DEFAULT 'PENDING',
    siphon_ships TEXT,      -- JSON array of ship symbols
    transport_ships TEXT,   -- JSON array of ship symbols
    max_iterations INT DEFAULT -1,
    last_error TEXT,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    started_at TIMESTAMP WITH TIME ZONE,
    stopped_at TIMESTAMP WITH TIME ZONE,
    PRIMARY KEY (id, player_id),
    FOREIGN KEY (player_id) REFERENCES players(id) ON DELETE CASCADE
);

-- Index for efficient status-based queries
CREATE INDEX idx_gas_operations_status ON gas_operations(player_id, status);

-- Index for gas giant lookups
CREATE INDEX idx_gas_operations_gas_giant ON gas_operations(gas_giant);
