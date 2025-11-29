-- Rollback BUG FIX #3: Remove phase tracking columns

DROP INDEX IF EXISTS idx_tasks_phase_completed;

ALTER TABLE manufacturing_tasks
DROP COLUMN IF EXISTS collect_phase_completed,
DROP COLUMN IF EXISTS acquire_phase_completed,
DROP COLUMN IF EXISTS phase_completed_at;
