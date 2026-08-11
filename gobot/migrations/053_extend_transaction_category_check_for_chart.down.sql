-- Roll back to migration 049's 9-branch category_is_f_type CHECK.

-- WARNING: safe only once no CHART rows exist, or with the application reverted alongside.
-- The 9-branch CASE returns NULL for CHART, so its writes stop being ENFORCED, not rejected.

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
            WHEN 'JUMP'               THEN 'TRAVEL_COSTS'
            WHEN 'MODULE_INSTALL'     THEN 'SHIP_INVESTMENTS'
            WHEN 'MODULE_REMOVE'      THEN 'SHIP_INVESTMENTS'
        END
    ) NOT VALID;

ALTER TABLE transactions
    VALIDATE CONSTRAINT category_is_f_type;
