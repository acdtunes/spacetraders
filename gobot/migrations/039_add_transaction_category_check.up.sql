-- sp-ofjx: enforce category = f(transaction_type) at the database (completes R1 / sp-bt6r).
--
-- category is a pure, deterministic relabel of transaction_type
-- (internal/domain/ledger/category.go: TypeToCategoryMap) -- NOT an independent axis.
-- NewTransaction is the sole writer and always derives category from type, so the stored
-- column has never diverged (audit 2026-07-14: 0/33833). This constraint makes that
-- invariant DB-enforced so a divergent category can never be stored, protecting the raw-SQL
-- consumers (briefing capex filter, detectors, history_repository) that still read the
-- stored category column directly. GUARD-SAFE: prod has exactly the 6 transaction_types
-- below, all covered by the CASE (verified live 2026-07-15).
--
-- Every WHEN branch mirrors ledger.TypeToCategoryMap exactly. The drift gate in
-- schema_category_constraint_drift_test.go fails if the two ever disagree: a missing branch
-- would SILENTLY stop enforcing the invariant for that type, because a Postgres CHECK holds
-- whenever its expression is NULL and the CASE returns NULL for an unlisted type. A wrong
-- branch would instead reject every write of that type with SQLSTATE 23514.
--
-- Lock profile (33k rows): ADD ... NOT VALID takes a brief ACCESS EXCLUSIVE (metadata only,
-- no scan); VALIDATE takes SHARE UPDATE EXCLUSIVE, which does NOT block reads or writes and
-- scans the existing rows in a single pass (0 violations expected). No table rewrite, no
-- backfill.
--
-- Idempotent / re-runnable: the leading DROP ... IF EXISTS makes a partial or repeated apply
-- (e.g. ADD succeeded but VALIDATE was interrupted) converge cleanly instead of erroring on
-- "constraint already exists".

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
