-- Remove performance metrics columns from goods_factories table

ALTER TABLE goods_factories
DROP COLUMN IF EXISTS ships_used,
DROP COLUMN IF EXISTS market_queries,
DROP COLUMN IF EXISTS parallel_levels,
DROP COLUMN IF EXISTS estimated_speedup;
