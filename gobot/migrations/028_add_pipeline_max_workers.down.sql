-- Rollback max_workers column from manufacturing_pipelines

ALTER TABLE manufacturing_pipelines
    DROP COLUMN IF EXISTS max_workers;
