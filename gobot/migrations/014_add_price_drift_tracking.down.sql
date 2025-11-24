-- Remove price drift tracking columns from arbitrage_execution_logs

ALTER TABLE arbitrage_execution_logs
DROP COLUMN IF EXISTS buy_price_at_validation,
DROP COLUMN IF EXISTS sell_price_at_validation,
DROP COLUMN IF EXISTS buy_price_actual,
DROP COLUMN IF EXISTS sell_price_actual;
