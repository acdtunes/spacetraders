# Capacity Reconciler — Shared Contract Surface (epic st-7zk)

Authority for every sibling lane of the epic. **Fill these structs and implement these
interfaces; do not rename, redefine, or fork them.** Spec:
`docs/superpowers/specs/2026-07-15-capacity-reconciler-design.md`.

## Import paths

| Package | Path | Holds |
|---|---|---|
| Domain contracts | `github.com/andrescamacho/spacetraders-go/internal/domain/capacity` | All shared types + port interfaces + NoOp components |
| Loop coordinator | `github.com/andrescamacho/spacetraders-go/internal/application/capacity/commands` | `RunCapacityReconcilerCoordinator{Command,Response,Handler}` |
| Launch/recovery adapter | `internal/adapters/grpc/container_ops_capacity_reconciler.go` | `DaemonServer.CapacityReconcilerCoordinator`, live-config resolve, factory builder |
| Config | `internal/infrastructure/config/capacity_reconciler.go` | `CapacityReconcilerConfig` (`[capacity_reconciler]` in config.yaml) |

## The loop (application/capacity/commands)

One tick = `SENSE → PLAN → DIFF → GOVERN → CONVERGE`, stateless per tick. Kill switch
(`capacity.KillSwitch`) is re-read at the TOP of every tick; engaged (or nil) ⇒ the tick idles
with **zero** phase invocations. A failing phase ends that tick (outcome carries
`FailedPhase` + `Error`) and the loop continues. Calibration is resolved once at launch
(defaults + validation; invalid ⇒ launch error) and passed to PLAN/DIFF/GOVERN each tick.

CONVERGE backstops (structural, independent of governor correctness):

- **Capital gate (invariant 4)**: an `Approved` tier-4 action with
  `EstimatedCostCredits >= ApprovalThresholdCredits` is REFUSED — recorded as a CONVERGE
  failure, never executed. Under the v1 default threshold (0) NO tier-4 action can execute
  from `Approved`; graduated below-threshold auto-approval passes. The proposal-approval
  path (st-0h8) bypasses converge and is unaffected.
- **Verb/tier check**: dispatch verifies the canonical verb → tier mapping (action.go); a
  mislabeled action (e.g. `buy_hull` claiming tier-2) is refused, so tier mislabeling
  cannot bypass the capital gate. Unknown verbs are refused too (fail-closed).
- **Proposal attribution**: the governor MAY leave `Proposal.PlayerID` zero (its Govern
  inputs carry no player identity); converge stamps the reconciling player's ID on
  zero-PlayerID proposals before `ProposalChannel.Submit`. Non-zero passes verbatim —
  `Submit` always sees a real player.

- `RunCapacityReconcilerCoordinatorCommand` — launch config: `PlayerID`, `ContainerID`,
  `TickIntervalSecs`, and the 7 calibration fields (zero ⇒ default).
- `NewRunCapacityReconcilerCoordinatorHandler(domain, differ, governor, actuator, proposals,
  killSwitch, clock)` — nil clock ⇒ real clock; nil killSwitch ⇒ fail-closed (always idle).
- `(*Handler).SetTickObserver(capacity.TickObserver)` — per-tick outcome stream (harness seam).
- `(*Handler).SetEventRecorder(captain.EventRecorder)` — error-streak captain events.
- `(*Handler).Handle(ctx, req)` — infinite loop until ctx cancel.

## Domain exports (internal/domain/capacity), one-line contracts

**signals.go** (SENSE lane st-7ee fills; planner st-hlw reads)
- `Signals{PlayerID, CollectedAt, Demand, Performance, Topology, Utilization, Economics}` — one read-only measurement snapshot per tick.
- `DemandSignals{Hubs []HubDemand}`; `HubDemand{HubSymbol, ContractFrequency, AvgPaymentCredits, GoodMix []GoodDemand}`; `GoodDemand{Good, Frequency, AvgUnits}` — per-hub contract frequency + good-mix.
- `PerformanceSignals{Hubs []HubPerformance}`; `HubPerformance{HubSymbol, CycleTimeSeconds, StallEvents}` — accept→fulfill cycle-time, the lever.
- `TopologySignals{Clusters []ClusterState}`; `ClusterState{HubSymbol, Warehouses, Stockers, Workers}`; `WarehouseState{ShipSymbol, Waypoint, Buffer []BufferedStock, GoodCaps map[string]int}`; `BufferedStock{Good, Units}`; `StockerState`/`WorkerState{ShipSymbol, Waypoint}` — the ACTUAL topology DIFF compares against.
- `UtilizationSignals{Hulls []HullUtilization}`; `HullUtilization{ShipSymbol, DedicatedFleet, Waypoint, DutyCyclePct, Idle}` — drives reuse-first (never poach a pinned hull).
- `EconomicsSignals{TreasuryCredits, IncomeVelocityPerHour, FleetPerHullCrHr, FleetHullCount, SourceDistances []GoodSourceDistance, StockerLoad []StockerLoad}`; `GoodSourceDistance{HubSymbol, Good, Distance}`; `StockerLoad{HubSymbol, ActiveStockers, LoadPct}`. `FleetHullCount` (st-7ee fills; keep consistent with `len(Utilization.Hulls)`) is the governor's ROI-arithmetic input `n` — see the proposal.go derivation.

**topology.go** (planner st-hlw emits; differ st-zr0 consumes)
- `DesiredTopology{Hubs []DesiredHub}` + `IsEmpty()` — the PLAN output; empty = want nothing.
- `DesiredHub{HubSymbol, BufferedGoods []DesiredBufferedGood, WarehouseCount, StockerCount, WorkerCount, WarehouseWaypoint, StockerWaypoint, WorkerWaypoint}`; `DesiredBufferedGood{Good, UnitsCap}`. Positions default to the hub when empty; `StockerWaypoint` is the roaming stockers' home/anchor.

**action.go** (differ st-zr0 emits; governor st-x00 + actuator st-5ig consume)
- `Tier` (int enum): `TierReuseIdle`=1, `TierRebalance`=2, `TierBufferAdjust`=3, `TierCapital`=4; `Autonomous()` (1–3 true), `RequiresApproval()` (4 true), `String()`.
- `ActionVerb` consts: `VerbReassignHull`(1), `VerbRepositionHull`/`VerbRebalanceWorkers`(2), `VerbAdjustBufferWhitelist`/`VerbAdjustBufferCap`(3), `VerbAddCluster`/`VerbBuyHull`(4).
- `Action{Tier, Verb, HubSymbol, ShipSymbol, Good, UnitsCap, TargetWaypoint, EstimatedCostCredits, HullDelta, ProjectedPerHullCrHr, Reason}` — one convergence step. `HullDelta` (st-zr0 fills; pure topology arithmetic: `buy_hull`=1, `add_cluster`=its warehouse+stocker+worker counts, 0 for free tiers) is the governor's ROI-arithmetic input `d`.
- `Gap{Kind GapKind, HubSymbol, Good, Want, Have, Detail}` + `GapKind` consts (`GapHubUncovered`, `GapBufferGoodMissing/Extra`, `GapBufferCapWrong`, `GapWarehouseShort`, `GapStockerShort`, `GapWorkerShort`, `GapHullMisplaced`).

**governor.go** (governor lane st-x00)
- `Calibration{ReserveFloorCredits, SurplusFraction, PerDecisionCapPct, ROIPaybackHorizon, AddThresholdPerHullCrHr, StockerCapacityBudget, TickInterval, ApprovalThresholdCredits}` — the spec's calibration set, resolved + validated at launch, passed into PLAN/DIFF/GOVERN each tick.
- `DefaultCalibration()`; consts `DefaultReserveFloorCredits`(50000), `DefaultSurplusFraction`(0.25), `DefaultPerDecisionCapPct`(25), `DefaultROIPaybackHorizon`(24h), `DefaultTickInterval`(300s). `Validate() error` — range gates, fail loud.
- `CapexBudget{TreasuryCredits, ReserveFloorCredits, DeployableCredits, PerDecisionCapCredits}` — Deployable = f × (treasury − floor), floored at 0; PerDecisionCap = Deployable × pct/100. st-x00 owns the math.
- `CapexDecision{Action, Approved, Reason, Budget}` — audited per-capital-action verdict.
- `GovernResult{Approved []Action, Proposals []Proposal, Decisions []CapexDecision}`.

**proposal.go** (proposal lane st-0h8)
- `Proposal{ID, PlayerID, Action, Evidence, Status, CreatedAt}` — capital action awaiting approval; ID must be stable across re-files of the same gap. The governor MAY leave `PlayerID` zero (Govern carries no player identity); the loop stamps the reconciling player before `Submit`, so st-0h8 always receives a real player.
- `ROIEvidence{CostCredits, ProjectedGainPerHour, PaybackHorizon, ProjectedPaybackHours, FleetPerHullCrHrBefore, FleetPerHullCrHrAfter, Narrative}`.
- Governor ROI derivation (division-free): `ProjectedGainPerHour = after×(n+d) − before×n` with `after = Action.ProjectedPerHullCrHr`, `before = EconomicsSignals.FleetPerHullCrHr`, `n = EconomicsSignals.FleetHullCount`, `d = Action.HullDelta`. Cold start (`FleetHullCount == 0`) or non-positive gain ⇒ payback UNDEFINED ⇒ the action is PROPOSAL-ONLY (never auto-approved), Narrative says why.
- `ProposalStatus` consts: `ProposalPending/Approved/Rejected/Executed/Expired`.

**ports.go** (the interfaces + observability)
- `Sensor.Sense(ctx, playerID) (Signals, error)` — read-only.
- `Planner.ComputeDesired(ctx, Signals, Calibration) (DesiredTopology, error)`.
- `Differ.Diff(ctx, desired, actual TopologySignals, Calibration) ([]Action, error)` — cheapest-lever-first order; empty desired ⇒ zero actions.
- `Governor.Govern(ctx, []Action, EconomicsSignals, Calibration) (GovernResult, error)` — cheap tiers → Approved; capital → Proposals (v1: ALL tier-4).
- `Actuator{ReuseIdleHull, Rebalance, AdjustBuffer, ExecuteCapital}(ctx, Action) error` — thin wrapper over existing primitives; ExecuteCapital is post-approval ONLY.
- `ProposalChannel.Submit(ctx, Proposal) error`.
- `CapacityDomain{Name(), Sensor(), Planner()}`; `NewStaticDomain(name, sensor, planner)`; `ContractDeliveryDomainName`.
- `KillSwitch.Disabled() bool` — production impl is `watchkeeper.NewWorkspace(cfg.Captain.WorkspaceDir)` (os.Stat of `captain/DISABLED`).
- `Phase` consts (`PhaseSense/Plan/Diff/Govern/Converge`); `TickOutcome{Sequence, At, Idle, FailedPhase, Error, ActionsExecuted, ProposalsFiled}`; `TickObserver.ObserveTick(TickOutcome)`.

**noop.go** — `NoOpSensor`, `NoOpPlanner`, `NoOpDiffer`, `NoOpGovernor` (inert), `NoOpActuator`, `NoOpProposalChannel` (FAIL LOUD if invoked).

## Start / stop / recovery (deploy-inert)

- Start: `spacetraders workflow capacity-reconciler --agent <A>` → gRPC
  `CapacityReconcilerCoordinator` → `DaemonServer.CapacityReconcilerCoordinator(ctx, playerID)`
  → persisted container (`CAPACITY_RECONCILER_COORDINATOR`, iterations=-1) → runner.
- **Double-launch guarded**: one standing reconciler per player. A second start while an
  ACTIVE (PENDING/RUNNING) reconciler exists fails with "already running" naming the
  existing container ID.
- **Never boot-standing-armed** — pinned by `TestCapacityReconciler_NotBootStandingArmed`.
  A fresh deploy changes nothing until an operator starts it.
- Restart-safe: RUNNING container re-adopts via `RecoverRunningContainers` →
  `buildCommandForType("capacity_reconciler_coordinator")`, calibration re-resolved LIVE from
  config.yaml (`resolveCapacityReconcilerConfig`, sp-ts82 pattern).
- Stop: `spacetraders container stop <id>`.

## Per-lane file ownership (keep lanes DISJOINT)

| Lane | Implements | Create/extend (yours) | Wiring swap (one line each) |
|---|---|---|---|
| st-6wa harness | scenario seeding + assertions | `bootstrap-harness`/`twin` files; harness-side observers via `SetTickObserver` | none |
| st-7ee SENSE | `Sensor` | `internal/adapters/capacity/sensor_*.go` (new dir) or `internal/application/capacity/sense/` | `NoOpSensor{}` → yours in `cmd/spacetraders-daemon/main.go` |
| st-hlw planner | `Planner` | `internal/domain/capacity/heuristic_planner.go` (pure) + fixtures | `NoOpPlanner{}` → yours |
| st-zr0 DIFF | `Differ` (+ may add `Gap` helpers in a NEW file `internal/domain/capacity/ladder.go`) | `internal/domain/capacity/ladder.go` | `NoOpDiffer{}` → yours |
| st-5ig actuator | `Actuator` | `internal/adapters/capacity/actuator.go` (new dir) | `NoOpActuator{}` → yours |
| st-x00 governor | `Governor` | `internal/domain/capacity/capex_governor.go` (pure math) | `NoOpGovernor{}` → yours |
| st-0h8 proposals | `ProposalChannel` + approval-execution | `internal/adapters/capacity/proposal_channel.go`; approval path calls `Actuator.ExecuteCapital` with `Proposal.Action` verbatim | `NoOpProposalChannel{}` → yours |

Rules: add fields to existing structs only additively (never rename); new exported types go in
YOUR files; the only shared file every lane touches is `cmd/spacetraders-daemon/main.go`
(single wiring line each — merge-trivial). Do not test loop internals; drive the handler and
assert `TickOutcome`s + spy ports (see
`internal/application/capacity/commands/run_capacity_reconciler_coordinator_test.go`).

## Safety invariants (harness must assert; never breach)

1. `captain/DISABLED` present ⇒ every tick idles (zero phase invocations) — honored per tick.
2. Reserve floor: no capital decision may spend below `ReserveFloorCredits`.
3. Per-decision cap: one decision ≤ `PerDecisionCapPct`% of deployable.
4. v1 tiered autonomy: NO tier-4 action executes without an approved proposal
   (`ApprovalThresholdCredits` default 0). Enforced STRUCTURALLY by the CONVERGE capital
   backstop (above), not only by governor correctness — the loop refuses Approved tier-4
   at/over the threshold.
