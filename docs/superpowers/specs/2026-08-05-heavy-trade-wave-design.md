# Heavy/Probe Trade Wave: Fleet-Growth Design

**Date:** 2026-08-05
**Status:** Design — awaiting review
**Epic:** sp-sy3dl
**Replaces:** the fleet autosizer (`FLEET_AUTOSIZER_COORDINATOR`)
**Retires:** off-gate warp expansion (the explorer hull class and its dispatch loop)

---

## Corrections to the brainstorm record

Four claims taken as settled during the brainstorm do not survive contact with the code or the
ledger. The **decisions are unchanged**; the supporting facts are corrected here so the plan is not
built on them.

| Claim as recorded | Verdict | What is actually true |
|---|---|---|
| "Zero explorer hulls have ever existed in either the prod or staging database." | **WRONG for prod** | Exactly one `SHIP_EXPLORER` was ever purchased: prod, 2026-07-24T22:52:12Z, waypoint X1-YY85-ZD2D, 722,511 credits, `operation_type = "fleet expansion"`. Staging has never bought one. Neither database holds an explorer-dedicated hull today. **The retirement case is stronger, not weaker:** the off-gate *sensing driver* landed 2026-07-28 (`cf8b4072`), four days **after** that purchase, so the off-gate warp loop has never had a hull to dispatch. The explorer autosizer class landed 2026-07-15 (`55b6853c`). |
| "The autosizer's four classes." | **Half wrong** | The `HullClass` enum has four constants (`fleet_autosizer_types.go:19,21,29,39`) but only **three** demand providers are ever registered — light, heavy, explorer (`fleet_autosizer_ports.go:50,58,68`). `contract_delivery` has **no provider at all** and is hard-disabled in `classDisabled`'s default arm (`run_fleet_autosizer_coordinator.go:313-325`). |
| "`contract_delivery` is pinned at 4/4 hulls." | **WRONG** | Nothing is pinned at 4. The contract fleet is owned by the separate contract scaler, ceiling `DefaultContractFleetMaxHulls = 3` (`run_contract_scaler.go:37`, live-tunable `contract_fleet_max_hulls`), against a p-median knee of `MaxDeliveryHulls = 6` (`domain/contractscaler/plan.go:14`). |
| "Term (a) is already the idiom in `ProbeBuyFloor`: `runway_hours × observed_cargo_spend_per_hour`." | **Shape right, units wrong** | `ProbeBuyFloor` uses **milli-hours**: `kMilli × cargoSpendPerHour / 1000` (`domain/parkedsensing/floor.go:36-41`). The multiplier `capital_multiplier_k_milli` is a **tunable** (default 2000, bounds 0–10000, `container_ops_tune.go:146`), not a const. The milli encoding exists because whole hours gave no setting between "blocked" and "unguarded" (`floor.go:12-24`). This spec adopts milli-hours; a whole-hour term would re-create the problem that encoding was written to solve. |

Two further facts, not in the brainstorm record, are load-bearing on the migration and appear in
**Kept surface** and **Migration path** below: the buy primitive is shared with the contract scaler,
and the sensing heavy reserve reads `heavy_cap` off the autosizer's own container row.

---

## Problem

The fleet autosizer buys hulls per class against per-class demand providers. Probe buying (frontier
charting and scanning, owned by `parkedsensing`) and heavy buying (trade capacity, owned by the
autosizer) draw on **one treasury with no coordination between them**.

A heavy is a **lump**; a probe is a **trickle**. Prod's ledger holds 95 `SHIP_HEAVY_FREIGHTER`
purchases spanning **1,516,566 to 2,529,556 credits** (as of 2026-08-05); a probe is orders of
magnitude below that. With probe buying running continuously, the treasury never climbs to the lump.
The two spenders are not competing over a shared policy — there is no shared policy.

---

## Core insight — the wave oscillates on its own

```
unserved_lanes = profitable_lanes − trade_hulls          (floored at 0)
```

- Buy heavies → `trade_hulls` rises → `unserved_lanes` falls → reaches 0 → switch to probes.
- Buy probes → more markets charted → `profitable_lanes` rises → `unserved_lanes` rises → switch to heavies.

No target counts. No era-scaled constants. Both directions fall out of **one live signal that is
already wired**:

| Component | Location | Role |
|---|---|---|
| `HeavyDemandProvider` | `internal/application/fleet/commands/fleet_autosizer_heavies.go:37` | `demand = current_heavies + unserved_lanes` (`:81-93`) |
| `autosizerHeavySources.UnservedLaneCount` | `internal/adapters/grpc/fleet_autosizer_demand_ports.go:78` | `profitable − heavies`, clamped at 0 |
| `ProfitableLaneReader.CountProfitableLanes` | `internal/application/trading/queries/profitable_lane_reader.go:53` | counts floor-clearing lanes off the market cache |
| `trading.RankSpreads` | `internal/domain/trading/arbitrage_lane.go:133` | the pure ranker the trade circuit itself uses |

The read is **explicitly read-only** and never perturbs the trade coordinator — it consumes the same
pure ranking off the market cache (`fleet_autosizer_demand_ports.go:70-77`). It fails **closed**: an
unreadable ship or lane surface yields `readable=false` and no buy (`fleet_autosizer_heavies.go:58-65`).
A *readable* zero (empty cache, no floor-clearing lane) is a genuine zero, not a fail-closed.

The signal is reused as-is. Nothing about it changes.

---

## Why waves and not interleaving

**Not because the lane signal flaps.** Because a heavy is a lump and probes are a trickle: without
pausing probe buying, the treasury never reaches the lump.

State this at the top of the implementation too, because it is the entire justification for phase
structure. A reader who does not see it will "simplify" the wave into a per-class priority ordering
that interleaves both spenders — restoring exactly the condition this design exists to remove.

---

## Wave predicate — DERIVED PER TICK, NEVER STORED

```
HEAVY wave  iff  unserved_lanes > 0
             AND heavies_owned < heavy_cap
             AND a priced heavy target exists
             AND surplus ≥ entry_threshold          // HeavyReserveEntrySharePct, reused
otherwise   PROBE wave
```

Clauses 2, 3 and 4 are **exactly what `common.HeavyReserve` + `HoldAt` already compute**, and the
predicate is expressed through them rather than beside them:

| Clause | Existing mechanism | Location |
|---|---|---|
| `heavies_owned < heavy_cap` | `HeavyReserve` returns 0 at or over cap | `common/heavy_reserve.go:139-141` |
| a priced heavy target exists | `HeavyReserve` returns 0 on `TargetYardPrice <= 0` and on `!CapabilityOpen` | `common/heavy_reserve.go:130-152` |
| `surplus ≥ entry_threshold` | `HoldAt` returns 0 when `surplus − entry ≤ 0` | `common/heavy_reserve.go:210-237` |

So the whole tail of the predicate is `HeavyReserve(...).HoldAt(treasury) > 0`. It reuses the
existing reachability test rather than inventing a second one, and it inherits that function's
overflow-safe entry arithmetic (`heavy_reserve.go:224`).

**The fourth clause is load-bearing.** Without it, an unaffordable heavy pauses probe buying forever
— the deadlock sp-zg71k fixed, re-created one layer up. `HeavyReserveEntrySharePct` is the credibility
threshold: a fleet holding half a hull's ask is credibly saving for it; a fleet holding a fifth of it
is starving. It is const-only with no config/tune/launch seam, and must stay that way
(`common/heavy_reserve.go:64-77`) — a savings curve an operator can set to zero is the freeze again.

**Derived, never stored.** RULINGS #2 is satisfied trivially: nothing is written, so restart safety
is free, no cursor goes stale, and there is no re-fire hazard. This is the same treatment
`HeavyReserve` itself takes (`heavy_reserve.go:107-113`) and the same treatment the heavy pricing
errand takes for its in-flight state (`fleet_autosizer_heavy_pricing.go:24-28`).

### The wave subsumes `HoldAt`'s probe-floor role

`HoldAt` has two consumers today:

| Consumer | Site | Role |
|---|---|---|
| autosizer affordability guard | `fleet_autosizer_act.go:192` → `fleet_autosizer_guards.go:395` | `spendable = treasury − floor − heavyReserve` |
| sensing probe-buy floor | `parkedsensing/buyqueue.go:284`, folded into the capex term at `:293-298` | ramps probe buying down as a heavy accumulates |

The wave **binary-gates** probe buying. `HoldAt`'s ramp as a floor term therefore becomes redundant:
the probe drain either runs at full speed (PROBE wave) or does not run (HEAVY wave). Two mechanisms
doing one job is how they drift.

**Decision:** keep `HoldAt` as the affordability predicate; **drop** `heavyHold` from the drain's
`ProbeBuyFloor` capex term. The drain gates on the wave instead.

### No new phase enum

Before any heavy is priced, no priced target exists → the predicate is PROBE → the heavy **pricing
errand** runs and sends a hull to read the ask. The "HEAVY TRADE phase" is not a state to enter; it
is simply the regime in which the predicate can flip.

**A fifth bootstrap phase was rejected.** Bootstrap's *exit* is load-bearing, and it says so:

> "That bound is load-bearing on a MATURE fleet: this coordinator's whole tick-cadence budget assumes
> it exits once the gate is built, and each tick it does not costs a fully-paginated fleet re-read
> against an account-wide request limit that fleet growth cannot raise."
> — `internal/application/bootstrap/commands/run_bootstrap_gate.go:469-471`

Adding a phase means bootstrap does not exit, which means that re-read runs forever against a ceiling
this work cannot raise.

---

## Working capital formula

```
working_capital = max(
    runway_milli_hours × observed_cargo_spend_per_hour / 1000,
    trade_hulls × observed_typical_hold_fill_cost
)
```

Both terms derive from **observed transactions**. No hardcoded prices: the prod ledger's own heavy
purchases span 1,516,566–2,529,556, so any constant is wrong in most of the range it has to cover.

| Term | Why it exists | Existing precedent |
|---|---|---|
| (a) runway | The busier the fleet, the more cargo spend is already committed. Buying a hull out of money the trading fleet is about to spend stalls the trades that fund everything else. | `ProbeBuyFloor`'s runway term, `domain/parkedsensing/floor.go:39`; source `AbsCargoBuySpendSince` over `PURCHASE_CARGO` rows in a trailing window (`adapters/parkedsensing/treasury_ports.go:58`, `cargoSpendLookback = 1h` at `parkedsensing/buyports.go:55`) |
| (b) hold-fill | (a) **under-reserves exactly when you have just added capacity** — a freshly bought hull has no spend history, so the observed runway does not yet contain it. | none; new |

Term (a) uses **milli-hours** and integer arithmetic throughout, matching `ProbeBuyFloor`. Every
factor is clamped non-negative *before* the product is formed, and the product is formed before the
divide, so a malformed observation can only make the guard stricter (`floor.go:26-35`). A float would
put NaN into a money guard, where it fails every comparison and passes every clamp.

### Money guard

A heavy purchase requires:

```
treasury − price  ≥  ImmutableReserveFloor + working_capital
```

`ImmutableReserveFloor` (`common/reserve_floor.go:11`) is **untouched**. This adds a term **above**
it, never below, and never relaxes it. RULINGS #4 holds. RULINGS #5's hard-floor exception keeps the
floor non-tunable.

---

## Architecture

A new long-running **fleet-growth coordinator**, dropping into the autosizer's exact container slot.

### What it inherits from that slot

| Facility | Autosizer's version |
|---|---|
| Container type | `ContainerTypeFleetAutosizer = "FLEET_AUTOSIZER_COORDINATOR"` — `domain/container/container.go:91` |
| Launch path | `DaemonServer.FleetAutosizerCoordinator` — `adapters/grpc/container_ops_fleet_autosizer.go:23` (iterations −1) |
| Restart recovery | builder `{CommandType: "fleet_autosizer"}` — `command_factory_registry.go:165` |
| Bootstrap hand-off launch | `bootstrap_ports_gate.go:451` |
| Health / stall | `fleet_autosizer_stall.go:25`, `health.NewStallEscalator` wired at `fleet_autosizer_ports.go:120` |
| Liveconfig tune reads | `fleet_autosizer_tune.go` (`heavy_cap`, `sizing_enabled`) |
| Metrics sink | `MetricsSink` — `fleet_autosizer_act.go:109`, wired `fleet_autosizer_ports.go:115` |
| Purchase notifier | `PurchaseNotifier` — `fleet_autosizer_act.go:104`, wired `fleet_autosizer_ports.go:110` |

### What it owns

1. **The wave predicate** — the one definition.
2. **The heavy buy path** — demand read, price resolution, guard stack, purchase.
3. **The heavy pricing errand** — moves over intact. It is already self-contained: in-flight state is
   derived from ship rows rather than stored (`fleet_autosizer_heavy_pricing.go:24-28`), it is bounded
   to one hull fleet-wide by a const (`:36`), and it selects carriers by an allowlist of exactly one
   fleet tag, `"trade"` (`:50`), which is what makes "never re-task a parked sensing probe" structural
   rather than remembered.

### Rejected alternatives

| Alternative | Why rejected |
|---|---|
| Extend `parkedsensing` | Already the largest application package: 30 non-test files, 58 with tests. |
| A fifth bootstrap phase | Bootstrap's exit is load-bearing — see above. |

### The API-cost switch survives the rewrite

`sizing_enabled` exists because the shipyard price walk runs **before** the guards can block, so a
blocked decision costs the same hundreds of `Get Shipyard` calls as an approved one
(`run_fleet_autosizer_reconcile.go:158-180`). That cost structure is a property of resolving a hull
price, not of the autosizer, so it survives into the growth coordinator. The switch is *not* a money
guard and must not be described as one. See **Open questions**.

---

## One definition, two consumers, lockstep-pinned

This is the idiom the codebase already trusts for `HeavyReserve`/`HoldAt`
(`common/heavy_reserve.go:92-105`, pinned by `TestHeavyReserveLockstep` at
`common/heavy_reserve_test.go:41`).

```
                    wave predicate (ONE definition)
                      /                      \
   growth coordinator's heavy buyer      parkedsensing drain
   (spends when HEAVY)                   (buys probes only when PROBE)
```

- The **coordinator owns only the DECISION.**
- The **drain keeps owning placement mechanics** — yard selection, slot claiming, fill order. It
  gains one gate and loses the `heavyHold` floor term.
- A **lockstep test** makes divergence a test failure rather than a live money bug.

Split-brain here is the same bug class as sp-v2a2h's `deliverGateLeg`, which sized `PlanFill` off the
raw bill without consulting the reservation and bought the same 36 units twice. Two spenders reading
one quantity through two derivations is the shape; the fix is one derivation, called by both.

---

## Off-gate warp expansion is RETIRED

**Decision: retire entirely. No explorers.**

### Evidence

- One `SHIP_EXPLORER` ever purchased (prod, 2026-07-24, 722,511 credits). Staging: none.
- No explorer-dedicated hull exists in either database today.
- The off-gate *dispatch loop* landed 2026-07-28, **after** that purchase — it has never had a hull.
- `autosizer_explorer_hulls_enabled` defaults false and appears in no checked-in YAML.

### The hazard retirement must clear

`parkedsensing.advanceOffGate` (`parkedsensing/offgate.go:131`) emits explorer demand onto
`OffGateDemandSink` (`offgate.go:49`) on **every tick** — a zero signal when there is nothing to
demand (`:149-152`), a positive one otherwise (`:154-158`). The concrete sink,
`adapters/expansion.ExplorerOffGateBridge` (`explorer_offgate_bridge.go:25`), **latches**: it answers
every read from the last write, forever. Its only reader is the autosizer's
`ExplorerDemandProvider` (registered at `fleet_autosizer_ports.go:68`; bridge constructed
`cmd/spacetraders-daemon/main.go:1158`, read `main.go:1200`, written `main.go:1404` → `main.go:390`).

Deleting the reader without handling the writer leaves a bridge latching demand nobody serves — the
exact failure `retractOffGateDemand` (`offgate.go:120`) exists to prevent. That retraction fires from
exactly **one** call site, the spend-pause branch (`expansion.go:413`), so it is not a general
teardown.

**Retirement must therefore prove the bridge is RETRACTED, not merely unread.**

### Accepted cost

**The gate network becomes the permanent reachable universe.** Any system not gate-connected is
unreachable by design. This is deliberate and is not a gap to be closed later.

### Surface to retire

Verified against the tree. All paths relative to `gobot/`.

| Path | Role | Status |
|---|---|---|
| `internal/application/parkedsensing/offgate.go` | ports, `advanceOffGate`, `retractOffGateDemand`, `dispatchExplorer` | explorer-only |
| `internal/application/parkedsensing/offgate_types.go` | `OffGateTarget`, `OffGateSelectionParams`, `OffGateDemandSignal` | explorer-only |
| — `OffGateTargetSelector` (`offgate_types.go:48`) | | **already dead** — 0 implementations, 0 call sites |
| — `ShipyardCoverageReader` / `GateShipyardsScanExhausted` (`offgate_types.go:57`) | | **already dead** — documents a trigger never implemented |
| `parkedsensing/expansion.go:473` | the `advanceOffGate` call | explorer-only |
| `parkedsensing/expansion.go:413` | `retractOffGateDemand` call | explorer-only |
| `parkedsensing/expansion.go:220, 305-313` | `ExpandPorts.OffGate`, `ExpandReport.OffGate*` | explorer-only |
| `parkedsensing/reach.go:290` `reachesAny` | the gate-reachability suppressor | explorer-only — one caller, `expansion.go:469` |
| `internal/adapters/expansion/explorer_offgate_bridge.go` | the latching bridge | explorer-only |
| `internal/adapters/expansion/off_gate_target.go` | target scoring | explorer-only |
| `internal/adapters/expansion/idle_explorer_port.go` | idle-explorer finder | explorer-only |
| `internal/adapters/expansion/explorer_dispatch_adapter.go` | warp dispatch | explorer-only |
| `internal/adapters/expansion/universe_systems.go` | whole-universe roster cache | explorer-only in practice — sole consumer is `off_gate_target.go:34` |
| `internal/adapters/api/client_universe.go:127` `ListSystems` | universe roster fetch | explorer-only in practice — sole consumer `universe_systems.go:227` |
| `internal/domain/system/universe.go` | roster DTOs | explorer-only in practice |
| `internal/application/fleet/commands/fleet_autosizer_explorer.go` | `ExplorerDemandProvider`, `OffGateDemandSource` | explorer-only |
| `internal/adapters/grpc/fleet_autosizer_demand_ports.go:128` | `autosizerExplorerFleetSource` | explorer-only |
| `internal/adapters/grpc/fleet_autosizer_ports.go:44, 68` | the `offGateDemand` param and registration | explorer-only |
| `scouting/commands/probe_sensing_stall.go:57,181-186` | `stallReasonOffGateNoTarget`, `rep.OffGateWarped` | explorer-only |
| 5 tune keys (below) | | explorer-only |

Tune keys, all in `fleetAutosizerConfigKeys` (`container_ops_fleet_autosizer.go:83-87`) and absent
from every checked-in YAML:

`autosizer_explorer_hulls_enabled` (default false) · `autosizer_fleet_ceiling_explorer` (1) ·
`autosizer_explorer_treasury_pct_per_purchase` (25) · `autosizer_max_price_explorer` (900000) ·
`autosizer_ship_type_explorer` (`SHIP_EXPLORER`).

### FLAGGED — has a non-explorer consumer, do NOT delete

The brainstorm asked for warp-charter paths "that exist solely for it". **None of the warp stack
does.** Every piece below is also reached by the manual `spacetraders ship warp` operator verb
(`adapters/cli/ship_navigate.go:157` → `grpc/container_ops_ship.go:100` →
`application/ship/commands/navigation/warp_ship.go:65`), which is an audited deliberate override.

| Path | Non-explorer consumer |
|---|---|
| `internal/application/ship/route_executor_warp.go` `ExecuteWarpRoute` | manual `ship warp` |
| `internal/application/ship/warp_system_charter.go` | wired unconditionally at `main.go:1117`, so the manual verb charts too |
| `internal/application/ship/warp_navigator.go`, `warp_escape_reader.go`, `route_executor_warp_errors.go`, `route_executor.go:115` | manual `ship warp` |
| `command_factory_registry.go:216` `"warp_ship"`, `container.ContainerTypeWarp` | restart recovery for the manual verb |
| `domain/navigation/ship_specs.go:26` `HasWarpDrive()` | also `route_executor_warp.go:77` |
| `parkedsensing/placement.go:63` `MaxWalkRings` | read by `foothold.go`, `seed.go`, `supply.go`, `reach.go`, `probe_orphan_dispatch.go`, `adapters/parkedsensing/mover_ports.go` |
| `adapters/expansion/{adapters,bootstrap_phase,candidates,frontier_bearing,shipyard_backfill}.go` | expansion scanner, probe-buyer-fleet coordinator, shipyard-backfill coordinator — the package cannot be deleted wholesale |
| `container_ops_tune.go:141` (`expansion_enabled` description), `probe_sensing_heartbeat.go:421` | shared strings that *mention* the explorer — **edit the prose, keep the key** |

Retiring explorers removes one of two callers of the warp stack. It does not remove the stack.

---

## Kept surface — what deleting the autosizer must NOT take with it

Two dependencies make "delete the autosizer package" wrong as written.

**1. The buy primitive is shared with the contract scaler.**
`contractScalerPurchaser` constructs its own `&autosizerPurchaser{...}` (`contract_scaler_ports.go:73`)
and drives it twice: `BuyAndHome` with `HullClassContractDelivery` (`:169`, tags `"contract"`) and
`BuyHull` with `HullClassLight` (`:191`, deliberately tags **nothing**, so the depot grower can
re-dedicate the hull). Both depend on `autosizerDedicatedFleet`'s class→tag map
(`fleet_autosizer_ports.go:200-216`).

Therefore **kept**: `autosizerPurchaser`, `BuyOrder`/`BuyResult`, `HullClass`, `HullClassLight`,
`HullClassContractDelivery`, `autosizerDedicatedFleet`. `HullClassLight` survives as the
*undedicated-buy primitive*, not as a demand class.

**2. The sensing heavy reserve reads `heavy_cap` off the autosizer's container row.**
`AutosizerCapPort.HeavyCap` queries `ContainerTypeFleetAutosizer` (`heavy_reserve_port.go:316`) and
`resolveHeavyCap`'s `!containerExists` rung returns `(0, false)` — **deliberately silent**, because a
probe-only deployment would otherwise warn every tick (`heavy_reserve_port.go:248-252`). Deleting the
container therefore zeroes the heavy reservation **invisibly**. See **Migration path** step 5.

For completeness: the `light` *demand* class is genuinely dead. Its chain count reads running
`goods_factory_coordinator` containers (`fleet_autosizer_demand_ports.go:33`) — a **retired** command
type with no builder (`daemon_server_recovery.go:27`) — and its `Vacancies` source returns a
hardcoded 0 (`fleet_autosizer_demand_ports.go:49-53`). Demand is structurally always 0, yet the
provider is evaluated every tick, costing a full-fleet read and a container-list read. The gate
factory is owned by the construction coordinator (`domain/manufacturing/gate/role.go:35`,
`run_construction_coordinator_gate_delivery.go:209`), which does not go through this class.

---

## Money guards — unchanged

| Guard | Treatment |
|---|---|
| `ImmutableReserveFloor` | Untouched. Working capital is a term **above** it. |
| `HeavyReserveEntrySharePct` | Untouched, const-only, no seam. |
| 25%-treasury rule (RULINGS #6) | Unchanged for heavy buys. |
| Unreadable inputs | Fail closed: no lane signal → no buy; no treasury → no buy. The reservation continues to fail *open* (reserve nothing) because it authorises no spend and holding on a blind signal starves expansion — the existing documented direction (`heavy_reserve.go:115-125`). |

No guard is relaxed as a side effect. RULINGS #4 holds.

---

## Testing

Mutation probes are the house standard; each probe must **name the tests it kills**.

| Case | What it pins |
|---|---|
| Wave flips PROBE → HEAVY | `unserved_lanes` rises above 0 with an affordable priced target |
| Wave flips HEAVY → PROBE | `unserved_lanes` reaches 0 after a heavy lands |
| **Anti-deadlock (the sp-zg71k regression)** | An **unreachable** heavy (`surplus < entry`) must **NOT** pause probe buying. Priced target present, treasury well below the entry threshold, assert probe buying continues and `ImmutableReserveFloor` still binds. |
| Wave at cap | `heavies_owned == heavy_cap` → PROBE, regardless of lanes |
| Wave with no priced target | unpriced yard → PROBE, and the pricing errand runs |
| Working capital, term (a) dominates | busy fleet, few hulls |
| Working capital, term (b) dominates | just-added capacity with no spend history |
| Working capital floors at the immutable | both terms zero → floor is exactly `ImmutableReserveFloor` |
| **Lockstep** | The growth coordinator's buyer and the `parkedsensing` drain read **one** predicate; a divergent second derivation fails the suite |
| **Explorer bridge retracted** | Prove the bridge is retracted, not merely unread. A test that only asserts "nobody reads it" passes against a latched `Demanded: true`. |
| Kept-surface regression | The contract scaler's `BuyAndHome` and `BuyHull` still work after the autosizer coordinator is deleted |
| `heavy_cap` resolution | The cap read resolves to the same value both consumers see after the container slot changes hands |

---

## Migration path

Ordered so that no step leaves a latched bridge, an unserved demand, or a silently-zeroed reserve.

1. **Growth coordinator alongside.** New coordinator, new container slot, wave predicate + heavy buy
   path. Autosizer still running; the two must not both buy heavies, so the autosizer's heavy class is
   switched off in the same step.
2. **Move the pricing errand.** It transfers intact — no in-flight state to migrate, since it is
   derived from ship rows every tick.
3. **Wire the wave into the drain.** The drain gains the wave gate and loses `heavyHold` from its
   `ProbeBuyFloor` capex term (`buyqueue.go:284,293-298`). Lockstep test lands here.
4. **Resolve the explorer bridge.** Retract it, then delete writer and reader together. Prove
   retraction.
5. **Re-home `heavy_cap`.** Repoint `AutosizerCapPort` at the growth coordinator's container type and
   key ladder before the autosizer container disappears. The `!containerExists` rung is silent, so
   getting this wrong is invisible in every gauge and heartbeat.
6. **Delete the autosizer coordinator** — reconcile loop, demand providers, act/guard stack, explorer
   class, stall plumbing, its tune keys, its container type and recovery builder. **Keep** the buy
   primitive and hull-class constants per **Kept surface**.
7. **Verify no orphaned demand.** No bridge left latching, no demand provider without a consumer, no
   consumer without a provider.

---

## Open questions

These are genuinely undecided. They are not to be answered inside this spec.

### From the brainstorm

1. **Hysteresis.** Does the predicate need a streak or band to avoid flipping on a jittery lane count?
   The precedent exists: `heavy_unserved_lanes_min` (default 3, `run_fleet_autosizer_coordinator.go:28`)
   requires the shortfall to persist that many consecutive ticks, held as per-container in-memory tick
   state in the ACT step (`fleet_autosizer_act.go:358`). **The tradeoff, not a recommendation:**
   flapping may be benign here because every buy is independently guarded, so a wave that oscillates
   simply defers a purchase rather than making a wrong one; against that, a streak is per-container
   in-memory state, which is exactly what "derived, never stored" avoids.

2. **`runway_milli_hours`: value, and const or tunable?** `ProbeBuyFloor`'s equivalent
   (`capital_multiplier_k_milli`) *is* a tunable with registered bounds. Bearing on the choice: the
   standing "no feature flags" order, and RULINGS #5 ("parametrize, don't hardcode" — except where the
   Admiral has ruled a hard floor). Note this knob is a *pacing* term above the immutable floor, not
   the floor itself.

3. **How is `observed_typical_hold_fill_cost` derived?** Which transactions, what window. The nearest
   existing idiom reads `PURCHASE_CARGO` rows over a trailing hour and takes the absolute value **per
   row**, so a stray positive row adds to measured outflow instead of cancelling real spend out of it
   (`adapters/parkedsensing/treasury_ports.go:58-79`). Per-hull, per-fill, and per-hold-capacity are
   all plausible grains and give different numbers.

4. **Do `contract_delivery` and `light` demand providers die with the autosizer, or need re-homing?**
   Partly answered by the code, partly not:
   - `contract_delivery` has **no provider** and never did — nothing to re-home. The contract scaler
     already owns that fleet end to end.
   - `light` *demand* is structurally dead and can die. But `HullClassLight` the **constant** must
     survive as the contract scaler's undedicated-buy primitive (`contract_scaler_ports.go:191`).
   - Still open: does anything want light demand back once the gate factory grows, or is the
     construction coordinator's own sizing permanently the owner?

### Newly surfaced by verification

5. **Does the growth coordinator inherit `sizing_enabled`?** The switch exists because a *blocked*
   decision costs the same shipyard price walk as an approved one — the walk runs before the guards
   (`run_fleet_autosizer_reconcile.go:158-180`). That cost belongs to price resolution, not to the
   autosizer, so it survives the rewrite. Open: same name, same 1=on/2=off encoding, same "off stops
   the reads, not just the buying" semantics?

6. **Where does `heavy_cap` live after the slot changes hands?** It is currently the autosizer's only
   live-tunable knob, and `AutosizerCapPort` mirrors the autosizer's exact two-key precedence ladder —
   bare key when positive, else prefixed key with present-vs-absent semantics, else the compiled
   default (`heavy_reserve_port.go:281-300`). Whatever owns the knob must reproduce that ladder or the
   two consumers of one predicate will disagree about the cap.

7. **Does the wave predicate need its own metric?** `sizing_enabled` is emitted on **both** paths every
   tick, deliberately, "so an operator can read which state the coordinator is in from a series, not
   infer it from the absence of one" (`run_fleet_autosizer_reconcile.go:154-158`). The same argument
   applies to the wave: a HEAVY wave and a stalled coordinator both look like "no probes bought".
