-- Migration: Add arbitrage_execution_logs table
-- This migration adds support for tracking arbitrage execution results for ML training

CREATE TABLE IF NOT EXISTS arbitrage_execution_logs (
    id SERIAL PRIMARY KEY,

    -- Execution metadata
    container_id VARCHAR(255) NOT NULL,
    ship_symbol VARCHAR(50) NOT NULL,
    player_id INT NOT NULL,
    executed_at TIMESTAMPTZ NOT NULL,
    success BOOLEAN NOT NULL,
    error_message TEXT,

    -- Opportunity features (at decision time)
    good_symbol VARCHAR(50) NOT NULL,
    buy_market VARCHAR(50) NOT NULL,
    sell_market VARCHAR(50) NOT NULL,
    buy_price INT NOT NULL,
    sell_price INT NOT NULL,
    profit_margin DECIMAL(10, 2) NOT NULL,
    distance DECIMAL(10, 2) NOT NULL,
    estimated_profit INT NOT NULL,
    buy_supply VARCHAR(20),
    sell_activity VARCHAR(20),
    current_score DECIMAL(10, 2),

    -- Ship state (at decision time)
    cargo_capacity INT NOT NULL,
    cargo_used INT NOT NULL,
    fuel_current INT NOT NULL,
    fuel_capacity INT NOT NULL,
    current_location VARCHAR(50),

    -- Execution results
    actual_net_profit INT,
    actual_duration_seconds INT,
    fuel_consumed INT,
    units_purchased INT,
    units_sold INT,
    purchase_cost INT,
    sale_revenue INT,

    -- Derived metrics (computed)
    profit_per_second DECIMAL(10, 4),
    profit_per_unit DECIMAL(10, 2),
    margin_accuracy DECIMAL(10, 2),

    -- Foreign key
    FOREIGN KEY (player_id) REFERENCES players(id) ON DELETE CASCADE
);

-- Indexes for querying
CREATE INDEX IF NOT EXISTS idx_arbitrage_logs_player_id ON arbitrage_execution_logs(player_id);
CREATE INDEX IF NOT EXISTS idx_arbitrage_logs_executed_at ON arbitrage_execution_logs(executed_at DESC);
CREATE INDEX IF NOT EXISTS idx_arbitrage_logs_good_symbol ON arbitrage_execution_logs(good_symbol);
CREATE INDEX IF NOT EXISTS idx_arbitrage_logs_success ON arbitrage_execution_logs(success);
CREATE INDEX IF NOT EXISTS idx_arbitrage_logs_container_id ON arbitrage_execution_logs(container_id);
CREATE INDEX IF NOT EXISTS idx_arbitrage_logs_ship_symbol ON arbitrage_execution_logs(ship_symbol);
