# MECHANICS.md — the automation mechanics (gobot's coordinator brain)

Reference, not doctrine — READ this by area when a task touches the automation; it is not
primed every wake. It maps the standing coordinators (the bot's "brain") and how they
compose across the cold-start → GATE → steady-state lifecycle. Doctrine and process rules
live in `RULINGS.md`; strategy in `PLAYBOOK.md`; the CLI surface + the 3-layer knob system in
`CLI-PRIMER.md`; engineering code-facts/traps in `ENGINEERING.md`. This book answers a
different question than any of those: **how does the automation actually decide?**

**The code is always truth; this is the map.** Every coordinator entry carries a source-file
pointer — when this doc and the code disagree, the code wins; fix the map. Paths are relative
to `gobot/`. No changelog prose and no bead-ids — this maps the code as it stands now,
present-tense; when the doc and the code drift, the code wins, fix the map.

---

## 1. The container / recoverable-coordinator model

Every unit of standing automation is a **container**: a daemon-owned goroutine with a
persisted lifecycle row. The daemon is the single writer of all game + container state
(RULINGS #3); the CLI and coordinators never write ship state directly. Source:
`internal/domain/container/container.go`, `internal/domain/container` list of `ContainerType`.

- **A standing coordinator is a container with `maxIterations = -1`** — an infinite reconcile
  loop inside one `Handle()`. It spawns short-lived **child** containers (a `parentContainerID`
  links them) for the concrete work; a child with `nil` parent is a root coordinator or a
  captain-launched one-shot.
- **`CoordinatorOwnsIterations` vs loop-forever.** The one-shot ship ops (NAVIGATE, DOCK,
  ROUTE, PURCHASE, WORKER_FERRY, CARGO_LIQUIDATION, …) are single-iteration
  `CoordinatorOwnsIterations` types. The standing brains (bootstrap, siting, autosizer,
  freshsizer, scout-post, capacity reconciler, …) are NOT that type — they loop inside one
  `Handle()` and own their own tick cadence.
- **Restart-resilience is law (RULINGS #2).** A daemon restart rebuilds every container from
  its persisted `config` JSON column via `RecoverRunningContainers` →
  `buildCommandForType(...)`. A RUNNING container is re-adopted, not double-launched. Anything
  a coordinator needs across a restart must live in persistence (posts table, ledger, ship
  dedication rows, the `config` column), never in memory — see the pattern-A/B/C knob taxonomy
  in `CLI-PRIMER.md` §3. In-memory state (backoff clocks, hysteresis streaks) is always
  re-derivable and fails safe on loss.
- **Failure → restart budget → honest-pause.** `MaxRestartAttempts = 3`. A coordinator that
  hits a transient dead-end must NOT return an error the runner burns its restart budget on;
  it sets a `NoWorkReason` and backs off (self-heals on a later tick). Terminalizing as a
  crash is reserved for genuine faults (see §6.8, the goods-factory honest-pause).

## 2. The reconciler shape (observe → derive → act)

Every standing coordinator is the SAME shape, stateless-per-tick:

1. **OBSERVE** the live world (ships, markets, ledger, posts, containers) — a read-only
   snapshot.
2. **DERIVE** the desired state / phase FROM the observation — never from a stored cursor or
   enum. A restart at any point re-derives the true state from live signals, so it never
   double-advances or double-acts.
3. **ACT** on the delta, each side-effect independently guarded "already done / in-flight?"
   and **failing CLOSED** on any unreadable input (a missing signal never drives a spend or an
   assignment — RULINGS #4).
4. **HEARTBEAT** a one-line phase · delta · next-action · blocker log so a wedged reconciler is
   visible, never a silent stall.

This is why the coordinators are safe to boot-launch unconditionally and to restart at any
moment: idempotence + live-derivation + fail-closed means re-evaluation is a no-op when
nothing changed, and a partial action re-completes on the next tick.

## 3. Arming — how a coordinator comes to run

There are exactly three ways a coordinator starts. Know which, because it determines what a
fresh deploy does. Source of truth for boot arming:
`internal/adapters/grpc/daemon_boot_standing.go` (`bootStandingCoordinatorTypes`).

| Arming | Coordinators | Trigger |
|---|---|---|
| **Boot-standing** (auto, every daemon boot, idempotent) | Bootstrap, Market-freshness sizer, Scout-post, Construction, Capacity reconciler | `ensureBootStandingCoordinators` at daemon Start(); also `ensureGateSourceFeeders` launches InputsOnly goods_factory feeders |
| **GATE hand-off** (launched by the bootstrap at COMPLETE) | Fleet-autosizer, Siting, Worker-rebalancer | `bootstrapHandoffLauncher.LaunchAutosizer` + `LaunchStandingCoordinators` (`bootstrap_ports_gate.go:253-283`) |
| **Captain-launched** (a CLI verb; re-adopts across restart via persisted config) | Frontier-expansion, Goods-factory, Contract-fleet, Trade-fleet, Auto-outfit, Shipyard-backfill, Gas, Stocker | `frontier start`, `goods factory/produce`, `contract start`, `workflow trade-fleet-coordinator`, `workflow auto-outfit`, `workflow shipyard-backfill`, `operations start`, `workflow stocker --standing` |

**"Closed ≠ armed" (RULINGS #19).** A coordinator that exists in `ContainerType` but is absent
from `bootStandingCoordinatorTypes` and from the GATE hand-off never runs at cold start — it
waits for a captain launch. Separately, most armable BEHAVIORS ship default-off
(byte-identical): a merged bead is not a live feature until the knob is armed (`tune ... 1` or
a `run.sh` export + restart). Arming is a separate, untracked step — keep the arming ledger,
audit dormant knobs at every deploy, re-verify arms after every restart (`CLI-PRIMER.md` §3.4).

**Boot launches are idempotent.** Each `ensure*Standing` pre-checks `containerTypeRunning`; a
warm restart re-adopts the existing container instead of double-launching. Genesis guard: no
player row (`playerID <= 0`) skips all boot-standing launches until registration.

## 4. Money guards (fail closed, never weakened — RULINGS #4/#5/#6)

Every spend passes a layered, fail-closed guard stack. **Cannot read live balance or price →
do not spend.** No fix may relax a guard as a side effect. The hard floors are non-tunable
(RULINGS #5); everything above them is treasury-RELATIVE so a guard tuned for a poor treasury
never throttles a flush one.

- **`common.ImmutableReserveFloor = 50000`** — the working-capital floor.
  `common.EffectiveReserveFloor(absolute, pct, liveTreasury) = max(50000, min(absolute,
  round(pct% × liveTreasury)))`, `DefaultReserveTreasuryPct = 40`. Mirrored across tour,
  factory, trade-route, outfitting, auto-outfit (`internal/application/common/reserve_floor.go`).
- **`maxTreasuryFractionPercent = 25`** — RULINGS #6 hard per-hull ceiling: a single hull/probe
  buy may never exceed 25% of live treasury (probebuy + frontier + autosizer per-buy).
- **`probebuy.GuardedProbeBuyer`** — the shared probe-buy guard stack (fleet cap, per-cycle
  spend window, cooldown, 25%-treasury, per-unit price ceiling). Used by the freshness sizer,
  frontier, and (via its own inline stack) the bootstrap.
- **Factory guard stack** — working-capital floor, pre-spend chain-margin + absorption guard
  (**fail-closed**), cross-container spend cap, input price ceiling, and the chain P&L
  kill-switch (**fail-OPEN** — a broken telemetry reader must never mass-kill live chains).
- **Bootstrap `reserve_margin = 0.50`** — the DATA-phase probe-buy capital pacer: buy a probe
  only if `price ≤ reserve_margin × remaining_treasury`, decrementing treasury each buy in the
  loop (both a guardrail and the ramp pacer).
- **Capacity reconciler capital gate** — tier-4 (capital) actions are PROPOSAL/APPROVAL-gated,
  never auto-executed. `ReserveFloorCredits = 50000`, `SurplusFraction = 0.25`,
  `PerDecisionCapPct = 25`, `ApprovalThresholdCredits = 0` (v1: ALL tier-4 gated). Enforced
  STRUCTURALLY by the CONVERGE capital backstop, not just governor correctness (§6.6).
- **`captain/DISABLED` kill switch** — present ⇒ the capacity reconciler idles every tick (zero
  phase invocations); also the supervisor's hard halt (`CLI-PRIMER.md` §1).

## 5. The lifecycle — how the coordinators compose

The bootstrap coordinator is the **master switch** that sequences a cold agent to a built jump
gate, then hands the mature economy to the steady-state brains and exits. The other
boot-standing coordinators run alongside it from hour 0; the GATE hand-off trio starts only
when the gate is built.

```
COLD START ─────────────────────────► GATE ──────────────► STEADY STATE
Bootstrap: DATA + INCOME (parallel)    Bootstrap: GATE       Bootstrap: COMPLETE → exits
  buy 3 probes, scout-all-markets        construction start    (re-observes COMPLETE on
  contract engine from hour 0            + gate workers         a restart, re-ensures the
  (RULINGS #1: contracts never wait)     (repurpose-first)      autosizer, exits)

Boot-standing (from hour 0, always):   GATE hand-off (at COMPLETE, once):
  Market-freshness sizer  ─┐             Fleet-autosizer    ─┐  the mature-economy brains
  Scout-post coordinator  ─┤ scouting    Siting             ─┤  the bootstrap launches then
  Construction coordinator ┤ + gate      Worker-rebalancer  ─┘  steps out of the way
  Capacity reconciler     ─┘ + topology

Captain-launched anytime: Frontier, Goods-factory, Contract-fleet, Trade-fleet,
  Auto-outfit, Shipyard-backfill, Gas, Stocker.
```

**The handoff contract.** At COMPLETE the bootstrap launches the autosizer + siting +
worker-rebalancer (each idempotent on its container type) and marks itself Done. A restart in
a built world re-derives COMPLETE, re-ensures the autosizer is up, and exits — so re-launching
the bootstrap every boot is a safe no-op. The construction executor the GATE phase adopts is
the boot-standing **Construction coordinator** (not the vestigial manufacturing coordinator);
adoption keys on that drain being RUNNING, not on the pipeline-status string.

---

## 6. The standing coordinators

Each entry: **what · logic (formulas/thresholds) · armed · hands off · source.**

### 6.1 Bootstrap coordinator — the cold-start phase machine

- **What.** The master switch. Observes the live world each tick, derives its phase, drives a
  cold agent DATA → INCOME → GATE → COMPLETE, then hands off and exits.
- **Logic.** Phase is DERIVED from the observation every tick, never stored
  (`derivePhase`, `run_bootstrap_reconcile.go:394`): `ConstructionComplete → COMPLETE`;
  `ConstructionStarted → GATE` (sticky, so repurposing haulers to construction can't regress
  it); `IncomePerHour ≥ income_bar → GATE`; else `coverage ≥ coverage_bar → INCOME` else
  `DATA`. DATA and INCOME run in PARALLEL at cold start (contracts from hour 0, RULINGS #1).
  Defaults (`run_bootstrap_coordinator.go:29-68`): tick 45s, `probe_target 3`,
  `coverage_bar 0.90`, `reserve_margin 0.50`, `hauler_target 4`, `income_bar 10000 $/hr`,
  `min_contract_earners 1`, `gate_worker_target 6`.
  - DATA: buy to `probe_target` in ONE capital-gated pass (each buy gated
    `price ≤ reserve_margin × remaining_treasury`); if the yard price is cold, position a hull
    at the shipyard so next tick reads it (never weakens the price guard). Assign every probe to
    scout-all-markets (a ONE-SHOT `SCOUT_FLEET_ASSIGNMENT` sweep).
  - INCOME: launch the contract-fleet coordinator; stage contract haulers up to
    `min(viable_hubs, hauler_target)`, one per tick, capital-gated; run the command frigate as
    pre-hauler sole earner (`batch-contract -1` loop).
  - GATE: `construction start` on the discovered jump-gate site; ensure the construction
    executor RUNNING; repurpose surplus contract haulers to the manufacturing fleet BEFORE
    buying gate workers (repurpose-first), size to `gate_worker_target`.
  - A count-sync bridge (`probeBuyBridge`) folds just-bought-but-unobserved probes into the
    count so a short tick never re-buys past target.
- **Armed.** Boot-standing (`daemon_boot_standing.go:49`). Auto-armed (dryRun=false);
  `[bootstrap] dry_run` forces observe-only.
- **Hands off.** At COMPLETE launches fleet-autosizer + siting + worker-rebalancer, sets Done,
  exits. Spawns the one-shot scout sweep + contract workers during DATA/INCOME.
- **Source.** `internal/application/bootstrap/commands/run_bootstrap_{coordinator,reconcile,income,gate}.go`;
  ports `internal/adapters/grpc/bootstrap_ports.go`, `bootstrap_ports_gate.go`,
  `container_ops_bootstrap.go`.

### 6.2 Market-freshness sizer — the coverage-freshness auto-buyer

- **What.** Keeps every SCANNED market fresh within an SLA by auto-sizing and auto-buying probe
  capacity per market-bearing system. DECLARES/resizes standing posts; DELEGATES all movement,
  manning, and partitioning to the scout-post coordinator. Moves and claims nothing (RULINGS #7).
- **Logic** (`run_market_freshness_sizer_coordinator.go`). Per system:
  `required_probes = ceil(markets × per_market_cycle / sla)` (:16), where `per_market_cycle` is
  MEASURED from live scan telemetry (`defaultSeedCycleSeconds = 180` until ≥3 samples), clamped
  by `defaultWorstCycleSeconds = 1800` and dampened toward the fleet median
  (`defaultCycleDampeningPercent = 50`). The empirical **value-weighted P90 market age**
  (`defaultTargetPercentile = 90`) is the closed-loop ground truth: a system whose P90 breaches
  its SLA has demand RAISED (`defaultBreachResponsePercent = 100`); a comfortably-fresh one
  RELEASES a probe below `defaultReleaseSlackPercent = 60`% of SLA, held for
  `defaultReleaseStableWindowSecs = 300` (hysteresis). Caps: `defaultSLASeconds = 3600`,
  `defaultMaxProbesPerSystem = 8`, `defaultSizerMaxProbeFleet = 40`,
  `defaultSizerMaxSpend = 500000`/`defaultSizerSpendWindow = 1h`, `defaultSizerCooldown = 1m`.
  Aggregate demand drives ONE guarded probe buy per cycle via `probebuy.GuardedProbeBuyer`;
  idle + in-flight + manning probes all count as supply (never over-buys). Tick 60s.
- **Armed.** Boot-standing (`daemon_boot_standing.go:40`). All-default launch; live-tunable via
  `tune --operation freshsizer`.
- **Hands off.** Writes desired-state post rows (Upsert keyed by system); the scout-post
  coordinator mans them. Its own only side-effect is the guarded probe buy.
- **Source.** `internal/application/scouting/commands/run_market_freshness_sizer_coordinator.go`;
  launch `internal/adapters/grpc/container_ops_market_freshness_sizer.go`.

### 6.3 Scout-post coordinator — the MANNING engine

- **What.** Mans the standing posts the freshness/frontier coordinators DECLARE: each tick it
  assigns a probe to every unmanned slot, partitions the system's MARKETPLACE-trait waypoints
  into N disjoint per-probe circuits, and drives the P90 rescans + idle-probe re-tasking.
- **Logic** (`run_scout_post_coordinator.go`). Claims an idle satellite under a `scout` ClaimShip
  OCCUPANCY (never `AssignFleet`'d — released on completion/restart; RULINGS #7 poach gate). A
  post with `--hulls N` VRP-partitions its markets across N probes (anchor model:
  `partitionAnchorFuelCapacity = 400`, `partitionAnchorEngineSpeed = 30`) → ~N× freshness at the
  same API rate. Re-partition is STABLE — fires only on a hull-count change or when the
  discovered market set drifts past `MarketDriftThreshold` (debounced by
  `BudgetChangeDebounceCycles`). Reposition relays carry failure cooldowns
  (`repositionRetryBackoff = 5m`, `defaultRepositionFailureCooldown`, reach bounded by
  `defaultMaxRepositionJumps` > the strict heavy cap `gategraph.MaxJumpPath = 5`). A manning
  watchdog (`defaultManningStallCycles`/`ManningStallCorrectionCap`) corrects a stalled post;
  coverage-first manning order (`CoverageSpreadDisabled` reverts to depth-first).
- **Armed.** Boot-standing (`daemon_boot_standing.go:76`). Double-launch guarded (one per
  player). Live-tunable via `tune --operation scoutpost`.
- **Hands off.** Spawns `SCOUT_REPOSITION` relay workers and drives per-probe tours; the posts
  it mans are declared by the freshness sizer / frontier coordinator.
- **Source.** `internal/application/scouting/commands/run_scout_post_coordinator.go`;
  posts adapter `internal/adapters/grpc/container_ops_scout_posts.go`.

> **Manning coupling.** The freshness sizer DECLARES posts; the scout-post coordinator MANS
> them — two separate coordinators, both must be running for coverage to hold and for the
> sizer's measured-cycle + P90 demand self-correction to leave its cold-start seed (a manned
> post is what generates the telemetry it corrects on). Interaction to watch: the bootstrap's
> one-shot `SCOUT_FLEET_ASSIGNMENT` sweep holds its probes, which can block the scout-post
> coordinator from claiming them.

### 6.4 Frontier-expansion coordinator — the coverage (discovery) auto-buyer

- **What.** The COVERAGE analogue of the freshness sizer: ranks uncovered frontier, DECLARES
  sweep-once posts for top systems, and buys probes under the money guards. Discovers NEW
  systems/markets; freshness keeps KNOWN ones fresh. Moves/claims nothing (RULINGS #7).
- **Logic** (`run_frontier_expansion_coordinator.go`). Ranking:
  `score = KnownMarkets×10 − Hops×5 (+15 virgin bonus)` (`WeightKnownMarket=10`,
  `WeightHopPenalty=5`, `WeightVirginBonus=15`); skips hop-0 anchors and scanned-marketless
  systems. Purchase gate (cheapest-first, all fail-closed): open post slots → effective-available
  (`availableCount − ReservedFreshnessFloor`) → fleet cap (`MaxProbeFleet = 40`) → cooldown
  (`PurchaseCooldown = 10m`, ledger-derived) → treasury/quote readable → per-unit `MaxProbePrice`
  ceiling (0 = off) → 25%-treasury rule → per-cycle spend cap (`MaxSpendPerCycle = 100000` over a
  1h window); `MaxBudget = 25% of treasury`. Depth-vs-breadth: `BreadthFractionPercent = 65`
  (⇒35% depth), `MaxDepthPathfinders = 3`, `MaxDepthHops = 8`. Declaration cap
  `MaxFrontierPostsInFlight = 5`; tick 60s.
- **Armed.** Captain-launched (`frontier start` → `container_ops_frontier_expansion.go`). NOT
  boot-standing, NOT in the GATE hand-off. Live-tunable via `tune --operation frontier`.
- **Hands off.** Writes sweep-once post rows (same seam as `scout posts add`); the scout-post
  coordinator relays a probe and mans them. No child containers.
- **Source.** `internal/application/expansion/commands/run_frontier_expansion_coordinator.go`;
  launch `internal/adapters/grpc/container_ops_frontier_expansion.go`; read-only status
  `container_ops_frontier_status.go`.

### 6.5 Shipyard-backfill coordinator — the shipyard blind-spot sweep

- **What.** Closes the charted-but-unscanned SHIPYARD blind spot the market-tour-only scan
  leaves: enumerates known-shipyard systems the depth frontier reached but no market tour
  toured, and declares deeper-first sweep-once posts the scout-post coordinator mans.
- **Logic.** Deeper-first sweep pacing bounded by `max_dispatches_per_cycle` [1,100] and
  `backfill_max_hops` [1,1000] (live-tunable via `tune --operation shipyardbackfill`). NOT a
  `CoordinatorOwnsIterations` type (loops forever inside one `Handle()`).
- **Armed.** Captain-launched (`workflow shipyard-backfill`). Not boot-standing.
- **Hands off.** Declares sweep-once posts → scout-post coordinator mans them.
- **Source.** `internal/application/scouting/commands/run_shipyard_backfill_coordinator.go`;
  launch `internal/adapters/grpc/container_ops_shipyard_backfill.go`.

### 6.6 Capacity reconciler — the contract-delivery topology brain

- **What.** Drives the contract-delivery machine's ACTUAL topology (warehouses, stockers,
  workers per hub) toward a computed DESIRED topology, capex-paced. One tick =
  `SENSE → PLAN → DIFF → GOVERN → CONVERGE`, stateless per tick; IDLES when converged (empty
  desired ⇒ zero actions, which is the cold-start state).
- **Logic** (domain `internal/domain/capacity`, contract in `capacity/CONTRACTS.md`). SENSE
  reads a `Signals` snapshot (demand, performance, topology, utilization, economics). PLAN
  emits `DesiredTopology` gated by an absorption ceiling for NEW coverage
  (`Economics.FleetPerHullCrHr`) and a universal floor for existing hubs
  (`AddThresholdPerHullCrHr`, cold-start default 500 cr/hr); counts clamped (workers 12,
  stockers 6). DIFF emits a cheapest-lever-first **escalation ladder**:
  tier-1 reuse idle hull → tier-2 rebalance/reposition → tier-3 buffer adjust → tier-4 capital.
  GOVERN sends tiers 1–3 to Approved and ALL tier-4 to Proposals. Calibration (governor.go):
  `ReserveFloorCredits 50000`, `SurplusFraction 0.25`, `PerDecisionCapPct 25`,
  `ROIPaybackHorizon 24h`, `TickInterval 300s`, `ApprovalThresholdCredits 0`.
  `CapexBudget.Deployable = f × (treasury − floor)`, `PerDecisionCap = Deployable × pct/100`.
  CONVERGE structural backstops (independent of governor correctness): the capital gate REFUSES
  an Approved tier-4 at/over threshold; verb→tier mapping is re-checked (mislabeling can't
  bypass the gate); `captain/DISABLED` idles every tick.
- **Armed.** Boot-standing (`daemon_boot_standing.go:65`). Auto-armed (dryRun=false);
  `[capacity_reconciler] dry_run` forces observe-only; a durable decommission needs
  config disable, not a bare STOP (boot re-launches it).
- **Hands off.** Autonomous tiers actuate via the `Actuator` port (reuse-idle-hull, rebalance,
  adjust-buffer) — a thin wrapper over existing primitives; it also drives the worker-rebalancer
  as a side-actuator. Capital actions become `Proposal`s on the approval channel — post-approval
  execution is the ONLY path to `ExecuteCapital`.
- **Source.** loop `internal/application/capacity/commands/run_capacity_reconciler_coordinator.go`;
  domain `internal/domain/capacity/*` (+ `CONTRACTS.md`); launch/recovery
  `internal/adapters/grpc/container_ops_capacity_reconciler.go`.

### 6.7 Construction coordinator — the gate-supply drain

- **What.** A standing, queue-driven supply drain: each tick it activates PENDING→READY
  `DELIVER_TO_CONSTRUCTION` tasks, polls READY tasks, claims idle in-system haulers, and sources
  + delivers on the shared `ProductionExecutor`. It is the executor the bootstrap GATE adoption
  check looks for.
- **Logic** (`run_construction_coordinator.go`). Tick 30s. NoWorkReasons
  `no_ready_construction_tasks` / `no_idle_hauler_in_system`. Per-supply-task timeout 30m
  (`constructionSupplyTaskDefaultTimeout`). Worker cap = max `MaxWorkers()` across EXECUTING
  pipelines, fallback 5, wired to `errgroup.SetLimit`. Fan-out `planDispatchLots`:
  `lots = ceil(remaining / hull-load)` bounded by demand and the idle pool
  (`defaultConstructionLotUnits = 40`). Two-phase supply: PHASE 1 deliver on-hand cargo
  unconditionally; PHASE 2 source the remainder via `ProduceGood` then deliver. An unsourceable
  remainder is DEFERRED not failed (`ParkForResupply` → PENDING for the SupplyMonitor). No P&L
  kill / input-pause / rest-signal (those are goods-factory-only).
- **Armed.** Boot-standing (`daemon_boot_standing.go:34` via
  `bootstrapManufacturingController.EnsureRunning`). Empty system ⇒ derive per-task. The
  construction PIPELINE (separate) is created by `StartConstructionPipeline`
  (`--depth 0..3`, `--min-supply`, per-good gate overrides).
- **Hands off.** No child containers — errgroup goroutines (one per lot) inside the drain, all
  work delegated to the shared `ProductionExecutor`.
- **Source.** `internal/application/manufacturing/commands/run_construction_coordinator.go`;
  launch `internal/adapters/grpc/container_ops_construction.go`.

### 6.8 Goods-factory coordinator — the production fleet

- **What.** Fleet coordinator for goods production: builds the supply-chain tree, splits it into
  parallel dependency levels, discovers idle haulers, and executes bottom-up (leaves→root) with
  a bounded worker fan-out, running a stack of pre-spend guards each pass.
- **Logic** (`run_factory_coordinator.go` + `_input_pause.go`, `_rest_signal.go`,
  `_chain_pnl_kill.go`). Guard stack order, each returning pre-spend with a `NoWorkReason`:
  input-pause recovery window → export-rest window → **input-poison anti-cycle** (pauses only on
  POSITIVE depletion — a required BUY input whose readable in-system EXPORT source is all
  SCARCE/LIMITED; recovery half-life 194m) → **export-ask-subsidy rest** (rest when own ask
  strictly exceeds the eligible cross-source median; window 90m) → **pre-spend chain-margin +
  absorption guard** (fail-CLOSED) → **chain P&L kill** (`NetPerHour < 30000` over a 6h
  window; fail-OPEN, RULINGS #4). Backoff `noWorkIterationDelay = 45s`, heartbeat re-log every
  10m. Live worker cap via `FactoryWorkerCapProvider` (`≤0` = unbounded). Ship discovery 30s.
- **Armed.** Captain-launched three ways, all through `StartGoodsFactory`
  (`container_ops_goods.go`): (1) captain `goods factory`/`goods produce`; (2) the **siting
  coordinator** launches standing profit chains (iterations=-1); (3) **gate source feeders**
  (`ensureGateSourceFeeders`, boot-time) launch standing InputsOnly feeders to keep gate
  export-factories fed so gate buys stay under the buy-ceiling. Under
  `unified_gate_fill` the feeders are deleted (feeding is inherent in the gate run's recursive
  tree).
- **Hands off.** No separate child containers — parallel goroutine workers within the
  coordinator, all on the shared `ProductionExecutor`. (The legacy `MANUFACTURING_COORDINATOR` /
  `PARALLEL_MANUFACTURING` / `MANUFACTURING_TASK_WORKER` container family is vestigial for
  construction — the drain in §6.7 replaced it.)
- **Source.** `internal/application/manufacturing/commands/run_factory_coordinator*.go`;
  shared engine `internal/application/manufacturing/services/production_executor.go`;
  launch `internal/adapters/grpc/container_ops_goods.go`.

> **Shared resolver + the goods honest-pause.** `supply_chain_resolver.go` builds the
> dependency tree. A recipe-good with NO in-system EXPORT factory (only IMPORT/EXCHANGE) is a
> not-yet-built supply chain (its exporter is built later at GATE), not a hard fault. The
> resolver returns a typed `ErrNoInSystemExporter` (`supply_chain_resolver.go:209`), which the
> factory coordinator catches via `errors.As` (`run_factory_coordinator.go:520-532`) → sets a
> `NoWorkReason` and honest-pauses (backoff, zero spend, no worker claimed), self-healing once
> the exporter exists. `ErrUnknownGood` / `ErrCircularDependency` remain hard errors.
> **Deploy caveat:** if a LIVE daemon CRASHES on this condition instead of honest-pausing, its
> binary is stale — verify the build stamp (`spacetraders version`) and redeploy the current
> build. Code is truth; the map follows it.

### 6.9 Siting coordinator — the factory-portfolio brain

- **What.** The standing factory "brain": a slow reconcile that discovers, places, and
  capacity-plans the factory-chain portfolio — launching/retiring goods-factory chains through
  the per-chain guard stack. Drives portfolio MEMBERSHIP, not per-chain pause.
- **Logic** (`run_siting_coordinator*.go`). Tick 900s; SCAN → SCORE → MAINTAIN → ACT → EMIT.
  Score: `ProjectedPL × (1 + WeightTourAlignment×tourSignal) − ProjectedPL×(WeightInputCompetition×overlap +
  WeightStaleness×ageFraction + WeightWorkerReachability×unreachFraction)` — all four weights
  default 1.0, all penalties fail-open. K sizing: `K = TopK` (override) else
  `floor(workers / WorkersPerChain)` (`WorkersPerChain = 3.5`). Concentration caps:
  `MaxChainsPerSystem = 3`, `MaxChainsPerInputMarket = 2` (a skipped candidate does not consume a
  K slot). ACT launches desired-not-running; retires running-not-desired only after
  `RetireHysteresisTicks = 2` consecutive out-of-K ticks (anti-thrash). EMIT sends scout-demand
  for stale candidates. LIVE by default (`siting_disabled` negation).
- **Armed.** GATE hand-off (`LaunchStandingCoordinators` → `SitingCoordinator`); also captain
  `workflow siting`. Not boot-standing. Live config from `[manufacturing.siting]`.
- **Hands off.** Launches/retires `goods_factory` child coordinators (each runs its own guard
  stack); emits scout-demand to the captain proposal channel. Siting itself never spends — the
  launch-guard veto is `ChainMarginGuard.Evaluate` (drops a candidate at zero cost).
- **Source.** `internal/application/manufacturing/commands/run_siting_coordinator{,_score,_act,_emit}.go`;
  launch `internal/adapters/grpc/container_ops_siting.go`.

### 6.10 Fleet-autosizer — the hull-pool sizer

- **What.** Sizes the hull pool to demand each tick and auto-buys the shortfall behind a
  fail-closed guard stack: **lights to factory-worker demand, heavies to unserved-trade
  demand** (plus opt-in warehouse/explorer/contract-delivery classes).
- **Logic** (`run_fleet_autosizer_coordinator.go` + `fleet_autosizer_{lights,heavies,guards,act}.go`).
  Tick 900s, ≤1 buy/tick. LIGHT demand: `ceil(DesiredChains × LightRotationSlots) + Vacancies`
  (`LightRotationSlots = 3.5`, `Vacancies` from the worker-rebalancer). HEAVY demand:
  `CurrentHeavies + UnservedLanes` (autosizer only grows). PER-CLASS ceilings: lights 35 /
  heavies 15 — there is deliberately **no fleet-wide total** (an absolute cap starves every class
  as the probe frontier grows). SEVEN guards (`EvaluateGuards`, every unreadable input BLOCKS),
  one question each: demand (`shortfall > 0` **and** the heavy anti-thrash streak — shortfall must
  persist 3 consecutive ticks) → class_ceiling → per_tick_cap → price (ask readable **and**
  `≤ MaxPriceClass` **and** `≤ cheapest + 50%`) → heavy_cap (owned heavy hulls, tag-independent;
  fail-closed on an unreadable census) → affordability (`price ≤ 25% × treasury` for
  heavies/explorer, NOT lights, **and** `treasury − ImmutableReserveFloor − heavyReserve ≥
  price + 200000`) → api_util (`< 85%`).
  The autosizer forms **no opinion on whether a hull will earn**: the `era_payback` and
  `realized_rate` income guards were deleted (the first could never read its own marginal-rate
  input and so refused every buy; the second refused on a declining aggregate rate while its own
  detail conceded the case did not apply), along with `explorer_exempt`, which existed only to
  cancel them for one class. Demand shortfall — for heavies, the unserved profitable-lane count —
  is the remaining economic input.
- **Armed.** GATE hand-off (`LaunchAutosizer` → `FleetAutosizerCoordinator`). DELIBERATELY not
  boot-standing (would fire prematurely during DATA/INCOME). Also captain
  `workflow fleet-autosizer`.
- **Hands off.** Calls `Purchaser.BuyAndDedicate` — buys ONE hull and dedicates it to its class
  fleet in one breath (dedicate-at-purchase). No child workers.
- **Source.** `internal/application/fleet/commands/run_fleet_autosizer_coordinator.go` (+ siblings);
  launch `internal/adapters/grpc/container_ops_fleet_autosizer.go`.

### 6.11 Worker-rebalancer — the cross-system hull ferry

- **What.** Ferries idle undedicated light-haulers cross-system to worker-starved factory
  systems. Entirely DB-derived, no purchases — it MOVES existing hulls.
- **Logic** (`run_worker_rebalancer_coordinator.go`). Tick 60s. A system is a vacancy iff ALL:
  ≥1 RUNNING factory container; oldest such container started ≥ `vacancy_min_minutes (15)` ago;
  zero idle in-system light-haulers; and demand>supply (`undedicated lights < factory count`,
  anti-hub). Source = nearest by gate-graph hops with `≥ source_min_idle (2)` idle hulls and
  `> sourceKeepMin (1)` (never strip a system below 1). Dispatch caps:
  `max_concurrent_ferries (2)`, per-vacancy `ferry_cooldown_secs (600)`, optional
  `max_lights_per_system`. Time knobs clamped at 24h to guard the nanoseconds-as-minutes
  overflow. All reads fail closed.
- **Armed.** GATE hand-off (`LaunchStandingCoordinators` → `WorkerRebalancerCoordinator`); also
  a side-actuator of the capacity reconciler. Not boot-standing. Off-switch
  `worker_rebalancer_disabled` (inert-when-disabled).
- **Hands off.** Spawns one-shot `WORKER_FERRY` children (atomic `ClaimShip(operation=worker_ferry)`
  occupancy claim, RULINGS #7; never `AssignFleet`'d). The ferry runs ONE iteration; the
  coordinator owns re-dispatch and reclaims ended ferries.
- **Source.** `internal/application/trading/commands/run_worker_rebalancer_coordinator.go`;
  launch `internal/adapters/grpc/container_ops_worker_rebalancer.go`.

### 6.12 Contract-fleet coordinator — the contract earner

- **What.** Runs ONE contract at a time (game constraint): discovers idle light haulers,
  negotiates/accepts, plans cheapest HOME-system sourcing, selects the closest capable hull, and
  spawns a single `CONTRACT_WORKFLOW` worker to source + deliver + fulfill.
- **Logic** (`run_fleet_coordinator.go` + `_depot_routing.go`). One-worker guard (refuses a
  second `CONTRACT_WORKFLOW`). Sourcing is HOME-system only (RULINGS #14, zero-jump worker),
  cheapest EXPORT/EXCHANGE. Sourcing-defer gate: `ProjectedNet = payout − EffectiveCost`, below
  `-(payout × 20%)` is flagged — but RULINGS #1 governs, so it is `Overridden` (source at the
  loss, log loudly), never skipped. In-flight cargo dedup (don't buy what a running worker
  already carries). Candidate ladder: idle lights (incl. command frigate) → cargo-baseline
  filter → EXCLUSIVE dedicated `contract` fleet if it has members → scope to contract-home
  system → drop unrelated cargo (each parked hull is then re-read ONCE at dispatch; a hold that
  cleared since the parking decision is re-admitted for THAT pass, not the next one) →
  spawn-governor eligibility → `SelectClosestShip` to the source.
  Command frigate hauls only as last resort (RULINGS #7, `ErrCommandFrigateNotLastResort`).
  Depot routing localizes buffered contract supply. Worker timeout 30m. External hauler sizing:
  bootstrap caps INCOME haulers at `hauler_target = 4`.
- **Armed.** Captain `contract start`; also the bootstrap INCOME phase launches it. NOT
  boot-standing.
- **Hands off.** Spawns `CONTRACT_WORKFLOW` (one at a time) and `CARGO_LIQUIDATION` (parked
  hull holding unrelated cargo, per-hull 5m cooldown); runs an in-process idle-arb dispatcher
  during idle time; homes dedicated hulls to standby stations between legs.
- **Source.** `internal/application/contract/commands/run_fleet_coordinator.go` (+
  `_depot_routing.go`); launch `internal/adapters/grpc/container_ops_contract.go`; sourcing gate
  `internal/application/contract/sourcing_optimizer.go`.
  - **`batch-contract`** (`workflow batch-contract --ship X [--loop]`) is NOT a coordinator: it
    is the single-hull `CONTRACT_WORKFLOW` worker with an iterations selector (`1` = one contract;
    `-1` = continuous single-hull loop). `DaemonServer.BatchContractWorkflow` →
    `container_ops_contract.go`. Used by the captain and by the bootstrap frigate sole-earner loop.
  - **Contract-hub coordinator** (`run_contract_hub_coordinator{,_score,_gate}.go`) is the
    contract analogue of siting — it scores WHERE contract haulers are homed (EWMA demand ×
    greedy max-coverage facility-location, `MaxHaulersPerHub = 3`). It is **built and unit-tested
    but UNWIRED**: no `ContainerType`, no launch method, no CLI verb. Nothing runs it yet.

### 6.13 Trade-fleet coordinator — the continuous-tour keeper

- **What.** A minimal coordinator over the `trade`-dedicated fleet: each tick it relaunches a
  fresh CONTINUOUS tour on every trade hull parked by an honest tour exit. Claims nothing itself
  (each tour claims its own hull).
- **Logic** (`run_trade_fleet_coordinator.go`). Tick 30s. Partition all `trade` hulls: assigned =
  running tour (leave); in-transit = skip; reserved-by-captain = skip; else idle relaunch
  candidate. `max_concurrent` (`≤0` = unlimited) holds surplus idle hulls. Per-hull relaunch
  cooldown measured from the persisted `ReleasedAt` (base 180s). Adaptive backoff: a productive
  exit (`≥90s`) resets to base; 1st fast-fail doubles the cooldown (capped 600s); 2nd escalates
  to reposition-reach at base cooldown; 3rd+ doubles again. Mass-park exemption (≥4 hulls
  released within 120s = a restart, not thin depth) leaves cooldown/streak untouched. Tour caps
  passed through: `min_margin`, `working_capital_reserve` (0 → tour's 40%-of-treasury default),
  `max_hops` (0→6), `max_spend` (0→25% of live treasury).
- **Armed.** Captain-launched only (`workflow trade-fleet-coordinator`). NOT boot-standing, NOT
  in the GATE hand-off. Off-switch `[trade_fleet]` enabled flag.
- **Hands off.** Spawns one `TOUR` (tour-run) container per idle hull via `LaunchTour` →
  `StartTourRun` (the exact path `workflow tour-run` uses; atomic `operation=trade` claim).
- **Source.** `internal/application/trading/commands/run_trade_fleet_coordinator.go`;
  launch `internal/adapters/grpc/container_ops_trade_fleet_coordinator.go`.

### 6.14 Auto-outfit coordinator — the module upgrader

- **What.** Measures per-hull cargo saturation from `tour_leg_telemetry`, catalogs buyable
  modules, ranks by marginal value, and installs the highest-value (hull, module) pair behind a
  fail-closed money guard — the module analogue of hull acquisition.
- **Logic** (`internal/application/autooutfit/coordinator.go`, scorer
  `internal/domain/outfitting/selection.go`). Tick 300s. `valuePerHour = CapacityGained ×
  saturation × throughput`; `cost = Price + InstallFee(1000) + ReachHops × HopCost(5000)`;
  `costPerUnit = cost / CapacityGained`. Reject if `legs < min_telemetry_samples (8)`;
  if `payback_horizon_hours > 0 && valuePerHour × horizon < cost` (absolute gate, default OFF);
  or if buying a new hull is cheaper per unit capacity. Inline money guard (each fails closed):
  `treasury − price − installFee < treasury_reserve (50000)` → park; `price×100 > 25% × treasury`
  → park; `price > price_ceiling (500000)` → park; `max_installs_per_tick (1)`.
- **Armed.** DEPLOY-INERT, captain-launched ONLY (`workflow auto-outfit`). One per player.
  Live-tunable via `tune --operation autooutfit`.
- **Hands off.** Calls `Outfitter.AcquireAndInstall` → the atomic guarded `InstallModuleCommand`
  (claims the hull, gates the fee on the working-capital floor, docks, installs). No child
  workers.
- **Source.** `internal/application/autooutfit/coordinator.go`; scorer
  `internal/domain/outfitting/selection.go`; launch `internal/adapters/grpc/container_ops_auto_outfit.go`.

### 6.15 Gas & stocker (captain-launched standing ops)

- **Gas coordinator** (`GAS_COORDINATOR`) — gas siphon extraction: `operations start --system
  --gas --siphons S1,S2 --storage ST1`. Spawns `GAS_SIPHON_WORKER` children against a storage
  hull. Captain-launched; not boot-standing.
  Source: `internal/application/gas/commands/run_gas_coordinator.go`, `container_ops_gas.go`.
- **Stocker coordinator** — one dedicated hull fills a home warehouse
  (`workflow stocker --standing --ship --warehouse-waypoint`). Deliberately NOT boot-standing
  (it is pinned to a specific hull + warehouse — no launch params to boot unconditionally);
  its "survives restart" comes from the persisted `standing` launch config +
  `RecoverRunningContainers`. Capital ceiling ~10% of treasury per buy.
  Source: `internal/application/trading/commands/run_stocker_coordinator.go`, `container_ops_stocker.go`.

---

## 7. Child workers (one-shot, coordinator-spawned)

Standing coordinators do the concrete work through short-lived children (one iteration, then
the parent re-dispatches). The parent owns re-dispatch, cooldowns, and reclaim-on-death.

| Child `ContainerType` | Parent | Role |
|---|---|---|
| `SCOUT_FLEET_ASSIGNMENT` | Bootstrap (DATA) | one-shot scout-all-markets sweep (holds its probes — see §6.3) |
| `SCOUT_REPOSITION` | Scout-post | relay a probe to a post / across systems |
| `WORKER_FERRY` | Worker-rebalancer | one cross-system idle-light ferry |
| `CARGO_LIQUIDATION` | Contract-fleet | clear unrelated cargo off a parked hull |
| `CONTRACT_WORKFLOW` | Contract-fleet / `batch-contract` | source+deliver+fulfill one contract |
| `TOUR` (tour-run) | Trade-fleet | fly one continuous arbitrage tour |
| `GAS_SIPHON_WORKER` | Gas coordinator | siphon gas to a storage hull |
| `goods_factory` (iterations=-1) | Siting / gate feeders | a standing production chain |

The one-shot ship ops (`NAVIGATE`, `DOCK`, `ORBIT`, `REFUEL`, `JETTISON`, `JUMP`, `ROUTE`,
`PURCHASE`, `OUTFITTING`) are `CoordinatorOwnsIterations` single-iteration containers behind the
`ship` verbs — used both directly and as building blocks inside the coordinators above.

## 8. Known interaction gaps

- **Manning coupling** (§6.3): the freshness sizer's DECLARE and the scout-post coordinator's
  MAN are two coordinators; both must be up for coverage to hold and for the sizer's demand
  self-correction to leave the cold-start seed. The bootstrap's one-shot sweep can hold probes
  the scout-post coordinator wants to claim.
- **Goods honest-pause deploy caveat** (§6.8): a live crash on the no-exporter condition means
  the running daemon is stale — the current build honest-pauses; redeploy.
- **Contract-hub coordinator is UNWIRED** (§6.12): engine + ports exist and are tested, but
  nothing launches it — contract-hauler homing is not yet automated by it.
