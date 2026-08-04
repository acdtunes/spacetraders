-- Backs ManufacturingPipelineModel.DeliveryBuyFloor / DeliveryResumeFloor
-- (internal/adapters/persistence/models_manufacturing.go) — the gate DELIVERY fleet's
-- supply-anchored buy/pause thresholds.
--
-- WHY THESE LIVE ON THE PIPELINE ROW AND NOT IN config.yaml. The right hysteresis gap is
-- not knowable in advance: too narrow and the fleet chatters at the supply boundary, too
-- wide and it starves while usable stock sits at the factory. So the thresholds must be
-- adjustable in real time, without a restart — a pattern-C live knob, re-read per tick,
-- on the same `construction override` verb that already carries --min-supply and
-- --price-ceiling-mult.
--
-- The manufacturing coordinator is pattern B: its resolve*Config clears persisted keys and
-- re-injects from config.yaml on every build. A floor stored on a manufacturing config key
-- would therefore appear to tune, silently revert on the next daemon restart, and give no
-- indication it had. That is the same defect class as the inert prefer-buy override this
-- design removes, so the value is persisted on the pipeline row instead, where the same
-- durable path that already carries min_supply and max_workers keeps it.
--
-- NAMED DISTINCTLY FROM min_supply, deliberately. min_supply is the construction
-- pipeline's ADMISSION floor (whether a material is promoted to READY, per sp-yexq) — a
-- different decision at a different stage. Two supply thresholds with confusable names
-- would be its own opacity, which is the failure this whole design exists to correct.
--
-- DEFAULT '' MEANS UNSET, WHICH IS THE ARMED DEFAULT, NOT AN OFF SWITCH. The reader
-- resolves '' to MODERATE (buy) and HIGH (resume) at the point of use. There is no
-- disabled state: these are tunables, and the path they sit in always runs. Existing rows
-- therefore need no backfill — every pre-existing pipeline reads as "armed at the
-- defaults", which is exactly what it should have been all along.
--
-- Migration-backed because boot AutoMigrate failure is NON-FATAL: without this, a boot
-- where AutoMigrate could not run would leave the floor writes hitting SQLSTATE 42703
-- (undefined_column). manufacturing_pipelines is CREATE'd by an earlier migration, so it
-- is checkable by TestModelColumnsBackedByMigrations and model and migration are held in
-- lockstep.
--
-- APPLY THIS TO PRODUCTION BEFORE DEPLOYING THE DAEMON THAT WRITES THESE COLUMNS.
--
-- No index. Both columns are read only as part of the pipeline row the drain already loads
-- by primary key or by construction_site (both indexed), and written only on an operator
-- tune; an index would charge every pipeline write for a sort of a handful of rows.
--
-- Idempotent (ADD COLUMN IF NOT EXISTS): a no-op on any database where boot AutoMigrate
-- already added them. Type/size/default mirror the GORM tags exactly (VARCHAR(20) DEFAULT
-- '') so a fresh database and an AutoMigrated one converge.
--
-- NULLABLE, matching the tags, and deliberately so. The sibling is min_supply — the other
-- VARCHAR(20) supply-level column on this table, whose tag is the same shape as these two.
-- Migration 036 adds it as `min_supply VARCHAR(20) DEFAULT ''` in the SAME statement where
-- it adds `sequence_number INTEGER NOT NULL DEFAULT 0`, distinguishing the two precisely by
-- their GORM tags; these follow min_supply.
--
-- A NOT NULL here would not survive EITHER deploy order, which is why it is absent rather
-- than merely unnecessary. Boot-before-migrate: AutoMigrate has already created the columns
-- nullable, so ADD COLUMN IF NOT EXISTS is a silent no-op and the constraint never lands.
-- Migrate-before-boot: GORM's MigrateColumn compares DB nullability against field.NotNull,
-- disagrees, and issues ALTER COLUMN ... DROP NOT NULL — quietly undoing what this migration
-- just added. One migration, two schemas, decided by deploy order. Neither case is catchable
-- by TestModelColumnsBackedByMigrations, which is a set-membership check on column NAMES and
-- never compares type, size or nullability.

ALTER TABLE manufacturing_pipelines
    ADD COLUMN IF NOT EXISTS delivery_buy_floor VARCHAR(20) DEFAULT '';

ALTER TABLE manufacturing_pipelines
    ADD COLUMN IF NOT EXISTS delivery_resume_floor VARCHAR(20) DEFAULT '';

COMMENT ON COLUMN manufacturing_pipelines.delivery_buy_floor IS 'Gate DELIVERY fleet: buy while the terminal factory''s supply is at or above this level. '''' = unset, which the reader resolves to the armed default MODERATE. Distinct from min_supply, which is the pipeline''s READY-admission floor.';

COMMENT ON COLUMN manufacturing_pipelines.delivery_resume_floor IS 'Gate DELIVERY fleet: once paused, resume buying only when supply recovers to this level. '''' = unset, which the reader resolves to the armed default HIGH. Two thresholds, not one: a single threshold chatters at the boundary.';
