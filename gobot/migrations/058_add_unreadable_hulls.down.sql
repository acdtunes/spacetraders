-- Rollback the unreadable-hull repair ledger. Idempotent via IF EXISTS.

DROP INDEX IF EXISTS idx_unreadable_hulls_due;
DROP TABLE IF EXISTS unreadable_hulls;
