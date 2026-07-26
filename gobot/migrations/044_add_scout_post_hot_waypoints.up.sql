-- Backs ScoutPostModel.HotWaypoints (internal/adapters/persistence/models.go): the
-- JSON-encoded stage-2 circuit of a standing scout post — the system's market
-- waypoints dealing in ≥1 whitelisted good, stamped sorted by the probe-sensing
-- coordinator. NULL/empty ⇒ stage 1: the tour flies its full circuit, so every
-- pre-existing row keeps full-circuit scanning. Migration-backed because boot
-- AutoMigrate failure is NON-FATAL: without this, a boot where AutoMigrate could not
-- run would leave writes touching the column hitting SQLSTATE 42703
-- (undefined_column).
--
-- Idempotent (ADD COLUMN IF NOT EXISTS): a no-op on any database where boot
-- AutoMigrate already added it. Nullable TEXT mirrors the GORM mapping (like
-- primary_partition/extra_slots) so a fresh database and an AutoMigrated one
-- converge.

ALTER TABLE scout_posts
    ADD COLUMN IF NOT EXISTS hot_waypoints TEXT;
