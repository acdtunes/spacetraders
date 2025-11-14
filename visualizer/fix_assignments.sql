-- Manual fix to update ship assignments to point to new running containers
-- Run this against PostgreSQL database

BEGIN;

-- Update COOPER-2 assignment
UPDATE ship_assignments
SET container_id = 'scout-tour-cooper-2-84cfcda0',
    status = 'active',
    assigned_at = NOW()
WHERE ship_symbol = 'COOPER-2'
  AND player_id = 2
  AND operation = 'command';

-- Update COOPER-3 assignment
UPDATE ship_assignments
SET container_id = 'scout-tour-cooper-3-989712ea',
    status = 'active',
    assigned_at = NOW()
WHERE ship_symbol = 'COOPER-3'
  AND player_id = 2
  AND operation = 'command';

-- Update COOPER-4 assignment
UPDATE ship_assignments
SET container_id = 'scout-tour-cooper-4-47d4c466',
    status = 'active',
    assigned_at = NOW()
WHERE ship_symbol = 'COOPER-4'
  AND player_id = 2
  AND operation = 'command';

-- Update COOPER-5 assignment
UPDATE ship_assignments
SET container_id = 'scout-tour-cooper-5-fa7030e8',
    status = 'active',
    assigned_at = NOW()
WHERE ship_symbol = 'COOPER-5'
  AND player_id = 2
  AND operation = 'command';

-- Update COOPER-6 assignment
UPDATE ship_assignments
SET container_id = 'scout-tour-cooper-6-fb3a9319',
    status = 'active',
    assigned_at = NOW()
WHERE ship_symbol = 'COOPER-6'
  AND player_id = 2
  AND operation = 'command';

-- Verify the updates
SELECT
    sa.ship_symbol,
    sa.container_id,
    sa.status as assignment_status,
    c.status as container_status
FROM ship_assignments sa
JOIN containers c ON sa.container_id = c.container_id AND sa.player_id = c.player_id
WHERE c.command_type = 'ScoutTourCommand'
  AND c.status IN ('RUNNING', 'STARTING', 'STARTED')
ORDER BY sa.ship_symbol;

COMMIT;
