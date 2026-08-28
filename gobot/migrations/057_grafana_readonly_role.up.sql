-- sp-7djkm: a dedicated read-only Postgres role for Grafana, carrying its own
-- hard resource ceilings, so a runaway dashboard panel can never starve the
-- tour planner that shares this instance.
--
-- WHY A SEPARATE ROLE AND NOT A GLOBAL SETTING: Grafana currently connects as
-- `spacetraders` — the same superuser the daemon writes with. Any ceiling set
-- globally (or on that role) would also bind the daemon, whose migrations,
-- AutoMigrate and long absorption writes legitimately run past any timeout
-- short enough to be useful against a dashboard. The ceiling has to be scoped
-- to the reader, which means the reader needs its own role.
--
-- THE LIMITS ARE SIZED FROM MEASUREMENT, not from taste. Every panel in the
-- nine provisioned dashboards was costed with EXPLAIN (ANALYZE, BUFFERS):
--   - the slowest healthy panel runs 583 ms and spills 30 MB of temp files;
--   - two panels on trading-hulls.json (Gross Margin, Per-Hull P&L) run
--     ~20-25 s each and spill 2.77 GB each, because their recursive `walk`
--     CTE re-scans the whole transactions history at every recursion level.
-- That leaves a wide empty band between 0.6 s and 20 s with nothing in it, and
-- the ceiling belongs in the middle of that band rather than at either edge.
-- 10s is 17x the slowest healthy panel — ample room for it to grow — while
-- still stopping both runaways deterministically. It was NOT set at 20s: the
-- measured runaways land either side of that line depending on cache state, so
-- a 20s ceiling makes them flap between working and failing, which is the one
-- outcome worse than a clean failure. temp_file_limit is sized the same way:
-- 1GB is 34x the largest healthy spill and 2.8x below the runaway spill.
--
-- The two runaway panels therefore now fail fast and visibly instead of
-- saturating the instance for 25 seconds at a time — which is the trade this
-- migration exists to make. Their SQL is tracked separately for a proper
-- bounding fix.
--
-- default_transaction_read_only is belt-and-braces on top of SELECT-only
-- grants: the grants are the real guarantee, this makes an accidental write
-- fail with a clear message rather than a permission error.
--
-- The role is created WITHOUT a password here. This repository is public, so a
-- literal secret in a tracked migration is published the moment it merges. Set
-- the password out of band after applying:
--   ALTER ROLE grafana_ro PASSWORD '<secret>';
-- and give Grafana the same value through GRAFANA_RO_PASSWORD in its
-- environment, which the datasource provisioning reads.

CREATE ROLE grafana_ro LOGIN;

GRANT CONNECT ON DATABASE spacetraders TO grafana_ro;
GRANT USAGE ON SCHEMA public TO grafana_ro;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO grafana_ro;

-- The daemon creates tables as `spacetraders` (migrations here, plus GORM
-- AutoMigrate at boot). Without this, every table added after today would be
-- invisible to Grafana and its panels would fail with "permission denied".
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO grafana_ro;

ALTER ROLE grafana_ro SET statement_timeout = '10s';
ALTER ROLE grafana_ro SET temp_file_limit = '1GB';
ALTER ROLE grafana_ro SET work_mem = '4MB';
ALTER ROLE grafana_ro SET lock_timeout = '5s';
ALTER ROLE grafana_ro SET idle_in_transaction_session_timeout = '60s';
ALTER ROLE grafana_ro SET default_transaction_read_only = on;

COMMENT ON ROLE grafana_ro IS
    'Read-only dashboard reader (sp-7djkm). Ceilings: statement_timeout 10s, temp_file_limit 1GB, work_mem 4MB. Never grant write.';
