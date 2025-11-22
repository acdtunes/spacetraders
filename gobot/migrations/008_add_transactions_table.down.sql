-- Rollback: Remove transactions table

-- Drop the transactions table and its indexes
-- PostgreSQL automatically drops indexes when the table is dropped
DROP TABLE IF EXISTS transactions;
