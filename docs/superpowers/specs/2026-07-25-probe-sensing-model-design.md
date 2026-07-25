# Probe Sensing Model — era 5 redesign

## Problem

The SpaceTraders API is capped at **2.00 req/s**, server-imposed and unraisable
(`x-ratelimit-limit-per-second: 2`, type `IP Address`). It is the fleet's permanent binding
constraint, and it binds earlier each era.

Era 4 measured:

- ~200 probes flew **25,829 of 31,332 legs and never docked**
- Scouting was ~77% of daemon activity and ~84% of navigation
- 355 systems scanned; 72–99 systems actually traded in
- Freshness demand reached **339 probes against a supply of 200** — chronically under-provisioned
- Requests per completed transaction: **~63**, against a floor of ~4

The cause is the freshness sizer's demand model:

```
required_probes = ceil(markets × per_market_cycle / sla)   [summed over every in-scope system]
```

This grows without bound as the map grows. Every system charted becomes a permanent freshness
obligation, so exploration ratchets sensing cost upward forever. Nothing retires a system unless
its markets literally vanish.

## Objective

Sensing cost must be **bounded by a budget we choose**, not derived from the size of the map.
The fleet should chart aggressively early, then settle into a fixed sensing footprint.

## Design

### 1. Goods whitelist (12 goods, era-invariant)

A market is worth observing if it trades goods that historically earn. Derived from
`tour_leg_telemetry` across eras 2, 3 and 4 — **31,761 legs, 51 distinct goods**.

Net profit per good, three eras pooled:

| good | eras traded | net | margin/unit | units sold |
|---|---|---|---|---|
| CLOTHING | 3 | 123.5M | 1,520 | 68,094 |
| LAB_INSTRUMENTS | 3 | 90.4M | 1,505 | 43,584 |
| FABRICS | 3 | 73.9M | 636 | 72,193 |
| FOOD | 3 | 72.3M | 511 | 101,367 |
| ADVANCED_CIRCUITRY | 3 | 71.6M | 1,481 | 45,130 |
| MEDICINE | 3 | 70.9M | 1,269 | 47,740 |
| EQUIPMENT | 3 | 59.8M | 906 | 59,618 |
| URANITE | 3 | 48.5M | 936 | 34,587 |
| MICROPROCESSORS | 3 | 31.6M | 1,177 | 20,976 |
| SHIP_PLATING | 3 | 30.3M | 2,562 | 10,461 |
| MACHINERY | 3 | 29.0M | 911 | 26,345 |
| ELECTRONICS | 3 | 24.5M | 797 | 22,758 |

The bottom of the table is raw ore — QUARTZ_SAND, PLATINUM_ORE, SILVER_ORE, AMMONIA_ICE,
GOLD_ORE — all ≈ 0.00M net. The split is structural (manufactured goods carry margin, raw ores
do not), and **every good above scored in all three eras**, so it is not era-specific luck.

The whitelist is era-invariant because goods are game constants; the map is not.

**Measured filtering effect** (era-4 map, 4,307 markets, 381 systems):

| whitelist size | markets kept | reduction |
|---|---|---|
| 5 | 1,828 | −58% |
| **12** | **2,490** | **−42%** |
| 16 | 2,782 | −35% |
| 20 | 2,861 | −34% |

The curve flattens past 16 — goods 17+ add nothing, consistent with the tail being raw ore.
Per system: average markets drop **11.3 → 6.5**, and **71 of 381 systems (19%) trade no
whitelisted good at all** and need no probe ever.

### 2. Depth floor

Whitelisted-goods depth per system, `Σ(trade_volume × mid_price)` over the 12 goods, is skewed:

```
min 0.09M   p25 1.95M   median 4.30M   p75 8.51M   p95 26.17M   max 84.62M
```

Systems below the floor have almost nothing for a probe to look at. A static floor (~2M) drops
roughly the bottom quartile with negligible information loss.

**No ranking above the floor.** Systems above it are treated as equal. Rationale:

- Depth measures market **size**, not arbitrage **opportunity**. A deep market with no spread
  earns nothing, and depth cannot see the difference.
- Ranking causes churn, and churn moves probes. Probe movement is the API spend being
  eliminated. A wrong ranking that also generates traffic is worse than no ranking.
- Era-4 trade concentration reflected **where hulls happened to be**, not which systems were
  better. Ranking on it would encode our own placement history as system quality.

A floor is static, so nothing churns; a ranking is continuous, so everything does.

### 3. Probe allocation

- **One probe per in-scope system.** In scope = clears the whitelist and the depth floor.
- **A second probe above 12 hot markets**, because one probe walking a large market set makes
  the cycle so long that the data is stale before it loops back. Most systems sit at 3–8 hot
  markets and need only one.
- **`N` is the single budget dial** — the total probe count the fleet may hold. It is the only
  number standing between us and era 4, and it is operative within hours, not a distant ceiling.

Measured against era 4's map, scoped to its actual footprint: **100–150 probes** depending on
whitelist size and second-probe threshold, versus ~200 run in era 4.

The structural win is larger than that ratio. Era 4's probe demand scaled with the **charted
map**; this scales with a **chosen budget**. Cold start needs one probe, not two hundred, and
charting a new system adds no sensing obligation until it clears the filters.

### 4. Two-stage scanning

- **Stage 1** — on first visit, scan every market in the system.
- **Stage 2** — thereafter, scan only markets carrying whitelisted goods.

**This selects markets by what they deal in, not by what they are currently worth**, which is
what makes it safe. Market prices are volatile and non-monotone: a market we crushed by dumping
into it reads low-value precisely when it is about to recover into being our best sink. Selecting
on current value would drop it and never look again. Selecting on traded goods keeps it in scope,
because what a market deals in is stable.

One VRP per system, sized to the count of whitelisted-goods markets.

### 5. Rotation under API pressure

- **Trigger: rate-limiter wait time** (`spacetraders_daemon_api_rate_limit_wait_seconds`).
  Utilisation is useless as a signal — it reads ~100% whenever the daemon has work queued. Wait
  time measures actual contention: near zero with headroom, climbing when calls queue.
- **Rotate the scanning, not the probes.** A probe that repositions costs navigate + jump + fuel.
  A parked probe that simply does not scan this cycle costs **zero**. Systems go dormant in place
  and wake in turn; hulls never move.
- **Fleet-wide, round-robin.** The limiter is a single global queue, so contention is not
  attributable to any one system. Round-robin guarantees every system scans within
  `ceil(in_scope / active)` cycles — degradation is graceful and bounded, never open-ended.
  Value-ranked rotation would starve exactly the crushed markets we most need to watch recover.

Under pressure the fleet sheds scanning **before** the API starts refusing calls, so era-4
saturation cannot recur.

### 6. Discovery

**Chart and first-scan are one atomic trip.** The probe is already in the system; the scan is
marginal cost. This deletes the `discover_scan_balance` knob outright — the knob exists only
because the two are separate post types today, letting charting outrun scanning and pile up
systems we know exist but cannot evaluate.

**Self-propagating branching purchase.** On charting a new system, buy **one probe per connected
uncharted neighbour**, subject to `N` and the money guards.

Justification: gate-graph branching is **avg degree 4.12, median 4, max 9** (1,170 systems; 73%
at degree 3–5). Buying a single probe explores one direction and leaves the rest — requiring a
backlog queue, a re-dispatch policy, and a selection rule. Buying one per direction matches the
branching exactly at the moment it is discovered, so **nothing is deferred and there is no queue
to build**. The simplification is the point; the speed is a bonus.

Probes are cheap and locally available: **166 of 382 charted systems (43%) sell probes** across
260 yards, median price **23,540**. A full 300-probe fleet costs ~7M. Buying at the frontier
means every new probe's first trip is one hop, so expansion cost stays flat with distance instead
of growing. No probe ever returns home.

**Discovery is funded by headroom**, using the same pressure signal as rotation: when limiter wait
is low, spare probes explore; when it climbs, discovery stops **first**, because it is
speculative. Steady scanning of known-good systems degrades only after exploration has stopped.

A system that fails the whitelist or depth floor on its first scan is dropped.

### 7. Home system

3 probes during INCOME (existing `defaultProbeTarget`), retiring to 1 at EXPANSION.

Home is **not** special-cased beyond this. Measured in era 4: home was `X1-DY16` and did not
appear in the top eight traded systems — trade concentrated in `X1-YY85` (38.9M), `X1-UM5`,
`X1-HF20`. Home is where the fleet starts, not where it earns. Once EXPANSION begins it is an
ordinary system, in or out of scope on identical terms, and subject to rotation like any other.

### 8. Cooldown

`purchase_cooldown_secs` default **60 → 10**. Branching fan-out buys ~4 probes per system; a 60s
cooldown serialises that into four minutes and throttles early expansion for no benefit, given
probes cost ~23.5k against a treasury in the tens of millions.

## Implementation strategy

Build a **new single coordinator from scratch**. Retrofitting the freshness sizer would preserve
machinery this design deletes.

**Deleted by this design:** the SLA table (`sla_seconds`, `_weak`, `_growing`, `_strong`,
`_restricted`), `target_percentile`, `breach_response_percent`, `cycle_dampening_percent`,
`worst_cycle_seconds`, `release_slack_percent`, `release_stable_window_secs`,
`max_probes_per_system`, `discover_scan_balance`, `reach_mode`, both reservation floors
(`reserved_frontier_floor`, `reserved_freshness_floor`), the P90 breach model, `TradedFootprint`,
`DemandWeightsBySink`, the discovery allowance, and the existing probe purchasing algorithm.

**Surviving knobs: three.** The whitelist, the depth floor, and `N`.

The legacy coordinators are **unwired** — removed from the launch path so only the new coordinator
runs. Their source stays in the tree as reference until the new design is proven in era 5, then is
deleted. There is no runtime switch between them and no fallback path: unreferenced code pending
deletion, not a second implementation held in reserve.

This is deliberate. A switch between two coordinators would be a feature flag, which standing
doctrine forbids, and a fallback nobody exercises is a fallback that does not work when needed.

## Risks

**Dropped systems are dropped on one observation.** Which goods a market trades is stable, so the
whitelist half of the judgement holds; depth can shift, so a system thin when observed might not
stay thin. Re-checking everything dropped costs exactly what this design saves. Accepted, with
re-evaluation only if a probe passes through anyway. This is a known blind spot, not an oversight.

**The whitelist is derived from tour telemetry only.** The arb and idle-arb engines write
absorption-ledger rows but not tour-leg telemetry, so goods those engines favour may be
under-counted. 31,761 legs across three eras is a strong sample and the manufactured-vs-raw split
is structural rather than sampling noise, but the blind spot is real.

**`N` fills fast.** Branching factor 4 from one seed reaches N=300 in about five generations, so
`N` becomes the operative constraint within hours of an era start. It is not a safety margin.

**Branching fan-out is bursty.** Even at a 10s cooldown, `max_spend_per_cycle` may pace a
multi-probe buy. Whether probes get an exemption or the pacing is accepted needs deciding.

## Measurement

Success is judged on **requests per completed transaction** (era-4 baseline: ~63, floor ~4) and on
scouting's share of daemon activity (era-4 baseline: ~77%). Probe count is an input, not a result.

Grade over ≥30 minutes or one full sweep, whichever is longer. Short windows are actively
misleading here: scanning is bursty, and era-4 measurement produced a fake 96% improvement from a
3-minute window and a fake 21% budget share from a 150-second one. Prefer
calls-per-useful-update (API calls ÷ distinct rows actually written) over raw rate — that ratio
exposes redundancy and is stable across window sizes.
