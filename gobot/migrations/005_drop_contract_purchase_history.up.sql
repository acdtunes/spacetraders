-- Migration: Drop contract_purchase_history table
-- Purchase history tracking has been removed in favor of distance-based fleet rebalancing
-- that discovers all markets in the system dynamically

DROP TABLE IF EXISTS contract_purchase_history;
