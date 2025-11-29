-- Create storage_operations table for generalized cargo storage operations
-- This extends the concept from gas_operations to support any extraction type
CREATE TABLE IF NOT EXISTS storage_operations (
    id TEXT NOT NULL,
    player_id INT NOT NULL,
    waypoint_symbol TEXT NOT NULL,           -- Extraction location (gas giant, asteroid)
    operation_type TEXT NOT NULL,            -- GAS_SIPHON, MINING, CUSTOM
    status TEXT DEFAULT 'PENDING',           -- PENDING, RUNNING, COMPLETED, STOPPED, FAILED
    extractor_ships TEXT,                    -- JSON array of ship symbols (siphoners/miners)
    storage_ships TEXT,                      -- JSON array of ship symbols (cargo buffers)
    supported_goods TEXT,                    -- JSON array of goods this operation produces
    last_error TEXT,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    started_at TIMESTAMP WITH TIME ZONE,
    stopped_at TIMESTAMP WITH TIME ZONE,
    PRIMARY KEY (id, player_id),
    FOREIGN KEY (player_id) REFERENCES players(id) ON DELETE CASCADE,
    CONSTRAINT valid_operation_type CHECK (operation_type IN ('GAS_SIPHON', 'MINING', 'CUSTOM')),
    CONSTRAINT valid_storage_status CHECK (status IN ('PENDING', 'RUNNING', 'COMPLETED', 'STOPPED', 'FAILED'))
);

-- Index for efficient status-based queries
CREATE INDEX idx_storage_operations_status ON storage_operations(player_id, status);

-- Index for waypoint lookups
CREATE INDEX idx_storage_operations_waypoint ON storage_operations(waypoint_symbol);

-- Index for finding operations by goods (requires text search on JSON array)
CREATE INDEX idx_storage_operations_type ON storage_operations(operation_type);
