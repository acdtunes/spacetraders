-- Rollback construction pipeline support

-- Drop index
DROP INDEX IF EXISTS idx_manufacturing_pipelines_construction_site;

-- Remove construction columns from manufacturing_tasks
ALTER TABLE manufacturing_tasks
    DROP COLUMN IF EXISTS construction_site;

-- Remove construction columns from manufacturing_pipelines
ALTER TABLE manufacturing_pipelines
    DROP COLUMN IF EXISTS supply_chain_depth,
    DROP COLUMN IF EXISTS materials,
    DROP COLUMN IF EXISTS construction_site;
