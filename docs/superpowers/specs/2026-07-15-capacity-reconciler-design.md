# Capacity Reconciler — Design Spec

- **Date:** 2026-07-15
- **Status:** Approved (design), pending build
- **Author:** economy-analyst (brainstormed with the Admiral)
- **Origin:** Admiral request — "an automation that bootstraps from 1–2 ships and gradually adds clusters/hubs/warehouses/stockers/workers depending on demand, contract history and treasury, autobuying and positioning capacity, continuously assessing whole-system efficiency to reduce cycle-time."
- **Build owner:** shipwright (economy-analyst is read-only; this spec + the linked epic are the handoff).

## Objective

Continuously drive the **contract-delivery machine's** actual capacity topology toward a computed **desired topology** that maximizes **per-hull-sustained $/hr**, with **cycle-time as the primary control lever**, paced by a treasury-based capex governor. Cheap/reversible moves execute autonomously; capital spends file proposals for human approval.

**North-star metric:** per-hull-sustained $/hr (Phase-2 KPI). Chosen over raw cycle-time minimization because it self-limits over-buying — it stops adding capacity the moment a new hull would lower the fleet-wide average (the absorption ceiling). Cycle-time compression is the dominant lever *inside* this metric.

### Non-goals (v1)
- Arb/tour and manufacturing fleet sizing (their own coordinators own these). Designed with a `CapacityDomain` seam so they can be added later.
- Replacing the existing actuator primitives (fleet autosizer, launch siting, worker-rebalancer, depot-rebalance). The engine **orchestrates** them; it does not reimplement buy/move/rebalance.
- Full unattended treasury spending on day one (see Autonomy — tiered, graduates later).

## Locked decisions (with rationale)

| Axis | Decision | Why |
|------|----------|-----|
| Objective | Maximize per-hull-sustained $/hr; cycle-time as lever | Self-limits over-buying; matches Phase-2 doctrine |
| Autonomy | **Tiered**: cheap/reversible autonomous, capital gated on approval; graduate to full autonomy once buy-policy is proven | Auto-spending treasury is the one place a flawed policy compounds before detection; de-risks the build (cheap-autonomous half ships first) |
| Scope | Contract-delivery machine only, with a `CapacityDomain` extension seam | "Cycle-time" is a contract concept; cleanest unit to measure; arb/mfg plug in later on the same $/hr yardstick |
| Architecture | Declarative **reconciler**: actual → desired → diff → converge, capex-paced | Idempotent, restart-safe, self-healing; maps onto existing daemon reconcile-loop infra |
| Planner | **Heuristic** (deterministic ranking + thresholds) behind a `Planner` interface; solver slots in later | Interpretability is a feature when auto-spending; ships fast; solver reserved for the one combinatorial sub-problem |
| Capex governor | Reserve floor + **surplus-fraction drain** (`f × (treasury − floor)`/cycle) + 25%-per-decision cap + ROI/payback gate | Single self-scaling knob; auto-throttles near the floor; no stale hard-coded amounts |
| Coupling | Decoupled standalone engine; reusable by the bootstrapper's INCOME phase | Admiral directive |

## Architecture — the reconcile loop

On startup and each tick: **`SENSE → PLAN → DIFF → GOVERN → CONVERGE`**. The loop is **stateless per tick** — desired state is recomputed from live state every pass — so it is idempotent, restart-safe, and self-healing: a failed buy or a drifted hull simply reappears as gap on the next pass and is re-converged. Honors the `captain/DISABLED` kill switch (idle when present).

### SENSE — signals (all read-only: daemon DB + live API)
- **Demand:** per-hub contract frequency + good-mix (contract history).
- **Performance:** per-hub accept→fulfill cycle-time (the lever) + stall events.
- **Topology:** current clusters/hubs/warehouses/stockers/workers, positions, buffer contents, per-good caps.
- **Utilization:** per-hull duty-cycle / idle state (drives reuse-first).
- **Economics:** treasury, income velocity, per-good source distances, stocker load.

### PLAN — the heuristic planner (core intelligence)
Emits a `DesiredTopology`: covered hubs, buffered goods per hub + caps, warehouse/stocker/worker counts + positions. Deterministic policy (the doctrine demonstrated in the 2026-07-15 session):
- **Hub coverage:** rank hubs by `frequency × cycle_penalty × payment`; cover the top hubs (co-located worker) until the marginal hull's projected per-hull-$/hr falls below threshold.
- **Buffer goods per hub:** select by **stall-prevention ÷ stocker-cost** = `frequency ÷ (avg_units × source_distance)`, under a per-hub stocker-capacity budget. Skip remote/bulky low-value goods (e.g. AMMONIA_ICE at J58: 59 units × ~751 dist, low value → never buffer).
- **Caps:** per-good cap ≈ `avg_units + margin` (uncapped whitelist over-fills the first good and starves the rest).
- **Counts:** warehouse/stocker/worker counts sized to buffered volume + restock cadence (`restock_throughput ≥ consumption_rate`).
- Every capacity add is **ROI-gated on per-hull-$/hr**, so the desired topology self-limits.
- Behind a `Planner` interface — heuristic now; solver (CP-SAT/ortools, already in the routing-service) later for multi-hub stocker allocation.

### DIFF + CONVERGE — escalation ladder + autonomy gate
Gap → ordered action list, **cheapest-lever-first** (this ordering is what preserves per-hull-$/hr):
1. **Reuse idle hulls** (reassign) — free → **auto**
2. **Rebalance / reposition existing** (drive worker-rebalancer / depot-rebalance) — free → **auto**
3. **Adjust buffer whitelist + caps** — cheap → **auto** (gated if it forces a new stocker)
4. **Add cluster / autobuy hull** — capital → **proposal for approval** (tiered-autonomy gate)

Cheap tiers call the existing actuator primitives directly through an `Actuator` wrapper. The capex tier emits a **proposal** (bead + captain nudge) carrying the ROI evidence; on approval it executes via the same primitives.

### GOVERN — capex governor
- **Reserve floor** (hard): never spend below it (protects operating runway).
- **Surplus-fraction drain:** each cycle, `deployable = f × (treasury − floor)`.
- **Hard filters (always):** 25%-per-decision cap; ROI/payback gate (item must pay back in-horizon AND raise per-hull-$/hr).
- Only tier-4 (capital) actions consume the budget; cheap tiers are free.

## Interfaces / seams
- **`Planner.ComputeDesired(signals) → DesiredTopology`** — heuristic ↔ solver.
- **`CapacityDomain`** — contract-delivery now; arb / manufacturing later, compared on the same per-hull-$/hr yardstick.
- **`Actuator`** — thin wrapper over existing autosizer / siting / worker-rebalancer / depot-rebalance so the reconciler never reinvents buy/move.

## Verification (trust the harness > the code)
Reuse the bootstrap-harness + twin infrastructure (`twin/`, `bootstrap-harness/`):
- An **autoscaler-harness** boots an isolated daemon against the twin, seeds a demand + contract-history + treasury scenario, runs the reconciler, and asserts:
  - **Convergence:** actual topology reaches the expected desired topology for the scenario.
  - **Pacing:** capex deployment matches the surplus-fraction governor.
  - **Safety invariants (must never breach):** reserve floor, 25%-per-decision cap, `captain/DISABLED`.
- **Calibrate + regression-test the planner's rankings against real prod history** (prod DB: 854 contracts, real cycle-times, per-good source distances) as fixtures — prove the desired-topology choices match what a correct analysis would pick before it spends real credits.

## Reuse by the bootstrapper
The bootstrapper's INCOME phase invokes the same reconciler with a cold-start seed (1–2 ships) and a low reserve floor, letting it grow the contract-delivery machine from scratch. The engine is otherwise standalone (runs on the live steady-state economy).

## Calibration params (config, not code)
`reserve_floor`, surplus-fraction `f`, per-decision cap (default 25%), ROI payback horizon, per-hull-$/hr add-threshold, stocker-capacity budget, reconcile tick interval, capex-proposal approval threshold.

## Build decomposition (→ shipwright epic)
1. **Reconciler skeleton + interfaces** — loop scaffold, `Planner`/`Actuator`/`CapacityDomain` interfaces, config, `DISABLED` honoring, no-op planner. Foundation.
2. **Autoscaler-harness + twin scenario seeding** — verification-first; scenario/reset shapes, convergence + safety assertions.
3. **SENSE signal collectors** — read-only demand/performance/topology/utilization/economics.
4. **Heuristic planner** — ranking+threshold → `DesiredTopology`; prod-history calibration fixtures.
5. **DIFF + escalation ladder** — gap → ordered cheapest-first actions.
6. **Actuator wrapper** — drive existing primitives for cheap tiers.
7. **Capex governor** — reserve floor + surplus-fraction + 25% cap + ROI gate.
8. **Tiered-autonomy proposal channel** — emit capex proposal (bead + nudge) + approval execution path.
- *Future:* solver planner (multi-hub stocker allocation); graduate to full autonomy (drop the capex gate) once the buy-policy has a track record.
