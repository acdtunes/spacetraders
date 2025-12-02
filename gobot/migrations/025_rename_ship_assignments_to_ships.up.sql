-- Migration: Rename ship_assignments table to ships
-- This migration consolidates ship assignment into the Ship aggregate
-- as part of the DDD refactoring to move assignments into the navigation bounded context

-- Rename table
ALTER TABLE ship_assignments RENAME TO ships;

-- Rename status column to avoid confusion with nav_status
ALTER TABLE ships RENAME COLUMN status TO assignment_status;

-- Add index for common query patterns
CREATE INDEX IF NOT EXISTS idx_ships_assignment_status ON ships(assignment_status);
CREATE INDEX IF NOT EXISTS idx_ships_player ON ships(player_id);
CREATE INDEX IF NOT EXISTS idx_ships_container ON ships(container_id) WHERE container_id IS NOT NULL;
