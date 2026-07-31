-- sp-zml2u: backs SensingSlotModel.LastScanAttemptAt (internal/adapters/persistence/models.go),
-- the column the parked-scan rotation PACES on.
--
-- WHY THE COLUMN EXISTS. sensing_slots.last_scan_at was being asked two questions at once and
-- could only answer one of them honestly. The scan pacer stamped it at the end of every turn,
-- and the reconcile paced the rotation off it — but the fleet's ONE market-scan budget declines
-- the overwhelming majority of those turns and a decline writes NOTHING to market_data.
-- Measured live before this landed: 3,551 declines to 310 real scans (92% declined), 909 slots
-- claiming a scan within 75 minutes while 714 of them (78.5%) held market data older than that,
-- and a median 144-minute gap between the stamp and the write it claimed. Fleet-wide, 909 slots
-- said fresh and 216 markets were. A sensing coordinator declining every single scan was
-- indistinguishable from one scanning perfectly.
--
-- WHY NOT SIMPLY STOP STAMPING ON A DECLINE, which is the obvious fix and is a trap. The
-- reconcile rebuilds the ENTIRE scan heap from this table every 30 seconds (SyncMembership),
-- so the stamp is not merely a report — it is the durable pacing clock. A decline that advanced
-- no clock would leave the slot due again on the very next rebuild, and at a 92% decline rate
-- that is the whole rotation spinning at full speed against the store, forever, producing
-- nothing. Far worse than the instrument defect it would be fixing.
--
-- So the two answers become two columns. This one advances on EVERY turn (scanned, declined or
-- failed) and is what the rotation paces against; last_scan_at advances only when market data
-- was actually written and is what the staleness gauge reports. They disagree 92% of the time,
-- which is exactly why one column could not serve both. Same split, and the same reasoning, as
-- last_attempt_at vs updated_at in migration 048.
--
-- NULLABLE, and NULL means "never attempted". The reader (ParkedSlotViews) COALESCES it to
-- last_scan_at rather than to the zero time, and that fallback is the migration: every existing
-- row reads NULL on the first tick after deploy, and without the coalesce that tick would
-- declare the entire rotation due at once — one full-speed sweep of every slot, the precise
-- regression this whole change exists to avoid. With it, the first tick paces identically to
-- the last one and the new clock takes over from each slot's next turn. No DEFAULT, for the
-- same reason 048 has none: a default would make a never-attempted row look already-tried.
--
-- No index. It is read by the same ParkedSlotViews query that already filters on player_id +
-- state (both indexed) and is written once per scan turn; an index would charge every one of
-- those writes for a sort of a few hundred rows that is already trivial.
--
-- Migration-backed because boot AutoMigrate failure is NON-FATAL: without this, a boot where
-- AutoMigrate could not run would leave writes touching the column hitting SQLSTATE 42703
-- (undefined_column). sensing_slots is CREATE'd by migration 045, so it is checkable by
-- TestModelColumnsBackedByMigrations and model and migration are held in lockstep.
--
-- Idempotent (ADD COLUMN IF NOT EXISTS): a no-op on any database where boot AutoMigrate
-- already added it. Type/nullability mirror the GORM tag exactly (TIMESTAMPTZ NULL) so a
-- fresh database and an AutoMigrated one converge.

ALTER TABLE sensing_slots
    ADD COLUMN IF NOT EXISTS last_scan_attempt_at TIMESTAMPTZ NULL;

COMMENT ON COLUMN sensing_slots.last_scan_attempt_at IS 'When the scan rotation last took this slot''s turn, whether or not it produced data; NULL = never attempted (reader coalesces to last_scan_at). The PACING clock — last_scan_at is the freshness claim and moves only on a real write (sp-zml2u)';

COMMENT ON COLUMN sensing_slots.last_scan_at IS 'When market data was last actually WRITTEN for this waypoint — a freshness claim, never advanced by a budget-declined turn. The rotation paces on last_scan_attempt_at instead (sp-zml2u)';
