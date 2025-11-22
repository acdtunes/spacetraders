-- Migration: Add goods factories table
-- This migration adds support for the automated goods production feature

-- Goods factories table
CREATE TABLE IF NOT EXISTS goods_factories (
    id VARCHAR(36) NOT NULL,
    player_id INT NOT NULL,
    target_good VARCHAR(255) NOT NULL,
    system_symbol VARCHAR(255) NOT NULL,
    dependency_tree TEXT NOT NULL,
    status VARCHAR(50) DEFAULT 'PENDING',
    metadata JSONB,
    quantity_acquired INT DEFAULT 0,
    total_cost INT DEFAULT 0,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    started_at TIMESTAMP,
    completed_at TIMESTAMP,
    PRIMARY KEY (id, player_id),
    FOREIGN KEY (player_id) REFERENCES players(id) ON DELETE CASCADE
);

-- Index for querying active factories by player
CREATE INDEX IF NOT EXISTS idx_goods_factories_player_status ON goods_factories(player_id, status);

-- Index for querying by target good
CREATE INDEX IF NOT EXISTS idx_goods_factories_target ON goods_factories(target_good);

-- Index for querying by system
CREATE INDEX IF NOT EXISTS idx_goods_factories_system ON goods_factories(system_symbol);
