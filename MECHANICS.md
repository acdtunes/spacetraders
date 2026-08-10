# MECHANICS.md — the automation mechanics (gobot's coordinator brain)

Reference, not doctrine — READ this by area when a task touches the automation; it is not
primed every wake. It maps the standing coordinators (the bot's "brain") and how they
compose across the COLDSTART → GATE → EXPANSION lifecycle. Doctrine and process rules
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
- **`CoordinatorOwnsIterations` vs loop-forever.** The one-shot commands (`navigate_ship`,
  `dock_ship`, `orbit_ship`, `refuel_ship`, `jettison_cargo`, `route_ship`, `warp_ship`,
  `worker_ferry`, `cargo_liquidation`, `tour_run`, `trade_route`, `arb_run`, `stocker`,
  `longhaul_arb`, `scout_tour`, `scout_fleet_assignment`) are single-iteration
  `CoordinatorOwnsIterations` types — the handler owns the whole run internally and re-entering
  it would double-loop its budget. The standing brains
  (bootstrap, probe-sensing, construction, opportunity-relocator, fleet-growth,
  contract-scaler, contract-fleet, trade-fleet, long-haul-arb, auto-outfit, gas) are NOT that
  type — they loop inside one `Handle()` and own their own tick cadence, so the container-level
  budget (`-1`) is irrelevant to them.
- **The registry is the membership list.** `containerSpecList()`
  (`internal/adapters/grpc/command_factory_registry.go`) declares every buildable command type
  and the single `Duty` it owns; a type absent from it is marked FAILED at restart recovery.
  A deliberately removed type goes in `retiredCommandTypes`
  (`internal/adapters/grpc/daemon_server_recovery.go`) instead, which marks its persisted rows
  terminated cleanly rather than alarming as an unexplained loss. **Reading those two lists is
  the fastest way to answer "does this engine still exist?"**
- **One duty, one owner.** `container_duty_registry.go` names each standing responsibility
  (market freshness, trade-fleet hull sizing, contract-fleet hull sizing, hull outfitting,
  contract execution, trade-tour dispatch, long-haul arb dispatch, idle-hull repositioning,
  construction supply, gas supply, cold-start sequencing) and holds it to exactly ONE container
  type: two engines answering one question bid against each other over one treasury and one API
  budget, neither seeing the other's spend. `declaredDutyOverlaps` is the dated-exception escape
  hatch and is currently empty. Most of the retirements below were duty collisions.
- **Restart-resilience is law (RULINGS #2).** A daemon restart rebuilds every container from
  its persisted `config` JSON column via `RecoverRunningContainers` →
  `buildCommandForType(...)`. A RUNNING container is re-adopted, not double-launched. Anything
  a coordinator needs across a restart must live in persistence (the sensing ledger, ledger
  rows, ship dedication rows, the `config` column), never in memory — see the pattern-A/B/C knob
  taxonomy in `CLI-PRIMER.md` §3. In-memory state (backoff clocks, hysteresis streaks) is always
  re-derivable and fails safe on loss.
- **Failure → restart budget → honest-pause.** `MaxRestartAttempts = 3`. A coordinator that
  hits a transient dead-end must NOT return an error the runner burns its restart budget on;
  it sets a `NoWorkReason` and backs off (self-heals on a later tick). Terminalizing as a
  crash is reserved for genuine faults. A money guard refusing a spend is a CORRECT refusal and
  is graded idle, never a stall.

## 2. The reconciler shape (observe → derive → act)

Every standing coordinator is the SAME shape, stateless-per-tick:

1. **OBSERVE** the live world (ships, markets, ledger, the sensing ledger, containers) — a
   read-only snapshot.
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
| **Boot-standing** (auto, every daemon boot, idempotent) — exactly FOUR | Construction, Probe-sensing, Bootstrap, Opportunity-relocator | `ensureBootStandingCoordinators` at daemon `Start()` |
| **Bootstrap-launched** (started by the bootstrap coordinator from a derived phase, not by an operator) | Contract-scaler (every COLDSTART tick), Contract-fleet (COLDSTART), Trade-fleet (every EXPANSION tick), Fleet-growth (EXPANSION hand-off) | `ensureContractScalerEarly` / `ensureBatchContract` (`run_bootstrap_reconcile.go`, `run_bootstrap_income.go`); `ensureTradeFleetCoordinator` / `launchHandoff → LaunchStandingCoordinators` (`run_bootstrap_gate.go`) |
| **Captain-launched** (a CLI verb; re-adopts across restart via persisted config) | Contract-fleet, Trade-fleet, Fleet-growth, Long-haul-arb, Auto-outfit, Gas, Stocker, Warehouse | `contract start`, `workflow trade-fleet-coordinator`, `workflow fleet-growth`, `workflow long-haul-coordinator`, `workflow auto-outfit`, `operations start`, `workflow stocker --standing`, `workflow warehouse` |

Note the middle row: four coordinators are reachable from BOTH a bootstrap phase and an operator
verb, and the once-only guard lives inside the launch method both paths call — never in the
caller. The opportunity relocator has **no CLI verb and no enable flag at all**; boot-standing is
its only activation path.

**"Closed ≠ armed" (RULINGS #19).** A coordinator that exists in `ContainerType` but is absent
from `bootStandingCoordinatorTypes` and from a bootstrap-phase launch never runs at cold start —
it waits for a captain launch. Separately, most armable BEHAVIORS ship default-off
(byte-identical): a merged bead is not a live feature until the knob is armed (`tune ... 1` or
a `run.sh` export + restart). Arming is a separate, untracked step — keep the arming ledger,
audit dormant knobs at every deploy, re-verify arms after every restart (`CLI-PRIMER.md` §3.4).
Read "default-off" precisely: for a coordinator it usually means *no unconditional activation
path* (a bare deploy starts nothing), not *starts disabled*. The contract scaler is the clean
example — nothing boots it, but once the bootstrap launches it, it is live.

**Boot launches are idempotent.** Each `ensure*Standing` pre-checks `containerTypeRunning`; a
warm restart re-adopts the existing container instead of double-launching. Genesis guard: no
player row (`playerID <= 0`) skips all boot-standing launches until registration.

## 4. Money guards (fail closed, never weakened — RULINGS #4/#5/#6)

Every spend passes a layered, fail-closed guard stack. **Cannot read live balance or price →
do not spend.** No fix may relax a guard as a side effect. The hard floors are non-tunable
(RULINGS #5); everything above them is treasury-RELATIVE so a guard tuned for a poor treasury
never throttles a flush one.

**ONE base, derived tiers** (`internal/application/common/reserve_floor.go`). Every floor is a
const expressed relative to the same base, so the base and every tier move together and can never
drift apart. None is live-tunable.

| Const | Value | Who answers to it |
|---|---|---|
| `common.ImmutableReserveFloor` | `50000` | every NON-contract spender — trade, construction, arb, stocker, outfitting, probe/bootstrap capex — directly or through a tier below |
| `common.ContractSolvencyReserve` | `12000` | the ONLY floor a CONTRACT source-buy answers to; measured in FUEL (a hull's worst-case out-and-back), because a source-buy IS the earning path |
| `common.NonContractWorkingCapitalFloor` | `= base + 100000` (150k) | DEFAULT working-capital floor for construction gate-fill and the trading coordinators (tour / trade-route / arb / stocker). An explicitly configured launch reserve still wins; only the absent-config default resolves here |
| `common.ContractReserveCushion` | `= base + 100000` (150k) | contract OPERATING capital; gates the bootstrap first-hauler and gate-worker buys |
| `common.ContractScalerCushion` | `= base + 150000` (200k) | the contract scaler's CAPEX floor (a lumpy whole-hull draw needs more headroom than a per-cycle operating spend), and the long-haul money envelope's cushion fence |

- **`common.EffectiveReserveFloor(...)` is a flat shim** — it ignores every argument and always
  returns `ImmutableReserveFloor`. No caller invokes it; it exists so an unmerged lane's call
  sites keep resolving. Reference `ImmutableReserveFloor` directly instead.
- **`common.ReserveFloorGate`** — the one shared fail-closed treasury comparison, parametrized by
  `Floor`. `Holds(committed, nextLegSpend)` reports whether one more spend on top of what is
  already committed would breach the floor. INERT when no treasury reader is wired; **always
  holds when the live read failed**. Idle-arb builds it with the flat 50k; long-haul with the
  200k contract-scaler cushion, so long-haul capital never dips into contract working capital.
- **`maxTreasuryFractionPercent = 25`** (`internal/application/probebuy/guarded_probe_buyer.go`)
  — RULINGS #6 hard per-hull ceiling: a single hull/probe buy may never exceed 25% of live
  treasury. **Caveat:** `probebuy.GuardedProbeBuyer` itself has no production constructor left —
  `NewGuardedProbeBuyer` is called only from its own tests. The live probe buy is the sensing
  buy queue's floor below; treat the `probebuy` package as the surviving definition of the 25%
  rule, not as a running guard.
- **The sensing probe-buy floor** (`internal/domain/parkedsensing/floor.go`) —
  `floor = max(ImmutableReserveFloor, ImmutableReserveFloor + capex_reserve_credits +
  capital_multiplier_k_milli × measured_cargo_spend_per_hour / 1000)`. Tested against the
  **landed** cost (quote + ferry, `DefaultGateFeeCredits = 5900`), not the sticker. Every term is
  clamped non-negative and the result re-floored at the immutable base, so a malformed
  observation upstream can only ever make the guard STRICTER. An unreadable probe cap, treasury
  or cargo-spend aborts the drain buying nothing — an unknowable cargo outflow is not a zero one.
- **The heavy reserve is a named type, not an int** (`internal/application/common/heavy_reserve.go`).
  `HeavyReserveTarget` (the full ask at the yard the purchase path would TARGET — nearest
  reachable priced yard, not cheapest) is a DISTINCT type from the credits actually withheld, so
  adding an aspiration to a floor term is a compile error rather than a code review. The census it
  caps (`HeaviesOwned`) counts every owned heavy regardless of fleet tag: under-counting is the
  direction that would authorise re-buying a hull already owned.
- **Bootstrap's probe buy** gates on the flat additive cushion — `treasury − price ≥
  ImmutableReserveFloor` — against a treasury DECREMENTED on each buy in the loop, so a
  one-tick 0→target ramp still reflects real remaining credits. (The former proportional
  `reserve_margin` pacer is deleted.)
- **`captain/DISABLED` kill switch** — a sentinel FILE that is the captain supervisor's hard
  halt; also written by the universe-reset detector, and cleared by the Admiral alone
  (`CLI-PRIMER.md` §1). `captain gag` is its soft, dynamic complement and never touches it.

## 5. The lifecycle — how the coordinators compose

The bootstrap coordinator is the **master switch** that sequences a cold agent to a built jump
gate, hands the mature economy to the standing brains, and exits. There are exactly **three**
phases — `COLDSTART`, `GATE`, `EXPANSION` — and each is DERIVED from the live observation every
tick, never from a stored cursor.

```
COLDSTART ─────────────────────────► GATE ──────────────────► EXPANSION (terminal)
Bootstrap: two workstreams together   Bootstrap: build         Bootstrap: hand off + exit
  scanning: buy to 3 probes,            construction start       launch fleet-growth
    start the home market tour          adopt the executor       ensure trade-fleet
  income: contract-fleet coordinator,   buy up to 4 gate         re-tag idle gate hulls
    frigate loop, staged hauler buys      workers (the             to `trade`, then Done
    (RULINGS #1: contracts from hour 0)   contract fleet is
  + ensure the contract scaler            EXCLUSIVE, never
                                          repurposed)

Boot-standing (launched at EVERY daemon boot, all phases):
  Construction coordinator ─┐  the gate-supply drain (idle until a pipeline exists)
  Probe-sensing coordinator ┤  INERT until EXPANSION — phase-gated first, fail-closed
  Bootstrap coordinator    ─┤  the phase machine above
  Opportunity relocator    ─┘  moves trade hulls; a no-op with no trade fleet

Captain-launchable at any time: Contract-fleet, Trade-fleet, Fleet-growth, Long-haul-arb,
  Auto-outfit, Gas, Stocker, Warehouse.
```

**Phase entry, exactly** (`derivePhase`, `run_bootstrap_reconcile.go`). Construction and funding
signals ONLY: `ConstructionComplete → EXPANSION` (checked FIRST, terminal and sticky — a built
home gate reads complete every tick, including after a restart when the pipeline is long gone,
so no income dip or fleet churn can pull the arc back into a buying phase);
`ConstructionStarted → GATE` (sticky, so repurposing haulers can never regress it); else
`gateFunded(obs) → GATE`; else `COLDSTART`. **Neither scan coverage nor realized $/hr is
consulted** — the code states outright that coverage is not a gate, and one contract payout can
swing $/hr from net-negative to a false all-clear in a single tick.

**`gateFunded` is two conditions, and both must hold:**
- the FULL contract fleet (`len(Haulers) + ContractDepotHullCount`) has reached the contract
  scaler's live achievable target (`ContractScalerTarget`) — a HARD bar that FAILS CLOSED: a
  `0`/unread target (no scaler running) NEVER gates; and
- `Treasury − ImmutableReserveFloor ≥ gateSurplusFloor` (`500000` ⇒ treasury ≥ 550k) — the gate
  is EARNED from contract surplus, never raced on a thin treasury its own material spend then
  crashes. This is a phase-entry threshold, not a spend guard.

**The escape hatch** (`reDeriveUnderScaledGate`, unconditionally on). It overrides a
STICKY-LATCHED GATE back to COLDSTART only when `len(Haulers) < gateMinHaulers (2)` **and**
`ConstructionPercent < gateReentryConstructionPct (5.0)` hold for `gateReentryStreakTicks (3)`
CONSECUTIVE ticks; any tick that breaks the condition resets the streak. Deliberately asymmetric
— slow to leave GATE, immediate to resume it once the op re-scales — so the phase strongly
prefers GATE and only releases a genuinely starved latch. The streak is in-memory per container
and fails safe on restart (it re-accrues from 0; the re-derive is a pure phase relabel — no
spend, no assignment).

**The hand-off contract.** `actExpansion` launches the **fleet-growth coordinator and nothing
else** — growth running IS the hand-off having happened, which is what makes it a single safe
latch. It then ensures the trade-fleet coordinator on every EXPANSION tick, re-tags every idle
construction-dedicated hull to `trade`, and marks itself Done. An unconfirmed hand-off is held
and retried for 3 consecutive ticks, then exits anyway with a WARN — bootstrap is boot-standing
and every launch is idempotent, so the next boot retries. A restart in a built world re-derives
EXPANSION, re-ensures growth, and exits, so re-launching the bootstrap every boot is a safe
no-op. The construction executor the GATE phase adopts is the boot-standing **construction
coordinator**; adoption keys on that drain being RUNNING, not on a pipeline-status string.

---

## 6. The standing coordinators

Each entry: **what · logic (formulas/thresholds) · armed · hands off · source.** Paths are
relative to `gobot/`. This section covers every type in `containerSpecList()` that loops
forever; §6.12 lists what was retired, so a stale reference resolves to a retirement rather than
to nothing.

### 6.1 Bootstrap coordinator — the cold-start phase machine

- **What.** The master switch. Observes the live world each tick, derives its phase (§5), drives
  a cold agent to a built jump gate, hands the economy to fleet-growth, and exits.
- **Logic.** The cold-start SHAPE is fixed in code, not configured — "these are the shape
  itself, not per-run knobs" (`run_bootstrap_coordinator.go`): `probeTarget 3`,
  `haulerTarget 4`, `gateWorkerTarget 4`, ship types `SHIP_PROBE` / `SHIP_LIGHT_HAULER`. The
  ONLY launch-config values are `bootstrap_disabled` and the tick (default 45s).
  - **COLDSTART** runs two workstreams TOGETHER, not in sequence (contracts from hour 0,
    RULINGS #1). *Scanning* (`actData`): buy to `probeTarget` in ONE tick — a loop over a single
    price check, each iteration gating `remaining − price ≥ ImmutableReserveFloor` against a
    treasury decremented per buy; a cold/unreadable yard sends a hull there and buys nothing.
    Then start/re-cut the home market tour — the same `ScoutMarkets` path behind
    `workflow scout-markets`, producing `SCOUT` containers, not the retired probe-holding sweep.
    *Income* (`actIncome`): hard-skipped entirely when the player is contract-GRADUATED; else
    retire the command frigate from the `contract` tag, ensure the contract-fleet coordinator,
    run the frigate's continuous contract loop (gated on `probes ≥ 3 && haulers == 0`), and
    stage hull buys — **#1 contract, #2 TRADE, #3+ contract up to `haulerTarget`**, each gated
    `treasury − price ≥ contractWorkingCapitalFloor (150000)`. Every COLDSTART tick also ensures
    the contract scaler.
  - **GATE** (`actGate`): no gate site ⇒ blocker `no_gate_site`; not started ⇒ `construction
    start` and return for that tick; then adopt the construction executor (`EnsureRunning`, or
    `BounceForAdoption` if it is running unadopted); then size gate workers to
    `gateWorkerTarget`, one buy per tick, gated on the same 150k floor. **The contract fleet is
    exclusive and is never repurposed** — the gate buys its own workers, because
    buy→repurpose→buy churns against the scaler. Bought hulls are role-tagged in the fixed order
    D, F, F, D.
  - **EXPANSION** (`actExpansion`): the hand-off in §5.
  - A count-sync bridge (`probeBuyBridge`) folds just-bought-but-unobserved probes into the count
    so a short tick never re-buys past target.
- **Armed.** Boot-standing (`daemon_boot_standing.go`), live by default — an absent config boots
  LIVE, pinned by test. There is no `dry_run` config key: the `DryRun` struct field is never set
  by `resolveBootstrapConfig` and is reachable only from in-package tests.
- **Hands off.** Launches the contract scaler + contract-fleet coordinator in COLDSTART, starts
  the construction pipeline in GATE, launches fleet-growth + trade-fleet in EXPANSION. Spawns
  `CONTRACT_WORKFLOW`-bearing work indirectly through those coordinators, and `SCOUT` containers
  through the home market tour.
- **Knobs.** `config.yaml [bootstrap]`: `bootstrap_disabled`, `tick_seconds` (injected as the
  container key `bootstrap_tick_secs`). Live: `tune --operation bootstrap tick_secs` — the only
  tunable key it has.
- **Source.** `internal/application/bootstrap/commands/run_bootstrap_{coordinator,reconcile,income,gate}.go`,
  `bootstrap_types.go`; ports `internal/adapters/grpc/bootstrap_ports.go`,
  `bootstrap_ports_gate.go`, `bootstrap_home_tour.go`, `container_ops_bootstrap.go`.

### 6.2 Probe-sensing coordinator — the fleet's one sensing engine

- **What.** The successor to the retired market-freshness sizer + frontier-expansion pair, and
  the sole owner of the market-freshness duty. Its model is **PARKED** probes: a hull is bought
  for a waypoint, flown there once, and then stands still forever, scanning its own market on a
  rotation the coordinator paces against whatever API headroom the rest of the fleet leaves.
  **Nothing tours** — steady-state sensing costs navigation nothing, and the only recurring spend
  is the scans themselves.
- **Logic.** It owns no algorithm of its own; it is a composition root that orders five engines
  in `internal/application/parkedsensing` within a tick and reports what they did — *screen* (is
  this system worth watching, and which waypoints in it), *buy queue* (can we afford a hull for
  that placement, and buy it), *placements* (fly the bought hulls out and stand them down on
  station), *expansion* (push the frontier outward, run charting seeds), *scanner* (the single
  fleet-wide pacer that spends the scan budget). Stage failures are COLLECTED, not fatal.
  Defaults (`probe_sensing_config.go`): tick **30s**, `probe_cap 3000`, `expansion_enabled 1`
  (1=on/2=off, not 0/1, because `tune <key> 0` means revert-to-default), `target_util_pct 92`,
  `min_scan_rate_milli 100` (0.1 req/s), `value_clamp_r 4`, `inflight_cap 3`,
  `capital_multiplier_k_milli 2000` (2h), `capex_reserve_credits 100000`,
  `quartermaster_cadence_secs 3600`, `surge_inflight_cap 8`, `wait_low_ms 50` /
  `wait_high_ms 1000`. The buy floor is §4's sensing formula, tested on LANDED cost.
- **It is INERT before EXPANSION.** The phase gate is checked FIRST and fails closed on an
  unreadable phase: a pre-EXPANSION tick spends nothing and moves nothing — no ledger read, no
  cutover, no buy — except a free shipyard-catalogue sweep. Past the EXPANSION edge it actively
  STOPS every running `scout_tour` container and force-releases its hull, because past the gate
  there is no legitimate market tour of any provenance.
- **Armed.** Boot-standing, all-default launch. Idempotent (`ensureProbeSensingStanding`
  pre-checks `containerTypeRunning`).
- **Hands off.** Spawns **no child containers**. It does move and claim hulls itself: placements
  fly probes to station, and a purchasing hull is held under an exclusive single-writer claim for
  the length of the buy.
- **Knobs.** `tune --operation sensing` — the 14 keys above. `value_clamp_r`, `inflight_cap` and
  `pressure_half_life_secs` bind when the scanner is BUILT, so they persist immediately but the
  running loop keeps its launch value until a restart; the write confirmation says which case
  applied. The goods whitelist is a string and therefore not tunable — it lives in
  `config.yaml [sensing]`. The retired touring core's keys (`probe_budget`,
  `purchase_cooldown_secs`, `freshness_target_secs`, …) are retained as struct fields for restart
  tolerance and are **read and ignored**; they are absent from the tune registry, so tuning one
  fails as an unknown key.
- **Operator verb.** `spacetraders sensing rescreen` re-opens every system verdict so the sweep
  re-judges under the current whitelist. Judgement-only: it cannot reach slot state, ownership,
  scan stamps or seed fields.
- **Durable state.** `sensing_systems` (one screening verdict + charting seed per system) and
  `sensing_slots` (one placement per (waypoint, slot kind), WANTED→QUEUED→BOUGHT→IN_TRANSIT→
  PARKED). Everything is re-derived from those two tables plus `ships` each tick; the only
  in-memory state is the scan rotation and the emergency brake, both re-derived within a few
  ticks.
- **Stall grading.** A money guard doing its job is a correct refusal, never a stall: probe cap
  and buy floor grade the tick IDLE, not blocked.
- **Source.** `internal/application/scouting/commands/run_probe_sensing_coordinator.go` (+
  `probe_sensing_{config,heartbeat,pacer,stall,surge,tunables}.go`, `legacy_tour_sweep.go`);
  engines `internal/application/parkedsensing/*`; domain `internal/domain/parkedsensing/*`;
  launch `internal/adapters/grpc/container_ops_probe_sensing.go`.

### 6.3 Construction coordinator — the gate-supply drain

- **What.** A standing, queue-driven supply drain: each tick it activates PENDING→READY
  `DELIVER_TO_CONSTRUCTION` tasks, polls READY tasks, claims idle in-system haulers, and sources
  + delivers on the shared `ProductionExecutor`. It is the executor the bootstrap GATE adoption
  check looks for, and — since the factory-ops retirement — the sole consumer of the
  `manufacturing` dedication.
- **Logic** (`run_construction_coordinator.go` + siblings). Tick **30s**. Three NoWorkReasons:
  `no_ready_construction_tasks`, `no_idle_hauler_in_system`, `supply_workers_saturated`. Worker
  cap = the LARGEST `MaxWorkers` among the distinct EXECUTING pipelines backing this tick's ready
  tasks, re-read every tick, fallback **5**, never below 1; it bounds workers IN FLIGHT, so
  `slots = cap − inFlight` and `slots ≤ 0` yields `supply_workers_saturated`. Per-task timeout is
  DEPTH-SCALED: `30m × depth`, clamped to a 2h ceiling, where depth is the static recipe-graph
  depth bounded by the pipeline's chain depth (default 3) — so the fleet default is 90m;
  registration expiry adds a 10m reap grace. Fan-out: `desiredLots = ceil(lotDemand / lotUnits)`
  where `lotUnits` is the first idle hull's cargo capacity (fallback
  `defaultConstructionLotUnits = 40`), globally capped by
  `min(idle hulls, total lot demand, free worker slots)`; per-lot fill caps slice the budget so
  concurrent same-material lots cannot over-buy.
- **Sourcing routes on the hull's gate role**, not on one path: `RoleFactory` → the feed leg (buy
  a hull-load of the neediest input at its terminal factory and feed it in; one step per leg is
  the entire spend bound); `RoleDelivery` → the delivery leg (buy at the terminal factory for the
  construction site); an untagged or legacy `manufacturing` hull → deliver on-hand cargo →
  withdraw from an in-system warehouse at zero cost → and only then `ProduceGood` the remainder
  through the scarcity-gated tree. An unsourceable remainder is DEFERRED, not failed.
- **Role reallocation and watchdogs.** Idle unheld gate hulls move between delivery and factory
  roles at most once per tick, damped by a 10m role dwell, around a D/F/F/D baseline mix. A
  read-only stall watch reports an ACTIVE pipeline whose unmet material has received zero
  delivered units for 60m — it reports and acts on nothing. A per-tick site read raise-only
  corrects the pipeline's delivered counters before any bill is consulted.
- **Armed.** Boot-standing (via `bootstrapManufacturingController.EnsureRunning`). The
  construction PIPELINE is a separate object created by `construction start` — flags are
  `--system`, `--min-supply`, `--good-override`, `--overrides`. **There is no `--depth` and no
  `--max-workers`**; both are fixed consts now (chain depth 3, worker cap deferred to the domain
  default 5 or the live-tuned cap).
- **Hands off.** No child containers — errgroup goroutines (one per lot) inside the drain, all
  work delegated to the shared `ProductionExecutor`.
- **Live knobs.** `construction workers <site> --count N` and `construction override --site
  --good [--min-supply|--price-ceiling-mult|--buy-floor|--resume-floor|--clear]`. `--strategy` is
  launch-time only (via `--good-override`); `smart` is the sole runtime path.
- **Source.** `internal/application/manufacturing/commands/run_construction_coordinator*.go`
  (dispatch, supply, gate_feed, gate_delivery, gate_realloc, budget, commitment, inventory, site,
  stall_watch, tasks, workers); shared engine
  `internal/application/manufacturing/services/production_executor.go`; launch
  `internal/adapters/grpc/container_ops_construction.go`.

### 6.4 Opportunity relocator — the upside-chasing hull mover

- **What.** A standing reconciler that ranks every (trade hull, reachable region) pair by
  relocation NPV and moves the best-valued hulls onto better-earning ground. It is the rate-floor
  rescue's trigger INVERTED: that one rescues an under-earner, this chases upside a
  perfectly-profitable hull would otherwise never leave for. Decisions are FLEET-WIDE by design —
  top-NPV ranking across all pairs, under one fleet-wide concurrency cap.
- **Logic** (`run_opportunity_relocator*.go`). Tick **120s**. `uplift = projected − current`;
  `payback = (current × travelHours + riskMargin) / uplift`;
  `window = min(remainingEra − travel, horizon)` floored at 0;
  `NPV = uplift × window − current × travelHours − riskMargin`. Gates in order: unreadable inputs
  refuse; the uplift bar (`defaultRelocatorUpliftBarPct 150`, clamped UP only — the floor is 150)
  must be strictly beaten; the endgame guard refuses when `remaining < 2 × travel + payback`; and
  `NPV ≤ defaultRelocatorNPVThresholdCredits (500000)` refuses. Other defaults:
  `max_concurrent_relocations 2`, per-hull cooldown 90m, horizon 24h, risk-margin tour minutes 60,
  region hop radius 2, rate window 240m (EWMA smoothing 0.5). Shared reposition limits apply:
  per-system anti-herd cap 5, jump bound 12. One relocation per hull per tick.
- **It spends nothing.** Its actuator port is documented and implemented as pure movement — it
  never buys, sells, quotes or reads a money guard, and it explicitly REFUSES the tour's deadhead
  look-back manifest because that would be a buy. The one credit outflow underneath is fuel,
  bought by the shared travel primitive, not by a relocator decision.
- **Protected hulls.** The command frigate and pinned hulls ARE observed (they are carried as
  fields) and are filtered at SCORING by `hullProtected`, then re-checked at COMMIT through the
  same predicate before anything is persisted. Two narrow exceptions: an `Offered` hull lifts the
  `mid_tour` rule only, never the frigate/pinned facts; and an in-flight resume skips the
  ownership gate so a multi-leg move is not abandoned mid-way.
- **Armed.** Boot-standing, and **that is its only activation path** — no CLI verb, no gRPC
  service method, no enable flag. Off-switch: the shared `reposition_disabled` container key,
  enforced as the first statement of `Reconcile`, which halts all three relocation triggers at
  once (margins-death, rate-floor, this).
- **Hands off.** No child containers; movement only.
- **Knobs.** None — it is absent from the tune registry, so tuning it by container id reports
  that it has no live-tunable knobs.
- **Source.** `internal/application/trading/commands/run_opportunity_relocator{,_commit,_config,_ports,_scoring,_stall}.go`;
  NPV domain `internal/domain/trading/relocation_npv.go`; launch
  `internal/adapters/grpc/container_ops_opportunity_relocator.go`.

### 6.5 Fleet-growth coordinator — the fleet's only heavy buyer

- **What.** Owns heavy/trade hull capacity. Each tick it derives the **wave** from durable facts
  and, on a HEAVY wave only, runs ONE heavy candidate through the fail-closed purchase-guard
  stack. It buys exactly one class (`HullClassHeavy`, default `SHIP_HEAVY_FREIGHTER`, tagged
  `trade`), with a substitution walk over the trade-hull preference order when the preferred type
  cannot be priced. It never buys contract capacity — that is §6.6's.
- **The wave** (`internal/application/common/growth_wave.go`) is the ONE definition growth and
  the sensing drain both read, so the pair can be compared for disagreement. `WaveHeavy` pauses
  probe buying so the treasury can climb toward a heavy's ask; `WaveProbe` lets probe buying run
  at full speed and authorises nothing. `DeriveWave`'s clauses, in order, every "cannot" landing
  on PROBE: growth off → lanes unreadable → no unserved lanes → saturation unreadable →
  saturated → capacity high-water unreadable → target unreachable at that high-water → else
  HEAVY. Reachability is judged on the treasury PEAK over a cycle, never a point read.
- **Logic.** Tick **133s** — derived, not chosen: 4 yard reads per tick against a 25% share of
  the 0.12 req/s yard budget. Anti-thrash is an anchor TIMESTAMP (`defaultGrowthShortfallDwell`
  900s), advanced on demand alone outside the wave gate; an unreadable demand clears it. One
  purchase per tick. A zero-effect alarm fires, edge-triggered, after 4 consecutive
  unmet-demand-no-purchase ticks. `EvaluateGuards` runs in this literal order and reports the
  FIRST failure: **demand** (shortfall > 0, waived past the dwell when the shortfall exceeds the
  whole pool) → **per_tick_cap** (1) → **price** (ask readable — unreadable fails CLOSED — and
  within `MaxPriceClass` if set, and ≤ cheapest + 50%) → **heavy_cap** (owned heavies,
  tag-independent, default 5; unreadable census fails CLOSED) → **affordability** (unreadable
  treasury fails CLOSED and is checked first; then
  `treasury − 50000 − heavyReserve − workingCapital ≥ price + 200000`) → **api_util** (< 85%,
  unreadable fails CLOSED). Ahead of the guards, three fail-closed rungs: unreadable demand, zero
  shortfall, and unreadable working capital.
- **Two deliberate exceptions to "everything fails closed."** The heavy RESERVATION fails OPEN —
  every "cannot" answer reserves zero, releasing treasury rather than freezing it. And the master
  switch fails OPEN: a nil live-config reader or a snapshot error leaves growth ON at its launch
  values. Growth OFF short-circuits the whole tick before any port read and publishes PROBE.
- **The 25%-treasury ceiling is NOT applied by default.** `TreasuryPctPerPurchase` has no default
  and deliberately no zero-fallback, so at defaults only the absolute floor term binds. Several
  code comments still describe a "25%-treasury rule" here; the guard is present but unset.
- **Armed.** Two launch sites, both funnelling through `FleetGrowthCoordinator` where the
  once-only guard lives: the bootstrap EXPANSION hand-off, and `workflow fleet-growth`. NOT
  boot-standing. Ships ARMED once launched (`growth_enabled` defaults to 1).
- **Hands off.** No child containers. The heavy-yard pricing errand dispatches a SHIP, not a
  container, bounded to one in flight fleet-wide and drawn only from the parked-sensing pool; it
  runs on PROBE waves too, because it spends nothing and makes a later tick's price readable.
- **Knobs.** `tune --operation growth` — exactly three, all live: `growth_enabled` (1=on/2=off),
  `heavy_cap` [1,50], `growth_runway_milli_hours` [1,10000]. Every minimum is 1 because
  `tune <key> 0` is the revert-to-default verb fleet-wide, so 0 is not an expressible value.
- **Source.** `internal/application/fleet/commands/run_fleet_growth_{coordinator,reconcile}.go`,
  `fleet_growth_{act,tune,stall}.go`, `purchase_guards.go`, `heavy_pricing_errand.go`; wave
  `internal/application/common/growth_wave.go`; launch
  `internal/adapters/grpc/container_ops_fleet_growth.go`.

### 6.6 Contract-scaler coordinator — the contract fleet's capacity owner

- **What.** A standing, permanent coordinator that ramps a FIXED, EXCLUSIVE contract fleet to a
  live-tunable ceiling behind ONE money guard. It resolves this era's waypoint roles ONCE at arm
  (a lookup, not a solve), builds the fixed buy sequence, and each tick tops the fleet up to
  `min(plan, ceiling)` while the treasury can spare the cushion. It drives the kept buy primitive
  rather than rebuilding a buyer. It buys `SHIP_LIGHT_HAULER` only.
- **Logic** (`run_contract_scaler.go`). Tick **900s** — the ramp is strategic and most ticks are
  a no-op at the ceiling. Per tick: memoized plan → live ceiling → budgeted per-role fill in the
  order **delivery → warehouse → stocker** → re-role surplus delivery → depot fill. Plan sizes:
  `MaxDeliveryHulls 6` (the p-median knee), `WarehouseUnits 8`, `StockerUnits 1`. Ceiling
  `contract_fleet_max_hulls` defaults to **3** — the same number bootstrap's GATE-entry bar reads
  as `ContractScalerTarget`. **There is no per-tick purchase cap**: it scales as fast as treasury
  and API allow.
- **One money guard, and only one.** Per hull, in order: a zero-spend REUSE tier first (free, and
  deliberately NOT cushion-gated, so it may proceed below the cushion) → price readable (fails
  CLOSED) → treasury readable (fails CLOSED) → **`treasury − price < ContractScalerCushion
  (200000)` ⇒ stop**. There is no reserve floor beyond the cushion, no treasury-fraction ceiling,
  no price ceiling. The cushion is a compile-time const with no config/tune seam. The ceiling by
  contrast fails OPEN to the default 3 — it is a throttle, not a money guard.
- **Armed.** No CLI verb at all. `ensureContractScalerEarly` runs on **every COLDSTART tick** and
  is unconditional within that phase — the gate is the derived phase itself, plus a wired
  launcher; idempotency lives in the launcher's `containerTypeRunning` check. Deliberately not
  run in GATE/EXPANSION. It is "default-off" in the sense that a bare deploy starts nothing —
  not in the sense that it starts disabled.
- **Hands off.** Spawns depot elements indirectly through the grower: `GrowWarehouse` →
  `WAREHOUSE`, `GrowStocker` → a standing continuous `STOCKER`. Both are gated on
  home-reachability; a non-viable hull is evicted rather than launched. At the default ceiling of
  3 the budget never reaches a warehouse index, so **zero depot calls occur by default**.
- **Knobs.** `tune --operation contractscaler contract_fleet_max_hulls` [0,16] — its only lever.
- **Source.** `internal/application/contractscaler/commands/run_contract_scaler.go`,
  `contract_scaler_tune.go`; plan `internal/domain/contractscaler/plan.go`; ports/launch
  `internal/adapters/grpc/contract_scaler_{ports,depot_ports,home_ports,reclaim_ports}.go`,
  `container_ops_contract_scaler.go`.

### 6.7 Contract-fleet coordinator — the contract earner

- **What.** Runs ONE contract at a time (game constraint): discovers idle light haulers,
  negotiates/accepts, plans cheapest HOME-system sourcing, selects the closest capable hull, and
  spawns a single `CONTRACT_WORKFLOW` worker to source + deliver + fulfill.
- **Logic** (`run_fleet_coordinator.go` + siblings). One-worker guard (refuses a second
  `CONTRACT_WORKFLOW`). Sourcing is HOME-system only (RULINGS #14, zero-jump worker), cheapest
  EXPORT/EXCHANGE. Sourcing-defer gate: `ProjectedNet = payout − EffectiveCost`, below
  `−(payout × 20%)` is flagged — but RULINGS #1 governs, so it is `Overridden` (source at the
  loss, log loudly), never skipped. In-flight cargo dedup (don't buy what a running worker
  already carries). Candidate ladder: idle lights (incl. command frigate) → cargo-baseline filter
  → EXCLUSIVE dedicated `contract` fleet if it has members → scope to contract-home system → drop
  unrelated cargo (each parked hull is then re-read ONCE at dispatch; a hold that cleared since
  the parking decision is re-admitted for THAT pass, not the next one) → spawn-governor
  eligibility → `SelectClosestShip` to the source. Command frigate hauls only as last resort
  (RULINGS #7, `ErrCommandFrigateNotLastResort`). Depot routing localizes buffered contract
  supply. Worker timeout 30m. A contract source-buy is EXEMPT from the immutable reserve floor
  and answers to `common.ContractSolvencyReserve` (12000, measured in fuel) alone.
- **Armed.** Captain `contract start`; the bootstrap COLDSTART income workstream also ensures it.
  NOT boot-standing. `contract graduate` retires a player off the contract funding floor
  durably (era-scoped), which hard-skips bootstrap's whole income workstream.
- **Hands off.** Spawns `CONTRACT_WORKFLOW` (one at a time) and `CARGO_LIQUIDATION` (parked hull
  holding unrelated cargo, per-hull 5m cooldown); runs an in-process idle-arb dispatcher during
  idle time; homes dedicated hulls to standby stations between legs.
- **Knobs.** `tune --operation contract min_home_contract_workers` [0,200] — its only key.
  `config.yaml [contract]` carries the idle-arb knobs (`idle_arb.max_spend`, `leash_radius`).
- **Source.** `internal/application/contract/commands/run_fleet_coordinator*.go`; launch
  `internal/adapters/grpc/container_ops_contract.go`; sourcing gate
  `internal/application/contract/sourcing_optimizer.go`.
  - **`batch-contract`** (`workflow batch-contract --ship X [--loop]`) is NOT a coordinator: it
    is the single-hull `CONTRACT_WORKFLOW` worker with an iterations selector (`1` = one
    contract; `-1` = continuous single-hull loop). Used by the captain and by the bootstrap
    frigate sole-earner loop.
  - **Contract-hub coordinator** (`run_contract_hub_coordinator{,_score,_gate}.go`) is the
    contract analogue of a placement brain — it scores WHERE contract haulers are homed (EWMA
    demand × greedy max-coverage facility-location, `MaxHaulersPerHub = 3`). It is **built and
    unit-tested but UNWIRED**: no `ContainerType`, no launch method, no CLI verb.

### 6.8 Trade-fleet coordinator — the continuous-tour keeper

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
  passed through: `min_margin`, `working_capital_reserve`, `max_hops` (0→6), `max_spend`
  (0→25% of live treasury).
- **Armed.** `workflow trade-fleet-coordinator`, and ensured by the bootstrap on every EXPANSION
  tick. NOT boot-standing. Off-switch `[trade_fleet]` enabled flag.
- **Hands off.** Spawns one `TOUR_RUN` container per idle hull via `LaunchTour` → `StartTourRun`
  (the exact path `workflow tour-run` uses; atomic `operation=trade` claim).
- **Knobs.** `tune --operation tour market_data_max_age_minutes` — the trade path's
  market-freshness FLOOR (see `CLI-PRIMER.md` §3.2 for why it is a floor and not a cap). The
  alias is `tour`, not `tradefleet`, because the knob governs what a TOUR will still price off.
- **Source.** `internal/application/trading/commands/run_trade_fleet_coordinator*.go`; launch
  `internal/adapters/grpc/container_ops_trade_fleet_coordinator.go`.

### 6.9 Long-haul arb coordinator — the out-of-horizon lane capturer

- **What.** The fleet-manager half of the long-haul arb op, modelled on the trade-fleet
  coordinator so the liveness watchdog and stateless recovery are SHARED rather than forked. Each
  pass is a cheap fleet read that launches a per-hull worker on every idle
  `dedicated_fleet="long-haul"` hull. It claims nothing itself.
- **Logic.** Tick 30s. **Concurrency is UNCAPPED by Admiral order** — a worker for every idle
  tagged hull each tick; no total-exposure ceiling is derived. Worker episodes are
  discover → select+size → reposition to source → OUT leg → opportunistic backhaul or deadhead;
  a laden hull resumes SELLING on restart and never re-buys on top of cargo. Ranking key is
  realized credits/hour: `net = realizedSpread × q − fuel`, `perHour = net / (tripSeconds/3600)`,
  keeping only positive `q` and `net`. Reposition reach 25 jumps; market data max age 1h; coarse
  spread pre-filter 100 cr/unit.
- **Money guards** (worker side; the coordinator itself never spends): per-haul cap
  `1000000` → the reserve fence `common.ReserveFloorGate{Floor: ContractScalerCushion (200000)}`
  → unreadable treasury refuses every buy and zeroes the spend ceiling → spend ceiling
  `min(perHaulCap, treasury − 200000)` floored at 0 → absorption headroom, fail-closed to 0 on a
  ledger error → routability before spend. `defaultLongHaulTotalExposureCap (2000000)` is
  threaded for parity but is **never enforced as a ceiling**.
- **Armed.** `workflow long-haul-coordinator`; idempotent (returns the live coordinator's id
  rather than spawning a rival). NOT boot-standing. **There is no feature flag**: the engine is
  naturally inert until the operator tags a hull, so `fleet add --operation long-haul --ship X`
  and untagging ARE the arm/disarm seam.
- **Hands off.** Spawns one `LONGHAUL_ARB` worker per idle tagged hull, claimed under
  `operation = "long-haul"`; the container row is added BEFORE the claim (FK order) and an
  orphan row is terminalized FAILED on claim refusal. A watchdog relaunches hung workers after
  720s and needs BOTH the liveness reader and the stopper wired (fail-closed).
- **Knobs.** **None live.** It is absent from the tune registry despite several code comments
  calling its knobs "live-tunable"; the only retune path is the persisted launch-config keys
  (`longhaul_tick_secs`, `longhaul_per_haul_cap`, `longhaul_total_exposure_cap`,
  `longhaul_watchdog_stall_secs`) plus a restart.
- **Source.** `internal/application/trading/commands/run_longhaul_arb_{coordinator,worker,envelope,discovery,pricing,selection,wiring}.go`;
  launch `internal/adapters/grpc/container_ops_longhaul.go`.

### 6.10 Auto-outfit coordinator — the module upgrader

- **What.** Measures per-hull cargo saturation from `tour_leg_telemetry`, catalogs buyable
  modules, ranks by marginal value, and installs the highest-value (hull, module) pair behind a
  fail-closed money guard — the module analogue of hull acquisition.
- **Logic** (`internal/application/autooutfit/coordinator.go`, scorer
  `internal/domain/outfitting/selection.go`). Tick 300s. `valuePerHour = CapacityGained ×
  saturation × throughput`; `cost = Price + InstallFee(1000) + ReachHops × HopCost(5000)`;
  `costPerUnit = cost / CapacityGained`. Reject if `legs < min_telemetry_samples (8)`; if
  `payback_horizon_hours > 0 && valuePerHour × horizon < cost` (absolute gate, default OFF); or if
  buying a new hull is cheaper per unit capacity. Inline money guard, each fails closed:
  `treasury − price − installFee < ImmutableReserveFloor` → park; `price × 100 > 25% × treasury`
  → park; `price > price_ceiling (500000)` → park; `max_installs_per_tick (1)`.
- **Armed.** DEPLOY-INERT, captain-launched ONLY (`workflow auto-outfit`). One per player.
- **Hands off.** Calls `Outfitter.AcquireAndInstall` → the atomic guarded `InstallModuleCommand`
  (claims the hull, gates the fee on the working-capital floor, docks, installs). No child
  workers.
- **Knobs.** `tune --operation autooutfit`: `min_telemetry_samples`, `price_ceiling` [0,5M],
  `max_installs_per_tick` [1,20], `payback_horizon_hours`, `max_treasury_fraction_pct` [1,100].
- **Source.** `internal/application/autooutfit/coordinator.go`; launch
  `internal/adapters/grpc/container_ops_auto_outfit.go`.

### 6.11 Gas, stocker & warehouse (captain-launched standing ops)

- **Gas coordinator** (`GAS_COORDINATOR`) — gas siphon extraction: `operations start --system
  --gas --siphons S1,S2 --storage ST1`. Spawns `GAS_SIPHON_WORKER` children against a storage
  hull. Captain-launched; not boot-standing.
  Source: `internal/application/gas/commands/run_gas_coordinator.go`, `container_ops_gas.go`.
- **Stocker** — one dedicated hull fills a home warehouse (`workflow stocker --standing --ship
  --warehouse-waypoint`). Deliberately NOT boot-standing: it is pinned to a specific hull +
  warehouse, so there is nothing to unconditionally boot-launch without captain-supplied config.
  Its "survives restart" comes from the persisted `standing` launch config +
  `RecoverRunningContainers`. Capital ceiling ~10% of treasury per buy. Also launched
  indirectly by the contract scaler's depot grower.
  Source: `internal/application/trading/commands/run_stocker_coordinator*.go`,
  `container_ops_stocker.go`.
- **Warehouse** — parks an idle hull as a passive inventory buffer at a home waypoint
  (`workflow warehouse --ship --waypoint --goods`). Also launched by the contract scaler's depot
  grower. Source: `container_ops_warehouse.go`, `container_ops_depot_launch.go`.

### 6.12 Retired engines

These types are named in `retiredCommandTypes` (or were deleted outright). A persisted row of one
is marked terminated at the first post-retirement boot rather than alarming as an unexplained
loss, and the registry refuses to build one either way. **Do not reason about them as live**; the
line after each is what absorbed the duty, taken from the retirement note in code.

| Retired | Duty went to |
|---|---|
| `goods_factory_coordinator`, `siting_coordinator`, `worker_rebalancer_coordinator`, `manufacturing_coordinator` (the factory-ops retirement) | Gate-material sourcing is the construction coordinator's (§6.3). The `WORKER_FERRY` primitive is retained; its spawner is not. |
| `market_freshness_sizer_coordinator`, `frontier_expansion_coordinator` | The probe-sensing coordinator (§6.2), which owns freshness sizing, discovery and the probe budget as one engine |
| `scout_post_coordinator`, `shipyard_backfill_coordinator`, `scout_reposition` (the legacy market-freshness retirement) | A circulating second freshness engine beside parked sensing, duplicating it market-for-market and unable to fund it. Market tours survive only as operator-started, bootstrap-phase-gated verbs |
| `probe_buyer_coordinator` | Probe supply is the sensing coordinator's. It was boot-standing, so nothing had to notice it before it had bought a fleet's worth of probes |
| `fleet_autosizer` | Fleet-growth owns trade capacity (§6.5); the contract scaler owns contract capacity (§6.6) |
| Capacity reconciler (deleted; no persisted type) | Contract-fleet capacity is the contract scaler's (§6.6) |

The gRPC methods for several of these are deliberately KEPT and REFUSE with a message naming the
replacement, so an operator or script reaching for the old habit gets a reason rather than a
missing method. `scout posts list`/`remove` likewise survive so leftover rows can be seen and
cleared. A vestigial `[worker_rebalancer]` / `[scouting]` section in `config.yaml` is inert.

---

## 7. Child workers (one-shot, coordinator-spawned)

Standing coordinators do the concrete work through short-lived children (one iteration, then
the parent re-dispatches). The parent owns re-dispatch, cooldowns, and reclaim-on-death.

Note the two naming layers: the registry keys on the **command type** (`tour_run`, `stocker`,
`arb_run`, `trade_route`), while the container row carries a coarser **`ContainerType`** — several
trading commands all persist as `TRADING`. Match on the command type when you mean the engine.

| Child (command type) | Parent | Role |
|---|---|---|
| `contract_workflow` (`CONTRACT_WORKFLOW`) | Contract-fleet / `batch-contract` | source+deliver+fulfill one contract |
| `cargo_liquidation` (`CARGO_LIQUIDATION`) | Contract-fleet | clear unrelated cargo off a parked hull |
| `tour_run` (`TRADING`) | Trade-fleet | fly one continuous multi-hop trade tour |
| `longhaul_arb` (`LONGHAUL_ARB`) | Long-haul arb coordinator | run continuous long-haul episodes on one hull |
| `warehouse` (`WAREHOUSE`) / `stocker` (`TRADING`) | Contract-scaler depot grower (also captain-launchable) | a depot element |
| `gas_siphon_worker` (`GAS_SIPHON_WORKER`) | Gas coordinator | siphon gas to a storage hull |

`WORKER_FERRY` (a one-shot cross-system hull move) is registered and recoverable but has **no
parent left** — its spawner was retired with the factory ops; it survives as a generic primitive
the daemon's persist/start dispatch still references. `SCOUT_FLEET_ASSIGNMENT` likewise has no
live caller: the bootstrap's old probe-holding scout-all-markets sweep is gone, and the only
remaining path to it is the `workflow scout-all-markets` verb.

The one-shot ship ops (`NAVIGATE`, `DOCK`, `ORBIT`, `REFUEL`, `JETTISON`, `JUMP`, `ROUTE`,
`WARP`, `PURCHASE`, `OUTFITTING`) are `CoordinatorOwnsIterations` single-iteration containers
behind the `ship` verbs — used both directly and as building blocks inside the coordinators
above.

## 8. Known interaction gaps

- **Sensing is inert until the gate is built** (§6.2). Pre-EXPANSION market freshness is entirely
  the bootstrap's home market tour plus whatever the operator starts by hand
  (`workflow scout-markets` / `scout-all-markets`). Nothing reconciles coverage during cold start,
  and nothing will until `ConstructionComplete` reads true.
- **The wave has two readers** (§6.5). Fleet-growth and the sensing buy queue each derive the
  wave under their own reader label and publish it; the pair exists precisely so the two
  DISAGREEING is catchable. A disagreement means one reader's inputs are unreadable, not that
  the wave definition forked.
- **Growth's 25%-treasury ceiling is unset at defaults** (§6.5) while several code comments still
  describe it as applied. Only the absolute floor term binds.
- **Long-haul concurrency is uncapped by order** (§6.9) and its per-hull cap is the only exposure
  bound; the total-exposure constant exists but is not enforced.
- **Contract-hub coordinator is UNWIRED** (§6.7): engine + ports exist and are tested, but
  nothing launches it — contract-hauler homing is not automated by it.
- **Unverified in this pass.** The trade-tour coordinator itself (`run_tour_coordinator*.go`, ~25
  files: planning, rate-floor rescue, relocation offers, absorption, distress liquidation) is the
  fleet's largest single engine and is NOT mapped here — §6.8 covers only the coordinator that
  launches it. Treat its internals as undocumented and read the source. Likewise the trade-route
  and arb one-shot engines.
