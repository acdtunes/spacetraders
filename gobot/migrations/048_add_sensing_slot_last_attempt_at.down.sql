-- Rollback the last_attempt_at column from sensing_slots.
--
-- Dropping it loses every slot's attempt history, so the placement worklist falls back to a
-- tick-stable waypoint order — which is the starvation this column was added to fix. Safe as
-- a rollback (nothing else reads the column, and no state or hull assignment is touched), but
-- the freeze it prevents returns with it.

ALTER TABLE sensing_slots
    DROP COLUMN IF EXISTS last_attempt_at;
