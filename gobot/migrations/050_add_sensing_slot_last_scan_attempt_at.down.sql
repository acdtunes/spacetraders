-- Rollback the last_scan_attempt_at column from sensing_slots.
--
-- DROPPING IT REQUIRES ROLLING BACK THE CODE TOO, and that is not the usual "you lose a
-- nice-to-have" caveat. Without this column the scan rotation has no durable pacing clock: the
-- reconcile rebuilds the heap from this table every 30 seconds, so a binary that still skips
-- the freshness stamp on a budget decline would find every declined slot permanently due and
-- spin the whole rotation at full speed producing nothing. At the measured 92% decline rate
-- that is every slot.
--
-- Safe once the code is back to stamping last_scan_at on every turn — the pre-sp-zml2u
-- behaviour, which paces correctly and reports freshness falsely. Nothing else reads this
-- column, and no state, hull assignment or scan history is touched by the drop.

ALTER TABLE sensing_slots
    DROP COLUMN IF EXISTS last_scan_attempt_at;

COMMENT ON COLUMN sensing_slots.last_scan_at IS 'Freshness stamp: when this slot was last scanned';
