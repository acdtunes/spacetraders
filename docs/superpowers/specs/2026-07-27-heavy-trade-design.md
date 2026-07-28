# HEAVY_TRADE — heavy-hauler acquisition (sp-fwk8z)

## Problem

The fleet trades with light hulls because they are what bootstrap can afford. Heavy haulers carry
several times the cargo per trip, so once the treasury can reach one, every tour a heavy flies earns
more than the same tour flown light. Nothing in the fleet buys them today: the autosizer sizes the
trading fleet in the hull class it already knows, and no code path notices when a shipyard selling
heavies comes into view.

Two things make this non-trivial rather than "add a buy call":

1. **Expansion and heavy buying starve each other asymmetrically.** Expansion spends continuously in
   small amounts (probes at ~23.5k, an always-hungry queue). A heavy is a single large purchase that
   requires *accumulation*. Sharing one treasury with only a floor between them, the small continuous
   spender always wins: the probe queue drains the surplus below the heavy's threshold every tick, so
   the heavy never accumulates and is never bought. The partition must let treasury build, not merely
   divide flow.
2. **Buying a ship requires a ship docked at the yard.** `PurchaseShipCommand` navigates and docks the
   purchasing hull itself, so a heavy-selling yard with no presence cannot be bought from at all.

## Objective

- Buy heavies and put them on trade routes, capped by an operator dial, without stalling frontier growth.
- Source each purchase from the cheapest heavy yard we can actually reach.
- Add no new phase, no new coordinator, and no persisted coordination state.

## Design

### 1. A capability gate, not a phase rung

HEAVY_TRADE runs **concurrently with EXPANSION**. The phase ladder (DATA → INCOME → GATE → EXPANSION,
EXPANSION terminal) is untouched, and so is everything reading it — including the parked-probe sensing
cutover, which fires at EXPANSION entry and must keep doing exactly that.

The capability is **derived, not latched**: heavies are purchasable when `shipyard_inventory` holds a
row for the heavy hull type. `HasAnyOfTypes` already answers this as a free DB read. Nothing is
persisted, so there is no latch to get stuck and no state to un-set — if the only known heavy yard ever
leaves inventory, the capability simply closes until another is found. This fleet has been bitten twice
by sticky phase latches; deriving the gate removes that failure class rather than guarding against it.

Discovery costs nothing extra: the parked-probe model parks a quartermaster at each system's shipyard
and rescans it on a slow cadence, so heavy-yard discovery and price freshness arrive as a side effect of
sensing expanding. The two features compound.

### 2. Ownership: the fleet autosizer

The autosizer already owns *how many trading hulls should exist and buying them*, and — verified during
sp-hv4f6 — it is the component that tags the hulls it buys. Heavies are that same question with a
different hull class and a hard cap. A separate coordinator would duplicate its treasury reads, its
fleet census, and its trade-fleet handoff, and would then need single-buyer arbitration against it. One
buyer of trading hulls stays one buyer.

### 3. The reservation is derived, not declared

The budget partition is a single predicate both spenders compute from the same durable tables:

```
heavyReserve = capabilityOpen && heaviesOwned < heavyCap ? cheapestKnownHeavyPrice : 0
```

The sensing buy-floor adds `heavyReserve` to its existing capex term, so probe buying pauses, treasury
accumulates, the heavy lands, `heaviesOwned` rises, the reserve drops to zero, and probes resume.

A *stored* intent was the obvious alternative and is the wrong shape: it is cross-container state (the
autosizer writes, sensing reads), which buys a write protocol, a staleness question, and a re-fire
hazard on restart. Deriving it costs one shared function and has none of those. This satisfies RULINGS
#2 trivially — there is nothing to re-derive because nothing was stored.

**One hard requirement:** the predicate lives in exactly one shared package and both callers use it,
held by a compile-time guard the way the two 50k floors already are. Two near-copies of this predicate
is precisely how a reservation silently drifts.

**Reserve one heavy at a time, never `cap − owned`.** The single-slot reservation is what creates the
interleaving: expansion gets a spending window between each heavy purchase instead of being held off
until the whole fleet is bought.

### 4. The purchase loop

Per autosizer tick:

1. Capability open? Else nothing.
2. `heaviesOwned < heavyCap`? Else nothing — and the reserve drops to zero, releasing the treasury to
   expansion.
3. Target = cheapest known heavy yard **with presence**.
4. Re-quote live, then buy if `treasury − actualPrice ≥ heavyBuyFloor`.
5. Tag the hull to the trade fleet immediately.

`heavyBuyFloor` is `ImmutableReserveFloor` + trading working capital + any other declared capex —
deliberately **not** including its own reserve, which would be circular.

Every unreadable input (treasury, fleet count, inventory) means no buy this tick: fail closed, RULINGS
#4. The quote/actual split follows the pattern the sensing buy queue already proved — bind on the
actual price, halt on an overrun rather than compounding a stale quote into the next decision.

**Counting is deliberately broad:** `heaviesOwned` counts every owned heavy hull, not only
trade-tagged ones. The cap is about capital exposure, and under-counting is the direction that
authorises buying a hull we already own.

### 5. Cheaper-yard re-targeting falls out

Because the target is recomputed each tick rather than stored, a newly-discovered cheaper yard *is* the
target on the next tick, and the reservation drops with it — handing the difference back to expansion
immediately. There is no stored target to migrate and no re-targeting logic to write.

One deliberate simplification: we buy at the cheapest yard **with presence**, not the cheapest yard
absolutely. When a cheaper yard has no quartermaster yet, sensing is already placing one; that yard
becomes the target for the *next* heavy rather than stalling this one. Waiting for a probe to fly costs
trade income to capture a discount we will capture anyway.

### 6. Presence: quartermasters cover heavy yards

Sensing places a quartermaster at each system's probe-selling yard. That placement extends to
heavy-selling yards, so a parked probe (~23.5k, once) makes every future heavy purchase at that yard
instant — including the switch to a cheaper yard, which is the requirement the alternatives either tax
(a courier round-trip per buy, pulling a working hauler off task) or silently forfeit (buy only where we
happen to stand).

### 7. Post-purchase

A bought heavy is tagged `trade` and becomes an ordinary trading hull. This tag is **mandatory, not
cosmetic**: verified during sp-hv4f6, nothing auto-adopts an untagged hull into trade — the capacity
reconciler was deleted, the trade coordinator works only hulls already tagged, and the sole adopter of
an undedicated hull takes it into *contract* work instead. An untagged heavy would silently go to the
wrong fleet.

The heavy starts its first tour from the yard it was bought at, which may be far from the best markets.
That is a real but self-correcting inefficiency; relocation logic before we have seen it hurt would be
speculative.

### 8. Knobs

`heavy_cap` — maximum heavies owned, default **5**, live-tunable (operator dial, const default per
RULINGS #5). Not an on/off seam: the feature ships armed, and `heavy_cap = 0` is a legitimate operator
choice to hold, not a flag.

## Deferred

**Auto-scaling the heavy fleet.** The cap is a fixed dial in this design. Sizing heavies to observed
trade demand is a separate problem with its own evidence requirements, and building it now would be
guessing at a curve we have no data for.

## Asymmetry, stated plainly

Expansion is protected by *bounded appetite* — five heavies, one at a time, with a spending window
between each — not by a reserve of its own. Reserving for expansion would mean reserving for an
unbounded continuous appetite, which is just "never buy heavies." Heavies are protected by the reserve
because their appetite is lumpy and finite. The protection is asymmetric because the two spenders are.

## Risks

1. **Reservation drift** if the shared predicate is ever copied. Mitigated by the compile-time guard;
   this is the single most important invariant in the design.
2. **Presence lag** — a cheaper yard is unusable until its quartermaster arrives, so early purchases may
   pay more than the map's best price. Bounded, self-correcting, and cheaper than the alternatives.
3. **Accumulation stalls expansion visibly.** While a heavy is being saved for, probe buying stops.
   That is the intended behaviour, but on a thin treasury it can look like sensing has died. The
   heartbeat must name the reserve so an operator can tell "saving for a heavy" from "broken".
4. **Trade planning may assume a cargo size.** Unverified: if tour construction or the VRP request
   carries a capacity assumption tuned to light hulls, a much larger hull may plan badly. This is a
   recon item for the build, not a design unknown — but if it comes back surprising it changes the plan.
5. **First heavy is a large single purchase** against guards that have never been exercised at that
   magnitude. The floor arithmetic is the same; the number is not.

## Unverified at design time

The recon sweep was cut short deliberately. Three facts must be confirmed by the implementation lane and
reported back; none changes the design's shape, but any surprise changes the plan:

- the exact heavy ship-type constant in use (SpaceTraders names it `SHIP_HEAVY_FREIGHTER`; a
  `heavy_ship_types` config key has also been seen referenced);
- whether the fleet autosizer reads live config or is launch-frozen (live-tunability is a hard
  requirement, so wiring it is in scope if frozen);
- whether anything in tour planning assumes a cargo capacity that a heavy would break (risk 4).

## Dependency

This branches off `sp-k6v8z` (parked-probe sensing) and merges after it. It consumes that branch's
buy-floor capex term and quartermaster placement. Rebase onto the final sensing branch before gating.

## Measurement

- **Credits per tour-hour, heavy vs light** — the headline. If a heavy does not out-earn a light hull
  per hour on comparable routes, the cap should not rise and this feature is not paying for itself.
- **Time-to-first-heavy** from capability open, and the accumulation stall it cost expansion (probe
  buys deferred) — the explicit price of the partition.
- **Price paid vs cheapest known** per purchase — measures how much the presence-lag simplification costs.
- Reserve value over time, on the heartbeat, so a stalled expansion is legible as saving rather than failure.

---

## RECON CORRECTIONS (2026-07-27, post-Task-1 — these SUPERSEDE the sections above)

Task 1 was recon-gated for exactly this reason. Three of this spec's claims were wrong.

### C1. "Nothing in the fleet buys them today" — FALSE

The fleet autosizer already buys `SHIP_HEAVY_FREIGHTER`, tags it `trade` at purchase, targets the
cheapest reachable yard, and runs the full money-guard stack — including demand-driven auto-scaling,
which §Deferred wrongly listed as a separate later problem. **The Deferred section is void: that
capability exists.**

What is genuinely new is therefore only: (a) the reservation that lets treasury accumulate against a
continuous probe drain, and (b) a heavy-hull cap and its live-tunability. Everything else in §4's
purchase loop already exists and must be *extended*, never re-implemented alongside.

### C2. `FleetCeilingHeavies` is NOT a heavy cap — a separate `heavy_cap` IS justified

It is enforced by `countShips(..., DedicatedFleet() == "trade")` — the predicate is the **tag**, and
hull type never enters it. So it caps the **trade pool**: a `SHIP_LIGHT_HAULER` tagged `trade` counts
against it, and a `SHIP_HEAVY_FREIGHTER` tagged `contract` or untagged does not count at all.

`FleetCeilingHeavies` and `heavy_cap` therefore ask different questions — pool size versus capital
exposure in large hulls — and are not duplicates. **`HeaviesOwned` must NOT reuse that census**: it
would under-count whenever a heavy is tagged anything but `trade`, which is precisely the direction
§4 names dangerous (an invisible heavy leaves the reserve open and authorises re-buying a hull we own).

This also retires the lockstep argument as applied here. Lockstep binds two computations of *one*
fact. These are two censuses of two different facts; sharing an implementation would not remove
drift, it would silently redefine one of the questions.

### C3. The heavy census cannot be built on an inferred frame symbol

The `ships` table has no ship-type column — only `frame_symbol` — and no ship-type→frame mapping
exists anywhere in the tree. `FRAME_HEAVY_FREIGHTER` appears in zero production code;
`FRAME_BULK_FREIGHTER` is inferred from naming symmetry alone. A wrong symbol under-counts, which is
the money-unsafe direction.

Live data (queried 2026-07-27) cannot corroborate it either: the fleet owns **no heavy hulls at all**
— `FRAME_LIGHT_FREIGHTER` ×8 at 80 cargo, `FRAME_FRIGATE` ×1 at 40, `FRAME_PROBE` ×3 at 0. There is no
owned heavy to read a frame from.

**Ruling — the census is frame-list primary with a capacity safety net.** Count a hull as heavy if its
frame is in the known-heavy list **OR** its cargo capacity is at or above a heavy threshold. The
threshold sits in the wide, empirically-verified gap between the largest hull we own (80) and a heavy
freighter (~225); anything ambiguous counts as heavy, which over-counts and therefore buys *fewer*
heavies — the safe direction. A hull that trips the capacity net while carrying an unrecognised frame
**must log loudly**: that is the census telling us the frame list is incomplete, which is the only
signal we can get before the first heavy lands. The frame list stays authoritative and gets corrected
from that log.

### C4. Live state — the capability is ALREADY open

16 yards sell `SHIP_HEAVY_FREIGHTER`, cheapest **1,565,500**; one sells `SHIP_BULK_FREIGHTER` at
2,931,905. So this feature begins buying on deploy rather than at some future discovery, and the
reservation is load-bearing immediately: at ~1.57M, one heavy is roughly 67 probes' worth of
treasury, which a continuous probe drain would never let accumulate.

### C5. Measurement is confounded — the headline metric needs a caveat

The per-tour spend cap is a cumulative, hull-blind 25% of live treasury. Heavy and light receive the
**same** credits at equal treasury, so a heavy sails partly empty for budget reasons whenever
`unit_price × hold_capacity > 0.25 × treasury` — roughly 2.8× easier to hit for a 225-slot hold than
an 80-slot one. This systematically **understates** the heavy's advantage precisely in the
thin-treasury window when the first heavy lands, i.e. when the headline metric would first be read.
Early readings are a **floor** on the true advantage, not an estimate of it. Filed separately.

### C6. Time-to-first-heavy is governed by eligibility, not only by accumulation (Task 2 finding)

C4 said the capability is already open and this "buys on deploy". Open, yes — imminent, no. Two
guards bind well after the reservation does, and both were surfaced by Task 2 fixtures failing on
guards the implementer was not testing:

1. **A ship purchase is capped at 25% of treasury.** At the live cheapest heavy of 1,565,500, no heavy
   is buyable until treasury reaches roughly **6.26M**. The reserve governs *accumulation*; this
   governs *eligibility*, and it binds much later. Expect the reserve to hold treasury open for a long
   stretch before any purchase becomes legal.
2. **Era payback.** The hull must amortise within the remaining era, so the required marginal rate is
   ≈156,550/hr at 20h remaining and rises as the era shortens. There is therefore a **late-era window
   in which no heavy is buyable at any treasury** — which is correct economics (do not buy a hull you
   cannot pay back) but means the feature is silently inert near an era boundary.

Neither is a defect. Both belong in the deploy expectations: an operator watching a held-open reserve
with no purchase is most likely seeing (1), not a broken buyer.

### C7. `heavy_cap = 0` cannot be set through `tune` (Task 2 finding)

§8 claimed `heavy_cap = 0` is a legitimate operator hold. That is true only through `config.yaml` plus
a restart. The tune mechanism treats a value of 0 as *revert to default* and deletes the key
(`mutateTuneConfigKey`), so `tune heavy_cap 0` restores 5 rather than holding at zero. The hold is
preserved where it can live — a `*int` config field injected only when non-nil and read with
`PresentInt` — and both the tune registry description and the config documentation state the
distinction. This is a constraint of the tune mechanism, not a design choice.
