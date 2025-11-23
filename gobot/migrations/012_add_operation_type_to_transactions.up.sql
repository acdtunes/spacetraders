-- Migration: Add operation_type column to transactions table
-- This adds support for categorizing transactions by business operation type

ALTER TABLE transactions
ADD COLUMN IF NOT EXISTS operation_type VARCHAR(50);

-- Add index for efficient filtering by operation type
CREATE INDEX IF NOT EXISTS idx_transactions_operation_type ON transactions(operation_type);

-- Add index for combined player + operation type queries
CREATE INDEX IF NOT EXISTS idx_transactions_player_operation ON transactions(player_id, operation_type);
