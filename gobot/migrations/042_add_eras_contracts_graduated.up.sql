-- sp-difa.1: back the eras.contracts_graduated column with a hand-written migration.
--
-- EraModel persists ContractsGraduated (internal/adapters/persistence/models.go) — the durable,
-- per-player, era-scoped MANUAL contract-graduation flag. When SET, the boot-standing bootstrap
-- coordinator and the capacity reconciler stop starting/maintaining the contract-delivery op, durably
-- across daemon restarts (the fix for a manual decommission being undone by a boot-standing relaunch).
-- Without a migration the column would exist only via the daemon's additive boot AutoMigrate, and
-- AutoMigrate failure is NON-FATAL ("logs loudly and continues on the existing schema",
-- cmd/spacetraders-daemon/main.go) — so a boot where AutoMigrate could not run would leave a write
-- touching this column hitting SQLSTATE 42703 (undefined_column). This makes it migration-backed.
--
-- Idempotent (ADD COLUMN IF NOT EXISTS): a no-op on any database where boot AutoMigrate already added
-- it. Type/default mirror the GORM tag exactly (BOOLEAN NOT NULL DEFAULT FALSE) so a fresh database and
-- an AutoMigrated one converge — and every existing era row reads UN-graduated (contracts run).

ALTER TABLE eras
    ADD COLUMN IF NOT EXISTS contracts_graduated BOOLEAN NOT NULL DEFAULT FALSE;
