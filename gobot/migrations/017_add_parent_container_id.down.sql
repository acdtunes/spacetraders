-- +goose Down
-- Remove check constraint
ALTER TABLE containers DROP CONSTRAINT IF EXISTS chk_no_self_parent;

-- Remove index
DROP INDEX IF EXISTS idx_containers_parent_player;

-- Remove column (cascades to all dependent views)
ALTER TABLE containers DROP COLUMN IF EXISTS parent_container_id;
