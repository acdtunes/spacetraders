-- Drop storage_operations table
DROP INDEX IF EXISTS idx_storage_operations_type;
DROP INDEX IF EXISTS idx_storage_operations_waypoint;
DROP INDEX IF EXISTS idx_storage_operations_status;
DROP TABLE IF EXISTS storage_operations;
