-- sp-shq63: extend category_is_f_type to cover the three credit-decreasing types that
-- were charged by the API but never recorded — JUMP (gate fee) and MODULE_INSTALL /
-- MODULE_REMOVE (shipyard modification fee).
--
-- WHY THIS MIGRATION IS MANDATORY, NOT COSMETIC. Migration 039 encodes
-- ledger.TypeToCategoryMap as CHECK ( category = CASE transaction_type WHEN ... END ).
-- A Postgres CHECK holds whenever its expression is NULL, and a CASE with no matching
-- WHEN returns NULL — so adding a 7th/8th/9th type to the Go map WITHOUT extending the
-- CASE does not reject the writes, it SILENTLY stops enforcing category = f(type) for
-- exactly those types. That is the treacherous direction 039's own header calls out, and
-- schema_category_constraint_drift_test.go fails the build until this file exists.
--
-- The CASE below mirrors ledger.TypeToCategoryMap in full (9 branches). Every branch is
-- restated rather than appended because a CHECK constraint cannot be altered in place;
-- the drift gate resolves "latest migration wins", so this file — not 039 — becomes the
-- effective definition and must therefore be complete.
--
-- Lock profile (134k rows): ADD ... NOT VALID takes a brief ACCESS EXCLUSIVE (metadata
-- only, no scan); VALIDATE takes SHARE UPDATE EXCLUSIVE, which does NOT block reads or
-- writes and scans existing rows in a single pass. The three new types have never been
-- written (that is the defect), so no existing row can violate the new branches and the
-- validation is a formality. No table rewrite, no backfill.
--
-- BACKFILL IS DELIBERATELY NOT ATTEMPTED. The historical fees were discarded before
-- anything could observe them; the per-jump amounts are unrecoverable. Inventing them
-- from the balance gaps would write guessed numbers into a money-guard source. The
-- ledger stays honestly incomplete for the past and becomes complete going forward.
--
-- Idempotent / re-runnable: the leading DROP ... IF EXISTS makes a partial or repeated
-- apply converge cleanly instead of erroring on "constraint already exists".

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
