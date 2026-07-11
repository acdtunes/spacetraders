-- Revert C1 (sp-64je) planner-visible-stock cost basis column.
ALTER TABLE storage_operations
    DROP COLUMN IF EXISTS cost_basis;
