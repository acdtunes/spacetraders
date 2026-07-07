-- Extend manufacturing_tasks valid_task_type constraint for construction tasks
-- The construction pipeline planner persists DELIVER_TO_CONSTRUCTION tasks
-- (internal/domain/manufacturing/task.go), but the CHECK constraint was never
-- migrated to accept them, so saving pipeline tasks failed with SQLSTATE 23514.

-- Update task type constraint to include DELIVER_TO_CONSTRUCTION
-- First drop the existing constraint
ALTER TABLE manufacturing_tasks
    DROP CONSTRAINT IF EXISTS valid_task_type;

-- Add new constraint with DELIVER_TO_CONSTRUCTION
-- (the atomic types are carried over unchanged from migration 024)
ALTER TABLE manufacturing_tasks
    ADD CONSTRAINT valid_task_type CHECK (task_type IN (
        'ACQUIRE_DELIVER',
        'COLLECT_SELL',
        'LIQUIDATE',
        'STORAGE_ACQUIRE_DELIVER',
        'DELIVER_TO_CONSTRUCTION'
    ));
