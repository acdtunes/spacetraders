-- Migration: Normalize ID columns from {table}_id to id
-- This migration renames all primary key columns to follow the convention of using 'id' as the column name

-- 1. Rename players.player_id to players.id
ALTER TABLE players RENAME COLUMN player_id TO id;

-- 2. Rename containers.container_id to containers.id
ALTER TABLE containers RENAME COLUMN container_id TO id;

-- 3. Rename container_logs.log_id to container_logs.id
ALTER TABLE container_logs RENAME COLUMN log_id TO id;

-- 4. Rename contracts.contract_id to contracts.id
ALTER TABLE contracts RENAME COLUMN contract_id TO id;

-- Note: Foreign key constraints are automatically updated in PostgreSQL when renaming columns
-- GORM will create the foreign key relationships on next AutoMigrate if they don't exist
