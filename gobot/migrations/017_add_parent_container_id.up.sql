-- +goose Up
-- Add parent container tracking column
-- NULL = top-level container (coordinator, standalone worker)
-- Non-NULL = child container spawned by a coordinator
ALTER TABLE containers ADD COLUMN parent_container_id VARCHAR(255);

-- Add partial index for efficient child lookups
-- Partial index: only index rows where parent_container_id IS NOT NULL
-- This saves space and improves performance for top-level container queries
CREATE INDEX idx_containers_parent_player
ON containers(parent_container_id, player_id)
WHERE parent_container_id IS NOT NULL;

-- Add documentation comment
COMMENT ON COLUMN containers.parent_container_id IS
'ID of parent coordinator that spawned this worker. NULL for top-level containers (coordinators, standalone workers).';

-- Add check constraint to prevent self-referencing containers
ALTER TABLE containers ADD CONSTRAINT chk_no_self_parent
CHECK (id != parent_container_id OR parent_container_id IS NULL);
