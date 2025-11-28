-- Add pipeline_type column to manufacturing_pipelines table
-- FABRICATION pipelines are counted toward max_pipelines limit
-- COLLECTION pipelines are unlimited

ALTER TABLE manufacturing_pipelines
ADD COLUMN pipeline_type VARCHAR(20) NOT NULL DEFAULT 'FABRICATION';

-- Create indexes for efficient querying by type
CREATE INDEX idx_pipelines_type ON manufacturing_pipelines(pipeline_type);
CREATE INDEX idx_pipelines_type_status ON manufacturing_pipelines(pipeline_type, status);
CREATE INDEX idx_pipelines_player_type ON manufacturing_pipelines(player_id, pipeline_type);
