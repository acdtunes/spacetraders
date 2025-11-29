-- Drop gas_operations table and indexes
DROP INDEX IF EXISTS idx_gas_operations_gas_giant;
DROP INDEX IF EXISTS idx_gas_operations_status;
DROP TABLE IF EXISTS gas_operations;
