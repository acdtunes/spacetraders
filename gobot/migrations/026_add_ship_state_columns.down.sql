-- Rollback: Remove ship state columns

DROP INDEX IF EXISTS idx_ships_nav_status;
DROP INDEX IF EXISTS idx_ships_arrival_time;
DROP INDEX IF EXISTS idx_ships_cooldown;
DROP INDEX IF EXISTS idx_ships_location;
DROP INDEX IF EXISTS idx_ships_system;

ALTER TABLE ships DROP COLUMN IF EXISTS nav_status;
ALTER TABLE ships DROP COLUMN IF EXISTS flight_mode;
ALTER TABLE ships DROP COLUMN IF EXISTS arrival_time;
ALTER TABLE ships DROP COLUMN IF EXISTS location_symbol;
ALTER TABLE ships DROP COLUMN IF EXISTS location_x;
ALTER TABLE ships DROP COLUMN IF EXISTS location_y;
ALTER TABLE ships DROP COLUMN IF EXISTS system_symbol;
ALTER TABLE ships DROP COLUMN IF EXISTS fuel_current;
ALTER TABLE ships DROP COLUMN IF EXISTS fuel_capacity;
ALTER TABLE ships DROP COLUMN IF EXISTS cargo_capacity;
ALTER TABLE ships DROP COLUMN IF EXISTS cargo_units;
ALTER TABLE ships DROP COLUMN IF EXISTS cargo_inventory;
ALTER TABLE ships DROP COLUMN IF EXISTS engine_speed;
ALTER TABLE ships DROP COLUMN IF EXISTS frame_symbol;
ALTER TABLE ships DROP COLUMN IF EXISTS role;
ALTER TABLE ships DROP COLUMN IF EXISTS modules;
ALTER TABLE ships DROP COLUMN IF EXISTS cooldown_expiration;
ALTER TABLE ships DROP COLUMN IF EXISTS synced_at;
ALTER TABLE ships DROP COLUMN IF EXISTS version;
