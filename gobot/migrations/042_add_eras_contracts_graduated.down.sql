-- Rollback the contracts_graduated column from eras (sp-difa.1).

ALTER TABLE eras
    DROP COLUMN IF EXISTS contracts_graduated;
