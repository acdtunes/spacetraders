-- Add heartbeat column for detecting stale/crashed containers
ALTER TABLE containers ADD COLUMN IF NOT EXISTS heartbeat_at TIMESTAMP WITH TIME ZONE;

-- Initialize heartbeat for running containers to started_at
UPDATE containers SET heartbeat_at = started_at WHERE status = 'RUNNING' AND heartbeat_at IS NULL;

-- Add index for efficient stale container queries
CREATE INDEX IF NOT EXISTS idx_containers_heartbeat ON containers(heartbeat_at) WHERE status = 'RUNNING';

COMMENT ON COLUMN containers.heartbeat_at IS 'Last activity timestamp - workers update this periodically to prove they are alive';
