-- sp-sdyo: back the manufacturing_pipelines.good_overrides column with a hand-written migration.
--
-- ManufacturingPipelineModel persists the per-good buy-gating override map
-- (internal/adapters/persistence/models.go: GoodOverrides) as a JSON string, so a per-good
-- construction sourcing-floor override survives a daemon bounce (RULINGS #2). Like every other
-- pipeline column it exists in production only via the daemon's additive boot AutoMigrate, and
-- AutoMigrate failure is NON-FATAL ("logs loudly and continues on the existing schema") — so a boot
-- where AutoMigrate could not run would leave a pipeline write hitting SQLSTATE 42703
-- (undefined_column). This makes the column migration-backed so it no longer depends on that
-- best-effort reconcile (the sp-s0mw column-drift gate).
--
-- Idempotent (ADD COLUMN IF NOT EXISTS): a no-op on any database where boot AutoMigrate already
-- added the column. The type/default mirror the GORM tag exactly so a fresh database and an
-- AutoMigrated one converge.

ALTER TABLE manufacturing_pipelines
    ADD COLUMN IF NOT EXISTS good_overrides TEXT DEFAULT '';
