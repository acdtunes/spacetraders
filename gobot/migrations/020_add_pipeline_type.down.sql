-- Remove pipeline_type column from manufacturing_pipelines table

DROP INDEX IF EXISTS idx_pipelines_player_type;
DROP INDEX IF EXISTS idx_pipelines_type_status;
DROP INDEX IF EXISTS idx_pipelines_type;

ALTER TABLE manufacturing_pipelines
DROP COLUMN pipeline_type;
