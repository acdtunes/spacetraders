-- Backs ScoutPostModel.Dormant (internal/adapters/persistence/models.go): the sensing
-- coordinator's pressure-rotation bit. A dormant post's probe parks in place — its tour
-- sleeps instead of scanning — so shedding under API pressure costs zero API and moves
-- no hull. Migration-backed because boot AutoMigrate failure is NON-FATAL: without this,
-- a boot where AutoMigrate could not run would leave writes touching the column hitting
-- SQLSTATE 42703 (undefined_column).
--
-- Idempotent (ADD COLUMN IF NOT EXISTS): a no-op on any database where boot AutoMigrate
-- already added it. Type/default mirror the GORM tag exactly (BOOLEAN NOT NULL DEFAULT
-- FALSE) so a fresh database and an AutoMigrated one converge — and every existing post
-- reads as active (keeps scanning).

ALTER TABLE scout_posts
    ADD COLUMN IF NOT EXISTS dormant BOOLEAN NOT NULL DEFAULT FALSE;
