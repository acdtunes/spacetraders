-- Rollback for 046: narrow the sensing_slots key back to (player_id, waypoint_symbol).
--
-- THIS ROLLBACK LOSES ROWS, and it cannot not. The wide key exists precisely so a
-- waypoint can carry two placements; narrowing it again means at most one may
-- survive, so any waypoint holding both a MARKET and a SPARE row must give one
-- up before the narrow primary key can be rebuilt.
--
-- WHICH ONE SURVIVES IS A MONEY DECISION, not a coin toss. A row naming a hull is
-- what CountOwnedProbes reads, so deleting one drops a probe we have paid for out
-- of the cap and authorises buying a replacement for a hull we already own — the
-- one direction RULINGS #4 forbids. So the ordering below keeps, in order:
--
--   1. a row that names a hull, over one that does not;
--   2. MARKET/YARD over SPARE, when neither or both name a hull — a market
--      placement is a working scan post, a SPARE is a staging intent the
--      expansion engine will simply re-request on its next tick.
--
-- When BOTH rows name hulls there is no safe answer available: one hull is going
-- to become invisible to the probe cap. The adoption pass re-collects it (it
-- sweeps hulls the ledger does not account for), which is the recovery path, but
-- expect the cap to under-read until it runs. Prefer rolling the BINARY back and
-- leaving the wide key in place: the old code's narrow ON CONFLICT target is the
-- only thing that actually requires this, and a wide key with rows only ever
-- written one-per-waypoint is harmless.

DELETE FROM sensing_slots a
USING sensing_slots b
WHERE a.player_id = b.player_id
  AND a.waypoint_symbol = b.waypoint_symbol
  AND a.slot_kind <> b.slot_kind
  AND (
        -- b names a hull and a does not: a loses.
        ((b.assigned_ship IS NOT NULL AND b.assigned_ship <> '')
         AND (a.assigned_ship IS NULL OR a.assigned_ship = ''))
        -- neither distinction above applies: SPARE loses to anything else.
     OR (((b.assigned_ship IS NOT NULL AND b.assigned_ship <> '')
          = (a.assigned_ship IS NOT NULL AND a.assigned_ship <> ''))
         AND a.slot_kind = 'SPARE' AND b.slot_kind <> 'SPARE')
      );

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'sensing_slots_pkey'
          AND conrelid = 'sensing_slots'::regclass
          AND array_length(conkey, 1) = 3
    ) THEN
        ALTER TABLE sensing_slots DROP CONSTRAINT sensing_slots_pkey;
        ALTER TABLE sensing_slots ADD PRIMARY KEY (player_id, waypoint_symbol);
    END IF;
END
$$;

COMMENT ON TABLE sensing_slots IS 'Per-waypoint parked-probe placement ledger, WANTED->QUEUED->BOUGHT->IN_TRANSIT->PARKED (sp-k6v8z)';
COMMENT ON COLUMN sensing_slots.slot_kind IS NULL;
