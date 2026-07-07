-- Revert task type constraint to the pre-construction set (migration 024)

ALTER TABLE manufacturing_tasks
    DROP CONSTRAINT IF EXISTS valid_task_type;

ALTER TABLE manufacturing_tasks
    ADD CONSTRAINT valid_task_type CHECK (task_type IN (
        'ACQUIRE_DELIVER',
        'COLLECT_SELL',
        'LIQUIDATE',
        'STORAGE_ACQUIRE_DELIVER'
    ));
