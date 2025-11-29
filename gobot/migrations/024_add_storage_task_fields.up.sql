-- Add storage operation fields to manufacturing_tasks
-- These are used by STORAGE_ACQUIRE_DELIVER tasks

-- Add new columns for storage operations
ALTER TABLE manufacturing_tasks
    ADD COLUMN IF NOT EXISTS storage_operation_id TEXT,
    ADD COLUMN IF NOT EXISTS storage_waypoint TEXT;

-- Update task type constraint to include STORAGE_ACQUIRE_DELIVER
-- First drop the existing constraint
ALTER TABLE manufacturing_tasks
    DROP CONSTRAINT IF EXISTS valid_task_type;

-- Add new constraint with STORAGE_ACQUIRE_DELIVER
-- Note: The original constraint used the old task types, we update to the atomic ones
ALTER TABLE manufacturing_tasks
    ADD CONSTRAINT valid_task_type CHECK (task_type IN (
        'ACQUIRE_DELIVER',
        'COLLECT_SELL',
        'LIQUIDATE',
        'STORAGE_ACQUIRE_DELIVER'
    ));

-- Index for finding tasks by storage operation
CREATE INDEX IF NOT EXISTS idx_tasks_storage_operation ON manufacturing_tasks(storage_operation_id)
    WHERE storage_operation_id IS NOT NULL;
