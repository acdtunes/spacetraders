-- Migration: Add ship state columns for database-as-source-of-truth
-- This extends the ships table to store full ship state, not just assignments

-- Navigation state columns
ALTER TABLE ships ADD COLUMN IF NOT EXISTS nav_status VARCHAR(20) DEFAULT 'DOCKED';
ALTER TABLE ships ADD COLUMN IF NOT EXISTS flight_mode VARCHAR(20) DEFAULT 'CRUISE';
ALTER TABLE ships ADD COLUMN IF NOT EXISTS arrival_time TIMESTAMPTZ NULL;

-- Location columns (denormalized for quick access without waypoint join)
ALTER TABLE ships ADD COLUMN IF NOT EXISTS location_symbol VARCHAR(64);
ALTER TABLE ships ADD COLUMN IF NOT EXISTS location_x DOUBLE PRECISION DEFAULT 0;
ALTER TABLE ships ADD COLUMN IF NOT EXISTS location_y DOUBLE PRECISION DEFAULT 0;
ALTER TABLE ships ADD COLUMN IF NOT EXISTS system_symbol VARCHAR(32);

-- Fuel columns
ALTER TABLE ships ADD COLUMN IF NOT EXISTS fuel_current INT DEFAULT 0;
ALTER TABLE ships ADD COLUMN IF NOT EXISTS fuel_capacity INT DEFAULT 0;

-- Cargo columns (JSONB for full item details)
ALTER TABLE ships ADD COLUMN IF NOT EXISTS cargo_capacity INT DEFAULT 0;
ALTER TABLE ships ADD COLUMN IF NOT EXISTS cargo_units INT DEFAULT 0;
ALTER TABLE ships ADD COLUMN IF NOT EXISTS cargo_inventory JSONB DEFAULT '[]';

-- Ship specification columns
ALTER TABLE ships ADD COLUMN IF NOT EXISTS engine_speed INT DEFAULT 0;
ALTER TABLE ships ADD COLUMN IF NOT EXISTS frame_symbol VARCHAR(64);
ALTER TABLE ships ADD COLUMN IF NOT EXISTS role VARCHAR(32);
ALTER TABLE ships ADD COLUMN IF NOT EXISTS modules JSONB DEFAULT '[]';

-- Cooldown tracking
ALTER TABLE ships ADD COLUMN IF NOT EXISTS cooldown_expiration TIMESTAMPTZ NULL;

-- Sync metadata
ALTER TABLE ships ADD COLUMN IF NOT EXISTS synced_at TIMESTAMPTZ DEFAULT NOW();
ALTER TABLE ships ADD COLUMN IF NOT EXISTS version INT DEFAULT 1;

-- Indexes for common query patterns
CREATE INDEX IF NOT EXISTS idx_ships_nav_status ON ships(nav_status);
CREATE INDEX IF NOT EXISTS idx_ships_arrival_time ON ships(arrival_time)
    WHERE arrival_time IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_ships_cooldown ON ships(cooldown_expiration)
    WHERE cooldown_expiration IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_ships_location ON ships(location_symbol);
CREATE INDEX IF NOT EXISTS idx_ships_system ON ships(system_symbol);

COMMENT ON TABLE ships IS 'Full ship state persisted from API. Database is source of truth after initial sync.';
