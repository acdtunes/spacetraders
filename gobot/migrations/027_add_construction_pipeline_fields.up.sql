-- Add construction pipeline support to manufacturing tables

-- Add construction-specific fields to manufacturing_pipelines
ALTER TABLE manufacturing_pipelines
    ADD COLUMN IF NOT EXISTS construction_site VARCHAR(64) NULL,
    ADD COLUMN IF NOT EXISTS materials JSONB DEFAULT '[]',
    ADD COLUMN IF NOT EXISTS supply_chain_depth INTEGER DEFAULT 0;

-- Add construction_site to manufacturing_tasks for DELIVER_TO_CONSTRUCTION tasks
ALTER TABLE manufacturing_tasks
    ADD COLUMN IF NOT EXISTS construction_site VARCHAR(64) NULL;

-- Add index for querying pipelines by construction site
CREATE INDEX IF NOT EXISTS idx_manufacturing_pipelines_construction_site
    ON manufacturing_pipelines(construction_site)
    WHERE construction_site IS NOT NULL;

-- Add comments for documentation
COMMENT ON COLUMN manufacturing_pipelines.construction_site IS 'Waypoint symbol of the construction site (e.g., X1-FB5-I61)';
COMMENT ON COLUMN manufacturing_pipelines.materials IS 'JSONB array of ConstructionMaterialTarget objects with tradeSymbol, targetQuantity, deliveredQuantity';
COMMENT ON COLUMN manufacturing_pipelines.supply_chain_depth IS 'How deep in supply chain: 0=full chain, 1=raw only, 2=intermediate only';
COMMENT ON COLUMN manufacturing_tasks.construction_site IS 'For DELIVER_TO_CONSTRUCTION tasks: the construction site waypoint symbol';
