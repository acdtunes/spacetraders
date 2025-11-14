-- Query to check for scout tour assignments in your database
-- Run this against your SpaceTraders bot database

-- Check for any scout tour assignments
SELECT
    sa.ship_symbol,
    sa.container_id,
    sa.player_id,
    c.status,
    c.config::jsonb->>'command_type' as command_type,
    c.config::jsonb->'params'->>'system' as system,
    jsonb_array_length(c.config::jsonb->'params'->'markets') as market_count,
    sa.assigned_at
FROM ship_assignments sa
JOIN containers c ON sa.container_id = c.container_id AND sa.player_id = c.player_id
WHERE sa.operation = 'command'
    AND sa.container_id IS NOT NULL
    AND (c.config::jsonb)->>'command_type' = 'ScoutTourCommand'
ORDER BY sa.assigned_at DESC;

-- Check container statuses
SELECT status, COUNT(*) as count
FROM containers
WHERE (config::jsonb)->>'command_type' = 'ScoutTourCommand'
GROUP BY status;

-- Check if any ships have scout tour assignments
SELECT COUNT(*) as total_scout_assignments
FROM ship_assignments sa
JOIN containers c ON sa.container_id = c.container_id
WHERE (c.config::jsonb)->>'command_type' = 'ScoutTourCommand';
