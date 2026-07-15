-- Rollback the category = f(transaction_type) CHECK constraint (sp-ofjx).
-- Dropping the constraint restores the pre-migration state where category was enforced
-- only by the application (NewTransaction deriving it from transaction_type).

ALTER TABLE transactions
    DROP CONSTRAINT IF EXISTS category_is_f_type;
