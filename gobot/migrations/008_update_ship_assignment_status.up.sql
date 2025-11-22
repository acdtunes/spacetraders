-- Update ship assignment status from 'released' to 'idle'
-- and clear container_id for idle ships

-- Step 1: Update all 'released' status to 'idle' and clear container_id
UPDATE ship_assignments
SET status = 'idle',
    container_id = NULL
WHERE status = 'released';

-- Note: No backfill of idle assignments for existing ships is performed here.
-- Ships without assignments will get idle assignments when next purchased or
-- when the purchase ship handler creates them automatically.
