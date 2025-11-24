-- Revert timezone fix: Convert timestamptz back to timestamp without time zone
-- WARNING: This will lose timezone information

ALTER TABLE market_data
ALTER COLUMN last_updated TYPE timestamp without time zone;

ALTER TABLE container_logs
ALTER COLUMN timestamp TYPE timestamp without time zone;

ALTER TABLE containers
ALTER COLUMN started_at TYPE timestamp without time zone,
ALTER COLUMN stopped_at TYPE timestamp without time zone;

ALTER TABLE ship_assignments
ALTER COLUMN assigned_at TYPE timestamp without time zone,
ALTER COLUMN released_at TYPE timestamp without time zone;

ALTER TABLE players
ALTER COLUMN created_at TYPE timestamp without time zone,
ALTER COLUMN last_active TYPE timestamp without time zone;

ALTER TABLE system_graphs
ALTER COLUMN created_at TYPE timestamp without time zone,
ALTER COLUMN updated_at TYPE timestamp without time zone;

ALTER TABLE mining_operations
ALTER COLUMN created_at TYPE timestamp without time zone,
ALTER COLUMN updated_at TYPE timestamp without time zone,
ALTER COLUMN started_at TYPE timestamp without time zone,
ALTER COLUMN stopped_at TYPE timestamp without time zone;

ALTER TABLE goods_factories
ALTER COLUMN created_at TYPE timestamp without time zone,
ALTER COLUMN updated_at TYPE timestamp without time zone,
ALTER COLUMN started_at TYPE timestamp without time zone,
ALTER COLUMN completed_at TYPE timestamp without time zone;

ALTER TABLE transactions
ALTER COLUMN timestamp TYPE timestamp without time zone,
ALTER COLUMN created_at TYPE timestamp without time zone;

ALTER TABLE arbitrage_execution_logs
ALTER COLUMN executed_at TYPE timestamp without time zone;
