-- Rollback sequence_number and min_supply columns from manufacturing_pipelines

ALTER TABLE manufacturing_pipelines
    DROP COLUMN IF EXISTS sequence_number,
    DROP COLUMN IF EXISTS min_supply;
