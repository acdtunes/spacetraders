# Margin-gap analysis: why a "25% planned spread" realizes ~17%, not 11%

**Bead:** sp-as4k4 · **Written:** 2026-08-26 · **Author:** data analyst (measurement-only session)
**Data:** player_id 9 (TORWIND, era 2026-08-23), 51,792 tour legs / 3.1 days
**Primary window:** 2026-08-25 16:36:09Z → 2026-08-26 16:36:09Z (24h), 60 bulk freighters, API pinned ~1.98/2.0 req/s
**Nothing in production was changed.** No knob was tuned, no service restarted, no Go/Python behaviour modified.

---

## 0. TL;DR — three findings

1. **The gap is mostly not real.** Of the claimed 13.5 margin points between the "25% planned
   spread" and "11% realized net", **5.14 points are an arithmetic artifact** of averaging
   per-leg unit prices instead of weighting by credits, and **6.34 points are measurement error**
   in the 11% itself. Over any window long enough to be stable, the fleet's realized net margin is
   **17.3%**, not 11%. `P(1h rolling window ≤ 11%) = 14.1%`, `P(2h) = 3.2%`, `P(6h) = 0.0%` — the
   11% was a left-tail 1–2h sample. Genuine economic leakage between plan and cash is
   **2.05 points** (execution slippage 1.09 + fuel/gate 0.70 + composition 0.26).

2. **The real money is own-market impact, and it is large: 145.3M credits/24h (6.05 M/h,
   4.14 margin points, 95% CI [110.9, 180.4] M/24h).** Realized sell price vs the last quote
   observed before we bought regresses at **−9.87% per unit of log(1 + own-volume share)**
   (95% CI [−11.36, −8.23], within-R² 0.385, unit-weighted, good fixed effects, n=19,303 pairs).
   Elapsed time is 25× weaker (−0.38pp per log-minute) and hold time is indistinguishable from
   zero. **The decay is our own footprint, not the clock.** Capital deployed where our fleet's
   60-minute volume exceeds 3× the market's `trade_volume` — 14.4% of all capital — realizes
   **−0.67% margin**; above 4× it realizes **−8.4%**; above 6× it realizes **−23.1%**.

3. **The armed depth caps do not bind in execution.** Measured against the solver's own visit
   unit `(ship, tour_id, leg_index, waypoint, good, side)`: **20.2% of sell visits carrying 37.5%
   of all sold units** exceed `tourACapTranches = 2`, and **5.7% of sell visits carrying 19.6% of
   units** exceed `TOUR_SOLVER_REALIZED_SINK_TRANCHES = 2.5`. Pairs at visit-tranche ordinal ≥ 8
   are outright loss-makers (−2.9% to −29% margin); refusing them alone is **+15.8M/24h
   (+0.66 M/h, +0.75 pts)** with no downside beyond the freed API legs.

**Corollary for doctrine:** the fleet is *not* saturating in aggregate — margin is uncorrelated
with hull count (`corr = +0.03`, n = 41 hours, fleet 23→60). It is saturating **per lane**. The
lever is dispersion, not fleet size.

---

## 1. Method

### 1.1 Source of truth and its validation

`tour_leg_telemetry` reconciles **exactly** with `transactions` for player 9 — not approximately,
byte-for-byte on both credits and units:

```sql
WITH t AS (
 SELECT sum(realized_units*realized_unit_price) FILTER (WHERE is_buy)        tel_buy,
        sum(realized_units*realized_unit_price) FILTER (WHERE NOT is_buy)    tel_sell,
        sum(realized_units) FILTER (WHERE is_buy)     tel_bu,
        sum(realized_units) FILTER (WHERE NOT is_buy) tel_su
 FROM tour_leg_telemetry WHERE player_id=9 AND realized_at > now() - interval '6 hours'),
x AS (
 SELECT sum(-amount) FILTER (WHERE category='TRADING_COSTS')   tx_buy,
        sum(amount)  FILTER (WHERE category='TRADING_REVENUE') tx_sell,
        sum((metadata->>'units')::bigint) FILTER (WHERE category='TRADING_COSTS')   tx_bu,
        sum((metadata->>'units')::bigint) FILTER (WHERE category='TRADING_REVENUE') tx_su
 FROM transactions WHERE player_id=9 AND operation_type='tour'
   AND timestamp > now() - interval '6 hours')
SELECT * FROM t, x;
-- tel_buy 992,052,384 = tx_buy 992,052,384 | tel_sell 1,181,305,645 = tx_sell 1,181,305,645
-- tel_bu 407,522 = tx_bu 407,522           | tel_su  413,157         = tx_su  413,157
```

So the whole analysis runs on telemetry (which carries the plan) and pulls only fuel/gate costs
from `transactions`.

### 1.2 A structural fact that reframes the question

`realized_at − planned_at` has **median 4.5 s, p99 43 s** (n = 51,792). `planned_unit_price` is a
**pre-trade quote taken seconds before the fill**, not the price the router used when it chose the
lane. Therefore:

- "planned vs realized" in telemetry measures **within-execution slippage only**. It cannot
  measure route-decision decay, and the bead's ~2.4-point slippage estimate is correctly small.
- To measure decision-time decay we must join `market_price_history` (791k rows, 90,246 in the
  last 24h, covering 4,486 of the 4,600 `(waypoint, good)` keys the fleet touched).

### 1.3 Matching purchases to sales

FIFO ledger per `(ship_symbol, good)` walked in `realized_at` order across the **entire** player-9
history (not just the window), so opening inventory is exact rather than assumed. Chronological
order is used rather than `leg_index` order because `leg_index` cycles (`0,1,2,3,0,1,2,3,…`) as a
hull repeats its tour circuit — a hull sells stock bought on the *previous* cycle, and leg order is
not execution order. Partial fills (`realized_units < planned_units`, 5.5% of buy legs / 5.0% of
sell legs), multi-leg sells, and cargo carried across tours all fall out of FIFO naturally.

**Validation of the matcher** (full history, 51,792 legs):

| check | value |
|---|---|
| matched pairs | 34,220 rows / 2,284,331 units |
| sells with no prior inventory (opening stock, mining, contract cargo) | **1 row / 97 units** |
| leftover unsold lots at T1 | 162 lots / **11,861 units** |
| independent check: `SELECT sum(cargo_units) FROM ships WHERE player_id=9` | **11,813 units** |

The FIFO book closes to within **0.4%** of live cargo. Sensitivity: LIFO and average-cost move the
matched-pair realized margin by < 0.05 points (the median hold is 2.6 minutes, so lot identity
barely matters).

### 1.4 Tooling

`pandas 3.0.5` / `numpy 2.5.1` in `gobot/services/routing-service/venv`. `scipy` is absent, so all
inference is done by **cluster bootstrap resampling whole hulls** (a hull's legs are strongly
correlated; naive row bootstrap would understate CIs by ~3×). 600–2,000 replicates per statistic.

---

## 2. The reconciliation bridge

Every step below is a difference of two exactly-computed ratios over the same 24h window. The
bridge is an **identity**: the residual is **0.00 by construction**, and it lands on the measured
17.34%, which is 6.34 points above the claimed 11%.

| Step | Metric | Value | Δ (margin pts) | Credits/24h | Credits/h |
|---|---|---|---:|---:|---:|
| **M0** | leg-average planned spread — the headline "25%" | **24.54%** | — | — | — |
| **M0u** | unit-weighted planned spread | 23.14% | **1.40** | n/a (artifact) | n/a |
| **M1** | credit-weighted planned margin (plan rev / plan cost) | **19.40%** | **3.74** | n/a (artifact) | n/a |
| **M2** | matched-pair planned margin | 19.22% | **0.19** | 6.5M | 0.27M |
| **M3** | matched-pair realized margin | **18.12%** | **1.09** | 30.9M | 1.29M |
| **M4** | window flow ratio (sells/buys) — the operational metric | 18.05% | **0.07** | 2.5M | 0.10M |
| **M5** | net of fuel + gate tolls | **17.34%** | **0.70** | 24.7M | 1.03M |
| — | *claimed* "11% realized net" | 11.00% | **6.34** | — | — |
| | **total** | | **13.54** | | |

`24.54 − 13.54 = 11.00` ✓ · `24.54 − 7.20 = 17.34` ✓ · **residual 0.00**

Throughput at M5: buys **146.3 M/h**, sells **172.7 M/h**, net **25.4 M/h**.

### Why M0 → M1 loses 5.14 points (the artifact)

`M0` averages `planned_unit_price` across legs with no weight. Two independent biases:

1. **Leg-count asymmetry (1.40 pts).** A hold bought at one market is discharged across several
   sinks: 14,391 buy legs at 99.9 units/leg vs 17,144 sell legs at 81.3 units/leg. Small sell legs
   are over-represented in an unweighted mean.
2. **Cheap-good weighting (3.74 pts).** 21 goods with mean planned buy price < 500 cr/unit are
   **12.6% of legs but 1.12% of credits**, and they carry a 25.2% percentage spread. FERTILIZERS
   alone is 3.13% of legs and 0.30% of credits.

```sql
-- both weightings side by side
SELECT
 avg(planned_unit_price) FILTER (WHERE NOT is_buy)
   / avg(planned_unit_price) FILTER (WHERE is_buy) - 1                              AS legavg_spread,
 (sum(planned_units*planned_unit_price) FILTER (WHERE NOT is_buy))::numeric
   / sum(planned_units) FILTER (WHERE NOT is_buy)
 / ((sum(planned_units*planned_unit_price) FILTER (WHERE is_buy))::numeric
   / sum(planned_units) FILTER (WHERE is_buy)) - 1                                  AS unit_wt_spread,
 (sum(planned_units*planned_unit_price) FILTER (WHERE NOT is_buy))::numeric
   / (sum(planned_units*planned_unit_price) FILTER (WHERE is_buy)) - 1              AS credit_wt_margin,
 (sum(realized_units*realized_unit_price) FILTER (WHERE NOT is_buy))::numeric
   / (sum(realized_units*realized_unit_price) FILTER (WHERE is_buy)) - 1            AS realized_margin
FROM tour_leg_telemetry
WHERE player_id=9 AND realized_at > now() - interval '24 hours';
-- 0.2454 | 0.2314 | 0.1940 | 0.1805
```

> **The planner never promised 25%. It promised 19.4% and delivered 18.1% gross / 17.3% net.**

### Why the "11%" is not reproducible

Rolling-window distribution of `sells/buys − 1`, 48h of data, windows stepped every 10 min:

| window | n | p5 | p25 | median | p75 | p95 | P(≤ 11%) |
|---|---:|---:|---:|---:|---:|---:|---:|
| 15m | 287 | −5.28% | 7.58% | 16.77% | 26.85% | 51.56% | **33.8%** |
| 30m | 286 | 1.61% | 12.03% | 17.14% | 23.43% | 38.22% | **23.1%** |
| 1h | 283 | 8.12% | 13.94% | 17.63% | 21.11% | 28.20% | **14.1%** |
| 2h | 277 | 11.59% | 14.89% | 17.39% | 19.91% | 24.18% | **3.2%** |
| 6h | 253 | 13.77% | 15.34% | 17.28% | 18.63% | 20.25% | **0.0%** |
| 12h | 217 | 14.36% | 15.34% | 16.86% | 17.98% | 18.97% | **0.0%** |

Fixed-window confirmation (`ratio = sells/buys − 1` over the trailing N hours): 1h **17.22%**,
2h 18.22%, 4h 18.72%, 6h 18.97%, 12h 19.32%, 24h 18.00%, 48h 17.38%, 72h 17.46%.

**A window shorter than 6 hours cannot measure this quantity.** The bead's own warning ("hourly net
is dominated by phase noise") applies to the ratio too, because buys and sells of the same cargo
land in different hours.

---

## 3. Component ledger

Two ledgers, deliberately kept apart. **Ledger A is accounting** — it sums to the observed gap.
**Ledger B is opportunity cost** — counterfactuals that are *not* additive to A, because the
planner re-quotes each tranche live, so own-impact is already inside both the planned and the
realized price. Conflating them is the easiest way to double-count here.

### Ledger A — accounting (sums to the gap, residual 0.00)

| # | Component | Credits/24h | Credits/h | Margin pts | n | 95% CI |
|---|---|---:|---:|---:|---:|---|
| **A1** | Leg-count asymmetry in the unweighted average | — | — | **1.40** | 31,535 legs | — (deterministic) |
| **A2** | Cheap-good weighting in the unweighted average | — | — | **3.74** | 21 goods / 12.6% of legs | — (deterministic) |
| **A3** | Matched-vs-unmatched composition (partial fills) | 6.5M | 0.27M | **0.19** | 34,220 pairs | — |
| **A4a** | Buy-side execution slippage (paid over quote) | **+39.11M** | 1.63M | **1.32** | 14,391 buy legs | [0.99, 1.20] pts¹ |
| **A4b** | Sell-side execution slippage (received *over* quote) | −8.00M | −0.33M | **−0.23** | 17,144 sell legs | **[−1.34, +0.82] pts — not significant** |
| **A5** | Inventory drift / window edge | 2.5M | 0.10M | **0.07** | 11,801 units, 24.3M at cost | — |
| **A6a** | Fuel | 4.65M | 0.19M | **0.13** | 4,300 refuels | — |
| **A6b** | Gate tolls (`TRAVEL_COSTS`) | **20.03M** | 0.83M | **0.57** | 3,844 jumps @ 5,070 median | — |
| **A7** | Measurement error in the claimed "11%" | — | — | **6.34** | 283 × 1h windows | P(1h ≤ 11%) = 14.1% |
| | **Total** | | | **13.54** | | **residual 0.00** |

¹ CI is on the matched-pair-cost basis (1.108 pts); the 1.32 in the table is the effect on the
ratio `M2 → M3`, which includes the denominator term. Both are reported so the bridge closes.

Component A4b is the honest answer to "sells realize 0.7% under plan": **at the matched-pair level
the sell side beats its quote**, and the 95% CI spans zero. That measurement is noise.

### Ledger B — economics (opportunity cost, NOT additive to A)

| # | Component | Credits/24h | Credits/h | Margin pts | n | 95% CI |
|---|---|---:|---:|---:|---:|---|
| **B1** | **Own-market impact** — realized sell vs the last quote observed before we bought | **145.3M** | **6.05M** | **4.14** | 19,303 pairs / 1.34M units / 3,356M cost | **[110.9, 180.4] M/24h** (cluster bootstrap, 600 reps) |
| B1a | …of which in markets at own-volume share > 3× `trade_volume` | 102.7M | 4.28M | 2.92 | 14.4% of capital | — |
| B1b | …of which in markets at own-volume share > 6× | 61.8M | 2.57M | 1.76 | 4.3% of capital | — |
| **B2** | **Deep-tranche tail** — pairs at visit-tranche ordinal ≥ 8 | **15.8M** | **0.66M** | **0.75** | ~400 pairs / 56M cost | — |
| **B3** | Gate tolls (also in A6b — the only line that appears in both) | 20.0M | 0.83M | 0.57 | 3,844 jumps | — |
| **B4** | Sold below the best same-system quote — **upper bound only** | 214.6M | 8.94M | 6.11 | 94.3% of sell credits covered | — |

**B4 is not a recoverable number** and is reported only to bound the search. It ignores travel
time, the depth of the "better" market, and the fact that moving our flow there would move its
price — which B1 shows is exactly what happens. 29.5% of sell credits already transact at the
system's best observed quote.

---

## 4. Component-by-component detail against the bead's checklist

### (a) Cargo that never reached its planned sell market — **≈ 0, and the sign is positive**

Defining the plan's intended sinks for a buy as the sell-leg waypoints of that good within the same
`tour_id`:

| slice | n pairs | units | cost | realized margin | planned margin |
|---|---:|---:|---:|---:|---:|
| sold at an on-plan waypoint | 20,882 | 1,422,212 | 3,501.9M | **18.11%** | 19.22% |
| sold at an **off-plan** waypoint | 99 | 4,640 | 7.1M | **+25.24%** | 19.10% |
| sold under the `liquidation` engine | 724 | 45,531 | 94.0M | **+25.68%** | — |
| bought in one tour, sold under another (`same_tour = false`) | 126 | — | 9.3M | **+26.64%** | — |

Cargo that goes off-plan **earns more, not less**. 0.2% of capital, +25% margin. Liquidation and
cross-tour carry are working. **This hypothesis is dead — close it.**

### (b) Realized price decay vs plan, as a function of time and own volume — **the dominant term**

Construction: for each matched pair, `merge_asof` the last `market_price_history` row for the
**sell** waypoint/good at or before the **buy** time (92.0% coverage, 3,356.5M of 3,509.0M cost).
That is the best estimate of what the router could have believed when it committed the hull.

```
expected revenue at decision-time quote  4,094.7M
realized revenue                         3,949.4M
decay                                     +145.3M/24h  = 6.05 M/h = 4.14 margin points
implied margin had the quote held           21.99%   (vs realized 17.67%)
```

Regression (unit-weighted WLS, good fixed effects, n = 19,303, within-R² = **0.385**), where
`gap = (realized_sell − decision_quote) / decision_quote` and
`vshare = (fleet units sold into that (waypoint, good) in the prior 60 min) / trade_volume`:

| term | coefficient | 95% CI (cluster bootstrap by hull, 400 reps) |
|---|---:|---|
| `log(1 + vshare)` | **−0.09867** (−9.87 pp of price) | **[−0.11361, −0.08231]** |
| `log(1 + obs_age_min)` | −0.00383 (−0.38 pp) | [−0.00567, −0.00191] |
| `log(1 + hold_min)` | −0.00077 (−0.08 pp) | **[−0.00210, +0.00043] — spans zero** |

Non-parametric confirmation:

| own-volume share | n | units | cost | mean gap | credits lost | realized margin |
|---|---:|---:|---:|---:|---:|---:|
| < 0.5× | 7,430 | 556,533 | 1,290.8M | **+0.30%** | −4.9M | — |
| 0.5–1× | 2,453 | 172,137 | 407.4M | −0.89% | 4.4M | — |
| 1–2× | 4,537 | 286,853 | 765.3M | −2.10% | 20.4M | 19.37% |
| 2–4× | 3,021 | 203,756 | 553.2M | −5.26% | 35.9M | 14.11% |
| 4–8× | 1,501 | 97,276 | 268.0M | **−14.77%** | 47.8M | **3.15%** |
| > 8× | 361 | 24,836 | 71.8M | **−50.75%** | 41.7M | **−38.25%** |

Cumulative view — capital and its return, by saturation:

| threshold | capital | share of capital | decay | realized margin **inside** | realized margin **outside** |
|---|---:|---:|---:|---:|---:|
| vshare > 1 | 1,382.9M | 41.2% | 140.5M (5.86 M/h) | **11.55%** | 21.95% |
| vshare > 2 | 794.7M | 23.7% | 121.3M (5.05 M/h) | **5.72%** | 21.37% |
| vshare > 3 | 483.2M | 14.4% | 102.7M (4.28 M/h) | **−0.67%** | 20.75% |
| vshare > 4 | 302.1M | 9.0% | 85.9M (3.58 M/h) | **−8.36%** | 20.24% |
| vshare > 6 | 145.3M | 4.3% | 61.8M (2.57 M/h) | **−23.11%** | 19.51% |

**Time-series confirmation** (this is not only cross-sectional): regressing the fleet's *hourly*
realized margin on the *hourly* share of capital deployed into `vshare > 3` markets over 25 hours
gives `corr = −0.560`, slope **−0.296 margin points per +1pp of capital in saturated markets**,
95% CI **[−0.433, −0.147]** (2,000 bootstrap reps).

**Direct causal evidence — the buy-side walk-up.** Own-impact is not an artifact of "we go where
prices are high and they mean-revert"; it is directly visible within a single source market:

| source market | good | distinct hulls | legs | units | cost | cheapest fill → dearest fill | walk-up |
|---|---|---:|---:|---:|---:|---|---:|
| X1-CQ8-XD2C | CLOTHING | 13 | 58 | 5,321 | 20.2M | 2,844 → 5,448 | **+91.6%** |
| X1-FC62-D12X | FABRICS | 22 | 70 | 13,819 | 26.3M | 1,368 → 2,403 | **+75.7%** |
| X1-SF31-F10Z | CLOTHING | 16 | 70 | 8,217 | 28.4M | 2,776 → 4,838 | **+74.3%** |
| X1-VM70-C13D | FOOD | 13 | 59 | 13,039 | 22.7M | 1,341 → 2,340 | **+74.5%** |
| X1-AQ63-D10E | CLOTHING | 10 | 59 | 9,032 | 35.2M | 3,157 → 5,108 | **+61.8%** |

Sell-side mirror, the worst sinks by credits lost (top 20 of 2,772 = 44.2% of all decay; 49 sinks
losing > 0.5M/24h = 87.3M = 2.49 pts):

| sink | good | hulls | units | revenue | lost | median vshare | `trade_volume` | mean gap | realized margin |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|
| X1-AQ63-AE7X | CLOTHING | 11 | 9,937 | 38.1M | **7.5M** | 3.50 | 245 | −16.0% | 2.7% |
| X1-VM70-E14D | FOOD | 8 | 12,400 | 22.3M | **5.9M** | 5.05 | 300 | −20.5% | 1.6% |
| X1-AQ63-ZD5C | CLOTHING | 13 | 5,044 | 17.2M | **5.6M** | 4.98 | 100 | −22.6% | **−11.1%** |
| X1-HP22-Z23B | FOOD | 7 | 8,928 | 15.2M | **5.1M** | 4.76 | 300 | −24.5% | 6.5% |
| X1-AQ63-XD8X | CLOTHING | 8 | 6,266 | 23.1M | **5.0M** | 3.41 | 150 | −17.2% | **−4.0%** |
| X1-KM81-BC3X | LAB_INSTRUMENTS | **1** | 1,040 | 2.4M | **3.8M** | 8.50 | 80 | **−61.0%** | **−49.8%** |

X1-KM81-BC3X is the single-hull case: one ship, 25 pairs, drove `LAB_INSTRUMENTS` down 61% by
itself. Crowding is sufficient but not necessary — **depth alone does it**.

### (c) Fuel, gate fees and travel per completed pair — **small and correctly small**

```sql
SELECT category, count(*) n, sum(-amount) credits
FROM transactions
WHERE player_id=9 AND operation_type='tour' AND timestamp > now() - interval '24 hours'
  AND category IN ('FUEL_COSTS','TRAVEL_COSTS')
GROUP BY 1;
-- FUEL_COSTS   4,300   4,648,216
-- TRAVEL_COSTS 3,844  20,017,828
```

24.67M/24h = **1.03 M/h = 0.703 margin points** (fuel 0.132, gate 0.570). Gate toll mean 5,210 /
median 5,070 credits, and **100%** of tour `TRAVEL_COSTS` are cross-system, so the toll is a pure
function of gate crossings. Across *all* operations (sensing + manual included) the figure is
32.9M/24h = 0.938 pts. This is a real but second-order drag; it is 1/6th of B1.

### (d) Units bought but never sold in the window — **negligible, and reconciled**

11,801 units bought inside the window remained unsold at T1, worth **24.30M at cost = 0.69% of one
day's buys**. `sum(ships.cargo_units) = 11,813` against a FIFO leftover of 11,861 (0.4% gap; the
FIFO book covers all 1,881 hulls, and 11,701 of the 11,813 live units sit on the 60 bulk
freighters). Effect on the flow ratio (M3 → M4): **0.07 points**. Sells with no prior purchase
anywhere in 3 days of history: **97 units**. There is no hidden inventory.

### (e) Stale market data at execution — **costs nothing; tightening the caps would cost money**

Freshness caps applied per `market_data.activity` (`gobot/internal/domain/trading/ranker_age_cap.go`):
WEAK 480, RESTRICTED 180, GROWING 60, STRONG 30, unknown → 180 minutes. Age measured as
`buy_time − market_price_history.recorded_at` for the *decision-time* quote of the sell market.

| activity | stale | n | cost | realized margin | mean gap |
|---|---|---:|---:|---:|---:|
| WEAK | no | 10,276 | 1,692.7M | 19.56% | −2.18% |
| WEAK | **yes** | 766 | 82.1M | **22.71%** | −0.82% |
| RESTRICTED | no | 3,117 | 716.3M | 14.27% | −4.21% |
| RESTRICTED | **yes** | 307 | 32.5M | **25.90%** | +0.04% |
| GROWING | no | 2,732 | 489.3M | 16.00% | −7.01% |
| GROWING | **yes** | 1,127 | 127.1M | **28.37%** | −2.84% |
| STRONG | no | 291 | 81.6M | **8.07%** | **−12.18%** |
| STRONG | **yes** | 77 | 21.4M | 7.96% | −8.43% |

Stale-at-decision capital is **8.9% of the total and earns 23.75% vs 17.07% for fresh**. Forcing it
to the fresh margin would **cost 19.88M/24h (−0.57 points)**.

This is confounded, and the confound is the point. Adding `log(1+vshare)` to the regression
collapses the staleness coefficient from **+0.01498 to +0.00079** — statistically nothing. Median
`vshare` is **1.00 for fresh** rows and **0.50 for stale** rows: *a market is stale precisely
because we are not hammering it.* Staleness is a proxy for low own-impact, not a cause of anything.

Note the STRONG tier: it has the **worst** gap in the table (−12.18% even when fresh) because
STRONG activity is where our flow concentrates. The live config already loosens STRONG 30 → 60 and
GROWING 60 → 120 (`gobot/config.yaml:186-189`); this data supports that loosening and argues
against reverting it.

### (f) Tranche depth — the caps are not binding

Measured on the solver's own visit unit `(ship, tour_id, leg_index, waypoint, good, side)` with
`tranches = units_in_visit / trade_volume` (`trade_volume` as observed at fill time):

| side | visits | legs/visit p50 / p90 / p99 | tranches/visit p50 / p90 / p99 | over 2.5 (`REALIZED_SINK_TRANCHES`) | over 3 (`MAX_PLANNED_TRANCHES`) | over 2 (`tourACapTranches`) |
|---|---:|---|---|---|---|---|
| BUY | 8,554 | 1 / 3 / 7 | 1.0 / 2.5 / 5.9 | 8.4% of visits, **25.4% of units** | 4.5% / 18.4% | 15.1% / **36.6%** |
| SELL | 9,589 | 1 / 3 / 7 | 1.0 / 2.5 / 5.5 | 5.7% of visits, **19.6% of units** | 4.4% / 16.4% | 20.2% / **37.5%** |

Pair economics by visit-tranche ordinal (`depth = max(buy ordinal, sell ordinal)`):

| ordinal | n | cost | profit | margin | cum. profit | cum. margin |
|---:|---:|---:|---:|---:|---:|---:|
| 0 | 8,124 | 1,434.3M | 297.0M | **20.71%** | 297.0M | 20.71% |
| 1 | 5,649 | 985.3M | 200.3M | 20.33% | 497.3M | 20.55% |
| 2 | 4,079 | 567.4M | 104.4M | 18.40% | 601.7M | 20.14% |
| 3 | 965 | 172.1M | 22.1M | 12.85% | 623.8M | 19.75% |
| 4 | 673 | 116.0M | 13.3M | 11.43% | 637.1M | 19.45% |
| 5 | 518 | 75.9M | 6.6M | 8.64% | 643.7M | 19.21% |
| 6 | 211 | 36.1M | 2.3M | 6.26% | 645.9M | 19.07% |
| 7 | 174 | 29.9M | 0.8M | 2.61% | **646.7M (peak)** | 18.93% |
| 8 | 136 | 19.9M | **−0.6M** | **−2.92%** | 646.1M | 18.80% |
| 9 | 68 | 13.0M | **−1.2M** | **−8.87%** | 645.0M | 18.70% |
| 10 | 57 | 9.2M | **−1.4M** | **−15.60%** | 643.5M | 18.60% |
| 11 | 45 | 6.3M | **−1.7M** | **−27.02%** | 641.8M | 18.52% |
| ≥12 | ~110 | 22M | **−9.9M** | — | 630.9M (total) | 18.24% |

Profit per API leg by depth — legs are the binding resource at 2 req/s:

| depth | 0 | 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8 | 9 |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| credits/leg | 18,056 | **20,132** | 15,840 | 13,886 | 13,799 | 5,981 | 4,097 | 2,224 | **−6,092** | **−8,293** |

**Static depth-cap counterfactual** (capital freed is *not* redeployed — a strict lower bound):

| cap | profit | vs now | margin | vs now |
|---|---:|---:|---:|---:|
| ≤ 1 | 21.80 M/h | **−4.70 M/h** | 19.57% | +1.45 pts |
| ≤ 2 | 25.84 M/h | −0.66 M/h | 19.48% | +1.36 pts |
| ≤ 3 | 26.54 M/h | +0.04 M/h | 19.36% | +1.24 pts |
| ≤ 4 | 26.93 M/h | +0.43 M/h | 19.24% | +1.11 pts |
| ≤ 7 | **27.12 M/h** | **+0.62 M/h** | 18.85% | +0.73 pts |
| ≤ 10 | 26.99 M/h | +0.49 M/h | 18.60% | +0.48 pts |

**Margin and credits/hour point in opposite directions below cap 3.** Cutting depth to 1 raises
margin 1.45 points and destroys 4.7 M/h. This is the central trap in optimizing margin: it is a
ratio, and the fleet is paid in credits. Only the tail beyond ordinal 7 is unambiguously
value-destroying.

### (g) Is the fleet saturating in aggregate? — **No**

41 hourly observations, fleet 23 → 60 hulls, 101 → 320 distinct markets touched per hour:

| relationship | value |
|---|---|
| `corr(hourly margin, hulls)` | **+0.032** |
| `corr(hourly margin, distinct markets)` | −0.061 |
| `corr(hourly margin, buys per market)` | −0.144 |
| `margin% ~ 1 + log(hulls / markets)` | slope +2.500, **R² = 0.005** |

Fleet growth from 24 to 60 hulls has **not** degraded margin. Contrast with the *lane*-level
result (§4b): `corr = −0.560` on the saturated-capital share. **The saturation is local, and it is
a routing/dispersion problem, not a fleet-size problem.** Do not cap the fleet on this evidence.

Per-*system* margin (buys in system X vs sells in system X) is **not** a valid diagnostic and is
deliberately excluded: a load-out system like X1-ZS96 shows "−77.8% margin" purely because 10 hulls
load there under the `lookback` engine and sell in the destination system. It measures geography,
not economics. (The crowding it reveals — 9–10 hulls buying ADVANCED_CIRCUITRY / MEDICINE /
MACHINERY at the single waypoint X1-ZS96-CC8A — is real, and is counted once, in §4b.)

---

## 5. What the data cannot tell us

Stated plainly, because several of these bound the recommendations:

1. **Counterfactual prices.** Every "if we had not sold there" number assumes the alternative
   market's quote would have held. B1 proves that assumption is false at scale — moving flow moves
   price. **Treat B1's 145.3M as an upper bound on recoverable value; a realistic capture is the
   loss-making slice only (vshare > 3: 102.7M/24h at −0.67% realized margin).**

2. **Reverse causality in B1.** We route hulls toward markets quoting good prices, and good quotes
   mean-revert. That mechanism would also produce a negative `gap` correlated with `vshare`. Three
   things argue it is not the main driver: (i) magnitudes (−50.8% at vshare > 8 is far beyond
   plausible reversion); (ii) the within-visit buy walk-up (+91.6% at one waypoint in one day) is
   directly observed and causally unambiguous; (iii) the hourly time-series slope holds
   (−0.296 pts per pp, CI excludes zero). It is not *fully* separated, and a clean separation would
   need a randomized hold-out — which we cannot run.

3. **Throughput effects of a depth cap.** A hull that stops at tranche 3 departs sooner and may
   complete more circuits per hour. Nothing in telemetry records the counterfactual dwell, so the
   §4f table is a **strict lower bound** on the value of capping depth.

4. **8.0% of matched capital has no prior observation** of its sell market in
   `market_price_history`. Those pairs are excluded from B1; if they behave like the rest, B1 is
   ~8% understated.

5. **`market_price_history` cadence is uneven** (median inter-observation gap 59.7 min, p90
   231 min). Decision-time quotes are therefore themselves stale by construction, which pushes some
   genuine market drift into the measured "own-impact". The regression separates them (the `lage`
   coefficient is 25× smaller than `lv`), but the split is model-dependent.

6. **One era, one window.** All numbers are player 9 at 60 hulls under a specific config. The
   `absorption_ledger_repository.go` calibration comments show the same quantities were fitted very
   differently in era 5 at a much smaller fleet.

7. **No A/B.** Every recommendation below is a prediction, not a measured result. Each names a
   metric to check and a revert condition.

---

## 6. Ranked recommendations

### 6.1 Configuration-addressable today

Ranked by expected credits/hour, with the exact knob, the value to test, the prediction, and the
revert trigger. **These are recommendations for the Admiral/captain, not changes made by this
session.**

---

**R1 — Cap fleet-wide sink depth to what the ledger already claims to enforce.**

- **Knob:** `TOUR_SOLVER_REALIZED_SINK_TRANCHES` (env var, read at
  `gobot/services/routing-service/tour_solver.py:200-203`, clamp `[1.0, 6.0]`, currently at its
  **2.5** default because `run.sh` does not set it). Requires a routing-service restart, **not** a
  daemon restart.
- **Test value: 1.8.** Rationale: sell visits above 2.5 tranches carry 19.6% of units, and pairs at
  ordinal ≥ 3 fall from 18.4% to 12.9% margin and keep falling.
- **Prediction:** +0.9 to +1.3 margin points; credits/hour roughly flat to **−0.3 M/h** in the
  static bound, and plausibly positive once faster departures are counted. **This trades credits
  for margin at the low end and must be judged on credits/hour, not margin.**
- **Watch:** 6h-window `sells/buys`, and `sum(realized_units)/hour`. **Revert if 6h credits/hour
  drops more than 2% across two consecutive 6h windows.**

---

**R2 — Widen the candidate shortlist so hulls stop stacking on the same lane.**

- **Knobs:** `[trade_fleet] candidate_shortlist_top_n` and `candidate_hop_depth` (currently **3**),
  `gobot/config.yaml:213`. Boot-only — needs `make deploy-daemon`.
- **Test value:** raise `candidate_shortlist_top_n` by 50% while holding `candidate_hop_depth: 3`
  and `max_tour_systems` at its default 2. **Do not raise `max_tour_systems`** — the recorded
  replay (config.yaml:205-212) says cap-4 costs −26%, and the mechanism given there (shortlist
  dilution) is *exactly* what a wider shortlist is meant to fix, so these two must not move
  together.
- **Prediction:** the share of capital at `vshare > 3` falls from 14.4%; at the measured hourly
  slope of −0.296 pts/pp, halving it to 7% is worth **≈ +2.2 margin points ≈ +3.2 M/h**.
- **Watch:** hourly `vshare > 3` capital share (query in §7), 6h margin ratio.
- **Risk:** the same dilution effect the `max_tour_systems` replay found. Ship it alone,
  and revert on any 6h margin drop.

---

**R3 — Arm the own-fleet externality harder on the sell side.**

- **Knob:** `[trade_fleet] externality_weight`, `gobot/config.yaml:215`, currently **0.35**.
  Boot-only (stamped into container config at `StartTourRun`; running containers keep the old
  value until rebuilt).
- **Test value: 0.70.** The term is sell-side only
  (`tour_solver.py:605-637 externality_cost_per_unit`) and prices exactly the recovery burden this
  analysis measures, but it **re-orders preference without gating eligibility** — the `min_margin`
  test still runs on the raw margin (`tour_solver.py:1228-1231`). So it can only shift flow toward
  un-crushed sinks; it cannot refuse a crushed one. That ceiling is why this is R3 and not R1.
- **Prediction:** +0.5 to +1.0 margin points, +0.7 to +1.5 M/h. Smaller and less certain than R2
  because of the eligibility ceiling.
- **Watch:** distribution of `vshare` at the sell leg; the p90 should fall from 3.9.

---

**R4 — Do not tighten the freshness caps; consider loosening RESTRICTED.**

- **Knobs:** `[trading] ranker_age_cap_minutes.{weak,restricted,growing,strong}`
  (`gobot/config.yaml:186-189`, currently `strong: 60`, `growing: 120`, others at their 480/180
  defaults), and `tune --operation tour market_data_max_age_minutes` (live, default 720).
- **Recommendation: change nothing, and record the negative result.** Staleness costs **−0.57
  points** (i.e. it *pays* 0.57 points), and its apparent benefit vanishes under own-volume control
  (coefficient +0.01498 → +0.00079). The existing STRONG 30 → 60 / GROWING 60 → 120 loosening is
  supported by this data.
- **If anything, test `restricted: 300`** (from 180): RESTRICTED-stale capital earns 25.90% vs
  14.27% for RESTRICTED-fresh. Expected value is small (+0.1 to +0.2 pts) and it is a visibility
  cap only — the fail-closed execution guards are untouched (RULINGS #4).
- **Explicitly do not** spend effort on a "staleness fix". It is not a leak.

---

**R5 — Stop optimizing on any window shorter than 6 hours.**

- **Not a knob — a measurement standing order.** `P(1h window ≤ 11%) = 14.1%`. The 11% that
  motivated this bead was a left-tail 1–2h sample. Any dashboard, alert or tuning decision reading
  a sub-6h margin ratio will fire on noise roughly one time in seven.
- **Recommendation:** the reporting metric is `sells/buys − 1` over a **trailing 6h minimum**
  (12h preferred), and credits/hour net over the same window is the objective. Margin alone is a
  ratio and is maximized by trading less.

---

### 6.2 Needs code — beads filed

| bead | title | expected value |
|---|---|---|
| **sp-k1x3h** | Arm a per-tranche sell floor on the tour sell path (mirror `tourPriceTolerancePct`) | **+1.5 to +2.5 pts, +2 to +4 M/h** |
| **sp-q5l63** | Make `externality_weight` gate eligibility, not just preference | +1 to +2 pts |
| **sp-68h6w** | Reconcile `tourACapTranches=2` with `TOUR_SOLVER_MAX_PLANNED_TRANCHES=3` and remove the silent cap-degrading floor | +0.5 to +1.5 pts |
| **sp-y2evb** | Re-fit the absorption shadow decay at 60-hull scale (`DefaultExecutedHardCap`, `DefaultShadowFloorFraction`) | enables R1/R3 to bind |
| **sp-2gjfq** | Emit own-volume-share per sell leg as a first-class metric and telemetry column | measurement enabler |

**C1 (sp-k1x3h) — the highest-value code change in this analysis.**
`run_tour_coordinator_trades.go:262-264` arms a per-tranche **buy ceiling**
(`maxAskPerUnit = planned × (1 + tourPriceTolerancePct/100)`, `tourPriceTolerancePct = 15`), and
buy-side slippage is correspondingly bounded at +1.11 points. The **sell path arms no floor**:
`run_tour_coordinator_trades.go:342` → `run_trade_route_coordinator_actions.go:123-125` calls
`sellWithFloor(..., 0)`. That asymmetry is exactly the shape of the data — buys bounded, sells
decaying to −50.8%. A symmetric floor at 15% would have refused the tail that produced 41.7M of
the 145.3M decay. **It is a money guard being added, never weakened (RULINGS #4), and it fails
closed** — a refused tranche leaves cargo aboard for the next sink, which §4a shows earns +25%.

**C2 (sp-q5l63).** `externality_weight` re-orders but does not gate; `min_margin` tests the raw
margin (`tour_solver.py:1228-1231`). A hull will still plan into a sink our own fleet has crushed
if the raw spread clears. Applying the externality charge to the eligibility test — or adding a
separate `min_margin_after_externality` — converts R3 from a nudge into a bound.

**C3 (sp-68h6w).** `run_tour_coordinator_absorption.go:28-34` states `tourACapTranches` "MUST stay
in lockstep with `MAX_PLANNED_TRANCHES_PER_MARKET_GOOD_SIDE`", but `run.sh:44` arms the solver at
**3** while the Go const is **2**. The defensive floor at
`run_tour_coordinator_absorption.go:388-393` (`if capUnits < units[k] { capUnits = units[k] }`)
then silently widens the fleet-wide cap to whatever the plan asked for. Measured consequence:
**37.5% of sold units move in visits exceeding 2 tranches.** The cap does not bind.

**C4 (sp-y2evb).** `DefaultExecutedHardCap = 4h` and `DefaultShadowFloorFraction = 0.5` were fitted
on era-5 data whose comment concludes "the shadow tracks no economically real depletion" (repeat
sales returned 99.2–100.9% of the previous price). At 60 hulls that finding **inverts**: capital at
`vshare > 3` realizes −0.67%. The calibration must be re-fit at current scale before R1/R3 can
bind, or they will be released by a shadow that expires too early.

**C5 (sp-2gjfq).** Own-volume share had to be reconstructed here from a 60-minute rolling join
against `market_price_history`. It is the single most predictive variable in the analysis
(within-R² 0.385 alone) and it is not observable in production. Emitting it per sell leg —
telemetry column plus Prometheus gauge — makes the whole class of decision measurable live.

---

## 7. Queries and reproduction

Scripts: `/private/tmp/.../scratchpad/{pull,match,analysis2..7}.py` (session scratch, not
committed). The load-bearing SQL:

```sql
-- 1. Realized margin ratio by window length (§2) — use 6h minimum
SELECT w.label,
  sum(t.realized_units*t.realized_unit_price) FILTER (WHERE t.is_buy)     AS buys,
  sum(t.realized_units*t.realized_unit_price) FILTER (WHERE NOT t.is_buy) AS sells,
  round((sum(t.realized_units*t.realized_unit_price) FILTER (WHERE NOT t.is_buy))::numeric
      / (sum(t.realized_units*t.realized_unit_price) FILTER (WHERE t.is_buy)) - 1, 4) AS margin
FROM (VALUES ('1h',1),('2h',2),('6h',6),('12h',12),('24h',24),('48h',48)) AS w(label,hrs)
JOIN tour_leg_telemetry t
  ON t.player_id=9 AND t.realized_at > now() - (w.hrs || ' hours')::interval
GROUP BY 1,2 ORDER BY 2;

-- 2. Leg-level execution slippage and fill rate (§3 A4)
SELECT is_buy,
  count(*) AS legs,
  sum(realized_units) AS real_units,
  sum(planned_units)  AS plan_units,
  round(sum(realized_units)::numeric/sum(planned_units), 4) AS fill_rate,
  sum(realized_units*(realized_unit_price - planned_unit_price)) AS slippage_credits,
  round(avg(realized_unit_price - planned_unit_price), 1) AS cr_per_unit
FROM tour_leg_telemetry
WHERE player_id=9 AND realized_at > now() - interval '24 hours' AND realized_units > 0
GROUP BY 1;

-- 3. Fuel + gate toll drag (§4c)
SELECT category, count(*) n, sum(-amount) credits, round(avg(-amount),0) per_event
FROM transactions
WHERE player_id=9 AND operation_type='tour'
  AND timestamp > now() - interval '24 hours'
  AND category IN ('FUEL_COSTS','TRAVEL_COSTS')
GROUP BY 1;

-- 4. Own-volume share per sell leg — the R2/R3 monitoring query (§4b, §6.1)
WITH sells AS (
  SELECT waypoint, good, realized_at, realized_units,
         realized_units*realized_unit_price AS cr
  FROM tour_leg_telemetry
  WHERE player_id=9 AND NOT is_buy AND realized_units > 0
    AND realized_at > now() - interval '24 hours'
), own AS (
  SELECT s.*,
    sum(s2.realized_units) AS own60
  FROM sells s
  LEFT JOIN sells s2
    ON s2.waypoint=s.waypoint AND s2.good=s.good
   AND s2.realized_at <  s.realized_at
   AND s2.realized_at >= s.realized_at - interval '60 minutes'
  GROUP BY s.waypoint, s.good, s.realized_at, s.realized_units, s.cr
)
SELECT date_trunc('hour', o.realized_at) AS hr,
       round(sum(o.cr) FILTER (WHERE o.own60 > 3*m.trade_volume)::numeric
           / nullif(sum(o.cr),0) * 100, 2) AS pct_credits_in_saturated_markets
FROM own o
JOIN market_data m
  ON m.player_id=9 AND m.waypoint_symbol=o.waypoint AND m.good_symbol=o.good
GROUP BY 1 ORDER BY 1;

-- 5. Worst sinks by own-impact (§4b) — the dispersion target list
--    (approximate: uses current market_data.trade_volume rather than the
--     decision-time observation; good enough to rank)
WITH s AS (
  SELECT t.waypoint, t.good, t.ship_symbol, t.realized_at, t.realized_units,
         t.realized_unit_price, m.trade_volume
  FROM tour_leg_telemetry t
  JOIN market_data m ON m.player_id=9
       AND m.waypoint_symbol=t.waypoint AND m.good_symbol=t.good
  WHERE t.player_id=9 AND NOT t.is_buy AND t.realized_units > 0
    AND t.realized_at > now() - interval '24 hours'
)
SELECT waypoint, good,
       count(DISTINCT ship_symbol) hulls, count(*) legs,
       sum(realized_units) units,
       max(trade_volume) tv,
       round(sum(realized_units)::numeric/greatest(max(trade_volume),1), 2) AS day_tranches,
       max(realized_unit_price) AS best_fill,
       min(realized_unit_price) AS worst_fill,
       round((1 - min(realized_unit_price)::numeric/max(realized_unit_price))*100, 1) AS walk_down_pct
FROM s GROUP BY 1,2
HAVING sum(realized_units) > 2000
ORDER BY walk_down_pct DESC LIMIT 30;
```

**FIFO matcher** (the one piece not expressible in SQL): walk `tour_leg_telemetry` for
`player_id=9` ordered by `(ship_symbol, realized_at, id)`; per `(ship, good)` push buy lots onto a
deque and pop them on sells, emitting one pair row per `(buy lot, sell leg, units)` triple.
Validate by comparing residual lots against `sum(ships.cargo_units)`.

---

## 8. Standing conclusions

- **Report the margin ratio over ≥ 6h. Optimize credits/hour, never margin alone.**
- **The planned spread is 19.4%, not 25%.** The 25% is an unweighted average of per-leg unit
  prices; it should not appear in any dashboard or bead again.
- **Execution is good.** 1.09 points of slippage on 3.5 billion credits of turnover, a 99.4% buy
  fill rate, a FIFO book that closes to 0.4% of live cargo, and cargo that goes off-plan earning
  +25%. The tour executor is not the problem.
- **The problem is that we trade against ourselves.** 145.3M credits/24h, four margin points, one
  variable with a within-R² of 0.385. 14.4% of the fleet's capital is deployed at a negative
  realized margin because we have already crushed the market we are selling into.
- **Fleet size is not the constraint. Lane dispersion is.**

---

*Measurement session, 2026-08-26. Read-only against the live database; no production
configuration, service or source behaviour was modified.*
