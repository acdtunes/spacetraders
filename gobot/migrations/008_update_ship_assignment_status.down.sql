-- Revert ship assignment status from 'idle' back to 'released'

-- Step 1: Update all 'idle' status back to 'released'
-- Note: We cannot restore container_id values as they were intentionally cleared
UPDATE ship_assignments
SET status = 'released'
WHERE status = 'idle';
