# Gate Construction: Two-Fleet Design

**Date:** 2026-08-03
**Status:** Design — awaiting review
**Supersedes the operational model of:** sp-vh1s (unified gate-fill single recursive tree)

## Problem

The gate construction operation is **opaque**. Three symptoms, one root cause:

- **Can't tell WHY it stalled.** A single gate-material acquisition passes through ~10 independent
  decision gates (`shouldBuyGood`, `selectInputSource`, `perNodeSupplyFloor`, fabricate depth cap,
  `chainMarginParked`, price ceiling, `spendFloorBreached`, capital budget, `hullFillTarget`,
  market `trade_volume`). Several decline *silently* — they park or skip and return a non-error.
  A declined operation and an idle one are indistinguishable.
- **Can't predict WHAT it will do.** Buy-vs-fabricate, source choice, and recursion depth are
  *emergent* from those ten gates interacting. The policy is written nowhere; it can only be
  discovered by reading all ten.
- **Can't predict WHEN it finishes.** With no record of attempts and outcomes there is no rate,
  so no ETA.

**The root cause: nothing in the operation ever materializes a decision.** Decisions exist only as
control flow — an `if` that returns `nil`, a park, a skipped task. No artifact records what was
tried, what was chosen, or why.

Current surface: 39 non-test files, ~10,800 LOC. `run_construction_coordinator` alone spans 7 files.

## The Design

Two fleets with one clean boundary.

### Factory fleet — keeps the chain fed

Feeds the production chain recursively, terminating at the factories that export the gate
materials. It delivers **into** those factories and never touches their output.

Recursion terminates naturally at **raw goods** (no recipe), which are bought from whatever exports
them. There is no depth limit.

### Delivery fleet — buys terminal output, hauls to gate

Buys FAB_MATS and ADVANCED_CIRCUITRY at the factories that export them, and delivers to the gate.
**It never fabricates.**

### The boundary

The terminal factory's *output*. Everything upstream is the factory fleet's problem; the
output→gate leg is the delivery fleet's.

## Why This Is Faster, Not Only Simpler

Today production and hauling are **serialized inside one haul**: `fabricateGood` → trigger
production → `PollForProduction` → `purchaseFabricatedOutput`. A hauler waits for a factory to
finish. Every trip pays production latency — this is the shape of the measured 8.6 units/hr.

Splitting the fleets **decouples production from hauling**. The factory fleet keeps the terminal
factories producing continuously; the delivery fleet arrives and buys whatever has accumulated.
Production latency leaves the haul critical path entirely.

It also dissolves the root-good problem for free. `supply_chain_resolver.go:145,:167` forces
`AcquisitionFabricate` for the root good, which makes `construction override
--good-override FAB_MATS:strategy=prefer-buy` a **silent no-op** — it reports success and changes
nothing. Under this design the delivery fleet simply *buys*, so the branch that made the override a
lie no longer exists on this path.

## Era Invariance

**Goods are invariant. Locations are not.**

Every era's gate requires FAB_MATS and ADVANCED_CIRCUITRY, and the recipe DAG is a game constant —
we know statically that FAB_MATS needs IRON + QUARTZ_SAND. These may be named directly.

**No system or waypoint symbol may be hardcoded.** Every location is resolved at runtime by role,
from market import/export data:

| Role | Resolution |
|---|---|
| Terminal factory | the waypoint that **exports** a gate material |
| Feed target | the waypoint that **imports** a given input |
| Raw source | the waypoint that **exports** a good with no recipe |
| Gate | the construction site waypoint |

`MarketLocator` already provides the primitives (`FindExportMarket`, `FindImportMarket`,
`FindExportMarketBySupplyPriority`); this is discovery, not new infrastructure.

**This makes sp-b27a2 unrepresentable.** That bug dispatched IRON_ORE to a waypoint that does not
import it, stranding haulers at 80/80 with cargo they could neither deliver nor dump. Resolving the
feed target *by import capability* means a good can only ever be dispatched somewhere that accepts
it.

## Delivery Fleet: Buy and Pause Rule

**Supply-anchored with hysteresis.**

- Buy while the terminal factory's supply is **at or above** the buy floor.
- Pause when supply drops **below** the floor; let stock rebuild.
- Resume only once supply recovers **one level above** the floor.

Two thresholds, not one. A single threshold chatters at the boundary: pause, one unit regenerates,
resume, immediately deplete. Supply ordering is SCARCE < LIMITED < MODERATE < HIGH < ABUNDANT
(`domain/shared/supply_level.go`). Proposed: buy floor MODERATE, resume at HIGH.

Price is deliberately **not** a gate here. The gate is a finite, high-ROI investment, and
supply-anchoring already paces against our own market impact — sustained buying depletes supply,
which trips the pause before the ask can ladder far.

## Delivery Fleet: Batching

**Always maximize cargo usage.** A hull fills its hold, not a per-material tranche.

Fill greedily from eligible factories **within the current system, nearest-first**, until the hold
is full:

```
capacity_left = hull cargo capacity
for each gate material, by remaining bill descending:
    if material is PAUSED (supply below floor):  skip
    take = min(remaining_bill[material], capacity_left, available_supply)
    buy in trade_volume tranches until `take` reached
    capacity_left -= take
    if capacity_left == 0: break
deliver entire hold to the gate
```

**Mixed loads are the default where factories are co-located.** Both terminal factories are
typically in the same system, so one trip amortizes the expensive gate leg across both materials
instead of paying it twice. Restricting the greedy fill to the current system is the operational
reading of "if that is faster" — it takes mixed loads when they're cheap and avoids cross-system
detours when they aren't, with no travel-time model required.

**Mixed loading also gives the pause rule an escape valve.** If one material is paused on low
supply, a single-material hull would idle. A hull that fills from any eligible material simply
loads the other one and departs. The fleet never sits waiting for stock to rebuild.

Buys remain bounded by the market's `trade_volume` per transaction — a hull filling 80 units at
`trade_volume=20` performs 4 transactions. This is a market constraint, not an architectural one.

## Observability

The simplification alone does **not** fix opacity. Two policies that decide silently rebuild
exactly the problem this design exists to solve — a paused delivery fleet and an idle one still
look identical.

Both policies must record their decisions:

- **Delivery pause:** factory, good, observed supply, resume condition.
- **Fill outcome:** per trip — capacity, what was loaded, what was skipped and why (paused /
  bill satisfied / no supply).
- **Factory feed:** good, resolved feed target, dispatched vs declined with reason.

This is the narrow, load-bearing part of a decision ledger: not ten instrumented gates, just the
two remaining policies reporting themselves.

## What Gets Deleted

- The fabricate **depth cap** (`maxDepth`, depth config, the `len(path) >= depthCfg.maxDepth`
  branch). Recursion is bounded by the recipe DAG terminating at raw goods.
- **Cycle detection (`visited`) STAYS.** The depth cap was a backstop for not trusting the DAG;
  `visited` is doing real work and must remain.
- The `isTargetGood` forced-fabricate branch on the delivery path.
- Most gate-mode exemption machinery — margin-blindness stops being a special mode when nothing
  on this path is resold.

## Money Guards — Unchanged

The 50k `common.ImmutableReserveFloor` applies to **both** fleets, unchanged. RULINGS #4: money
guards fail closed and are never weakened. This is a simplification of *control flow*, not of
*spend safety*. No new floor, no new knob, no config seam (RULINGS #5).

## Risks

1. **This does not beat the `trade_volume` cap.** ~1460 FAB_MATS is ~73 buy-trips regardless of
   architecture. Decoupling removes production latency per trip; rate beyond that is a function of
   how many delivery hulls run concurrently. Parallelism is a dial this design *enables* but does
   not set.
2. **Behavior change across money-adjacent paths.** The prior throughput attempt (sp-ijbe2) caused
   a 2h40m outage and was reverted before being re-landed as `e2c4fbc3`. This change is larger.
   Characterization tests over current acquisition behavior should precede the cut.
3. **Factory-fleet starvation is now visible but not automatically solved.** If the factory fleet
   cannot keep a terminal factory supplied, the delivery fleet pauses indefinitely. That is correct
   and honest, but it means the factory fleet's own failure modes become the binding constraint.

## Decomposition

This is **too large for a single implementation plan**. Proposed phases, each independently
shippable and each leaving the system working:

1. **Era-invariant topology resolution.** Role-based lookup (terminal factory / feed target / raw
   source) as a single seam over `MarketLocator`. Foundational, no behavior change on its own,
   and it is what makes sp-b27a2's mis-routing unrepresentable. Lowest risk — do it first.
2. **Delivery fleet.** Buy at terminal factories, supply-anchored pause with hysteresis, greedy
   max-cargo mixed fill, deliver to gate. Depends on (1).
3. **Factory fleet.** Recursive feeding to the terminal factories; delete the depth cap; keep
   `visited`. Depends on (1).
4. **Delete the old path.** The single recursive tree, `isTargetGood` forced fabrication, and the
   gate-mode exemption machinery. Only after (2) and (3) are live and validated.

Observability (per the section above) ships **inside each phase**, not as a fifth — a phase whose
decisions are invisible cannot be validated, which is the failure this design exists to correct.

## Open Question — Fleet Mechanics

The design specifies what each fleet *does*, not what each fleet *is*. Unresolved: whether "factory
fleet" and "delivery fleet" are distinct container types, one container with two worker roles, or a
scheduling policy over a shared hull pool; and how hulls are allocated and rebalanced between them
as the bill drains.

This matters because it determines whether the pause rule idles hulls or releases them. Existing
constraints to respect: worker rebalancing is automatic, and a `-1` goods_factory container cannot
self-terminate. **Resolve before phase 2.**

## Testing

- **Characterization first:** pin current acquisition behavior before deleting the depth cap or the
  `isTargetGood` branch.
- **Era invariance:** a test that no system/waypoint symbol is hardcoded on either fleet path;
  fixtures resolve topology purely from market import/export data.
- **Hysteresis:** supply oscillating at the floor must not produce buy/pause chatter.
- **Mixed fill:** a hull with one material paused fills entirely with the other; a hull with both
  eligible fills to capacity across both; fill never exceeds remaining bill.
- **Feed routing:** a good is never dispatched to a waypoint that does not import it (sp-b27a2).
- **Money floor:** both fleets refuse to spend below the 50k floor (fail-closed).
