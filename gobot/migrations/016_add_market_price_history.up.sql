-- Add market_price_history table for tracking price changes over time
-- This enables volatility analysis and ML-based arbitrage optimization

CREATE TABLE market_price_history (
    id                SERIAL PRIMARY KEY,
    waypoint_symbol   VARCHAR(50) NOT NULL,
    good_symbol       VARCHAR(100) NOT NULL,
    player_id         INTEGER NOT NULL REFERENCES players(id) ON DELETE CASCADE,

    -- Price data
    purchase_price    INTEGER NOT NULL,  -- What market pays us to sell
    sell_price        INTEGER NOT NULL,  -- What market charges us to buy

    -- Market conditions
    supply            VARCHAR(20),       -- ABUNDANT, MODERATE, SCARCE, etc.
    activity          VARCHAR(20),       -- GROWING, RESTRICTED, etc.
    trade_volume      INTEGER NOT NULL,

    -- Timestamp
    recorded_at       TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),

    -- Foreign key constraint
    CONSTRAINT fk_market_history_player FOREIGN KEY (player_id)
        REFERENCES players(id) ON UPDATE CASCADE ON DELETE CASCADE
);

-- Index for time-series queries on specific market/good pairs
CREATE INDEX idx_market_history_waypoint_good_time
    ON market_price_history(waypoint_symbol, good_symbol, recorded_at DESC);

-- Index for good-specific volatility analysis
CREATE INDEX idx_market_history_good_time
    ON market_price_history(good_symbol, recorded_at DESC);

-- Index for recent history queries
CREATE INDEX idx_market_history_recorded_at
    ON market_price_history(recorded_at DESC);

-- Index for player-specific queries
CREATE INDEX idx_market_history_player
    ON market_price_history(player_id);
