-- Remove trade_type column from market_data
DROP INDEX IF EXISTS idx_market_data_import;
DROP INDEX IF EXISTS idx_market_data_export;
DROP INDEX IF EXISTS idx_market_data_trade_type;
ALTER TABLE market_data DROP COLUMN IF EXISTS trade_type;
