-- sp-en5h7 (P1 data corruption): market_data and market_price_history have held
-- their two price columns TRANSPOSED for the project's entire recorded history.
--
-- THE DEFECT. application/ship/market_scanner.go passed the API's two prices to
-- market.NewTradeGood(symbol, supply, activity, purchasePrice, sellPrice, ...) in
-- the wrong order — two adjacent ints, so the compiler could not see it, and the
-- domain type's own doc comment described the INVERSE convention, so the call
-- site looked correct to every reader who checked it against that doc. Verified
-- against the live API at X1-DA89-DC6A on 2026-07-29:
--
--   good        API purchase  API sell   stored purchase_price  stored sell_price
--   FUEL              72          68              68                   72
--   IRON             324         161             161                  324
--   MACHINERY       6334        3123            3123                 6334
--
-- Scope measured before this migration was written: 100% of rows, zero mixed
-- population, every era back to player 1 — market_data 30,657 rows,
-- market_price_history 266,084 rows, and NOT ONE correctly-oriented row in either.
-- That total absence of a normal row is what makes the predicate below both safe
-- and unambiguous. Measured cost of the swap itself: ~130ms for both tables.
--
-- Every row satisfies purchase_price < sell_price, which is impossible in a real
-- market: purchase_price is what WE PAY (the ask) and sell_price is what WE
-- RECEIVE (the bid), and the gap between them is the market's rake. That
-- signature — and the fact that NO row is correctly oriented — is what makes the
-- predicated swap below safe and re-runnable.
--
-- ============================================================================
-- BOTH TABLES ARE SWAPPED IN PLACE, PREDICATED ON THE CORRUPTION'S SIGNATURE.
-- ============================================================================
--
-- The swap is applied `WHERE purchase_price < sell_price`. That predicate is the
-- whole design, and it does three things at once:
--
--   1. It makes the swap IDEMPOTENT. Only rows still in the corrupt orientation
--      are touched; a corrected row (purchase_price > sell_price) is invisible to
--      it. Run this once, twice, or after a partial failure — same result. A blind
--      `SET a=b, b=a` is neither idempotent nor safe to re-run, and must not be
--      substituted for this.
--   2. It leaves EVIDENCE OF HAVING BEEN APPLIED, which a blind swap does not.
--      With no marker column anywhere on either table, `purchase_price >
--      sell_price` IS the marker: after this runs, a row violating it is either
--      un-migrated or freshly broken, and either way it is a real signal.
--   3. It bounds a partial failure. A crashed run leaves a MIXED population, and
--      a mixed population is still fully describable by the same predicate, so the
--      recovery action is simply "run it again".
--
-- market_data is SWAPPED rather than deleted, which was the earlier plan here.
-- Deleting it would also have been safe — it is a cache (UpsertMarketData deletes
-- and re-inserts a whole waypoint on every scan) and a missing row fails closed —
-- but with the daemon stopped for the swap there is nothing to gain from it and a
-- real cost: 2283 of the live player's 3343 rows are refreshed inside 15 minutes,
-- so deleting them buys a 15-to-60-minute window of degraded (lane-starved)
-- trading in exchange for avoiding a 130ms UPDATE. Preserve the rows.
--
-- The one argument that WOULD have forced deletion does not apply: a swap can in
-- principle LAUNDER a genuinely crossed quote into a normal-looking one, and the
-- new binary deliberately refuses crossed quotes (trading.GoodListing.IsCrossed,
-- plus the equivalent guards in tour_snapshot.go and run_arb_coordinator.go), so
-- laundering one would manufacture a tradeable input out of data the code declines
-- to trade. But there is no such row to launder: ZERO of the 30,657 rows are
-- correctly oriented, so every row the predicate matches is known-transposed. Were
-- that ever untrue — a genuinely mixed population — DELETE would become the
-- correct treatment for this table again, because it never fabricates a price.
--
-- market_price_history additionally CANNOT be deleted: it is append-only and
-- irreplaceable, 266,084 rows of series no scan can reproduce. It also feeds NO
-- automated money decision. Verified reader by reader: the only application-layer
-- consumer (manufacturing input_source_selector, via ProductionExecutor.priceHistory)
-- sits behind SetPriceHistoryReader, which NOTHING in production ever calls — the
-- field is nil and the read short-circuits. The factory input price ceiling
-- (sp-iv65/sp-a5j7) takes its baseline from EligibleSourceMedianAsk over
-- market_data, not from this table. Every remaining reader is operator-facing
-- reporting: the `market` CLI's history/volatility/stability commands and
-- HistoryRepository's GoodsStats / Summary JSON reports.
--
-- What the reports said before this ran: every spread, volatility figure and
-- median in GoodsStats/Summary measured the bid-ask gap BACKWARDS. Nothing spent
-- money on it, but no historical price analysis from before this migration should
-- be trusted without re-running it.
--
-- ============================================================================
-- ORDERING IS HARD AND NOT NEGOTIABLE:
--
--     DAEMON DOWN  ->  APPLY THIS  ->  VERIFY  ->  DEPLOY THE NEW BINARY
--
-- ============================================================================
--
-- Every other ordering loses money, for one reason: a row and a binary that
-- disagree about the convention produce a PHANTOM ARBITRAGE rather than an error.
-- The two failure directions are not symmetric in cause but they are identical in
-- consequence — an ask that appears to sit below a bid, i.e. free money, roughly
-- twice the true spread (the "inverted-margin trap" the trading package already
-- warns about).
--
--   Old binary + swapped rows  -> the old binary's readers COMPENSATE for the
--       transposition (that is why nothing ever failed). Feed them corrected rows
--       and every quote inverts. This is why the daemon must be DOWN before the
--       swap and must never come back up on the OLD binary afterwards.
--   New binary + un-swapped rows -> the corrected readers see the legacy rows'
--       ask below their bid.
--
-- THE SECOND DIRECTION IS DEFENDED IN CODE, THE FIRST IS NOT. The binary shipped
-- with this migration refuses a crossed quote at all three places one can enter a
-- money decision — RankSpreads (via trading.GoodListing.IsCrossed), the tour
-- snapshot (the solver bypasses RankSpreads), and run_arb_coordinator before its
-- margin subtraction (that subtraction is the one money guard a crossed quote
-- defeats by making the spread look BIGGER). So new-binary-first degrades to
-- lane-starved trading rather than to loss.
--
-- That is a safety net, NOT a licence to reorder. It does nothing for the
-- old-binary-plus-swapped-rows direction, because the old binary has no such
-- guard — the guards ship WITH the fix. Stop the daemon.
--
-- VERIFY between the swap and the deploy. Both queries must return zero:
--     SELECT count(*) FROM market_data          WHERE purchase_price < sell_price;
--     SELECT count(*) FROM market_price_history WHERE purchase_price < sell_price;
-- A non-zero count means the swap did not complete; re-run this file (it is
-- idempotent) rather than proceeding to the deploy.
--
-- NO BLIND WINDOW. Both tables keep every row, so the new binary starts against a
-- fully-populated, correctly-oriented cache and trading resumes at full lane
-- coverage immediately. The only outage is the daemon-down interval itself, and the
-- swap inside it measures ~130ms.
--
-- DELIBERATELY NOT DONE HERE: era-scoping these two tables. Neither carries
-- era_id, unlike gate_edges/sensing_slots/sensing_systems/shipyard_inventory/
-- system_coords/waypoints/scout_posts. It is a real inconsistency and it is a
-- separate bead — it neither caused nor prevents this defect, waypoint symbols
-- are fully regenerated per era with zero overlap (so a dead-era row cannot match
-- a live lookup), and backfilling era_id onto 266,084 history rows needs a
-- time->era boundary mapping that this table does not contain. Guessing it would
-- silently mislabel the series.
--
-- Re-runnable: yes, both statements, any number of times, in any deploy order.

-- Postgres evaluates the whole SET list against the row's OLD values, so this
-- single statement really does exchange the two columns; it needs no temporary.
--
-- The predicate IS the idempotency guarantee on both statements. Do not remove it
-- and do not "simplify" either statement to an unconditional UPDATE.

BEGIN;

UPDATE market_data
SET purchase_price = sell_price,
    sell_price     = purchase_price
WHERE purchase_price < sell_price;

UPDATE market_price_history
SET purchase_price = sell_price,
    sell_price     = purchase_price
WHERE purchase_price < sell_price;

COMMIT;

COMMENT ON COLUMN market_data.purchase_price IS 'What WE PAY per unit to buy from this market (the market ASK). Always EXCEEDS sell_price at a real market — the gap is the market rake. Held the sell price until sp-en5h7; rows written before that were swapped by migration 047.';
COMMENT ON COLUMN market_data.sell_price IS 'What WE RECEIVE per unit for selling to this market (the market BID). Always BELOW purchase_price. Held the purchase price until sp-en5h7; rows written before that were swapped by migration 047.';
COMMENT ON COLUMN market_price_history.purchase_price IS 'What WE PAID per unit to buy from this market (the market ASK). Always EXCEEDS sell_price. Rows recorded before sp-en5h7 held the sell price and were swapped by migration 047.';
COMMENT ON COLUMN market_price_history.sell_price IS 'What WE RECEIVED per unit for selling to this market (the market BID). Always BELOW purchase_price. Rows recorded before sp-en5h7 held the purchase price and were swapped by migration 047.';
