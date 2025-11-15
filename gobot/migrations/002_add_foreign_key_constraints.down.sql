-- Rollback: Remove foreign key constraints
-- This migration drops all foreign key constraints added in the up migration

-- 1. Drop foreign key from contracts
ALTER TABLE contracts DROP CONSTRAINT IF EXISTS fk_contracts_player;

-- 2. Drop foreign key from market_data
ALTER TABLE market_data DROP CONSTRAINT IF EXISTS fk_market_data_player;

-- 3. Drop composite foreign key from ship_assignments to containers
ALTER TABLE ship_assignments DROP CONSTRAINT IF EXISTS fk_ship_assignments_container;

-- 4. Drop foreign key from ship_assignments to players
ALTER TABLE ship_assignments DROP CONSTRAINT IF EXISTS fk_ship_assignments_player;

-- 5. Drop composite foreign key from container_logs
ALTER TABLE container_logs DROP CONSTRAINT IF EXISTS fk_container_logs_container;

-- 6. Drop foreign key from containers
ALTER TABLE containers DROP CONSTRAINT IF EXISTS fk_containers_player;
