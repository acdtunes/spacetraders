-- sp-7djkm rollback. Revoke before DROP: a role still holding grants cannot be
-- dropped, and the default-privileges entry has to be withdrawn by the same
-- role that created it (`spacetraders`, which is who runs this file).

ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE SELECT ON TABLES FROM grafana_ro;
REVOKE SELECT ON ALL TABLES IN SCHEMA public FROM grafana_ro;
REVOKE USAGE ON SCHEMA public FROM grafana_ro;
REVOKE CONNECT ON DATABASE spacetraders FROM grafana_ro;

DROP ROLE IF EXISTS grafana_ro;
