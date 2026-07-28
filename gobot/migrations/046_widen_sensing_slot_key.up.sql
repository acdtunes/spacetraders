-- sp-dpfp8: widen the sensing_slots key so ONE waypoint can carry ONE placement
-- PER KIND — a MARKET row (a probe parked there scanning) and a SPARE row (a
-- seed staged for purchase there) at the same time.
--
-- WHAT THIS UNBLOCKS. Expansion froze at two charting seeds and could not
-- request a third, ever. requestSeeds stages a SPARE want at a probe-selling
-- yard, and stagingYardFor will only pick a yard carrying no placement of its
-- own. The fleet's only two probe-selling yards (X1-KP23-A2, X1-KP23-C38) both
-- hold a parked MARKET placement, so every tick found yard=="" and no SPARE was
-- ever enqueued: zero rows in (slot_kind='SPARE', state='WANTED'), forever. The
-- free-waypoint test was not the cause — the KEY was. One row per waypoint means
-- a yard that is scanning cannot simultaneously be staging, and the two are not
-- the same claim on the waypoint at all.
--
-- The same key is what forces the stranded-probe adoption pass to skip any hull
-- standing where a row already exists (see the residual note on ledgerHolds in
-- probe_sensing_adoption.go). That skip under-counts the probe cap, which is the
-- money-UNSAFE direction; widening the key retires it.
--
-- WHAT REPLACES THE UNIQUENESS THE NARROW KEY GAVE AWAY. The old key made every
-- waypoint-keyed write unambiguous for free: one row per waypoint, so "the row
-- at this waypoint" always named exactly one. That is now false, and the
-- replacement is NOT a second unique constraint — it is that every write which
-- names a waypoint must also name a KIND. DeleteSlot, TransitionSlot, MarkScanned
-- and both upsert conflict targets all carry slot_kind as of this change, so
-- "the row at this waypoint" is never a question the code has to guess at.
--
-- Deliberately NOT added: a unique index on (player_id, assigned_ship). It looks
-- like the natural replacement — one hull, one row — but it would BREAK a money
-- guard rather than add one. reuseSpareHull and claimSpares both order their two
-- writes so the hull is named by BOTH rows for an instant, because the
-- alternative ordering leaves a crash with the hull named by NEITHER and the
-- probe cap authorising a replacement for a hull we already own (RULINGS #4).
-- That transient double-naming is chosen on purpose; a unique index would turn
-- it into a hard write failure on the exact path the guard protects.
--
-- ============================================================================
-- APPLY THIS DURING THE RESTART, NOT BEFORE IT.
-- ============================================================================
-- This is a coupled schema/binary change and the coupling is unavoidable: an
-- ON CONFLICT target must match an existing unique index exactly.
--
--   old binary + new schema -> the upserts name (player_id, waypoint_symbol),
--                              which is no longer unique. Postgres rejects them
--                              with SQLSTATE 42P10 ("no unique or exclusion
--                              constraint matching the ON CONFLICT
--                              specification") until the binary is replaced.
--   new binary + old schema -> the upserts name (player_id, waypoint_symbol,
--                              slot_kind), which does not exist yet. Same error,
--                              until this migration is applied.
--
-- There is no ordering that avoids the window and no way to write one that does:
-- the narrow unique index is exactly what forbids the second row, so it cannot
-- survive alongside the behaviour this change exists to allow. Sequence it as
-- one step -- stop the daemon, apply this, start the new binary -- and the
-- window is closed. Slot upserts are the only writes affected; they surface as
-- errors rather than corrupting anything, so an overrun is recoverable.
--
-- GORM AutoMigrate does NOT convert a primary key on an existing table (it is
-- additive only), which is why this is hand-written SQL — the same reason
-- 007_fix_contracts_primary_key exists. AutoMigrate DOES build the wide key on a
-- fresh database straight from the models.go primaryKey tags, so the test
-- harness and any new deployment get it without this file.
--
-- Re-runnable: the swap is skipped when the wide key is already in place.

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'sensing_slots_pkey'
          AND conrelid = 'sensing_slots'::regclass
          AND array_length(conkey, 1) = 2
    ) THEN
        ALTER TABLE sensing_slots DROP CONSTRAINT sensing_slots_pkey;
        ALTER TABLE sensing_slots ADD PRIMARY KEY (player_id, waypoint_symbol, slot_kind);
    END IF;
END
$$;

COMMENT ON TABLE sensing_slots IS 'Per-(waypoint,kind) parked-probe placement ledger, WANTED->QUEUED->BOUGHT->IN_TRANSIT->PARKED; one MARKET and one SPARE row may share a waypoint (sp-k6v8z, sp-dpfp8)';
COMMENT ON COLUMN sensing_slots.slot_kind IS 'MARKET|YARD|SPARE. Part of the primary key: a scanning yard can also stage a seed. Immutable per row — a kind change is a different row, never an UPDATE (sp-dpfp8)';
