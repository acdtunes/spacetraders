-- Migration: Add transactions table for ledger functionality
-- This migration adds support for financial tracking and reporting

-- Transactions table
CREATE TABLE IF NOT EXISTS transactions (
    id VARCHAR(36) NOT NULL PRIMARY KEY,
    player_id INT NOT NULL,
    timestamp TIMESTAMP NOT NULL,
    transaction_type VARCHAR(50) NOT NULL,
    category VARCHAR(50) NOT NULL,
    amount INT NOT NULL,
    balance_before INT NOT NULL,
    balance_after INT NOT NULL,
    description TEXT,
    metadata JSONB,
    related_entity_type VARCHAR(50),
    related_entity_id VARCHAR(100),
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    FOREIGN KEY (player_id) REFERENCES players(id) ON DELETE CASCADE ON UPDATE CASCADE
);

-- Indexes for efficient querying
CREATE INDEX IF NOT EXISTS idx_transactions_player_timestamp ON transactions(player_id, timestamp);
CREATE INDEX IF NOT EXISTS idx_transactions_type ON transactions(transaction_type);
CREATE INDEX IF NOT EXISTS idx_transactions_category ON transactions(category);
CREATE INDEX IF NOT EXISTS idx_transactions_related ON transactions(related_entity_type, related_entity_id);
