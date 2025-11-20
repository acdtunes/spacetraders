-- Migration: Add mining operations tables
-- This migration adds support for the mining operation feature

-- Mining operations table
CREATE TABLE IF NOT EXISTS mining_operations (
    id VARCHAR(36) NOT NULL,
    player_id INT NOT NULL,
    asteroid_field VARCHAR(255) NOT NULL,
    status VARCHAR(50) DEFAULT 'PENDING',
    top_n_ores INT DEFAULT 3,
    miner_ships TEXT NOT NULL,
    transport_ships TEXT NOT NULL,
    batch_threshold INT DEFAULT 3,
    batch_timeout INT DEFAULT 300,
    max_iterations INT DEFAULT -1,
    last_error TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    started_at TIMESTAMP,
    stopped_at TIMESTAMP,
    PRIMARY KEY (id, player_id),
    FOREIGN KEY (player_id) REFERENCES players(id) ON DELETE CASCADE
);

-- Index for querying active operations
CREATE INDEX IF NOT EXISTS idx_mining_ops_player_status ON mining_operations(player_id, status);
CREATE INDEX IF NOT EXISTS idx_mining_ops_asteroid ON mining_operations(asteroid_field);

-- Cargo transfer queue table
CREATE TABLE IF NOT EXISTS cargo_transfer_queue (
    id SERIAL PRIMARY KEY,
    mining_operation_id VARCHAR(36) NOT NULL,
    miner_ship VARCHAR(255) NOT NULL,
    transport_ship VARCHAR(255),
    status VARCHAR(50) DEFAULT 'PENDING',
    cargo_manifest TEXT NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    completed_at TIMESTAMP,
    player_id INT NOT NULL,
    FOREIGN KEY (player_id) REFERENCES players(id) ON DELETE CASCADE
);

-- Indexes for cargo transfer queue
CREATE INDEX IF NOT EXISTS idx_transfer_queue_operation ON cargo_transfer_queue(mining_operation_id, status);
CREATE INDEX IF NOT EXISTS idx_transfer_queue_miner ON cargo_transfer_queue(miner_ship, status);
