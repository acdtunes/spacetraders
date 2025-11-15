-- Rollback: Revert ID column normalization
-- This migration reverts the column renames back to the original {table}_id format

-- 1. Revert players.id back to players.player_id
ALTER TABLE players RENAME COLUMN id TO player_id;

-- 2. Revert containers.id back to containers.container_id
ALTER TABLE containers RENAME COLUMN id TO container_id;

-- 3. Revert container_logs.id back to container_logs.log_id
ALTER TABLE container_logs RENAME COLUMN id TO log_id;

-- 4. Revert contracts.id back to contracts.contract_id
ALTER TABLE contracts RENAME COLUMN id TO contract_id;
