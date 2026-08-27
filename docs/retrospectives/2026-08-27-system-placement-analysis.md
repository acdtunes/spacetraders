# System placement: where the fleet actually earns, and whether moving hulls would earn more

**Bead:** sp-33x14 · **Written:** 2026-08-27 · **Author:** data analyst (measurement-only session)
**Data:** player 9 (TORWIND), 89,327 tour legs / 4.2 days, 57,508 FIFO-matched buy→sell pairs
**Primary window:** 2026-08-26 16:00:00Z → 2026-08-27 16:00:00Z (24h), 75 bulk freighters.
Chosen to end **before** the 08-27 daemon restarts (~16:15, 17:50, 18:20Z), which are excluded
throughout. Tenure work uses 2026-08-24 00:00Z → 2026-08-27 16:00Z (88h) because long system
visits are rare.
**Nothing in production was changed.** No knob tuned, no service restarted, no Go/Python behaviour
modified.

---

## 0. TL;DR — three findings

1. **The 12.35 M/h of "losing systems" is 100% artifact, and then some.** Reproducing the naive
   measurement on its own 4h window gives −12.00 M/h across 36 systems. Decomposed exactly:
   **−11.85 M/h is cross-system booking** (cargo bought in one system, sold in another),
   **−1.62 M/h is window truncation** (a buy whose sell falls after the window ends), and the
   genuine same-system in-window P&L of those very systems is **+1.47 M/h**. Attributing each
   matched pair's profit to its (source, sink) **pair** and splitting 50/50, **not one of the 177
   systems the fleet touched has negative attributed profit** — the minimum is +7,560 cr/24h. The
   naive drain also shrinks monotonically with window length (−16.4 M/h at 2h → −5.5 M/h at 48h),
   which is by itself proof it is measuring truncation rather than geography.

2. **The fleet is already rotating hard — about once every 40 minutes of hull time — and it is
   rotating slightly EARLIER than optimal, not later.** 4,806 system crossings in 3,195 observed
   hull-hours = 1.50 per hull-hour; **61.8% of all hull-time is spent crossing between systems**,
   38.2% trading inside one. Median system visit is **10 minutes** of trading; the daemon's own
   counters show **304 repositions and 312 margin-deaths in 3.36h of uptime** (≈91/h, ~1.2 per hull
   per hour). A renewal-reward calculation on the measured within-visit decay curve puts the
   profit-maximising visit at **~25 minutes** (bootstrap CI 20–25), against an actual median of 10
   and p75 of 18. **Leaving earlier would lower credits/hour**, because the 24.7-minute mean transit
   is already larger than the median visit.

3. **The decay is real, fast, and fleet-level — but the lever is spacing, not tenure.** Within a
   single visit, margin falls from **24.2% in the first 5 minutes to 7.0% past 30 minutes**
   (balanced panel, visits ≥30 min; paired within-visit change −6.72 pts, 95% CI
   [−7.71, −5.76]). The mechanism is repeat tranches on one lane: the 9th-or-later sell of the same
   good at the same dock inside one visit runs at **−14.11% margin on 461 M of cost = −0.87 M/h**.
   But a hull's *own* history in a system predicts nothing; what predicts everything is **how long
   ago any TORWIND hull was there**. Entering ground untouched for ≥30 min is worth
   **+2.94 margin points (95% CI [+1.83, +4.20], system fixed effects, 120 systems)**, and **65% of
   visits (57% of capital) land on ground another hull left less than 30 minutes ago.** Closing
   that is worth ≈ **+2.0 M/h**. No live knob does it: the anti-herd cap counts *resident* hulls,
   not recent visitors.

**Answer to the brief's question (e):** **raising `reposition_rate_floor_pct` would not raise fleet
margin, and the honest reading is that the fleet is already well placed.** The trigger evaluates at
tour boundaries (median tour 2.8h) against a decay that unfolds in 10–25 minutes — the wrong
granularity by ~16×. It is also already non-binding: 304 of 304 repositions are accounted for by
the 312 margin-death tap-outs, so the rate-floor path is contributing ≈0 relocations today. And the
prize is small: relocating **every** hull-hour in the six occupied systems that measurably
underperform, to ground earning the fleet mean, is worth **+0.62 M/h (+2.4%)** as a strict upper
bound.

---

## 1. Method

### 1.1 Matching, and where a pair's profit belongs

FIFO ledger per `(ship, good)` walked in `realized_at` order across the **entire** player-9 history,
so opening inventory is exact. Chronological order, not `leg_index`, because `leg_index` cycles
(`tour_id` is the per-hull container id, and a hull repeats its circuit inside one).

| matcher check | value |
|---|---|
| matched pairs | **57,508** rows / 3,980,814 units |
| sells with no prior inventory anywhere in history | **6 rows / 459 units** |
| leftover unsold lots at T1 | 312 lots / 21,078 units |
| independent check: `SELECT sum(cargo_units) FROM ships WHERE player_id=9` | 20,591 |
| book closes to | **2.37%** of live cargo (cargo is in flux during the read) |
| **cross-system pairs** | **34.5% of rows, 33.5% of cost** |

That last row is the whole problem. One third of the fleet's capital is bought in one system and
sold in another, so one third of the fleet's capital books a loss in the buying system and a gain in
the selling system regardless of whether the round trip made money.

**The split rule (stated, and defended).** A completed pair needs a source and a sink; neither end
produces anything alone. In a two-player cooperative game whose singleton coalitions are worth zero,
the Shapley value of each player is exactly half the coalition's value. So the primary rule is
**50/50**: a same-system pair's profit goes wholly to that system, a cross-system pair's profit is
split evenly between source and sink.

**Robustness.** A second, quite different rule — a *reference-price decomposition* where the
unit-weighted fleet-mean price of each good in the window is the reference, the source earns
`(ref − buy) × units` and the sink earns `(sell − ref) × units` — gives a **Spearman rank
correlation of 0.984** and a Pearson of 0.99 against the 50/50 ranking. The choice of split does not
move the answer. (Naive-vs-50/50 rank correlation, for contrast: **0.56**.)

### 1.2 Hull-hours

A **hull-system episode** is a maximal run of one hull's consecutive legs inside one system. Each
episode is *charged* from the midpoint of the preceding inter-episode gap to the midpoint of the
following one, so charged hull-hours **partition** the fleet's whole timeline with nothing lost in
travel. Where a table reports "traded" minutes instead, it says so.

### 1.3 Tooling and inference

`pandas 3.0.5` / `numpy 2.5.1` in `gobot/services/routing-service/venv`. **`scipy` is absent**, so
all inference is bootstrap: cluster-resampled by **hull** or by **system** as appropriate (legs
within a hull, and hours within a system, are strongly correlated), 400–4,000 replicates. Every
headline number below carries n and either a 95% CI or quartiles.

Prometheus was read with a real line parser (`^name(\{labels\})?\s+value` regex), never
`awk '{print $2}'` — label values contain spaces.

---

## 2. (a) True per-system profit, and the size of the artifact

### 2.1 The naive metric is a function of window length

Grouping every tour transaction by its waypoint's system, over windows ending 2026-08-27 16:00Z:

| window | systems | "losing" systems | apparent drain | buy/sell ratio, losers | …winners | fleet net | p10 | p50 | p90 |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 2h | 78 | 27 | **−16.38 M/h** | 1.52 | 0.73 | 24.88 M/h | −749k | +106k | +1,706k |
| **4h** | **114** | **36** | **−12.07 M/h** | **1.56** | **0.75** | 25.22 M/h | **−401k** | **+110k** | **+939k** |
| 6h | 135 | 45 | −10.73 M/h | 1.35 | 0.75 | 26.37 M/h | −281k | +96k | +798k |
| 12h | 160 | 57 | −8.15 M/h | 1.32 | 0.78 | 25.53 M/h | −166k | +57k | +661k |
| 24h | 177 | 61 | −6.90 M/h | 1.38 | 0.80 | 24.72 M/h | −124k | +49k | +595k |
| 48h | 194 | 61 | −5.46 M/h | 1.37 | 0.81 | 25.35 M/h | −70k | +36k | +545k |

The 4h row **reproduces the bead's measurement** (bead: 123 systems, 47 losers, 12.35 M/h,
p10 −401k, p50 +119k, p90 +971k). The drain falls by a factor of three as the window lengthens
while the fleet's net barely moves — a quantity that depends that strongly on the observation
window is not a property of the systems.

### 2.2 Exact decomposition

Every pair is classified by whether each half falls inside the window and whether the two halves
share a system. The three terms sum to the naive figure with residual 0.000 by construction:

| term | 4h window (36 losers) | 24h window (61 losers) |
|---|---:|---:|
| naive net of the "losing" systems | **−12.00 M/h** | **−6.88 M/h** |
| …of which **cross-system booking** (both halves in window, different systems) | **−11.85 M/h** | **−8.01 M/h** |
| …of which **window truncation** (one half outside the window) | −1.62 M/h | −0.35 M/h |
| …of which **genuine same-system, in-window P&L** | **+1.47 M/h** | **+1.47 M/h** |
| residual | +0.000 | +0.000 |
| **TRUE 50/50-attributed profit of those same systems** | **+5.69 M/h** | **+4.27 M/h** |

**112% of the apparent 4h loss (121% at 24h) is booking or truncation.** The genuine economic
component of those systems is *positive*, and their properly-attributed profit is +4 to +6 M/h.

### 2.3 The corrected distribution

24h window, 50/50 pair split, 177 systems touched:

| | naive net/h | **true attributed profit/h** |
|---|---:|---:|
| p10 | −124k | **+8k** |
| p25 | −16k | **+25k** |
| p50 | +49k | **+73k** |
| p75 | +194k | **+189k** |
| p90 | +595k | **+342k** |
| systems below zero | **61 of 177** | **0 of 177** |
| total | 24.72 M/h | **25.38 M/h** (gross of fuel/tolls) |

The true distribution is **narrower and strictly positive**. The naive view's long negative tail and
its fat positive tail are the same artifact seen from the two ends: load-out systems are pushed
down and discharge systems pushed up. Only **0.35% of capital** moves in (source, sink) lanes that
are net loss-making (38 of 1,115 lanes, 13.0 M cost, −1.1 M profit).

Worked examples, 24h:

| system | naive net/h | buy/sell ratio | **true (50/50)** | ref-split | mean hulls | cr/hull-h |
|---|---:|---:|---:|---:|---:|---:|
| X1-JY28 | **−721k** | 1.5 | **+269k** | +286k | 1.5 | 175,778 |
| X1-DS52 | **−357k** | 1.4 | **+288k** | +311k | 0.6 | 497,454 |
| X1-VN51 | **−192k** | 1.1 | **+359k** | +342k | 0.7 | 532,230 |
| X1-MZ17 | −132k | 1.1 | **+318k** | +370k | 0.8 | 405,524 |
| X1-KH24 | −436k | **8.6** | +68k | +87k | 0.1 | 783,401 |

X1-DS52 and X1-VN51 are among the naive metric's worst offenders and are, in fact, **above** the
fleet's mean earning rate. X1-JY28 is the one case where the naive flag and the true rate agree — it
is genuinely the fleet's weakest occupied ground.

**A cross-system pair is not a worse pair.** Cross-system pairs are 41.9% of rows and 36.7% of cost
in the 24h window and run at **20.92% margin against 13.81% for same-system pairs**. The pre-jump
look-back loader is the fleet's *highest*-margin engine, which is precisely why the systems it buys
in look so bad under the naive lens.

---

## 3. (b) Which occupied systems genuinely underperform

Credits per **charged hull-hour**, 24h window, 50/50 attribution, cluster-bootstrapped by hull
within each system (2,000 reps). Fleet mean **409,989 cr/hull-h**. Only systems currently holding a
bulk freighter and carrying ≥2 hull-hours in the window are shown.

| system | **cr/hull-h** | 95% CI | % of fleet | hull-h | profit 24h | hulls now | hulls seen | visits |
|---|---:|---|---:|---:|---:|---:|---:|---:|
| **X1-JP90** | **186,266** | [158,890 – 295,490] | **45%** | 2.1 | 0.40 M | 1 | 3 | 4 |
| **X1-GF30** | **201,799** | [129,193 – 263,220] | **49%** | 26.6 | 5.37 M | 1 | 13 | 25 |
| **X1-H26** | **208,859** | [162,167 – 249,187] | **51%** | 21.9 | 4.57 M | 2 | 7 | 50 |
| **X1-JP62** | **231,984** | [118,002 – 303,466] | **57%** | 7.6 | 1.76 M | 1 | 5 | 15 |
| **X1-XF31** | **272,267** | [137,186 – 374,330] | **66%** | 19.5 | 5.30 M | 4 | 12 | 50 |
| **X1-PX64** | **360,300** | [330,993 – 398,418] | **88%** | 7.5 | 2.70 M | 1 | 6 | 12 |
| X1-JZ74 | 301,174 | [159,493 – 456,785] | 73% | 13.9 | 4.17 M | 1 | 13 | 29 |
| X1-AF27 | 342,457 | [272,353 – 413,971] | 84% | 4.3 | 1.46 M | 1 | 6 | 8 |
| X1-CH44 | 349,165 | [283,771 – 421,859] | 85% | 17.1 | 5.98 M | 2 | 7 | 47 |
| X1-DF41 | 360,849 | [250,751 – 469,579] | 88% | 11.7 | 4.22 M | 1 | 6 | 14 |
| … | | | | | | | | |
| X1-PV2 | 831,153 | [617,614 – 1,030,774] | 203% | 18.3 | 15.25 M | 3 | 14 | 25 |
| X1-PX31 | 958,259 | [568,017 – 1,070,912] | 234% | 2.1 | 2.03 M | 1 | 2 | 4 |

**Only 6 of 31 measured occupied systems have a 95% CI lying entirely below the fleet mean.** They
hold **10 of 56 hulls** and account for **15.0% of occupied hull-hours**.

**The upside bound.** If all 85.2 of those hull-hours earned the fleet mean instead of their
measured rate: **+14.82 M over 24h = +0.618 M/h = +2.43%** of the fleet's 25.38 M/h attributed
gross. That is a *strict upper bound* — it assumes relocation is free, that the destination earns
the fleet mean rather than a marginal rate, and that removing those hulls does not depress the
destination (§5 says it would, a little).

X1-JZ74 (73%), X1-AF27 (84%), X1-CH44 (85%), X1-DF41 (88%) sit below the mean but their CIs cross
it; at n = 8–47 visits the data cannot call them.

---

## 4. (c) Do any unoccupied systems look attractive?

### 4.1 What market_data covers

| | count |
|---|---:|
| market waypoints known to player 9 | **3,499** across **374 systems** |
| (waypoint, good) quotes stored | 20,998 |
| fresh at 300 s (daemon's own gauge) | 574 of 3,501 |
| systems traded in the last 24h | **177** |
| systems traded at any point this era | 208 |
| systems holding a bulk freighter now | **48** (75 hulls, **1.56 hulls/system**) |
| systems in the gate graph | 7,763 (31,134 edges) |

### 4.2 The unworked pool is thin, not fresh

Per system: markets, count of in-system directed pairs with a positive spread at the A-cap tranche
depth, the best percentage spread available, and median `trade_volume`.

| bucket | systems | median markets | median tradeable pairs | median best spread | median `trade_volume` | share with any tradeable pair |
|---|---:|---:|---:|---:|---:|---:|
| traded in 24h | 177 | **9** | **16** | 61.5% | 120 | **92.1%** |
| traded earlier this era | 31 | 2 | 0 | 0.0% | 80 | 41.9% |
| **never traded** | 166 | **2** | **1** | 15.3% | 100 | **60.8%** |

**Controlling for system size**, which is the confound that matters (the best spread in a system is
a maximum over pairs, so bigger systems win mechanically):

| markets in system | worked | n | median best spread | median tradeable pairs |
|---|---|---:|---:|---:|
| 1–2 | unworked | 113 | 0.0% | 0 |
| | worked | 11 | 0.0% | 0 |
| 3–4 | unworked | 45 | 21.4% | 2 |
| | worked | 21 | **28.1%** | 2 |
| 5–8 | unworked | 19 | 23.6% | 4 |
| | worked | 56 | **45.6%** | 9 |
| 9–16 | unworked | 17 | 69.7% | 22 |
| | worked | 64 | **72.6%** | 32 |
| 17+ | unworked | **3** | 81.4% | 80 |
| | worked | 25 | 66.1% | 79 |

**At every size band with enough systems to compare, the ground we already work quotes spreads at
least as wide as the ground we do not.** 113 of the 197 unworked systems have 1–2 markets and
therefore *zero* in-system pairs — they are not ground, they are waypoints.

There are **79 unworked systems with ≥3 markets and ≥2 tradeable in-system pairs**; only 37 have
quotes fresher than 24h. The best-looking, by observable spread:

| system | markets | tradeable pairs | best spread | pack depth | tradeable goods | median `trade_volume` | quote age | traded this era |
|---|---:|---:|---:|---:|---:|---:|---:|---|
| X1-CM29 | 10 | 39 | 101.3% | 490 | 17 | 60 | 0.6h | no |
| X1-N91 | 19 | 80 | 82.0% | 490 | 28 | 120 | 3.2h | no |
| X1-CS71 | 23 | 41 | 81.4% | 490 | 14 | 240 | 0.6h | no |
| X1-GD39 | 18 | 75 | 78.1% | 490 | 26 | 120 | 0.5h | no |
| X1-JD94 | 20 | 124 | 72.1% | 490 | 30 | 120 | 4.3h | no |
| X1-HN3 | 16 | 55 | 80.5% | 360 | 30 | 60 | 0.5h | no |

### 4.3 What market_data cannot tell us — stated plainly

1. **Depth, which is what actually binds.** `trade_volume` is per-transaction chunk size, not
   absorption. sp-as4k4 showed a *single* hull driving LAB_INSTRUMENTS down 61% at X1-KM81-BC3X.
   A wide quoted spread over a shallow book is a two-tranche opportunity, not a system.
2. **Whether the spread survives our arrival.** Every unworked quote is an *undisturbed* quote.
   Our own occupied systems looked like that before we got there. The −9.87%-per-log-own-volume-
   share decay sp-as4k4 measured applies on arrival, not later.
3. **Reachability and deadhead.** `gate_edges` gives adjacency, not the jump-toll-and-fuel cost of
   getting a hull there and the cost of getting it back if the ground disappoints.
4. **Refresh economics.** A system with 3.2h-old quotes needs a probe to keep it warm; the
   yard-sentinel/probe-sensing budget is finite and already allocated.
5. **The 95% of the map we have never sensed.** 374 systems of 7,763 in the gate graph carry any
   market data at all. The interesting unknown is a *sensing* question, not a placement question,
   and this analysis cannot speak to it.

### 4.4 How to validate cheaply, before committing hulls

The cheap validation already exists in the fleet: **the look-back manifest**. A hull that must cross
anyway can be routed through a candidate system, and its arrival sale prices the ground for
essentially free — 279 of 299 recent jumps already carried a manifest. Concretely, a one-hull,
one-visit probe of a candidate yields, from the same telemetry this analysis reads:

- realized margin on the arrival sale versus the quoted spread (tests caveat 2), and
- units absorbed before the fill price walks down more than the `tourPriceTolerancePct` band
  (tests caveat 1).

**Two visits per candidate is enough to rank it**, because the within-visit decay curve (§6) is
steep and observable inside 10 minutes. That is roughly 30 minutes of one hull's time and ~14k cr
of tolls per candidate — against the ~0.6 M/h that the *entire* relocation opportunity is worth,
probing more than a handful of candidates is not economic.

---

## 5. (d) The marginal value of the 2nd–5th hull in a system

System-hour panel: hulls present that hour, hull-hours accrued, and the profit attributed to that
system-hour under the 50/50 rule. Restricted to system-hours with ≥0.5 hull-hours so the ratio is
not dominated by a two-minute arrival dump. **System fixed effects** — the deviation of the
per-hull rate from that system's own hull-hour-weighted mean — because rich systems attract hulls
and the raw cross-section is therefore uninformative. Bootstrap clustered by system, 2,000 reps.

| hulls in system | system-hours | systems | hull-h | per-hull cr/h | **FE deviation** | 95% CI |
|---:|---:|---:|---:|---:|---:|---|
| 1 | 124 | 61 | 89 | 1,056,594 | −84,384 | [−223,244, +70,020] |
| 2 | 207 | 81 | 161 | 1,055,448 | −54,339 | [−139,795, +31,618] |
| 3 | 211 | 75 | 186 | 1,202,571 | +53,434 | [−49,699, +156,440] |
| 4 | 200 | 68 | 210 | 1,122,390 | −17,604 | [−106,788, +74,126] |
| 5 | 149 | 47 | 184 | 1,226,421 | +54,759 | [−22,773, +136,639] |

**Every CI spans zero. There is no detectable system-level crowding penalty between 1 and 5 hulls.**
Implied marginal product of the Nth hull, on the same FE basis (panel mean 1,143,303 cr/hull-h):

| hull | marginal product | as % of a solo hull |
|---|---:|---:|
| 2nd | 1,119,008 | 106% |
| 3rd | 1,412,284 | 133% |
| 4th | 912,583 | 86% |
| 5th | 1,487,516 | 140% |

A looser panel (no hull-hour floor) does show a significant −105,793 cr/hull-h at 4 hulls
(CI [−205,316, −7,402]) and +153,507 at 1 (CI [+56,395, +250,571]) — i.e. an 11–17% per-hull decline
from solo to crowded — but that panel is contaminated by system-hours holding a few minutes of hull
time, where an arrival dump divides by near-zero. Taking the tighter panel as primary and the looser
one as an upper bound on the effect: **the per-hull penalty from 1 → 4 hulls is somewhere between 0
and 17%, and the marginal hull is still worth 85–140% of a solo hull.**

**So 1.56 hulls/system is well BELOW the crowding knee, and the anti-herd cap of 5 is not the
binding constraint.** This is exactly consistent with sp-as4k4 (`corr(hourly margin, hulls) = +0.03`
across 23→60 hulls): **the 6.05 M/h of own-market impact is a per-LANE phenomenon, not a
per-SYSTEM one.** A system can hold five hulls happily as long as they are not all selling
CLOTHING into the same dock — and §6 shows that when the damage happens, one hull does it to itself
inside twenty minutes.

**On buying more hulls:** nothing here argues against it. The system-level marginal product is
positive and flat through five. The constraint sp-as4k4 identified — API legs at 2 req/s, and
per-lane depth — is unchanged by this work.

---

## 6. The decay curve per system tenure (Admiral Q1)

### 6.1 The timescale is minutes, and it is visible in a single hull's trace

TORWIND-71D, X1-AQ63, 11:43→12:08 on 08-27. Buying CLOTHING at D12Z/D10E/F14Z, selling at XD8X:

```
sell fills at X1-AQ63-XD8X:  4502  4479 → 4359  4310 → 4150  4085 → 3874 → 3617 → 3297  3276
buy  fills   (three sources): 3613  3754 → 3548  3721 → 3804  4036 → 3640 → 3843  4084 → 4266  4559
```

A **27% walk-down on the sink and a 26% walk-up on the source, by one hull, in 19 minutes.** The
first round trip earns 26%; the last one loses money. The hull then leaves for X1-MK55. This is not
a system that needs abandoning; it is a lane that needed to stop after four tranches.

### 6.2 The curve

Matched pairs assigned to the hull-system visit holding their **sell** leg; tenure is minutes since
the visit's first trade. `cr/hull-h` here excludes inter-system transit, so its *level* is ~2.6×
the fleet's all-in rate; only the **shape** is the finding.

**Balanced panel — only visits lasting ≥30 min (n=511 visits, 72 hulls, 100 systems), so
composition is held fixed and short visits cannot masquerade as decay:**

| tenure | visits | pairs | cost | profit | **margin** | hull-h | cr/hull-h |
|---|---:|---:|---:|---:|---:|---:|---:|
| 0–5m | 506 | 2,706 | 475.0 M | 114.8 M | **24.16%** | 42.6 | 2,695,294 |
| 5–10m | 446 | 2,387 | 415.9 M | 94.2 M | **22.65%** | 42.6 | 2,211,981 |
| 10–15m | 429 | 2,225 | 418.8 M | 75.8 M | **18.10%** | 42.6 | 1,779,980 |
| 15–20m | 409 | 2,342 | 422.2 M | 65.5 M | **15.52%** | 42.6 | 1,539,234 |
| 20–30m | 414 | 3,748 | 704.2 M | 77.8 M | **11.05%** | 85.2 | 913,662 |
| 30m+ | 300 | 5,598 | 1,041.6 M | 72.6 M | **6.97%** | 189.6 | 382,929 |

**The knee is at 10–15 minutes.** Earning rate falls 86% from the first five minutes to past thirty.
The unbalanced curve over all 4,449 visits agrees (21.96% → 6.36% from 0–2m to 45m+).

**Within-visit paired estimate**, which removes visit-level composition entirely: first 5 minutes vs
the rest of the same visit, n=1,439 visits ≥15 min — **mean −6.72 pts, median −7.32 pts, 95% CI
[−7.71, −5.76]**.

### 6.3 The mechanism is repeat tranches on one lane, not the clock

Margin by the *n*-th sell of the same good at the same sink waypoint inside one visit:

| n-th sell | pairs | cost | profit | margin | median tenure |
|---:|---:|---:|---:|---:|---:|
| 0 | 23,764 | 4,125.9 M | 840.7 M | 20.38% | 2.6 m |
| 1 | 9,672 | 1,745.5 M | 338.2 M | 19.38% | 7.7 m |
| 2 | 7,249 | 1,112.3 M | 218.5 M | 19.64% | 8.5 m |
| 3 | 4,667 | 676.1 M | 124.6 M | 18.43% | 9.4 m |
| 4 | 2,852 | 413.3 M | 70.4 M | 17.04% | 10.3 m |
| 5 | 1,556 | 262.7 M | 32.9 M | 12.54% | 14.5 m |
| 6 | 930 | 163.8 M | 13.6 M | 8.32% | 16.7 m |
| 7 | 715 | 124.5 M | 9.8 M | 7.91% | 17.0 m |
| **≥8** | **2,492** | **461.0 M** | **−65.0 M** | **−14.11%** | **20.3 m** |

And by cumulative units the hull has already moved in the visit:

| cumulative units | pairs | cost | profit | margin | median tenure |
|---|---:|---:|---:|---:|---:|
| 0–200 | 15,770 | 2,560.5 M | 578.5 M | 22.59% | 0.2 m |
| 200–500 | 11,417 | 1,724.8 M | 392.0 M | 22.73% | 4.3 m |
| 500–1k | 10,021 | 1,567.7 M | 308.2 M | 19.66% | 8.7 m |
| 1k–2k | 8,744 | 1,567.8 M | 219.7 M | 14.01% | 15.3 m |
| 2k–4k | 5,408 | 1,110.1 M | 76.7 M | 6.91% | 25.3 m |
| 4k+ | 2,537 | 553.8 M | 8.7 M | 1.58% | 46.8 m |

**The ≥8 tail alone is −65.0 M over 88h = −0.87 M/h on 5.07% of matched capital.** It is a clean,
already-identified target: this is the same tail sp-as4k4's **R1** (`TOUR_SOLVER_REALIZED_SINK_
TRANCHES` 2.5 → 2.0) and **sp-k1x3h** (a per-tranche sell floor) were written to cut. **Refusing
that tail is worth more than any relocation posture in this document.**

### 6.4 A hull's own history does not matter; the fleet's does

| hours since **this hull** last worked the system | visits | cost | margin | cr/hull-h |
|---|---:|---:|---:|---:|
| <0.5h | 319 | 560.2 M | 13.91% | 1,157,673 |
| 1–2h | 406 | 819.7 M | 18.39% | 1,338,981 |
| 8–24h | 341 | 741.7 M | 17.96% | 1,381,146 |
| never / 24h+ | 142 | 334.5 M | 16.48% | 1,241,034 |

| hours since **any TORWIND hull** last worked it | visits | cost | margin | cr/hull-h |
|---|---:|---:|---:|---:|
| **<15m** | **2,143** | **3,946.3 M** | **15.21%** | 1,070,262 |
| 15–30m | 464 | 847.1 M | 16.45% | 1,305,789 |
| 30–60m | 396 | 769.0 M | 19.34% | 1,439,724 |
| 1–2h | 255 | 783.6 M | 16.21% | 1,415,219 |
| 2–4h | 249 | 802.2 M | 16.03% | 1,451,260 |
| **4h+ / never** | **395** | **1,051.8 M** | **22.64%** | **1,736,472** |

Raw contrast (≥2h vs <30m): **+5.66 margin points, 95% CI [+4.04, +7.20]**. That is partly
selection — a system nobody has visited for four hours is one the solver only reaches when it finds
a big spread. **Controlling with system fixed effects** (120 systems that have both fresh and stale
visits, cost-weighted, 4,000 reps): **+2.94 points, 95% CI [+1.83, +4.20]**; unweighted median
system gap +5.17 points.

**This is the rotation trigger the Admiral asked for, and it is not a tenure trigger.** It is a
*fleet-level arrival cooldown*. The quantity that predicts a fat visit is how long the ground has
been left alone by anybody, and today **65% of visits (57% of capital) land on ground another hull
left less than 30 minutes ago.**

---

## 7. What rotating costs, and the break-even (Admiral Q2)

### 7.1 The fleet already rotates constantly

Over 2026-08-24 00:00Z → 08-27 16:00Z:

| | value |
|---|---:|
| observed hull-hours (per-hull first-to-last leg) | 3,195 |
| system crossings | **4,806** = **1.50 per hull-hour** (one every 40 min) |
| hull-time **inside** a system | 1,221 h (**38.2%**) |
| hull-time **crossing between** systems | 1,974 h (**61.8%**) |
| system visit traded span | p25 5m · **p50 10m** · p75 18m · p90 31m · p99 86m |
| next-but-one visit returns to the same system | 30.1% |
| visits re-entering a system the hull has worked before | 60.8% |

Live daemon counters, 3.36 h of uptime: `tour_repositions_total{success} = 304`,
`tour_margins_death_total = 312`, `tour_jump_loaded_total{loaded=true} = 279` of 299.
**≈91 repositions/hour across 75 hulls — about 1.2 per hull per hour.** The margin-death 3-strike
tap-out (`tourStarvationLimit = 3`) accounts for essentially all of them.

The premise that hulls "sit in a system until its margins are exhausted" is **empirically false at
the system level.** Margins do die — in about ten minutes — and the hull leaves.

### 7.2 The cost of one more crossing

| component | value |
|---|---|
| dead time, last trade in old system → first trade in new | p25 12.6m · **p50 18.6m** · p75 28.2m · p90 46.9m · **mean 24.7m** (95% CI [24.1, 25.3], n=4,781) |
| control: consecutive trades inside a system | p50 **0.3m**, mean 0.9m |
| `TRAVEL_COSTS` (gate tolls) | 10,212 events, 54.03 M, median 5,074 cr |
| `FUEL_COSTS` | 13,904 events, 13.31 M, median 1,008 cr |
| **cash per crossing** | **14,084 cr** |
| opportunity cost of the median 18.6 m at the fleet's all-in 495,632 cr/hull-h | **154,059 cr** |
| **total cost of one extra crossing** | **≈ 168,000 cr** |

Note the shape: **the cash is 8% of the cost. Time is 92% of it.** A rotation policy that
economises tolls is optimising the wrong term.

### 7.3 The optimal visit length

Renewal-reward. Marginal credits per minute of tenure taken from the **balanced ≥30 min panel**
(so the marginal curve is not composition), then long-run rate = (cumulative profit − cash) ÷
(visit + transit).

| leave at | cumulative profit | cycle min | **cr/hull-h @ median transit (18.6m)** | @ mean transit (24.7m) |
|---:|---:|---:|---:|---:|
| 5m | 224,608 | 23.6 | 534,098 | 425,926 |
| 10m | 408,940 | 28.6 | 826,923 | 683,607 |
| 15m | 557,271 | 33.6 | 968,536 | 821,841 |
| 20m | 685,541 | 38.6 | 1,042,365 | 902,165 |
| **25m** | **780,191** | **43.6** | **1,053,068** | **925,690** |
| 30m | 837,818 | 48.6 | 1,015,910 | 904,268 |
| 45m (interp.) | ~918,238 | 58.6 | 924,966 | 839,040 |
| 60m | 1,034,029 | 78.7 | 778,089 | 722,884 |
| 90m | 1,226,322 | 108.7 | 669,436 | 634,367 |

**Optimum ≈ 25 minutes; bootstrap over visits (400 reps) gives p2.5 = 20m, median 25m, p97.5 = 25m.**
The curve is flat between 20 and 30 (1,016k–1,053k) and falls off hard on both sides.

**The fleet's actual visit is p50 10m / p75 18m / p90 31m.** Only 14.9% of visits, and 20.0% of
in-system hull-minutes, run past the 25-minute optimum.

**Break-even tenure — the direct answer.** A hull should leave when its *marginal* rate drops below
the *average* rate of a fresh cycle, which is the optimum's 1,053,068 cr/hull-h. Reading that off
the marginal curve: 25–30 min (691,521 → the crossing point sits just under 25 min). **A hull that
has been trading for 25 minutes, or has moved ~2,000 units, or has hit the 5th repeat sell on a
lane, has exhausted its ground.** All three markers land in the same place, which is reassuring.

**Conclusion for the strategy: the fleet is already rotating slightly earlier than optimal.** The
gain from moving *more* aggressively is negative; the 24.7-minute mean transit is larger than the
median visit, and the fleet already burns 61.8% of its hull-time paying it.

---

## 8. Is there anywhere to go? (Admiral Q3)

### 8.1 How fast a market we left recovers

`market_price_history` bid price relative to its **pre-TORWIND baseline** (median bid before our
first sale into that `(waypoint, good)`), by hours since our last sale. Only markets where we pushed
≥3 tranches.

| tranches pushed | markets | <1h | 1–2h | 2–4h | 4–8h | 8–16h | 16h+ |
|---|---:|---|---|---|---|---|---|
| 3–10 | 627 | 0.997 (p25 0.953) | 1.000 (0.962) | 1.003 (0.974) | 1.007 (0.982) | 1.006 (0.979) | 1.015 (0.995) |
| 10–30 | 309 | **0.978** (0.927) | 0.976 (0.934) | 0.994 (0.953) | **1.011** (0.966) | 1.013 (0.983) | 1.022 (1.001) |
| 30+ | 41 | **0.969** (0.852) | 0.964 (0.866) | **1.052** (0.973) | 1.049 (1.019) | — | — |

**Recovery is fast and the persistent scar is small.** Even for markets we pushed 10–30 tranches,
the median bid is 97.8% of baseline within an hour and back **above** baseline by 4–8 hours. The
**half-life is on the order of 1–2 hours**; the p25 deficit runs 7% at <1h and 3% by 4–8h.

This is not in tension with sp-as4k4's −50.75% gap at `vshare > 8`. That gap is the **within-visit,
transient** depletion the trace in §6.1 shows — the price recovers before the next
`market_price_history` observation (median inter-observation gap ~60 min) even sees it. **There is
no long-lived exhausted ground to flee, and correspondingly no large stock of recovered ground to
flee to.** The resource replenishes on the same clock the fleet already cycles on.

### 8.2 The supply arithmetic

| | value |
|---|---:|
| crossings per hour (88h basis) | 63.6 |
| systems needed in rotation for a 0.5h arrival cooldown | 32 |
| …for 1h | 64 |
| …for 2h | 127 |
| …for 4h | 254 |
| systems traded in the last 24h | **177** |
| unworked systems with ≥3 markets and ≥2 tradeable pairs | **79** |
| **total addressable** | **~256** |
| distinct systems touched per 1h / 6h / 24h | 65 / 135 / 177 |
| distinct markets touched per 1h / 6h / 24h | 350 / 961 / 1,372 |
| distinct (market, good) lanes per 1h / 24h | 766 / 5,101 |

**A 1–2 hour system cooldown is feasible on the ground we already know; a 4 hour cooldown is at the
edge of it.** The fleet is not supply-constrained at 30–60 minutes of spacing — **it is choosing to
concentrate.** Arrivals are heavily skewed: the top 5 systems take 14.2% of all arrivals, the top
20 take 41.7%, the top 50 take 69.8% (median 12 arrivals/system, max 167 over 88h).

### 8.3 The prize, and its ceiling

65% of visits (57% of matched capital = 5,060 M over 75h) land on ground touched <30 min ago. Moving
that capital onto ≥30-min-cold ground at the **system-FE** premium of +2.94 points is worth
**+1.98 M/h**; at the raw cross-sectional +5.66 points it would be +3.81 M/h. Take **+2.0 M/h
(≈ +8% of net)** as the defensible figure and the raw number as an optimistic bound.

**The ceiling on the whole strategy, stated as asked.** Because recovery is ~1–2 hours and 256
systems are addressable at 63.6 crossings/hour, this is **not** a "rotate through a small pool and
return to systems we already crushed" trap — the pool is large enough and refills faster than we
consume it. What caps the strategy instead is that **the ground is already good**: the +2.9-point
premium is the entire prize, the fleet already visits 65 systems an hour, and nothing in the data
supports the picture of a fleet parked on exhausted ground.

---

## 9. (e) / Q4 — the knob, and the recommendation

### 9.1 What `reposition_rate_floor_pct` actually is

Chain traced end to end. `[trade_fleet] reposition_rate_floor_enabled: true` and
`reposition_rate_floor_pct` **absent** (`gobot/config.yaml:224`), so
`resolveRateFloorPct(0) → 40` (`run_tour_coordinator_rate_floor.go:51`). The trigger fires **only
after a hull completes a PRODUCTIVE CONTINUOUS TOUR**, and then:

- **under-earner predicate:** hull's `MedianTourRate` over a 60-min window < 40% of the fleet median;
- **dwell:** 45 min since that hull last rate-floor-relocated (`…DwellMinutesDefault = 45`);
- **improvement ratchet:** best candidate's deadhead-netted projected rate ≥ **2×** the hull's
  realized rate, clamped so a configured value can never fall below **150%**;
- **anti-herd:** candidate must survive `excludeHerdedSystems` (5 resident hulls);
- **kill-switch:** `RepositionDisabled` silences it.

Bind semantics: the value is stamped into the per-hull `TRADING` container's launch config at
`StartTourRun` (`container_ops_tour.go:168`) and read back from the persisted row
(`command_factory_builders.go:667`). Changing it needs `make restart-daemon` **and** containers
re-stamped through `StartTourRun` — a recovery rebuild replays the old persisted value.

### 9.2 Reconstructing the trigger on live data

`MedianTourRate` reimplemented exactly (group by `tour_id` inside a 60-min window; a trade counts
only when the window holds **both** halves for that `(tour_id, good)`; rate = net ÷ span hours;
span ≥ 60 s; median across tours). Evaluated on a 30-minute step over the 48h ending 08-27 16:00Z —
94 windows, 4,552 hull-window observations.

Fleet median tour rate: p25 361,535 · p50 487,295 · p75 607,249 cr/h.
Hull rate as a fraction of the fleet median: p10 **−0.70** · p25 0.17 · p50 1.00 · p75 2.36 · p90 4.24.

| `reposition_rate_floor_pct` | hull-windows under floor | share | distinct hulls | flagged per hull/day |
|---:|---:|---:|---:|---:|
| **40 (live)** | 1,483 | **32.6%** | 79 | 4.63 |
| 50 | 1,623 | 35.7% | 79 | 5.07 |
| 60 | 1,757 | 38.6% | 79 | 5.49 |
| 70 | 1,889 | 41.5% | 79 | 5.90 |
| 80 | 2,013 | 44.2% | 79 | 6.29 |
| 100 | 2,252 | 49.5% | 79 | 7.04 |
| 120 | 2,557 | 56.2% | 79 | 7.99 |

Three things follow.

**(i) The knob is not selective.** The hull-rate distribution is enormously dispersed — a 60-minute
window routinely catches a hull mid-cycle with buys and no matching sells (p10 is *negative*), which
is why a third of evaluations already sit under 40%. Doubling the floor to 80% moves the flagged
share only 32.6% → 44.2%. There is no threshold in this distribution that separates "chronic
under-earner" from "caught mid-cycle".

**(ii) It is at the wrong granularity by ~16×.** Tour duration, which is the evaluation cadence:
p25 1.0h · **p50 2.8h** · p75 6.3h (n=513). System visit, where the decay happens: **p50 10 min**.
The trigger cannot act on a curve that has already run its course 16 times before the trigger next
looks.

**(iii) It is already non-binding.** Over 3.36h of daemon uptime, `tour_margins_death_total = 312`
and `tour_repositions_total{success} = 304`. The 3-strike margin-death tap-out already accounts for
every relocation; **the rate-floor path is contributing approximately zero.** Raising its threshold
raises a bar that nothing is currently jumping over.

### 9.3 Thrash risk, for completeness

The fleet's *existing* movement is the right baseline:

| | value |
|---|---:|
| crossings, 48h window | 3,908 = **81.4/h** = 1.02 per hull-hour |
| hulls in transit at any instant (81.4/h × 24.7 min) | **33.5 of 80 (42%)** |
| arrivals into one system in one hour | p50 2 · p90 4 · p99 6 · max 10 |

The anti-herd cap counts **resident** hulls (≤5), not arrivals, so a p99 of 6 arrivals into one
system in one hour passes it untouched. The 45-minute dwell and the ≥2× improvement ratchet are
robust anti-thrash gates and are not the constraint. **The thrash risk of raising the floor is low —
but so is the benefit, which is the point.**

### 9.4 The recommendation

**Do not raise `reposition_rate_floor_pct`. The fleet is already well placed and already rotating
near-optimally.** The evidence, assembled:

- **Zero** of 177 touched systems has negative attributed profit; the 12.35 M/h "drain" is 112%
  artifact.
- Only **6 of 31** occupied systems measurably underperform, holding **10 of 56** hulls; relocating
  every one of their hull-hours to fleet-mean ground is worth **+0.62 M/h (+2.4%) as a strict upper
  bound**, before any relocation cost.
- The renewal-reward optimum visit is **25 minutes** and the fleet's median is **10** — it leaves
  *too early*, not too late. Raising the floor pushes it further in the wrong direction.
- The knob evaluates at 2.8h against a 10-minute phenomenon and is already dominated by
  margin-death.
- **1.56 hulls/system is below any detectable crowding knee.**

**Expected magnitude if raised anyway:** between **0 and −0.3 M/h.** Zero because margin-death
already fires first and the improvement ratchet refuses marginal moves; negative because any
relocation the higher floor *does* add costs ~168,000 cr (92% of it dead time), and the marginal
relocation is by construction the least attractive one.

**What to do instead, in order of measured value:**

| # | change | expected value | status |
|---|---|---:|---|
| **1** | **Cut the ≥8th repeat sell on a lane** — sp-as4k4 **R1** (`TOUR_SOLVER_REALIZED_SINK_TRANCHES` 2.5 → 2.0, routing-service restart only) and **sp-k1x3h** (per-tranche sell floor) | **+0.87 M/h** measured loss removed | already recommended; this analysis independently confirms the tail at −14.11% on 461 M |
| **2** | **A fleet-level arrival cooldown** — refuse or de-rank a system another TORWIND hull left less than ~30 minutes ago | **+2.0 M/h** (system-FE basis; raw bound +3.8) | **no mechanism exists — needs code** (see §11) |
| **3** | Relocate the six measured underperformers by hand, once | +0.62 M/h **upper bound**, realistically much less | operator action; not worth a knob change |
| — | raise `reposition_rate_floor_pct` | **0 to −0.3 M/h** | **do not** |

---

## 10. What the data cannot tell us

1. **No A/B.** Every forward-looking number is a prediction from observational data. The +2.94-point
   spacing premium is a fixed-effects estimate, not a randomised one; a system left alone for 30
   minutes may differ from the same system 10 minutes after a departure in ways the system dummy
   does not absorb.
2. **The renewal optimum's level is not the fleet's rate.** The 25-minute optimum is read off a
   marginal curve estimated on visits that *lasted ≥30 minutes*, a selected sample. The **shape**
   is what is claimed; the ~1.05 M cr/hull-h level excludes transit and is ~2.6× the all-in rate.
3. **"Transit" is time-not-trading, not pure travel.** The 24.7-minute mean gap between the last
   trade in one system and the first in the next includes gate travel, jump cooldown, intra-system
   navigation, docking, planning, and any no-plan idle. It is the right cost for a renewal
   calculation and the wrong number to quote as "travel time".
4. **Recovery baselines are approximate.** The pre-TORWIND baseline is the median bid before our
   *first* observed sale into that `(waypoint, good)`; for markets we have worked for days that
   baseline is old, and `market_price_history` has a median ~60-minute cadence, so the fastest part
   of the recovery is unobserved.
5. **Unoccupied-system inference is quotes only.** §4.3 lists the five things market_data cannot
   say. In particular, 374 of 7,763 gate-graph systems carry any market data; the other 95% is a
   sensing question this analysis is silent on.
6. **The fleet grew inside the primary window** (59 → 74 hulls between 12:00 and 14:00 on 08-27).
   Hull-hours are measured, not assumed, so this affects composition rather than arithmetic, but the
   per-system hull-hour figures blend a pre- and post-growth fleet.
7. **Daemon counters are since-restart.** The 304/312 reposition and margin-death counts are over
   3.36h of uptime; the ratio between them is the finding, not the absolute level.
8. **One era, one window,** as sp-as4k4 also noted.

---

## 11. Follow-ups filed

| bead | title | expected value |
|---|---|---|
| **sp-xmut5** | Space fleet arrivals in TIME, not just in resident count — de-rank a system another TORWIND hull left < 30 min ago | **+2.0 M/h** (system-FE), raw bound +3.8 M/h |
| **sp-ez973** | Emit visit tenure, per-visit cumulative units and lane repeat-ordinal as telemetry columns + Prometheus gauges | measurement enabler; today all three must be reconstructed offline |
| **sp-pq8sa** | `reposition_rate_floor_pct` evaluates at 2.8h against a 10-minute decay and is dominated by margin-death — re-point or document as inert | prevents a future lane tuning a knob that cannot bind |

---

## 12. Reproduction

Scripts under session scratch (`pull.py`, `lib.py`, `a_systems.py`, `c2_tenure.py`, `d_optimum.py`,
`e_supply.py`, `f_knob.py`, `g_decomp.py`), not committed. The load-bearing SQL:

```sql
-- 1. The naive per-system view and its window dependence (§2.1). Vary the interval.
SELECT split_part(waypoint,'-',1)||'-'||split_part(waypoint,'-',2) AS system,
       sum(realized_units*realized_unit_price) FILTER (WHERE is_buy)        AS buys,
       sum(realized_units*realized_unit_price) FILTER (WHERE NOT is_buy)    AS sells,
       sum(realized_units*realized_unit_price) FILTER (WHERE NOT is_buy)
     - sum(realized_units*realized_unit_price) FILTER (WHERE is_buy)        AS net
FROM tour_leg_telemetry
WHERE player_id=9 AND realized_units>0
  AND realized_at > timestamptz '2026-08-27 16:00Z' - interval '4 hours'
  AND realized_at < timestamptz '2026-08-27 16:00Z'
GROUP BY 1 ORDER BY net;

-- 2. System VISITS and their length (§6, §7). leg_index cycles, so segment on the system
--    changing between consecutive legs of one hull, never on leg_index.
WITH l AS (
  SELECT ship_symbol, realized_at,
         split_part(waypoint,'-',1)||'-'||split_part(waypoint,'-',2) AS system,
         lag(split_part(waypoint,'-',1)||'-'||split_part(waypoint,'-',2))
           OVER (PARTITION BY ship_symbol ORDER BY realized_at, id) AS prev_system,
         lag(realized_at) OVER (PARTITION BY ship_symbol ORDER BY realized_at, id) AS prev_at
  FROM tour_leg_telemetry
  WHERE player_id=9 AND realized_units>0 AND realized_at > now() - interval '48 hours')
SELECT count(*) FILTER (WHERE system IS DISTINCT FROM prev_system)          AS crossings,
       percentile_cont(0.5) WITHIN GROUP (
         ORDER BY extract(epoch FROM realized_at-prev_at)/60)
         FILTER (WHERE system IS DISTINCT FROM prev_system)                 AS median_transit_min,
       percentile_cont(0.5) WITHIN GROUP (
         ORDER BY extract(epoch FROM realized_at-prev_at)/60)
         FILTER (WHERE system = prev_system)                                AS median_insystem_min
FROM l WHERE prev_at IS NOT NULL;

-- 3. Movement cash (§7.2)
SELECT category, count(*) n, sum(-amount) credits,
       percentile_cont(0.5) WITHIN GROUP (ORDER BY -amount) median
FROM transactions
WHERE player_id=9 AND operation_type='tour'
  AND timestamp > now() - interval '48 hours'
  AND category IN ('FUEL_COSTS','TRAVEL_COSTS')
GROUP BY 1;

-- 4. Recovery of a market we stopped selling into (§8.1)
WITH ours AS (
  SELECT waypoint, good, min(realized_at) first_sell, max(realized_at) last_sell,
         sum(realized_units) units
  FROM tour_leg_telemetry
  WHERE player_id=9 AND NOT is_buy AND realized_units>0
  GROUP BY 1,2)
SELECT width_bucket(extract(epoch FROM h.recorded_at-o.last_sell)/3600,
                    0, 32, 8)                                   AS hours_bucket,
       count(*), count(DISTINCT h.waypoint_symbol) markets,
       round(percentile_cont(0.5) WITHIN GROUP (ORDER BY h.sell_price::numeric/b.base),3) med
FROM ours o
JOIN market_data m ON m.player_id=9 AND m.waypoint_symbol=o.waypoint AND m.good_symbol=o.good
JOIN LATERAL (SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY sell_price) base
              FROM market_price_history p
              WHERE p.player_id=9 AND p.waypoint_symbol=o.waypoint
                AND p.good_symbol=o.good AND p.recorded_at < o.first_sell) b ON b.base>0
JOIN market_price_history h ON h.player_id=9 AND h.waypoint_symbol=o.waypoint
                           AND h.good_symbol=o.good AND h.recorded_at > o.last_sell
WHERE o.units >= 3*m.trade_volume
GROUP BY 1 ORDER BY 1;

-- 5. Fleet-level arrival freshness — the monitoring query for recommendation #2 (§6.4, §8.3)
WITH v AS (
  SELECT ship_symbol, realized_at,
         split_part(waypoint,'-',1)||'-'||split_part(waypoint,'-',2) AS system
  FROM tour_leg_telemetry
  WHERE player_id=9 AND realized_units>0 AND realized_at > now() - interval '24 hours')
SELECT round(avg(CASE WHEN gap_min < 30 THEN 1 ELSE 0 END)*100,1) AS pct_arrivals_on_hot_ground
FROM (SELECT a.system,
             extract(epoch FROM a.realized_at - max(b.realized_at))/60 AS gap_min
      FROM v a LEFT JOIN v b
        ON b.system=a.system AND b.ship_symbol<>a.ship_symbol
       AND b.realized_at < a.realized_at
      GROUP BY a.system, a.realized_at) g;
```

**FIFO matcher** (not expressible in SQL): walk `tour_leg_telemetry` for `player_id=9` ordered by
`(ship_symbol, realized_at, id)`; per `(ship, good)` push buy lots onto a deque and pop them on
sells, emitting one row per `(buy lot, sell leg, units)` triple carrying both waypoints and both
systems. Validate against `sum(ships.cargo_units)`.

---

## 13. Standing conclusions

- **Never rank systems on `sells − buys` grouped by waypoint.** One third of the fleet's capital
  crosses a system boundary between its two halves, and the metric also shrinks by 3× purely as the
  observation window lengthens. Attribute matched pairs to the (source, sink) pair and split; the
  choice of split rule does not matter (rank correlation 0.984 between two very different rules),
  the choice to split at all matters enormously (0.56 against the naive view).
- **The fleet is well placed.** Zero systems lose money; six of thirty-one occupied systems
  measurably underperform, holding ten hulls, worth at most +2.4% of gross.
- **The fleet is well timed, and if anything leaves too early.** The optimum visit is ~25 minutes;
  the median is 10. Transit is 61.8% of all hull-time, so any policy that adds crossings is fighting
  the dominant cost term.
- **The decay is real and steep — 24.2% margin in the first five minutes, 7.0% past thirty — but it
  is a LANE phenomenon.** The 9th repeat sell of one good at one dock inside one visit runs at
  −14.11%. Cap the lane, not the tenure.
- **What predicts a fat visit is how long the ground has been left alone by ANY hull, not by this
  one:** +2.94 points (95% CI [+1.83, +4.20]) for ≥30 minutes of cooldown. 65% of visits land inside
  that window. **This is the one genuinely unexploited lever this analysis found, and no live knob
  reaches it** — the anti-herd cap counts residents, not recent visitors.
- **Markets recover in 1–2 hours.** There is no exhausted ground to flee and no large reserve of
  recovered ground to flee to; the resource replenishes on the clock the fleet already cycles on.
- **1.56 hulls/system is below the crowding knee.** The marginal hull is worth 85–140% of a solo
  hull through five. Fleet size remains uncorrelated with margin; do not cap it on this evidence.

---

*Measurement session, 2026-08-27. Read-only against the live database and the daemon's `/metrics`
endpoint; no production configuration, service or source behaviour was modified.*
