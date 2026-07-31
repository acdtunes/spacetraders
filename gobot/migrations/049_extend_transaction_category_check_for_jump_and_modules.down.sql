-- Roll back to migration 039's 6-branch category_is_f_type CHECK (sp-shq63).
--
-- WARNING: this rollback is only safe once no JUMP / MODULE_INSTALL / MODULE_REMOVE rows
-- exist, or with the application reverted alongside it. The 6-branch CASE returns NULL
-- for those types, so their writes are not REJECTED — they simply stop being enforced,
-- restoring exactly the silent non-enforcement 049 was written to close. Reverting the
-- schema without reverting the code therefore loses the guarantee, not the rows.

ALTER TABLE transactions
    DROP CONSTRAINT IF EXISTS category_is_f_type;

ALTER TABLE transactions
    ADD CONSTRAINT category_is_f_type CHECK (
        category = CASE transaction_type
            WHEN 'REFUEL'             THEN 'FUEL_COSTS'
            WHEN 'PURCHASE_CARGO'     THEN 'TRADING_COSTS'
            WHEN 'SELL_CARGO'         THEN 'TRADING_REVENUE'
            WHEN 'PURCHASE_SHIP'      THEN 'SHIP_INVESTMENTS'
            WHEN 'CONTRACT_ACCEPTED'  THEN 'CONTRACT_REVENUE'
            WHEN 'CONTRACT_FULFILLED' THEN 'CONTRACT_REVENUE'
        END
    ) NOT VALID;

ALTER TABLE transactions
    VALIDATE CONSTRAINT category_is_f_type;
