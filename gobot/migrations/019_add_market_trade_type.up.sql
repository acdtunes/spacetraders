-- Add trade_type column to market_data table
-- This captures whether a good is an EXPORT, IMPORT, or EXCHANGE at this market
-- EXPORT = market produces and sells this good (factory)
-- IMPORT = market consumes and buys this good (consumer)
-- EXCHANGE = market trades this good but doesn't produce/consume it

ALTER TABLE market_data ADD COLUMN trade_type VARCHAR(32);

-- Create index for finding export/import markets efficiently
CREATE INDEX idx_market_data_trade_type ON market_data(good_symbol, trade_type);
CREATE INDEX idx_market_data_export ON market_data(player_id, good_symbol, trade_type) WHERE trade_type = 'EXPORT';
CREATE INDEX idx_market_data_import ON market_data(player_id, good_symbol, trade_type) WHERE trade_type = 'IMPORT';
