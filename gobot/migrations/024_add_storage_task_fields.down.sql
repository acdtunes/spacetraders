-- Remove storage task fields and revert task type constraint

-- Drop index
DROP INDEX IF EXISTS idx_tasks_storage_operation;

-- Revert task type constraint to previous version
ALTER TABLE manufacturing_tasks
    DROP CONSTRAINT IF EXISTS valid_task_type;

ALTER TABLE manufacturing_tasks
    ADD CONSTRAINT valid_task_type CHECK (task_type IN (
        'ACQUIRE_DELIVER',
        'COLLECT_SELL',
        'LIQUIDATE'
    ));

-- Remove storage columns
ALTER TABLE manufacturing_tasks
    DROP COLUMN IF EXISTS storage_operation_id,
    DROP COLUMN IF EXISTS storage_waypoint;
