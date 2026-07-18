# Mikado: injected-metrics-facade (L6)

**Goal (business value):** Restore dependency inversion by closing the largest
confirmed hexagonal breach — application files reaching into
`internal/adapters/metrics` — and remove the Service-Locator global mutable
state. Define a metrics-recorder port (interface) consumed by the application
layer and injected from the daemon composition root, replacing the direct
`metrics.RecordX` / `metrics.GetGlobalMarketCollector` calls with a
no-op-defaulted injectable recorder. End-state target: the non-test application
files that import `internal/adapters/metrics` depend on the port instead;
`grep` shows zero `internal/application -> internal/adapters/metrics` imports.

**Status:** parked
**START (baseline SHA on refactor/sp-1z4q-rpp):** 6525ce4cfb9580d5d940cb317e183ba814e3011c
**Affected packages:** `internal/adapters/metrics`, `internal/application`, `cmd/spacetraders-daemon` (metrics composition root actually lives in `internal/adapters/grpc/daemon_server.go`)

## Why parked (both PARK-RULE triggers fired, with proof)

1. **Tree exceeds ~12 nodes.** The real bottom-up execution tree is ~16–19
   nodes (3 prerequisites + ~10–13 caller-conversion nodes + market_scanner
   special case + goal). See "Discovered tree" below.
2. **Riskier than the upstream assessment.** The assessment scoped the blast
   radius to "25 callers + adapters/metrics + daemon composition root" and a
   behavior-preserving no-op default. Exploration found FOUR facts it missed,
   the first of which is a proven test breakage on a LIVE money system whose
   failure mode is *silent metric loss*:
   - **(H1 — proven) 5 application TEST files assert real metric emission** and
     break under a no-op default (evidence below).
   - **(H2) `market_scanner.go` uses a getter, not a Record free-function**
     (`metrics.GetGlobalMarketCollector().RecordScan(...)`) — no matching
     free-function exists, so the port needs a bespoke method.
   - **(H3) The 19 `SetGlobalXCollector` globals + ~50 free-functions cannot be
     deleted by this goal.** They are still consumed by `internal/adapters/*`
     (api, capacity, grpc), `internal/captain`, and `internal/domain`
     (apibudget, dutycycle). So "replacing SetGlobalX + free-functions" is only
     achievable *from the application's viewpoint* (application stops importing
     them); the globals remain as an adapters-internal detail. Full removal is a
     strictly larger, separate goal.
   - **(H4) `internal/adapters/metrics` already imports `internal/application`**
     (`common`, `ledger/queries`, `mediator`) — pre-existing bidirectional
     coupling. The facade does not worsen it and is cycle-free (proven), but it
     confirms the package graph is tangled.

## Baseline facts (from exploration)

**Count reconciliation:** exactly **25 application files** import
`internal/adapters/metrics` = **20 non-test + 5 test**. The upstream "25
non-test" conflated the two; the 5 test files are precisely the H1 hazard.

**The 20 non-test importers and the symbols they use** (all nil-safe
free-functions except the one getter):

- `contract/idle_arb.go` — RecordAbsorptionConsultVerdict
- `ledger/commands/record_transaction.go` — RecordTransaction
- `manufacturing/commands/run_factory_coordinator_chain_pnl_kill.go` — RecordChainPnLKill, RecordChainPnLRealizedPerHour  *(H1 test)*
- `manufacturing/commands/run_factory_coordinator_input_pause.go` — RecordChainInputPause  *(H1 test)*
- `manufacturing/commands/run_factory_coordinator_rest_signal.go` — RecordChainExportRest  *(H1 test)*
- `manufacturing/commands/run_siting_coordinator_act.go` — RecordSitingLaunch, RecordSitingRetire
- `manufacturing/commands/run_siting_coordinator_emit.go` — RecordSitingScoutDemand
- `manufacturing/services/factory_supply_poller.go` — RecordManufacturingFactoryCycle, RecordManufacturingSupplyTransition
- `scouting/commands/run_scout_post_coordinator.go` — RecordScoutFreshness
- `ship/commands/tactics/refuel_ship.go` — RecordFuelPurchase
- `ship/market_scanner.go` — **GetGlobalMarketCollector** *(H2 getter)*
- `ship/route_executor.go` — RecordFuelConsumption, RecordRouteCompletion, RecordSegmentCompletion
- `trading/commands/run_tour_coordinator_lookback.go` — RecordTourReserveFloorEngagement
- `trading/commands/run_tour_coordinator_metrics.go` — RecordAbsorptionCapBinding
- `trading/commands/run_tour_coordinator_placement.go` — RecordTourJumpLoaded, RecordTourPlacementDecision, RecordTourReposition
- `trading/commands/run_tour_coordinator_rate_floor.go` — RecordTourJumpLoaded, RecordTourReposition
- `trading/commands/run_tour_coordinator_reposition.go` — RecordHullStranded, RecordTourJumpLoaded, RecordTourLanesStaleExcluded, RecordTourReposition
- `trading/commands/run_tour_coordinator.go` — ObserveTourDuration, ObserveTourLegPriceDrift, ObserveTourPlanRate, RecordAbsorptionLadderIncident, RecordTourCandidateDropped, RecordTourExit, RecordTourMarginsDeath, RecordTourReserveFloorEngagement, SetTourResolvedMaxSpend
- `trading/commands/run_trade_route_coordinator_absorption.go` — RecordAbsorptionConsultVerdict
- `trading/services/tour_snapshot.go` — RecordTourLanesStaleExcluded  *(H1 test)*

**The 5 emission-asserting TEST files (H1)** — each installs a real registry +
collector via `SetGlobalXxxCollector` and asserts an emitted counter/gauge via
`metrics.Registry.Gather()`; production must keep emitting through them:

- `manufacturing/commands/run_factory_coordinator_chain_pnl_kill_test.go`
- `manufacturing/commands/run_factory_coordinator_input_pause_test.go`
- `manufacturing/commands/run_factory_coordinator_rest_signal_test.go`
- `trading/commands/run_tour_coordinator_unreachable_lanes_test.go`
- `trading/services/tour_snapshot_test.go`

**Port surface:** 31 distinct free-functions (exact signatures captured during
exploration) + 1 bespoke `RecordMarketScan(playerID int, waypoint string,
elapsed time.Duration, scanErr error)` for the H2 getter case = **32 methods**.

## Exploration log (EXPERIMENT -> LEARN -> REVERT)

Built the full proposed design and ran a targeted gate, then reverted all code
(this doc is the only artifact):

1. Created `internal/application/ports/telemetry/recorder.go`: `Recorder`
   interface (32 methods), `nopRecorder` no-op default, package `active Recorder
   = nopRecorder{}` + `SetRecorder`, and delegating free-functions.
2. Created `internal/adapters/metrics/telemetry_recorder.go`: `telemetryRecorder`
   implementing `telemetry.Recorder` by forwarding each method to the existing
   nil-safe free-functions (and the getter for RecordMarketScan);
   `NewTelemetryRecorder()` constructor.
3. Wired `daemon_server.go`: `telemetry.SetRecorder(metrics.NewTelemetryRecorder())`
   right after the first `SetGlobalCollector`.
4. Converted two representative callers (pure import+qualifier swap
   `metrics.X` -> `telemetry.X`): `contract/idle_arb.go` (no emission test) and
   `manufacturing/commands/run_factory_coordinator_chain_pnl_kill.go` (has
   emission test).

**Results:**
- `go build ./...` -> **exit 0**. Proves the design is **cycle-free**
  (adapters/metrics -> application/ports/telemetry compiles even though
  adapters/metrics already imports other application packages — H4) and every
  32-method signature is correct.
- `go test ./internal/application/contract/...` -> **ok** (control): a
  no-emission caller converts cleanly and behavior-preserving under the no-op
  default.
- `go test -run Kill ./internal/application/manufacturing/commands/...` ->
  **FAIL: TestChainPnLKill_EpisodeDedupAndResume — "kill counter series not
  found"**. This is the H1 proof: swapping production to the no-op-defaulted
  port silently drops the emission the test asserts through its registered
  collector.

**Learning:** each of the 5 emission-test files is a *false leaf* — its
production conversion is blocked by a prerequisite: wire
`telemetry.SetRecorder(metrics.NewTelemetryRecorder())` (+ `defer
telemetry.SetRecorder(nil)`) into that test's setup, in the SAME commit, so the
existing assertion keeps passing without being weakened.

## Discovered tree (bottom-up; `[ ]` = ready, `[!]` = false leaf w/ prereq)

- [ ] GOAL: application depends on `telemetry.Recorder` port; zero non-test `internal/application -> internal/adapters/metrics` imports; full `go test -race ./...` green
    - [ ] Prereq A: add `internal/application/ports/telemetry` (Recorder 32-method iface + nopRecorder + `active`/`SetRecorder` + 32 delegating free-funcs). Compiles standalone. *(proven)*
    - [ ] Prereq B: add `internal/adapters/metrics/telemetry_recorder.go` forwarding `telemetry.Recorder` -> existing free-funcs + getter; `NewTelemetryRecorder()`. Compiles unused, cycle-free. *(proven)*
    - [ ] Prereq C: inject at composition root — `telemetry.SetRecorder(metrics.NewTelemetryRecorder())` in `daemon_server.go` (metrics-enabled block). *(proven)*
    - [ ] Convert no-emission callers (each = import+qualifier swap; safe under no-op default). Batch by subpackage:
        - [ ] `contract/idle_arb.go` + `trading/commands/run_trade_route_coordinator_absorption.go` *(idle_arb proven)*
        - [ ] `ledger/commands/record_transaction.go`
        - [ ] `manufacturing/commands/run_siting_coordinator_act.go` + `run_siting_coordinator_emit.go`
        - [ ] `manufacturing/services/factory_supply_poller.go`
        - [ ] `scouting/commands/run_scout_post_coordinator.go`
        - [ ] `ship/commands/tactics/refuel_ship.go`
        - [ ] `ship/route_executor.go`
        - [ ] `trading/commands/run_tour_coordinator*.go` (main + lookback + metrics + placement + rate_floor + reposition — no emission tests among these)
    - [!] Convert `ship/market_scanner.go` (H2) — PREREQ: add `RecordMarketScan` to the port + a `RecordMarketScan` free-function (or inline the getter nil-check in the forwarding adapter, as proven); replace `metrics.GetGlobalMarketCollector()...RecordScan` with `telemetry.RecordMarketScan(...)`
    - [!] Convert `run_factory_coordinator_chain_pnl_kill.go` (H1) — PREREQ: co-wire `..._chain_pnl_kill_test.go` with `telemetry.SetRecorder`(+reset) in same commit *(break proven)*
    - [!] Convert `run_factory_coordinator_input_pause.go` (H1) — PREREQ: co-wire `..._input_pause_test.go`
    - [!] Convert `run_factory_coordinator_rest_signal.go` (H1) — PREREQ: co-wire `..._rest_signal_test.go`
    - [!] Convert `trading/services/tour_snapshot.go` (H1) — PREREQ: co-wire `tour_snapshot_test.go`
    - [!] Convert `trading/commands/run_tour_coordinator_reposition.go` — CHECK: `run_tour_coordinator_unreachable_lanes_test.go` asserts staleness emission; co-wire it if `RecordTourLanesStaleExcluded` path is exercised

## Recommended way forward (split into <=12-node sub-goals)

The design is proven; the blast radius, not the design, is the blocker. Land it
as three sequential, independently-green sub-goals:

1. **`metrics-port-scaffold`** (3 nodes, zero behavior change): Prereqs A+B+C.
   Adds the port, the forwarding adapter, and the daemon injection. No caller
   changes yet -> byte-identical behavior; full race gate green. Low risk.
2. **`metrics-port-safe-callers`** (~8 nodes): convert the ~15 no-emission
   callers + `market_scanner` (H2). Each a pure swap; tests stay green under the
   injected adapter (prod) / no-op (tests that don't assert emission).
3. **`metrics-port-emission-callers`** (~6 nodes): convert the 5 emission-test
   callers, each co-wiring `telemetry.SetRecorder` into its test in the same
   commit. This is where the H1 discipline lives.

After sub-goal 3, `grep -rl adapters/metrics internal/application --include='*.go'
| grep -v _test.go` is empty. (Test files may still import adapters/metrics to
construct real collectors — that is legitimate integration setup and out of
scope for the non-test end-state.)

## Behavior preservation (design contract for execution)

- `telemetry.active` defaults to `nopRecorder{}` == today's "global nil -> no-op"
  when metrics are disabled.
- In production the daemon injects `telemetryRecorder`, which forwards to the
  SAME free-functions that read the SAME globals set by the 19 `SetGlobalXxx`
  calls -> identical metrics emitted. Forwarding reads globals at call time, so
  injection order vs the SetGlobalXxx block is irrelevant.
- No proto/SQL/CLI/config/log-string/wire changes; no exported-identifier
  renames outside the new package; the 19 globals + free-functions are untouched
  (still used by adapters/captain/domain).

## Suspected issues (recorded, NOT changed — behavior-preserving refactor only)

- **Service-Locator remains (partially).** Even after full execution, the 19
  package globals persist because `internal/domain/{apibudget,dutycycle}`,
  `internal/captain`, and `internal/adapters/{api,capacity,grpc}` still call the
  free-functions. A domain layer (`domain/apibudget`, `domain/dutycycle`)
  importing `adapters/metrics` is itself a hexagonal breach — a candidate for a
  separate future Mikado goal (`domain-metrics-inversion`).
- **H4 bidirectional coupling:** `adapters/metrics` importing
  `internal/application/{common,ledger/queries,mediator}` is an inward-adapter
  dependency worth revisiting; it does not block this goal.
