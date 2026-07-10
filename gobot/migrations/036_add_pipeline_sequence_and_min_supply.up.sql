-- Catch-up migration (sp-s0mw): back the manufacturing_pipelines.sequence_number
-- and min_supply columns with a hand-written migration.
--
-- ManufacturingPipelineModel persists both columns
-- (internal/adapters/persistence/models.go: SequenceNumber, MinSupply) but no
-- migration ever created them. In production they existed only because the
-- daemon's boot AutoMigrate is additive — and AutoMigrate failure is NON-FATAL
-- ("logs loudly and continues on the existing schema", cmd/spacetraders-daemon/
-- main.go), so a boot where AutoMigrate could not run would leave a pipeline
-- write hitting SQLSTATE 42703 (undefined_column) on these columns. This makes
-- them migration-backed so they no longer depend on that best-effort reconcile.
--
-- Idempotent (ADD COLUMN IF NOT EXISTS): a no-op on any database where boot
-- AutoMigrate already added the columns. Column types/defaults mirror the GORM
-- tags exactly so a fresh database and an AutoMigrated one converge.

ALTER TABLE manufacturing_pipelines
    ADD COLUMN IF NOT EXISTS sequence_number INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS min_supply VARCHAR(20) DEFAULT '';
