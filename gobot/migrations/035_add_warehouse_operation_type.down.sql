-- Revert operation type constraint to the pre-warehouse set (migration 023)

ALTER TABLE storage_operations
    DROP CONSTRAINT IF EXISTS valid_operation_type;

ALTER TABLE storage_operations
    ADD CONSTRAINT valid_operation_type CHECK (operation_type IN (
        'GAS_SIPHON',
        'MINING',
        'CUSTOM'
    ));
