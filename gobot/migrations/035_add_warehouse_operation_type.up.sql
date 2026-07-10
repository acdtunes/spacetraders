-- Extend storage_operations valid_operation_type constraint for warehouse operations
-- Lane C (sp-dchv) persists WAREHOUSE storage operations
-- (internal/domain/storage/operation.go: NewWarehouseOperation), but the CHECK
-- constraint was never migrated to accept it, so persisting the pre-positioning
-- warehouse's first operation failed with SQLSTATE 23514 (sp-cu42).

-- Update operation type constraint to include WAREHOUSE
-- First drop the existing constraint
ALTER TABLE storage_operations
    DROP CONSTRAINT IF EXISTS valid_operation_type;

-- Add new constraint with WAREHOUSE
-- (the extractor-fed types are carried over unchanged from migration 023)
ALTER TABLE storage_operations
    ADD CONSTRAINT valid_operation_type CHECK (operation_type IN (
        'GAS_SIPHON',
        'MINING',
        'CUSTOM',
        'WAREHOUSE'
    ));
