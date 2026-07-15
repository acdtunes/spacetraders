-- Rollback sp-vp9k: remove the nav route origin + departure columns for ships.

DROP INDEX IF EXISTS idx_ships_departure_time;

ALTER TABLE ships DROP COLUMN IF EXISTS origin_symbol;
ALTER TABLE ships DROP COLUMN IF EXISTS origin_x;
ALTER TABLE ships DROP COLUMN IF EXISTS origin_y;
ALTER TABLE ships DROP COLUMN IF EXISTS departure_time;
