-- Rollback migration: Remove mining operations tables

DROP TABLE IF EXISTS cargo_transfer_queue;
DROP TABLE IF EXISTS mining_operations;
