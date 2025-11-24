-- Add price drift tracking columns to arbitrage_execution_logs
-- These columns capture prices at three critical stages to measure price drift over time

ALTER TABLE arbitrage_execution_logs
ADD COLUMN buy_price_at_validation INTEGER,
ADD COLUMN sell_price_at_validation INTEGER,
ADD COLUMN buy_price_actual INTEGER,
ADD COLUMN sell_price_actual INTEGER;

-- Add comments for documentation
COMMENT ON COLUMN arbitrage_execution_logs.buy_price_at_validation IS 'Buy market SellPrice at validation time (SAFETY CHECK 3A)';
COMMENT ON COLUMN arbitrage_execution_logs.sell_price_at_validation IS 'Sell market PurchasePrice at validation time (SAFETY CHECK 3B)';
COMMENT ON COLUMN arbitrage_execution_logs.buy_price_actual IS 'Actual price paid per unit during purchase';
COMMENT ON COLUMN arbitrage_execution_logs.sell_price_actual IS 'Actual price received per unit during sale';
