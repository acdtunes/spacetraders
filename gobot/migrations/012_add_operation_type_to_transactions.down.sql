-- Rollback: Remove operation_type column from transactions table

DROP INDEX IF EXISTS idx_transactions_player_operation;
DROP INDEX IF EXISTS idx_transactions_operation_type;

ALTER TABLE transactions
DROP COLUMN IF EXISTS operation_type;
