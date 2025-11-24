-- Fix timezone issue: Convert all timestamp columns to timestamptz
-- This ensures timestamps are stored with timezone information and comparisons work correctly

-- market_data table
ALTER TABLE market_data
ALTER COLUMN last_updated TYPE timestamp with time zone USING last_updated AT TIME ZONE 'America/Sao_Paulo';

-- container_logs table
ALTER TABLE container_logs
ALTER COLUMN timestamp TYPE timestamp with time zone USING timestamp AT TIME ZONE 'America/Sao_Paulo';

-- containers table
ALTER TABLE containers
ALTER COLUMN started_at TYPE timestamp with time zone USING started_at AT TIME ZONE 'America/Sao_Paulo',
ALTER COLUMN stopped_at TYPE timestamp with time zone USING stopped_at AT TIME ZONE 'America/Sao_Paulo';

-- ship_assignments table
ALTER TABLE ship_assignments
ALTER COLUMN assigned_at TYPE timestamp with time zone USING assigned_at AT TIME ZONE 'America/Sao_Paulo',
ALTER COLUMN released_at TYPE timestamp with time zone USING released_at AT TIME ZONE 'America/Sao_Paulo';

-- players table
ALTER TABLE players
ALTER COLUMN created_at TYPE timestamp with time zone USING created_at AT TIME ZONE 'America/Sao_Paulo',
ALTER COLUMN last_active TYPE timestamp with time zone USING last_active AT TIME ZONE 'America/Sao_Paulo';

-- system_graphs table
ALTER TABLE system_graphs
ALTER COLUMN created_at TYPE timestamp with time zone USING created_at AT TIME ZONE 'America/Sao_Paulo',
ALTER COLUMN updated_at TYPE timestamp with time zone USING updated_at AT TIME ZONE 'America/Sao_Paulo';

-- mining_operations table
ALTER TABLE mining_operations
ALTER COLUMN created_at TYPE timestamp with time zone USING created_at AT TIME ZONE 'America/Sao_Paulo',
ALTER COLUMN updated_at TYPE timestamp with time zone USING updated_at AT TIME ZONE 'America/Sao_Paulo',
ALTER COLUMN started_at TYPE timestamp with time zone USING started_at AT TIME ZONE 'America/Sao_Paulo',
ALTER COLUMN stopped_at TYPE timestamp with time zone USING stopped_at AT TIME ZONE 'America/Sao_Paulo';

-- goods_factories table
ALTER TABLE goods_factories
ALTER COLUMN created_at TYPE timestamp with time zone USING created_at AT TIME ZONE 'America/Sao_Paulo',
ALTER COLUMN updated_at TYPE timestamp with time zone USING updated_at AT TIME ZONE 'America/Sao_Paulo',
ALTER COLUMN started_at TYPE timestamp with time zone USING started_at AT TIME ZONE 'America/Sao_Paulo',
ALTER COLUMN completed_at TYPE timestamp with time zone USING completed_at AT TIME ZONE 'America/Sao_Paulo';

-- transactions table
ALTER TABLE transactions
ALTER COLUMN timestamp TYPE timestamp with time zone USING timestamp AT TIME ZONE 'America/Sao_Paulo',
ALTER COLUMN created_at TYPE timestamp with time zone USING created_at AT TIME ZONE 'America/Sao_Paulo';

-- arbitrage_execution_logs table
ALTER TABLE arbitrage_execution_logs
ALTER COLUMN executed_at TYPE timestamp with time zone USING executed_at AT TIME ZONE 'America/Sao_Paulo';

-- Migration complete: All timestamp columns now use timestamptz to fix 3-hour timezone phantom age bug
