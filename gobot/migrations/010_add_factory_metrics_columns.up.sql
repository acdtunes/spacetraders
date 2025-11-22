-- Add performance metrics columns to goods_factories table

ALTER TABLE goods_factories
ADD COLUMN IF NOT EXISTS ships_used INTEGER DEFAULT 0,
ADD COLUMN IF NOT EXISTS market_queries INTEGER DEFAULT 0,
ADD COLUMN IF NOT EXISTS parallel_levels INTEGER DEFAULT 0,
ADD COLUMN IF NOT EXISTS estimated_speedup DOUBLE PRECISION DEFAULT 0;

-- Add comments for documentation
COMMENT ON COLUMN goods_factories.ships_used IS 'Number of ships utilized during production';
COMMENT ON COLUMN goods_factories.market_queries IS 'Number of market queries performed';
COMMENT ON COLUMN goods_factories.parallel_levels IS 'Number of parallel execution levels';
COMMENT ON COLUMN goods_factories.estimated_speedup IS 'Estimated speedup factor from parallelization';
