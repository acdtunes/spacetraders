-- sp-k6v8z: the parked-probe sensing ledger — the durable placement spine that
-- makes the whole parked-probe model re-derivable from the database after a
-- daemon restart (RULINGS #2). Two tables:
--
--   sensing_systems — one screening verdict per (player, system): PENDING (not
--   yet screened), IN_SCOPE (deals in >=1 whitelisted good, place slots here) or
--   NO_WHITELIST (screened and rejected), plus the one-off charting seed
--   (seed_ship/seed_state: DISPATCHED -> CHARTING -> DONE) that resolves an
--   uncharted_count > 0.
--
--   catalog_synced_at records that we have actually SWEPT the system's waypoint
--   list, and is the guard on the NO_WHITELIST verdict. A system nobody has ever
--   visited has no waypoint rows at all, so a screen of it finds no markets and
--   nothing uncharted -- which reads identically to "charted through, deals in
--   nothing we want" and would durably write off an unexplored system on
--   evidence that is simply absent. Worse, NO_WHITELIST systems are propagation
--   ORIGINS for the expansion frontier, so one such write-off walks outward.
--   NULL means unswept: the screen records PENDING instead, and expansion treats
--   the system as needing a seed.
--
--   sensing_slots — one probe PLACEMENT per (player, waypoint), advancing
--   WANTED -> QUEUED -> BOUGHT -> IN_TRANSIT -> PARKED. assigned_ship is
--   NULLABLE on purpose: WANTED/QUEUED slots are intents with no hull yet, and
--   "state IN (BOUGHT, IN_TRANSIT, PARKED) AND assigned_ship IS NOT NULL" is
--   exactly the probe_cap read that gates probe spend. A state alone must never
--   imply a purchase.
--
-- Composite primary keys make duplicate screenings/placements structurally
-- impossible — a re-screen or a re-declared placement updates the row in place.
--
-- GORM AutoMigrate at daemon boot also creates these tables (models are the
-- source of truth, AutoMigrate is additive), but boot AutoMigrate is best-effort
-- and NON-FATAL: without this migration, a boot where AutoMigrate could not run
-- would leave every write here hitting SQLSTATE 42P01/42703 (undefined table or
-- column). Because this CREATE TABLEs both tables, they also become CHECKABLE by
-- TestModelColumnsBackedByMigrations — model and migration are held in lockstep
-- from here on, so this DDL mirrors the GORM tags in
-- internal/adapters/persistence/models.go exactly (types, sizes, nullability,
-- defaults) and the index names match the ones GORM's naming strategy generates,
-- so AutoMigrate finds them already present instead of creating duplicates.
--
-- Idempotent via IF NOT EXISTS — safe to re-run, and safe whether or not
-- AutoMigrate already created the tables.
--
-- era_id NULL = written before any era row existed (the pre-close transition
-- window, mirroring gate_edges/scout_posts/shipyard_inventory). The PLANNING
-- reads are era-scoped so a universe reset never resurrects a dead era's
-- verdicts or placements; the probe_cap COUNT deliberately is not, because a
-- hull bought last era still exists and under-counting it would authorise
-- buying a replacement we already own (money guards fail closed, RULINGS #4).
--
-- No players foreign key — like the other operational-state rows (spend
-- reservations, scout posts, shipyard inventory), player_id is a plain scoped
-- column.

CREATE TABLE IF NOT EXISTS sensing_systems (
    player_id       BIGINT       NOT NULL,
    system_symbol   VARCHAR(50)  NOT NULL,
    verdict         VARCHAR(20)  NOT NULL DEFAULT 'PENDING',
    screened_at     TIMESTAMPTZ  NULL,
    uncharted_count BIGINT       NOT NULL DEFAULT 0,
    catalog_synced_at TIMESTAMPTZ NULL,
    seed_ship       VARCHAR(50)  NULL,
    seed_state      VARCHAR(20)  NULL,
    depth_credits   BIGINT       NOT NULL DEFAULT 0,
    era_id          BIGINT       NULL,
    updated_at      TIMESTAMPTZ  NOT NULL,
    PRIMARY KEY (player_id, system_symbol)
);

CREATE TABLE IF NOT EXISTS sensing_slots (
    player_id       BIGINT           NOT NULL,
    waypoint_symbol VARCHAR(50)      NOT NULL,
    system_symbol   VARCHAR(50)      NOT NULL,
    slot_kind       VARCHAR(10)      NOT NULL,
    state           VARCHAR(12)      NOT NULL,
    assigned_ship   VARCHAR(50)      NULL,
    purchase_yard   VARCHAR(50)      NULL,
    whitelist_goods TEXT             NOT NULL DEFAULT '[]',
    spread_ewma     DOUBLE PRECISION NOT NULL DEFAULT 0,
    last_scan_at    TIMESTAMPTZ      NULL,
    depth_credits   BIGINT           NOT NULL DEFAULT 0,
    era_id          BIGINT           NULL,
    updated_at      TIMESTAMPTZ      NOT NULL,
    PRIMARY KEY (player_id, waypoint_symbol)
);

-- Era scoping filters every planning read; the slot reads also filter by system
-- (per-system placement) and by state (the WANTED/QUEUED/PARKED work queues).
-- assigned_ship is indexed for the reverse lookup: which slot is this hull filling.
CREATE INDEX IF NOT EXISTS idx_sensing_systems_era_id ON sensing_systems(era_id);
CREATE INDEX IF NOT EXISTS idx_sensing_slots_system_symbol ON sensing_slots(system_symbol);
CREATE INDEX IF NOT EXISTS idx_sensing_slots_state ON sensing_slots(state);
CREATE INDEX IF NOT EXISTS idx_sensing_slots_assigned_ship ON sensing_slots(assigned_ship);
CREATE INDEX IF NOT EXISTS idx_sensing_slots_era_id ON sensing_slots(era_id);

COMMENT ON TABLE sensing_systems IS 'Per-system parked-probe screening verdict + charting seed, era-scoped (sp-k6v8z)';
COMMENT ON TABLE sensing_slots IS 'Per-waypoint parked-probe placement ledger, WANTED->QUEUED->BOUGHT->IN_TRANSIT->PARKED (sp-k6v8z)';
COMMENT ON COLUMN sensing_slots.assigned_ship IS 'NULL until a hull exists; with state IN (BOUGHT,IN_TRANSIT,PARKED) this is the probe_cap ownership read (sp-k6v8z)';
