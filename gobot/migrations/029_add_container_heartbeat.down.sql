-- Remove heartbeat column
DROP INDEX IF EXISTS idx_containers_heartbeat;
ALTER TABLE containers DROP COLUMN IF EXISTS heartbeat_at;
