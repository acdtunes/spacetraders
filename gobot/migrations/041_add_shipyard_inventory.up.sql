-- sp-42ow: shipyard-inventory store — the persisted substrate of the scout
-- tour's piggybacked shipyard scan. One row per (player, waypoint, ship_type):
-- price, supply tier, last_scanned, era_id. Written by ReplaceScan (a
-- waypoint's whole row set swapped per scan — no duplicate rows, delisted
-- types disappear), read era-scoped by the reachable-yard ranking that feeds
-- the fleet autosizer's heavy yard-price signal.
--
-- GORM AutoMigrate at daemon boot also creates this table (models are the
-- source of truth, AutoMigrate is additive), but boot AutoMigrate is
-- best-effort and NON-FATAL, so per the column-drift gate's rationale this
-- migration is the durable, auditable record. Because it CREATE TABLEs, the
-- table becomes CHECKABLE by TestModelColumnsBackedByMigrations: model and
-- migration are held in lockstep from here on. Idempotent via IF NOT EXISTS —
-- safe to re-run and safe whether or not AutoMigrate already created it.
--
-- purchase_price 0 = ship type listed but unpriced at scan time (availability
-- known; never feeds a price guard). era_id NULL = scanned before any era row
-- existed (the pre-close transition window, mirroring gate_edges/scout_posts).
CREATE TABLE IF NOT EXISTS shipyard_inventory (
    player_id       BIGINT       NOT NULL,
    system_symbol   VARCHAR(64)  NOT NULL,
    waypoint_symbol VARCHAR(64)  NOT NULL,
    ship_type       VARCHAR(64)  NOT NULL,
    purchase_price  BIGINT       NOT NULL DEFAULT 0,
    supply          VARCHAR(32)  NOT NULL DEFAULT '',
    last_scanned    TIMESTAMPTZ  NOT NULL,
    era_id          BIGINT       NULL,
    PRIMARY KEY (player_id, waypoint_symbol, ship_type)
);

-- The reachable-yard ranking filters by system; era scoping filters every read.
CREATE INDEX IF NOT EXISTS idx_shipyard_inventory_system ON shipyard_inventory(system_symbol);
CREATE INDEX IF NOT EXISTS idx_shipyard_inventory_era ON shipyard_inventory(era_id);

COMMENT ON TABLE shipyard_inventory IS 'Scout-scanned shipyard ship-type availability + prices, era-scoped (sp-42ow)';
COMMENT ON COLUMN shipyard_inventory.purchase_price IS '0 = type listed but unpriced at scan time; never feeds a price guard (sp-42ow)';
