-- Migration: Drop credits column from players table
-- Date: 2025-11-12
-- Reason: Credits are now always fetched fresh from SpaceTraders API, never persisted
--
-- IMPORTANT: This is a breaking change that requires manual execution
--
-- To apply this migration:
--   For PostgreSQL:
--     psql -U spacetraders -d spacetraders < migrations/001_drop_credits_column.sql
--
--   Or connect to your database and run:
--     \c spacetraders
--     \i migrations/001_drop_credits_column.sql
--

BEGIN;

-- Drop the credits column from players table
ALTER TABLE players DROP COLUMN IF EXISTS credits;

COMMIT;

-- Verification query (optional - uncomment to run):
-- SELECT column_name, data_type
-- FROM information_schema.columns
-- WHERE table_name = 'players';
