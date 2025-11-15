-- Pre-migration cleanup: Remove orphaned data before adding foreign key constraints
-- This should be run BEFORE 002_add_foreign_key_constraints.up.sql

-- 1. Delete orphaned container_logs (logs referencing non-existent containers)
DELETE FROM container_logs cl
WHERE NOT EXISTS (
    SELECT 1 FROM containers c
    WHERE c.id = cl.container_id AND c.player_id = cl.player_id
);

-- 2. Clean up orphaned ship_assignments (assignments referencing non-existent containers)
-- Set container_id to NULL for assignments where the container no longer exists
UPDATE ship_assignments sa
SET container_id = NULL
WHERE sa.container_id IS NOT NULL
AND NOT EXISTS (
    SELECT 1 FROM containers c
    WHERE c.id = sa.container_id AND c.player_id = sa.player_id
);

-- Show cleanup summary
SELECT 'Cleanup complete' as status;
