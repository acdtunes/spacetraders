-- Migration: Add foreign key constraints to all tables
-- This migration adds proper referential integrity constraints

-- 1. Add foreign key from containers.player_id to players.id
DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'fk_containers_player'
    ) THEN
        ALTER TABLE containers
        ADD CONSTRAINT fk_containers_player
        FOREIGN KEY (player_id) REFERENCES players(id)
        ON UPDATE CASCADE ON DELETE CASCADE;
    END IF;
END $$;

-- 2. Add composite foreign key from container_logs to containers
DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'fk_container_logs_container'
    ) THEN
        ALTER TABLE container_logs
        ADD CONSTRAINT fk_container_logs_container
        FOREIGN KEY (container_id, player_id) REFERENCES containers(id, player_id)
        ON UPDATE CASCADE ON DELETE CASCADE;
    END IF;
END $$;

-- 3. Add foreign key from ship_assignments.player_id to players.id
DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'fk_ship_assignments_player'
    ) THEN
        ALTER TABLE ship_assignments
        ADD CONSTRAINT fk_ship_assignments_player
        FOREIGN KEY (player_id) REFERENCES players(id)
        ON UPDATE CASCADE ON DELETE CASCADE;
    END IF;
END $$;

-- 4. Add composite foreign key from ship_assignments to containers (nullable)
DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'fk_ship_assignments_container'
    ) THEN
        ALTER TABLE ship_assignments
        ADD CONSTRAINT fk_ship_assignments_container
        FOREIGN KEY (container_id, player_id) REFERENCES containers(id, player_id)
        ON UPDATE CASCADE ON DELETE SET NULL;
    END IF;
END $$;

-- 5. Add foreign key from market_data.player_id to players.id
DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'fk_market_data_player'
    ) THEN
        ALTER TABLE market_data
        ADD CONSTRAINT fk_market_data_player
        FOREIGN KEY (player_id) REFERENCES players(id)
        ON UPDATE CASCADE ON DELETE CASCADE;
    END IF;
END $$;

-- 6. Add foreign key from contracts.player_id to players.id
DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'fk_contracts_player'
    ) THEN
        ALTER TABLE contracts
        ADD CONSTRAINT fk_contracts_player
        FOREIGN KEY (player_id) REFERENCES players(id)
        ON UPDATE CASCADE ON DELETE CASCADE;
    END IF;
END $$;
