# MVT Trade Loop — Design

**Date:** 2026-09-02
**Status:** approved in review, pending implementation plan
**Era at time of writing:** `torwind-2026-08-30`, player 10

## Goal

Spread the trade fleet across every priced system by making each hull trade
intra-system until that system is exhausted, then move to the nearest
profitable system nobody else is working. A small fixed pool of specialists
works the fat cross-system lanes. The design must run unchanged from 1 hull
and 1 system up to hundreds of hulls over the whole map.

## Why

The API ceiling is the binding constraint: 2.00 req/s, about 7,200 calls per
hour, measured at 67–100% utilisation. `income = calls × credits_per_call`,
and calls are fixed, so only credits per call can rise.

Measured over the previous 24 hours:

| Fact | Value |
|---|---|
| Calls that transact | 10–16%; about 5 movement calls per transaction |
| `Jump` alone | 8–10% of budget, 361 s cooldown each |
| Units per transaction | ~70, capped by market `trade_volume`, not the 490 hold |
| Realised trade margin | ~19M/hour at 28.8%, stable |
| Hull density | yield-neutral: 1/2/3/4 hulls per system-hour earn ~290k each |
| Depth per market | the real ceiling: unit margin 1,412 → 555 from lowest to highest throughput quintile |
| Coverage | 942 of 1,588 usable markets traded over 24h; 194 of 1,399 systems have fresh prices |
| `max_tour_systems` | cap 2 beat cap 4 by 26% in replay |

Three designs were replayed and rejected this week, and this spec must not
re-litigate them: partitioning markets into exclusive clusters (22.8% of
margin retained), releasing a hull's own absorption reservations (10× against
on the buy side), and widening candidate hop depth (cap 2 beat cap 4).

Intra-system trading until exhaustion attacks the jump cost directly, draws
each market down once rather than grinding it, and — because density is
yield-neutral — loses nothing by spreading one hull per system.

## Scope

**Kept, byte-identical**

- The OR-Tools intra-system tour solver (`tour_solver.py`). Called as today
  with `max_tour_systems` pinned to 1 for intra-system hulls.
- The absorption ledger and both crowding guards (`cf694c5b`, `bce168ad`).
  The ledger gains a reader; it gains no writer.
- The execution money guards (`1b0b60bf`).
- The look-back loader and its per-transaction toll (`0cfa0a32`).
- Navigation, jump, refuel, and the stranded-hold disposal ladder.

**Replaced**

- System selection. Today it is implicit in `tourSystemsFrom` (home + 1 gate
  hop). It becomes an explicit per-hull claim on exactly one system.
- Reposition. Today three heuristics: margin-death reposition, the rate floor,
  and reach-with-anti-herd. They are retired and replaced by one departure rule.

**New**

- The hull loop: a per-hull state machine.
- The system ranker: a pure function.
- The claim registry: one durable table.
- The specialist pool: a fleet tag plus a lane qualifier.

**Out of scope**

Any change to the solver, the ledger, the guards, or the look-back loader. If
one is needed to make this work, that is a finding that halts the migration
and gets its own bead.

## Design rule: scale invariance

No component may assume a minimum count of hulls, systems, or priced markets.
Every ranking is over whatever exists. Every pool size is derived from counts.
A 1-hull, 1-system fleet runs the same code path as 400 hulls over 1,399
systems, and the unit tests in §7 pin both ends.

## 1. The hull loop

One state machine per intra-system hull.

```
TRADE ──(yield below alternative)──▶ CLAIM ──▶ TRAVEL ──(arrived)──▶ TRADE
  ▲                                    │
  └────────(no better alternative)─────┘
```

**TRADE.** The hull runs the existing solver against its claimed system, leg
after leg, with scope pinned to that one system. After each **sell** leg it
updates `yield_here` and evaluates the departure rule. Buys do not trigger
evaluation: a hull mid-load discharges here first.

**The departure rule**, evaluated after each sell:

```
leave  iff  yield_here < best_alternative_yield − travel_cost(here → alternative)
```

- `yield_here`: exponentially weighted moving average of realised margin per
  unit over the hull's own sells in this system, window `yield_window_sells`.
- `best_alternative_yield`: the top candidate from the ranker (§2).
- `travel_cost`: the seconds the move takes (jump cooldown, transit, refuel)
  multiplied by the hull's own recent earning rate in credits per second, then
  divided by the hull's cargo capacity, so it is in the same credits-per-unit
  terms as the two yields. Gate fees are added directly. A hull that earns
  nothing yet uses the fleet mean rate.

**Cold-start guard.** With fewer than `yield_min_sells` sells in the current
system the hull has no yield estimate and cannot leave on yield. It can leave
only on emptiness.

**Empty-system exit.** If the solver returns no feasible intra-system plan,
that is exhaustion regardless of yield history; the hull enters CLAIM at once.

**CLAIM.** The hull asks the ranker for candidates, upserts its row in the
claim registry for the top one, and clears any previous claim. If nothing
scores above the current system — including the 1-system case — the hull
stays in TRADE and re-evaluates after its next sell.

**TRAVEL.** Existing navigation and jump code. On arrival, stamp `arrived_at`
and enter TRADE. If travel fails, release the claim and re-enter CLAIM from
the current position.

## 2. The ranker

A pure function with no side effects:

```
Rank(hull, candidates []System) []ScoredSystem

score(S, hull) = expected_yield(S) − travel_cost(hull → S)
```

**`expected_yield(S)`** is read from the absorption ledger: for every
market-good in S with a price younger than `ranker_age_cap_minutes`,
`unoccupied_depth × spread`, summed. Unoccupied depth already nets out other
hulls' EXECUTED shadows and PLANNED reservations at market grain — this is the
ledger's existing job and is the "not currently being explored" term. A system
with no fresh prices is excluded from candidates, not scored zero.

**`travel_cost`** is gate hops × (cooldown + gate fee) plus transit and refuel,
converted to credits per unit. It is zero for the hull's current system. This
is the "as close as possible" term.

**In-transit claims.** A hull that has claimed S but not arrived is invisible
to the ledger. Each unarrived claim on S subtracts one hull's typical draw from
`expected_yield(S)`, where typical draw is the fleet's mean credits of margin
per system-visit over the trailing 24 hours of `transactions`, recomputed on
the same cadence as the specialist pool. The claim is a **penalty, never a lock**: at N hulls and
1 system every hull's score falls by the same amount, ordering is unchanged,
and all N trade there sharing depth through the guards. At 40 hulls and 194
systems an empty system beats an equally rich occupied one by one hull's draw,
which spreads the fleet. Same code, both regimes.

**Candidates** are the hull's current system plus every system within
`claim_reach_hops`. Unreachable and unpriced systems are removed before
scoring. An empty candidate list means stay.

**Bootstrap.** Every hull starts in CLAIM. The ranker runs from wherever the
hull stands; its current system has zero travel cost, so a profitable current
system wins and the hull stays. There is no separate bootstrap path.

## 3. The claim registry

```
trade_claims(
  hull        text  primary key,
  system      text  not null,
  claimed_at  timestamptz not null,
  arrived_at  timestamptz,
  era_id      bigint not null
)
```

One row per hull. Upserted on claim, `arrived_at` stamped on arrival, deleted
on demotion to specialist or on hull retirement. Era-scoped. Read by the
ranker (in-transit penalty) and by container recovery.

**Restart.** Recovery reads the hull's row: `arrived_at` set → resume TRADE;
null → resume TRAVEL through the existing jump-resume logic. The yield EWMA is
deliberately not persisted; it resets to no estimate and the cold-start guard
prevents departure until the hull has `yield_min_sells` fresh sells. Restart
never triggers a fleet-wide re-claim.

## 4. The specialist pool

Specialists run the existing cross-system tour path — `max_tour_systems` 2,
look-back loader, all guards — unchanged. This section defines only
membership.

**Lane qualification.** A (source, sink, good) triple is fat when

```
margin_per_tranche − jump_cost(source → sink) > fat_lane_multiple × fleet_intra_system_margin_per_tranche
```

Both sides are measured from the ledger and the transactions table, so the
threshold moves with the fleet.

**Pool size**, derived every `specialist_cadence`:

```
pool = min( count(fat lanes), floor(N × specialist_fraction) )
```

At N = 1 the floor is 0: no specialists. The count of fat lanes is the real
cap; the fraction stops the pool consuming the fleet.

**Membership** is the fleet tag `trade-lane`. When the pool grows, the
intra-system hull in CLAIM state (between systems, empty hold) closest to a
qualifying lane's source is promoted. Never a hull mid-load. When the pool
shrinks, the specialist with the lowest realised margin on its lane demotes
first. A specialist with no qualifying lane reachable from where it stands
demotes itself at the end of its current leg rather than idling.

Contention on fat lanes is bounded by pool size structurally and by the
absorption guards within it.

## 5. Interfaces

**Ranker inputs (read-only)**

| Call | Source | Status |
|---|---|---|
| `Ledger.SystemDepth(system) → (units, credits)` | absorption ledger | new reader |
| `Gates.Hops(from, to) → int` | `gateReach.hops` | exists |
| `Claims.InTransitTo(system) → int` | claim registry | new |

**Hull loop calls (unchanged)**

- `SolveTour(hull, systems=[claimed])`
- `ExecutePlan(...)`
- `Navigate(...)`, `Jump(...)`

**Telemetry.** One line per state transition:
`hull, from_state, to_state, system, yield_here, best_alternative, travel_cost, reason`.
Every replay and dashboard reads this line. Missing fields are added before
ship, not after.

**Proto.** Untouched. No routing-service change, no Python stub regeneration.

**Config**

| Knob | Default | Governs |
|---|---|---|
| `yield_window_sells` | 8 | EWMA window for `yield_here` |
| `yield_min_sells` | 3 | cold-start guard |
| `claim_reach_hops` | 2 | candidate radius |
| `specialist_fraction` | 0.10 | pool ceiling as share of N |
| `fat_lane_multiple` | 2.0 | the `k` in lane qualification |
| `specialist_cadence` | 1h | pool re-evaluation interval |

Every default is fitted from the replay in §7 before ship. None encodes fleet
size.

## 6. Failure modes

The rule throughout: fail toward staying put.

| Situation | Behaviour |
|---|---|
| Ledger unreadable | Ranker returns empty; hull stays in TRADE |
| Claimed system stale on arrival | Solver finds no plan → empty-system exit → CLAIM. No mid-jump re-route |
| Two hulls claim the same system on one tick | Allowed; the claim is a penalty. Guards share depth on arrival |
| Route to claimed system fails | Release claim, re-enter CLAIM. After 3 consecutive failures, hold for one `yield_window_sells` before retrying |
| Every candidate below current after travel cost | Stay. The 1-system case and the drained-neighbourhood case share this path |
| Cargo unsellable on arrival | Existing stranded-hold ladder (`7e5b26c1`); the loop does not own cargo rescue |
| Restart mid-TRAVEL | Resume jump from the claim row |
| Restart mid-TRADE | Resume TRADE; yield resets; cold-start guard applies |
| Specialist lane dies mid-run | Finish leg, sell down, self-demote, enter CLAIM |
| Fleet shrinks below pool size | Pool recomputes on cadence; excess specialists demote at leg end |

**Money guards own every money decision.** The loop never bypasses
`maxAskPerUnit`, `sellFloorPerUnit`, or the absorption reservations. It
chooses where; the guards decide whether. RULINGS #4 is unchanged.

**No fleet-wide rebalance command.** A wrong-looking distribution is fixed by a
knob or a ranker change that is replayed first.

## 7. Testing

**Unit.** The ranker and departure rule are pure; every test is a table.

| Case | Asserts |
|---|---|
| 1 hull, 1 system | never leaves; `Rank` returns current system or nothing |
| N hulls, 1 system | all stay; ordering unchanged by the claim penalty |
| 1 hull, N systems | walks to highest `yield − travel`; stays when none beats current |
| Cold-start guard | below `yield_min_sells` cannot leave on yield, can leave on empty |
| Unreadable ledger | empty ranking, no transition |
| Pool size at N = 1, 2, 10, 40, 400 | `min(fat_lanes, floor(N × fraction))`; 0 at N = 1 |
| Claim penalty | occupied system loses one hull's draw, never excluded |

**Replay**, against the last 24 hours of real legs before any code runs live.
Legs reconstructed from `transactions` (metadata carries hull, waypoint, good,
units). The harness runs the ranker and departure rule over recorded hull
positions and ledger state and reports: where the loop would have sent each
hull versus where it went; realised margin per unit on chosen lanes; **jump
count under the loop versus actual — the primary metric**; any hull the loop
would have stranded for over an hour.

**Ship gate:** jumps down **and** margin per hull not down. Either alone fails.

**Live cohort.** Five hulls on the new loop via fleet tag, the rest on the old
path, for two complete hours. Compare margin per hull and jumps per hull
between cohorts on **lot-matched** figures (`ledger positions`). Never the
naive series, never a partial hour, never treasury delta. Rollback is one tag
change, no restart.

## 8. Migration

Old and new coexist, selected per hull by fleet tag.

1. **Ranker and registry, dormant.** Land the ranker, `trade_claims`, and
   `SystemDepth`. Log the ranker's would-be decisions for every hull while the
   old path drives. No behaviour change. Ships ON — it is a reader.
2. **Hull loop behind tag `trade-mvt`.** Five hulls promoted for the cohort
   test. Specialist pool not yet built; the cohort is pure intra-system so one
   variable is tested.
3. **Specialists.** Land the pool once the cohort passes. First point
   cross-system trading runs under new rules.
4. **Retire the old path.** When every trade hull carries `trade-mvt`, delete
   margin-death reposition, the rate floor, reach-with-anti-herd, and their
   knobs. Not before.

One bead per step, each landing through worktree → captain-gate → main. Step 1
can start immediately; steps 2–4 each gate on the previous step's measurement.

## Decisions recorded

| Question | Decision | Reason |
|---|---|---|
| Exhaustion trigger | yield for departure, depth for destination | yield is ground truth and catches model error; depth is the only fleet-wide view |
| Specialists | fixed pool, derived size | bounds contention structurally; keeps the optimiser as two simple problems |
| Optimiser shape | per-hull claim on exhaustion | systems outnumber hulls 5:1; density yield-neutral; proven by the charting seed loop; no solve budget |
| Claim semantics | penalty, not lock | required for N hulls in 1 system |
| Yield persistence | not persisted | cold-start guard makes reset safe; simpler |
| Scope | keep solver, ledger, guards, loader | measured good; six agents this week could not improve on them |
