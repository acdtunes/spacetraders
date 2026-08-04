-- Rollback the gate delivery fleet's buy/resume floors from manufacturing_pipelines.
--
-- DROPPING THESE LOSES EVERY OPERATOR TUNE, and the fleet reverts to the armed defaults
-- (MODERATE buy, HIGH resume) rather than stopping — the delivery path always runs, so
-- there is no stall risk here, only a loss of tuning. If the fleet was tuned away from the
-- defaults because the defaults chattered or starved it in this era's markets, that
-- diagnosis is lost with the columns; record the values before rolling back.
--
-- Requires rolling the code back too: a binary that still writes these columns would hit
-- SQLSTATE 42703 (undefined_column) on the first tune. Nothing else reads them, and no
-- pipeline state, material progress or hull assignment is touched by the drop.

ALTER TABLE manufacturing_pipelines
    DROP COLUMN IF EXISTS delivery_buy_floor;

ALTER TABLE manufacturing_pipelines
    DROP COLUMN IF EXISTS delivery_resume_floor;
