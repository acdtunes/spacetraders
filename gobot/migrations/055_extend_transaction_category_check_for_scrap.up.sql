-- Extend category_is_f_type to cover SCRAP_SHIP, the credits the API pays for a scrapped hull.
-- Without the branch the CASE returns NULL and category = f(type) stops being enforced for it.

-- The CASE restates every branch: a CHECK cannot be altered in place, and the drift gate
-- resolves "latest migration wins", so this file becomes the effective definition.

-- No existing row can carry SCRAP_SHIP, so VALIDATE is a formality: no rewrite, no backfill.
-- Idempotent: the leading DROP ... IF EXISTS makes a partial or repeated apply converge.

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
            WHEN 'CHART'              THEN 'CHARTING_REVENUE'
            WHEN 'SCRAP_SHIP'         THEN 'SHIP_INVESTMENTS'
        END
    ) NOT VALID;

ALTER TABLE transactions
    VALIDATE CONSTRAINT category_is_f_type;
