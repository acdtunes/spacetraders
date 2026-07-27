# Parked-Probe Sensing — probe-per-market redesign (sp-k6v8z)

Supersedes the **touring core** of `2026-07-25-probe-sensing-model-design.md`. Everything that
design got right — the 12-good whitelist, the wait-time pressure signal, discovery-yields-first,
branching frontier purchase, chart+scan atomicity — survives. What dies is touring itself.

## Problem

The API is capped at **2.00 req/s**, permanent and unraisable. The era-5 shipped design bounded
sensing *demand* by budget (probe-per-system, VRP touring of whitelisted markets), but touring
still pays **~2–3 calls + transit time per observation** (navigate/orbit/GET), and intra-system
navigation remains the dominant sensing waste. Era-4 reference: ~63 requests per completed
transaction against a floor of ~4; scouting was 77% of daemon activity and 84% of navigation.

The shipped design also carries a known blind spot it documented itself: systems dropped by the
depth floor are dropped on one observation and never re-checked.

## Objective

- **Every steady-state observation costs exactly 1 GET.** Zero sensing navigation after placement.
- **Probe count and API cost decouple.** A parked probe that is not scanned costs zero API. The
  API dial lives entirely in the scan scheduler; probe count is pure capex, throttled by treasury.
- **Full-map planner visibility with bounded staleness.** Dark and thin markets stay visible
  forever at low cadence — the planner needs to see dormant routes to ever route through them.
- **Trading always outranks sensing**, enforced per-call at the rate limiter, not by politeness.

## Design

### 1. Probes are parked assets, not agents

Lifecycle: `QUEUED → BOUGHT → IN_TRANSIT → PARKED`, and **PARKED is terminal** — the probe docks
at its slot and never moves, orbits, or acts again. Its only navigation ever is the one placement
trip (purchase yard → slot). After parking, no API call ever originates *for* the probe; the scan
scheduler GETs the market *using* its presence (price data requires a ship at the waypoint; the
goods list does not — this asymmetry is load-bearing, see §5). No per-probe containers, no VRP.

### 2. The placement ledger

A coordinator-owned DB table, one row per **slot** — a waypoint we want permanent presence at:

- **Market slots:** every market carrying ≥1 whitelisted good in a screened system.
- **Quartermaster slots:** one per system at its probe-selling shipyard, where one exists. Ship
  purchase requires a docked ship at the yard, so a parked quartermaster makes every future fill
  purchase in that system instant. It also scans the yard on a slow cadence (ship prices, probe
  stock for the buy planner).

Row: slot waypoint → assigned probe, state, purchase yard, measured depth, spread stats. Ledger
states include `SPARE` (see §5). Boot recovery re-derives everything from the ledger.

**Structural rule: no O(fleet) API enumeration, anywhere, ever.** Probe positions are known
because we sent them; arrivals confirm through the daemon's own ship-state tracking (single
writer). Fleet enumeration is a DB query. This is an explicit implementation-audit gate.

### 3. Scope: the whitelist screen is the only gate

The 12-good whitelist (era-invariant, derived from 31,761 telemetry legs across eras 2–4) is
unchanged. What changes is *how it gates*:

- **Remote screen, zero presence, zero probes:** a market's imports/exports/exchange list is
  visible by plain GET on any *charted* waypoint. A system with no whitelisted good anywhere is
  dropped without ever buying a probe (~19% of systems on era-4 data). Where the DB already holds
  scanned market data, the screen runs **offline** against Postgres first, API only for gaps.
- **The depth floor is demoted from scope gate to queue ordering.** It existed to bound probe
  count because probe count drove API cost; parking broke that link. Deep systems fill first,
  thin systems fill last, **nothing in-whitelist is excluded**. Thin markets get parked probes at
  a low value weight — scanned rarely, near-zero API cost, visible to the planner forever. This
  deletes the shipped design's "dropped on one observation" blind spot outright.

### 4. The buy queue: sensing is the residual claimant on treasury

Sensing is the elastic, lowest-priority consumer on **both** axes — API (§6) and credits (here).

**Drain rule** — pop the queue while both hold:

```
treasury − price ≥ probe_buy_floor        AND        owned_probes < probe_cap
```

**`probe_buy_floor` is dynamic**, not a constant:

```
probe_buy_floor = ImmutableReserveFloor                        (50k, RULINGS #5, untouchable)
                + k × trailing cargo-buy spend EWMA (1h)        (trading working capital, k=2)
                + declared pending ship-capex intents           (bootstrap / fleet-autosizer)
```

Trading busy → floor rises, sensing waits. Fleet idle-rich → floor falls toward 50k and the queue
drains hard. Including capex intents slots sensing in as the **third and lowest buyer** under the
existing single-buyer arbitration (ktio-B seam); it can never starve bootstrap's first hauler or
the autosizer's scaling. The floor is also the phase gate: early-era treasury never clears it, so
sensing capex stays silent through DATA/INCOME with **no phase coupling anywhere**.

**No purchase cooldown and no per-cycle spend cap for the probe class.** The cap and the floor
are the only limits, by direct Admiral directive. Consequence accepted: when the residual is
high, the queue drains in bursts — tens of probes in minutes. A quartermaster buys its system's
entire fill complement in one docked session.

**Queue ordering:** in-scope market fills before frontier seeds (exploit before explore on the
credit axis — self-balancing early-era, when the queue is mostly seeds anyway); deepest measured
system first within fills; FIFO within a system. Buy-order ranking causes no churn — each buy
happens once. Purchases execute at the nearest known probe-selling yard (43% of systems sell
probes, median price 23.5k); the queue prefers dispatching a nearby `SPARE` over buying new.

### 5. Expansion: a BFS over the gate graph, behind an operator switch

**Charting facts the design leans on:** charts are permanent and global across agents; uncharted
waypoints hide their *traits* (MARKETPLACE, SHIPYARD) but not their existence; the remote screen
therefore works only on charted waypoints; prices need presence, goods lists do not.

- **Charted neighbour:** no seed at all. Remote screen → fills enqueue → probes fly direct.
- **Seed dispatch rule: the system has ≥1 uncharted waypoint** — including already-in-scope
  systems hiding markets behind uncharted waypoints. One seed per gate connection, bought at the
  nearest probe-selling yard, funded by the same queue (after fills).
- **The chart tour is the only touring left in the fleet.** The seed visits every uncharted
  waypoint and charts it — a one-time capital investment per waypoint *in the universe*,
  amortized forever, converging to zero work as the map completes. At each revealed whitelisted
  market the seed takes **one price reading while standing there** (1 call, zero navigation), so
  every slot enters the buy queue with measured depth instead of a blind prior.
- **Pipelining:** nothing waits on charting globally. The instant a waypoint reveals a
  whitelisted market, its fill enqueues — fills stream in behind the tour waypoint-by-waypoint,
  and parked probes start scanning while the seed is still charting the far side of the system.
  Chart tours are nearly free; fills are the capex. The frontier deliberately **runs ahead of the
  wallet**, building an inventory of screened, depth-rated, fill-ready slots that the queue
  drains as treasury allows. (`discover_scan_balance` stays dead; the queue and the floor
  arbitrate.)

**Seed terminal states** — no probe is ever idle by accident:

1. System in-scope, has a probe yard → seed parks at the yard as **quartermaster**, buying the
   system's fills in one docked session on the way in.
2. In-scope, no local yard → seed parks as an ordinary **fill** at the nearest whitelisted
   market; remaining fills fly in from the nearest external yard.
3. Nothing whitelisted → seed **carries the frontier onward** through the dead system's gates to
   the next unevaluated connection. Only with no reachable frontier work does it park in place as
   `SPARE`, reclaimable by the queue as a future seed or fill.

The whole engine sits behind `expansion_enabled` (operator kill-switch) and pauses first under
API pressure (§6) and treasury pressure (§4).

### 6. The scan scheduler

**One loop, one meter.** A single pacer issues *all* parked-market scans — nothing else in the
fleet GETs a market. Stampedes are impossible by construction: there are no per-probe timers, and
a freshly parked probe's first scan enters the same queue (staleness ∞ → pops soon, paced).

**Paced-parallel dispatch.** The pacer launches one scan every `1/sensing_rate` seconds — the
spacing is the anti-stampede property. Execution is async in a small worker pool (a serial
issue-await loop would couple throughput to API latency; only the pacer decides the rate), with
**in-flight capped at 3**. The cap never binds in health (avg in-flight ≈0.7 at full rate); it is
(a) the fastest backpressure reflex — three scans stuck waiting for limiter tokens halt the pacer
within one call-latency, long before any EWMA notices — and (b) a hard bound on sensing's
occupancy of the limiter queue, so sensing can never stand more than 3-deep ahead of a trade.
Same-market overlap is impossible (a market reschedules only on scan completion; intervals are
minutes against sub-second latencies). DB writes happen on workers, never the pacer thread.

**Budget — residual claimant, recomputed continuously:**

```
sensing_rate = clamp( target_util × 2.0 − nonsensing_ewma ,  min_scan_rate , ∞ )
```

`target_util` 0.925 (the 90–95% band; the remainder is burst reserve so a sudden trade flurry
never queues). `nonsensing_ewma` comes from the source-tagged budget meter (§7). `min_scan_rate`
0.1 req/s so planner data never goes fully dark. On top of the arithmetic, the shipped design's
proven contention signal — `rate_limit_wait` p95 — applies a multiplicative back-off when wait
climbs anyway (arithmetic lies when bursts align).

**Ordering — staleness × spread value, as a next-due heap.** Each market gets a rate share
proportional to its value weight; the heap is keyed on `next_due = last_scan + interval`, O(log n)
per pop, no rescoring sweeps; budget changes renormalize one scalar.

- `value` = EWMA of observed relative spread across whitelisted goods at that market.
  **Market-intrinsic only — no route-activity weighting**, so dark markets the planner might
  route through tomorrow are valued on their own history, and our placement never feeds back
  into what we watch.
- Weights clamp to **[1, R], R = 4**: the hottest market scans at most R× more often than the
  coldest. The clamp is the anti-starvation guarantee — worst-case staleness ≤ R × the uniform
  interval, for every market, always.
- New markets get an **optimistic prior** (p75 of the fleet spread distribution), converging to
  measured over ~5 scans — discovery is never starved by its own lack of history.

**Approved freshness envelope** (illustrative at ~20% hot, 3,000 markets ≈ full map including
thin systems; 1,500 markets ≈ era-4 in-scope scale — halve the intervals):

| tier | at full residual (~1.8 req/s) | under heavy trading (~0.5 req/s) |
|---|---|---|
| hot (high spread) | ~8–11 min | ~30–40 min |
| median | ~30 min | ~100 min |
| coldest (bound) | ≤ R × uniform ≈ 2 h | ≤ ~7 h |

**Yield order under pressure:** expansion pauses first (speculative; same meter) → scan cadence
stretches uniformly (ordering preserved) → trades never wait (§7). Quartermaster yard scans ride
the same heap at a floor weight, ~hourly.

### 7. Source-tagged API budget and right-of-way

**One tag, two consumers.** Every API call is tagged with a `source` at the call site through the
existing `apibudget.Purpose` seam — `trading`, `contract`, `navigation`, `bootstrap`, `charting`,
`scanning`, extended as needed. From that single taxonomy:

1. **Right-of-way (per-call):** the existing priority scheduler in the rate limiter — HIGH
   acquires contended tokens ahead of LOW, bounded aging prevents starvation — **is armed by this
   design**. Mapping: trading + contract-delivery = HIGH; navigation/dock/refuel = NORMAL,
   promoted via the existing `WithPriority` when enabling an imminent trade; charting + scanning
   = LOW. A sensing call already dispatched loses the next token to a trade call arriving a
   moment later.
2. **Allocation (seconds):** the §6 residual arithmetic consumes the tag-derived non-sensing
   EWMA. Trading spend rises → sensing shrinks within ticks.

**Deliberately no per-source quotas.** Quota systems waste idle shares and breed knobs. Sensing
is the only source with unbounded appetite; trading, contracts and navigation are intrinsically
bounded by fleet size and tour structure. Bounded sources need right-of-way, not rations; the one
unbounded source takes the residual. Exploration lives inside sensing's budget and yields first
within it.

### 8. Home system

Unchanged from the shipped design's §7: bootstrap owns its early probes (3 during INCOME,
retiring to 1); home becomes an ordinary system at EXPANSION, in or out of scope on identical
terms.

## Architecture

**Evolve `probe_sensing_coordinator` in place** (approved over a from-scratch rebuild and a
two-container split). The container, boot-standing wiring, tune surface, whitelist config and
wait-time signal survive; the touring core (per-system VRP, 1-probe-per-system allocation,
rotation/dormancy) is replaced by three internal engines: **placement ledger**, **scan
scheduler**, **expansion engine** — distinct packages, one container, no fallback path, no
runtime switch. Parked probes have no containers at all.

## Knobs

All live operator tunables (3-layer knob system). No arming seams — ships armed on deploy.

| knob | default | role |
|---|---|---|
| `probe_cap` | 3000 | hard ceiling on ledger-owned probes (safety rail, not a scoping tool) |
| `expansion_enabled` | on | operator kill-switch for the expansion engine |
| `target_util` | 0.925 | total API utilisation target (90–95% band) |
| `min_scan_rate` | 0.1/s | sensing never fully dark |
| `R` | 4 | value-weight clamp; worst-case staleness = R × uniform |
| `inflight_cap` | 3 | max concurrent sensing calls |
| `k` | 2 | trading working-capital multiplier in the buy floor |
| `quartermaster_cadence` | 1h | yard scan interval |

**Deleted:** per-system VRP touring, the 1-per-system/+1-above-12-markets allocation, rotation
and dormancy machinery, `purchase_cooldown_secs` and `max_spend_per_cycle` for the probe class.
The depth floor survives only as buy-queue ordering.

## Cutover

Era-5-now is the cheapest possible moment: the fleet is tiny, treasury is small, and the residual
floor keeps capex silent until income flows. Coordinator restart adopts the new core (recoverable
container). The ledger bootstraps **from the DB, not the API**: the remote screen runs offline
against already-scanned market goods lists; existing sensing probes are assigned to their nearest
slots; bootstrap's home probes are untouched. Armed on deploy per standing doctrine — the knobs
above are operator dials, not arming seams.

## Risks

1. **Queue-drain bursts are by design** (no cooldown): bounded by cap + floor; watch the first
   large drain live.
2. **Arming the priority scheduler reorders live limiter traffic for the first time.** Grade
   trade p95 limiter-wait before/after as a deploy gate.
3. **Spread-EWMA cold start** mis-orders briefly; bounded by R, self-heals in ~5 scans/market.
4. **Yard scarcity at the frontier** (43% of systems sell probes) lengthens placement trips —
   SPARE reuse and nearest-yard selection mitigate; failure mode is slow fills, never wrong ones.
5. **O(fleet) enumeration audit** is a hard implementation gate (§2). Fleet grows ~10×; any
   hidden full-fleet API sweep becomes a budget hole.
6. **Capex concentration:** ~35M at era-4 full scale, of which ~12M covers thin systems. Bounded
   by cap + floor; accepted — it buys full-map planner vision, which is the point.

## Measurement

Grade over **≥30 minutes or one full sweep, whichever is longer** — short windows are actively
misleading (era-4 produced fake improvements from 3-minute windows).

- **Requests per completed transaction** — headline. Era-4: ~63; floor ~4.
- **Sensing share of daemon activity** — era-4: 77%; expect <30%.
- **Calls per useful update** — target ≈1.0–1.2 for steady scans (1 GET → rows written).
- **Per-tier staleness gauges** against the approved envelope table; plus max-staleness (the R
  bound is a testable invariant).
- **Trade p95 limiter wait** before/after priority arming.
- Steady-state **sensing navigation legs/day → ~0** (placement trips only).
- Probe count is an input, not a result.
