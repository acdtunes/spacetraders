-- Rollback the per-good buy-gating overrides column from manufacturing_pipelines (sp-sdyo).

ALTER TABLE manufacturing_pipelines
    DROP COLUMN IF EXISTS good_overrides;
