-- buy_side_fill_analysis.sql — sp-1zho6 diagnosis (2026-07-29)
--
-- Asks whether hulls departing half-full on the BUY side is a defect or an optimum.
-- Read-only. Player 5, era 5.
--
-- HEADLINE: ~50% of buy phases visit exactly ONE market and depart at 56% fill, and the planner
-- is roughly RIGHT to do so. On cycles that actually closed out their load, net cr/hr is FLAT
-- across 1-3 buy markets (121k / 113k / 166k) while profit per cycle triples. The only real prize
-- is amortising between-cycle overhead, measured here at ~15% of hull time — so the ceiling on
-- this lever is ~+15%, not the ~1.5x the bead estimated.
--
-- METHODOLOGY WARNING — THE ONE THING THAT MATTERS IN THIS FILE.
-- tour_id is the CONTAINER, not a single tour: one tour_id spans many buy/sell cycles over hours.
-- Grouping by tour_id yields hull "fill" up to 485% and is meaningless. Cycles are therefore
-- segmented gaps-and-islands style: a new cycle starts at each buy that follows a sell.
-- MORE IMPORTANTLY: cycles whose sells fall outside the window book the BUYS but not the matching
-- SELLS, so their profit is understated — and that bias grows with how much was bought. Without
-- the closure filter the data appears to show 1-market phases earning 223k/hr and 4+ market
-- phases earning MINUS 69k/hr, i.e. "chaining markets destroys value". That is a pure artifact.
-- Restricting to cycles where |bought-sold|/bought < 0.10 REVERSES it. Only 94 of 323 phases (29%)
-- survive that filter, so treat the closed-cycle table as small-n and note the survivorship risk:
-- the excluded 71% skew toward heavy buying.
--
-- Run:  PGPASSWORD=dev_password psql -h localhost -p 5432 -U spacetraders -d spacetraders \
--         -f gobot/scripts/buy_side_fill_analysis.sql
--
-- market_data is INVERTED (purchase_price = the bid WE RECEIVE, sell_price = the ask WE PAY), but
-- this file reads tour_leg_telemetry.realized_unit_price, which is already side-correct per leg.

\set ON_ERROR_STOP on

CREATE TEMP VIEW v_cycles AS
WITH legs AS (
  SELECT tour_id, ship_symbol, leg_index, waypoint, is_buy,
         realized_units, realized_unit_price, realized_at,
         lag(is_buy) OVER (PARTITION BY tour_id ORDER BY leg_index) AS prev_buy
  FROM tour_leg_telemetry
  WHERE player_id = 5 AND realized_units > 0
    AND planned_at > now() - interval '24 hours'),
marked AS (
  SELECT *, sum(CASE WHEN is_buy AND coalesce(prev_buy, false) = false THEN 1 ELSE 0 END)
              OVER (PARTITION BY tour_id ORDER BY leg_index
                    ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS cycle
  FROM legs)
SELECT tour_id, ship_symbol, cycle,
       count(DISTINCT waypoint) FILTER (WHERE is_buy)              AS buy_markets,
       sum(realized_units)      FILTER (WHERE is_buy)              AS bought,
       sum(realized_units)      FILTER (WHERE NOT is_buy)          AS sold,
       sum(realized_units * realized_unit_price) FILTER (WHERE is_buy) AS spend,
       sum(CASE WHEN is_buy THEN -realized_units * realized_unit_price
                ELSE realized_units * realized_unit_price END)     AS net_profit,
       EXTRACT(epoch FROM (max(realized_at) - min(realized_at)))/3600.0 AS hours
FROM marked WHERE cycle > 0 GROUP BY 1, 2, 3;

\echo '== (a) Is 50% fill "one market and stop", or "several and still short"? =='
SELECT c.buy_markets AS distinct_buy_markets, count(*) AS buy_phases,
       round(avg(s.cargo_capacity)) AS avg_hold,
       round(avg(c.bought))         AS avg_units_loaded,
       round(100.0*avg(LEAST(c.bought, s.cargo_capacity)::numeric
                       / NULLIF(s.cargo_capacity,0)), 1) AS pct_fill
FROM v_cycles c JOIN ships s ON s.ship_symbol = c.ship_symbol AND s.player_id = 5
WHERE c.bought > 0 GROUP BY 1 ORDER BY 1;
-- Result: 1 market -> 56% fill (161 phases, ~half of all). 2 -> 89%. 3+ -> ~99%.
-- The planner CAN chain and does; one-and-stop is a choice, not an inability.

\echo '== (c) UNFILTERED rate by market count — THE ARTIFACT. Do not report this table. =='
SELECT buy_markets, count(*) AS phases, round(avg(bought)) AS avg_units,
       round(sum(net_profit)/NULLIF(sum(hours),0)) AS net_cr_per_hr_MISLEADING
FROM v_cycles WHERE bought > 0 AND hours > 0.01 GROUP BY 1 ORDER BY 1;

\echo '== (c) CLOSED-OUT cycles only (|bought-sold|/bought < 0.10) — the honest comparison =='
SELECT buy_markets, count(*) AS closed_phases,
       round(avg(bought))     AS avg_units,
       round(avg(net_profit)) AS avg_net_profit,
       round(avg(hours)::numeric, 2) AS avg_hours,
       round(sum(net_profit)/NULLIF(sum(hours),0)) AS net_cr_per_hr
FROM v_cycles
WHERE bought > 0 AND sold > 0 AND hours > 0.01
  AND abs(bought - sold)::numeric / bought < 0.10
GROUP BY 1 ORDER BY 1;
-- Result: profit/cycle triples (78k -> 133k -> 233k) while cr/hr stays FLAT (121k/113k/166k).
-- A flat rate is what an optimiser sitting on its optimum looks like.

\echo '== (c) Between-cycle overhead — the ONLY place a flat-rate gain can come from =='
WITH legs AS (
  SELECT tour_id, leg_index, is_buy, realized_at,
         lag(is_buy) OVER (PARTITION BY tour_id ORDER BY leg_index) AS prev_buy
  FROM tour_leg_telemetry
  WHERE player_id = 5 AND realized_units > 0 AND realized_at IS NOT NULL
    AND planned_at > now() - interval '24 hours'),
marked AS (
  SELECT *, sum(CASE WHEN is_buy AND coalesce(prev_buy,false) = false THEN 1 ELSE 0 END)
              OVER (PARTITION BY tour_id ORDER BY leg_index
                    ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS cycle
  FROM legs),
bounds AS (
  SELECT tour_id, cycle, min(realized_at) AS t0, max(realized_at) AS t1
  FROM marked WHERE cycle > 0 GROUP BY 1, 2),
g AS (
  SELECT EXTRACT(epoch FROM (t1 - t0))/3600.0 AS in_cycle_h,
         EXTRACT(epoch FROM (t0 - lag(t1) OVER (PARTITION BY tour_id ORDER BY cycle)))/3600.0 AS gap_h
  FROM bounds)
SELECT count(*) FILTER (WHERE gap_h IS NOT NULL) AS transitions,
       round(avg(in_cycle_h)::numeric,2) AS avg_in_cycle_hours,
       round(avg(gap_h)::numeric,2)      AS avg_gap_hours,
       round(100.0*sum(gap_h)/NULLIF(sum(gap_h)+sum(in_cycle_h),0),1) AS pct_time_between_cycles
FROM g WHERE gap_h IS NULL OR gap_h >= 0;
-- Measured between 6% and 24% depending on framing, on only 27-39 transitions in a rolling 24h
-- window: 5.6% counting ALL cycles' in-cycle time against the gaps that exist, ~15-24% counting
-- only cycles that HAVE a preceding gap (the per-transition view, which is the one that matters
-- since each avoided cycle saves one gap). The figure moved 15.3% -> 5.6% between two runs
-- minutes apart, so treat it as an order of magnitude, not a number. Either way it is FAR below
-- the 56% the sp-g9td epic assumed for a broader definition of between-tour time, and it caps
-- this lever at roughly +5-25% rather than the bead's estimated ~1.5x.

\echo '== (b) Was it a SPEND bound? =='
SELECT buy_markets, count(*) AS phases,
       round(percentile_cont(0.5) WITHIN GROUP (ORDER BY spend)) AS median_spend,
       round(max(spend)) AS max_spend
FROM v_cycles WHERE bought > 0 GROUP BY 1 ORDER BY 1;
-- No: one-market phases spend a median of ~114k while multi-market phases routinely spend
-- 400k-1.75M, so capital was available and unused. The 300k working_capital_reserve is a floor
-- on REMAINING treasury, not a per-phase cap. Not a money-guard problem — do not touch it.

\echo '== (d) Hull class — who is actually under-filled? =='
SELECT CASE WHEN s.cargo_capacity >= 200 THEN 'HEAVY (>=200)'
            WHEN s.cargo_capacity >= 100 THEN 'MID (100-199)'
            ELSE 'LIGHT (<100)' END AS hull_class,
       round(avg(s.cargo_capacity)) AS avg_hold, count(*) AS phases,
       round(avg(c.buy_markets)::numeric, 2) AS avg_buy_markets,
       count(*) FILTER (WHERE c.buy_markets = 1) AS one_market_phases,
       round(100.0*avg(LEAST(c.bought, s.cargo_capacity)::numeric / s.cargo_capacity), 1) AS pct_fill
FROM v_cycles c JOIN ships s ON s.ship_symbol = c.ship_symbol AND s.player_id = 5
WHERE c.bought > 0 GROUP BY 1 ORDER BY avg_hold DESC;
-- INVERTS the bead: LIGHTS fill 78.9% (bead said 50%), HEAVIES fill 65.0%. The under-fill is a
-- HEAVY problem. Fleet-composition signal, not a router one.
