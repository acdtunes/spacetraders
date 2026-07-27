-- Rollback the parked-probe sensing ledger (sp-k6v8z). Dropping the tables drops
-- their indexes with them. Idempotent via IF EXISTS.

DROP TABLE IF EXISTS sensing_slots;
DROP TABLE IF EXISTS sensing_systems;
