-- Add max_workers column to manufacturing_pipelines
-- This controls the maximum parallel workers for pipeline task execution

ALTER TABLE manufacturing_pipelines
    ADD COLUMN IF NOT EXISTS max_workers INTEGER DEFAULT 5;

COMMENT ON COLUMN manufacturing_pipelines.max_workers IS 'Maximum parallel workers for task execution (default 5, 0=unlimited)';
