-- sp-cwnwb: backs SensingSlotModel.LastAttemptAt (internal/adapters/persistence/models.go),
-- the column the placement worklist rotates on.
--
-- WHY THE COLUMN EXISTS. The placement machine works a fixed per-tick budget over a queue
-- that DOES NOT DRAIN: a slot whose move is refused stays in the state it was in, so it is
-- back on the worklist next tick. While that list was read in a tick-stable order (waypoint
-- symbol), the same slots held the head of it forever and the budget never saw past them.
-- Measured live before this landed: 266 BOUGHT + 52 IN_TRANSIT slots, ~40 actions' worth of
-- ticks, ZERO dispatches, and one slot 22.5 hours old that had never been attempted once
-- because its symbol sorted behind a hundred others that were failing every tick. Stamping
-- each slot as it consumes a budget and reading least-recently-attempted first makes the
-- order rotate, so N slots are covered in ceil(N/budget) ticks. Same fix the screening sweep
-- already made for itself on sensing_systems.screened_at.
--
-- WHY NOT updated_at, which already exists on this table. It moves only on a SUCCESSFUL
-- transition, so a slot whose move fails every tick carries the OLDEST one — ordering by it
-- would have entrenched the failing head rather than breaking it. Restamping updated_at on
-- failures instead would have been worse: it is the only record of how long a slot has sat
-- in one state, and a stuck placement that reads as freshly updated is invisible. The two
-- columns answer different questions ("when was it last tried" vs "when did it last move")
-- and a starving slot is exactly the row where they must be allowed to disagree.
--
-- NULLABLE, and NULL is meaningful: a slot the placement machine has never tried. Those sort
-- FIRST in the worklist — a never-attempted slot is the case the rotation most needs to
-- reach, and it is the shape the live defect took. Every existing row therefore reads as
-- never-attempted and gets an early turn on the first tick after deploy, which is exactly
-- the wanted behaviour for the 266 slots this unfreezes. No DEFAULT for the same reason: a
-- default would make new rows look already-tried.
--
-- No index. The read filters player_id + state + era (all indexed) and then sorts a few
-- hundred rows, while the stamp is written up to 40 times a tick — an index here would buy a
-- sort that is already trivial and charge every one of those writes for it.
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
    ADD COLUMN IF NOT EXISTS last_attempt_at TIMESTAMPTZ NULL;

COMMENT ON COLUMN sensing_slots.last_attempt_at IS 'When the placement machine last spent a tick budget on this slot; NULL = never tried. Rotates the worklist least-recently-attempted first (sp-cwnwb)';
