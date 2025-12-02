-- Rollback: Revert ships table back to ship_assignments

-- Drop new indexes
DROP INDEX IF EXISTS idx_ships_assignment_status;
DROP INDEX IF EXISTS idx_ships_player;
DROP INDEX IF EXISTS idx_ships_container;

-- Revert column rename
ALTER TABLE ships RENAME COLUMN assignment_status TO status;

-- Revert table rename
ALTER TABLE ships RENAME TO ship_assignments;
