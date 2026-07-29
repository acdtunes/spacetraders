-- Rollback for 047 (sp-en5h7 price transposition).
--
-- ONLY RUN THIS IF YOU ARE ALSO ROLLING THE BINARY BACK, and read the ordering
-- note at the bottom before you do — the two must move together or the planner
-- is left reading corrupt rows with the corrected convention, which is the one
-- state this whole change exists to make impossible.
--
-- WHAT THIS UNDOES. Both tables, fully: the swap is its own inverse, and the
-- predicate inverts with it. 047.up moved rows where purchase_price < sell_price,
-- so this moves back exactly the rows where purchase_price > sell_price. Both
-- statements stay idempotent and re-runnable.
--
-- ============================================================================
-- ORDERING, MIRRORING 047.up EXACTLY:
--
--     DAEMON DOWN  ->  RUN THIS  ->  VERIFY  ->  START THE OLD BINARY
--
-- ============================================================================
--
-- The reason is the same one 047.up documents, in the same direction that file
-- calls undefended: the OLD binary's readers COMPENSATE for the transposition, so
-- corrected rows invert every quote for it and read as free money. The old binary
-- has NO crossed-quote guard to refuse them — those guards ship with the NEW
-- binary — so it would rank phantom lanes and spend on them.
--
-- Therefore the old binary must never observe corrected rows: put the daemon down
-- BEFORE running this, and start the old binary only AFTER it has completed. Do
-- not run this against a live new-binary daemon either; it would be writing
-- corrected rows while this un-corrects them.
--
-- VERIFY before starting the old binary — both must return zero:
--     SELECT count(*) FROM market_data          WHERE purchase_price > sell_price;
--     SELECT count(*) FROM market_price_history WHERE purchase_price > sell_price;
--
-- PREFER ROLLING FORWARD. This file exists for completeness, but re-corrupting
-- 296,741 rows to accommodate a binary is the worse trade in almost every case.
-- If the new binary must come out, consider leaving the DATA corrected and fixing
-- the binary instead: corrected data plus the new readers is the intended state,
-- and it is the only combination in which the crossed-quote guards protect you.
--
-- Re-runnable: yes.

BEGIN;

-- Put both tables back into the pre-047 (transposed) orientation, which is the
-- only orientation the old binary's compensating readers interpret correctly.
UPDATE market_data
SET purchase_price = sell_price,
    sell_price     = purchase_price
WHERE purchase_price > sell_price;

UPDATE market_price_history
SET purchase_price = sell_price,
    sell_price     = purchase_price
WHERE purchase_price > sell_price;

COMMIT;

COMMENT ON COLUMN market_data.purchase_price IS NULL;
COMMENT ON COLUMN market_data.sell_price IS NULL;
COMMENT ON COLUMN market_price_history.purchase_price IS NULL;
COMMENT ON COLUMN market_price_history.sell_price IS NULL;
