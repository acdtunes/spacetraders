-- sp-42ow rollback: drop the shipyard-inventory store. Pure cache data — the
-- scout tour's piggybacked scans repopulate it within one tour cycle, so the
-- drop loses nothing durable.
DROP INDEX IF EXISTS idx_shipyard_inventory_era;
DROP INDEX IF EXISTS idx_shipyard_inventory_system;
DROP TABLE IF EXISTS shipyard_inventory;
