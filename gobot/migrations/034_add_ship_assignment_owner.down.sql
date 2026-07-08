-- Remove captain reservation support (sp-i1ku)
DROP INDEX IF EXISTS idx_ships_captain_reservation;
ALTER TABLE ships DROP COLUMN IF EXISTS assignment_reason;
ALTER TABLE ships DROP COLUMN IF EXISTS assignment_owner;
