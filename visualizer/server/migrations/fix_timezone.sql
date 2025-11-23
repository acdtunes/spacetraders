-- Migration to fix timezone issues
-- This converts timestamp columns from GMT-3 to UTC and changes type to timestamptz

BEGIN;

-- 1. Convert transactions.timestamp from 'timestamp without time zone' to 'timestamptz'
-- The existing timestamps are in GMT-3, so we tell PostgreSQL to interpret them as such
ALTER TABLE transactions
  ALTER COLUMN timestamp TYPE timestamptz
  USING (timestamp AT TIME ZONE 'America/Argentina/Buenos_Aires');

-- 2. Convert other timestamp columns if they exist
-- Check if these columns exist in your schema and uncomment as needed

-- If ship_assignments has timestamp columns:
-- ALTER TABLE ship_assignments
--   ALTER COLUMN assigned_at TYPE timestamptz
--   USING (assigned_at AT TIME ZONE 'America/Argentina/Buenos_Aires');
--
-- ALTER TABLE ship_assignments
--   ALTER COLUMN released_at TYPE timestamptz
--   USING (released_at AT TIME ZONE 'America/Argentina/Buenos_Aires');

-- If containers has timestamp columns:
-- ALTER TABLE containers
--   ALTER COLUMN started_at TYPE timestamptz
--   USING (started_at AT TIME ZONE 'America/Argentina/Buenos_Aires');
--
-- ALTER TABLE containers
--   ALTER COLUMN stopped_at TYPE timestamptz
--   USING (stopped_at AT TIME ZONE 'America/Argentina/Buenos_Aires');

-- If container_logs has timestamp column:
-- ALTER TABLE container_logs
--   ALTER COLUMN timestamp TYPE timestamptz
--   USING (timestamp AT TIME ZONE 'America/Argentina/Buenos_Aires');

-- 3. Set database timezone to UTC for all future connections
ALTER DATABASE spacetraders SET timezone = 'UTC';

COMMIT;

-- Verify the migration
SELECT
  'transactions' as table_name,
  MIN(timestamp) as oldest_timestamp,
  MAX(timestamp) as newest_timestamp
FROM transactions;
