-- Captain reservation support (sp-i1ku): a ship's active assignment can now be
-- held either by a coordinator container (the existing behavior) or by the
-- captain directly for manual, hands-on use. A captain reservation is
-- invisible to every coordinator's claim/discovery path because it shares the
-- same "active assignment" row shape they already skip over — only the owner
-- column is new.
ALTER TABLE ships ADD COLUMN IF NOT EXISTS assignment_owner VARCHAR(16) NOT NULL DEFAULT 'container';
ALTER TABLE ships ADD COLUMN IF NOT EXISTS assignment_reason TEXT;

-- Backfill existing rows explicitly (default above already covers new rows,
-- but be explicit for any row written before the default existed).
UPDATE ships SET assignment_owner = 'container' WHERE assignment_owner IS NULL OR assignment_owner = '';

-- Partial index to make "find all captain-reserved ships" (used by `ship list`
-- and the reserve-time idle-critical warning) cheap without penalizing the
-- much more common container-assignment lookups.
CREATE INDEX IF NOT EXISTS idx_ships_captain_reservation ON ships (player_id)
    WHERE assignment_status = 'active' AND assignment_owner = 'captain';

COMMENT ON COLUMN ships.assignment_owner IS 'Who holds the active assignment: container (coordinator claim) or captain (manual reservation, sp-i1ku)';
COMMENT ON COLUMN ships.assignment_reason IS 'Free-text reason given at `ship reserve` time; NULL for container assignments';
