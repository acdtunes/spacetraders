-- Migration: Add transactions table
-- This migration adds support for the financial ledger tracking system

-- Transactions table
CREATE TABLE IF NOT EXISTS transactions (
    id VARCHAR(36) PRIMARY KEY NOT NULL,
    player_id INT NOT NULL,
    timestamp TIMESTAMPTZ NOT NULL,
    transaction_type VARCHAR(50) NOT NULL,
    category VARCHAR(50) NOT NULL,
    amount INT NOT NULL,
    balance_before INT NOT NULL,
    balance_after INT NOT NULL,
    description TEXT,
    metadata JSONB,
    related_entity_type VARCHAR(50),
    related_entity_id VARCHAR(100),
    created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (player_id) REFERENCES players(id) ON DELETE CASCADE
);

-- Index for querying transactions by player and timestamp (most common query pattern)
CREATE INDEX IF NOT EXISTS idx_player_timestamp ON transactions(player_id, timestamp DESC);

-- Index for filtering by transaction type
CREATE INDEX IF NOT EXISTS idx_type ON transactions(transaction_type);

-- Index for filtering by category (used in cash flow reports)
CREATE INDEX IF NOT EXISTS idx_category ON transactions(category);

-- Index for filtering by related entity (e.g., all transactions for a specific contract)
CREATE INDEX IF NOT EXISTS idx_related ON transactions(related_entity_type, related_entity_id);
