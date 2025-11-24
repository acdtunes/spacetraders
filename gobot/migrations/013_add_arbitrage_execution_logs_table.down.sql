-- Migration rollback: Drop arbitrage_execution_logs table

DROP INDEX IF EXISTS idx_arbitrage_logs_ship_symbol;
DROP INDEX IF EXISTS idx_arbitrage_logs_container_id;
DROP INDEX IF EXISTS idx_arbitrage_logs_success;
DROP INDEX IF EXISTS idx_arbitrage_logs_good_symbol;
DROP INDEX IF EXISTS idx_arbitrage_logs_executed_at;
DROP INDEX IF EXISTS idx_arbitrage_logs_player_id;

DROP TABLE IF EXISTS arbitrage_execution_logs;
