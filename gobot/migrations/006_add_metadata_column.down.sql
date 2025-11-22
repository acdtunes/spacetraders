-- Rollback: Remove metadata column from container_logs table

-- Drop metadata column
ALTER TABLE container_logs
DROP COLUMN IF EXISTS metadata;
