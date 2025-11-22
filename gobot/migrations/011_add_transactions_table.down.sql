-- Rollback migration: Remove transactions table

-- Drop indexes first
DROP INDEX IF EXISTS idx_related;
DROP INDEX IF EXISTS idx_category;
DROP INDEX IF EXISTS idx_type;
DROP INDEX IF EXISTS idx_player_timestamp;

-- Drop table
DROP TABLE IF EXISTS transactions;
