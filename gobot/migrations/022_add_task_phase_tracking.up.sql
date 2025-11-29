-- BUG FIX #3: Add phase tracking columns for daemon restart resilience
-- These columns track which phase of multi-phase tasks has completed,
-- allowing workers to skip to the second phase after a daemon restart.

ALTER TABLE manufacturing_tasks
ADD COLUMN collect_phase_completed BOOLEAN NOT NULL DEFAULT FALSE,
ADD COLUMN acquire_phase_completed BOOLEAN NOT NULL DEFAULT FALSE,
ADD COLUMN phase_completed_at TIMESTAMP WITH TIME ZONE;

-- Index for finding tasks that need phase-aware recovery
CREATE INDEX idx_tasks_phase_completed ON manufacturing_tasks (status, collect_phase_completed, acquire_phase_completed)
WHERE status IN ('ASSIGNED', 'EXECUTING', 'READY');

COMMENT ON COLUMN manufacturing_tasks.collect_phase_completed IS 'COLLECT_SELL: true if goods were collected from factory before interruption';
COMMENT ON COLUMN manufacturing_tasks.acquire_phase_completed IS 'ACQUIRE_DELIVER: true if goods were purchased from market before interruption';
COMMENT ON COLUMN manufacturing_tasks.phase_completed_at IS 'Timestamp when the first phase completed';
