# RPP L1–L6 Refactoring Log — gobot (`sp-1z4q`)

Consolidated record of the Refactoring Priority Premise (RPP) pass over the `gobot`
module. Behavior-preserving throughout: no changes to config keys/defaults, proto/
generated code, SQL, CLI flags/output, wire formats, or log/error strings unless
explicitly noted. Every lane and every Mikado goal was gated green before merge.

- **Branch:** `refactor/sp-1z4q-rpp`
- **Base (merge-base):** `dc647939` (origin/main, green)
- **Commits ahead of base:** 93 — 12 lane merges (`merge(rpp-l1l3)`), 35 `refactor`,
  20 `mikado`, 13 `docs` (incl. this log + quality-metrics), 8 `chore(rpp)`, 3 `test`
  (characterization leaves), 1 `style` (gofmt), 1 `fix(rpp)`.
- **Final gate (whole module):** `gofmt -l` clean · `go build ./...` = 0 ·
  `go vet ./...` = 0 · `go test -race -count=1 ./...` = 0 (71 pkg `ok` / 36 no-test =
  **107 packages**, 0 FAIL / 0 panic / 0 DATA RACE).
- **Lanes merged:** 12 · **Lanes dropped:** 0 · **Mikado goals:** 4 achieved, 1 parked.

The recurring finding across every lane: **this is a mature, heavily pre-refactored,
live money-earning codebase** with named constants, guard clauses, small helpers, and a
strong load-bearing WHY/`sp-*`-bead comment culture. Genuinely safe L1–L3 wins were
therefore *sparse*; each lane applied only high-confidence changes and recorded larger
opportunities as candidates rather than churning pristine code. The most valuable
outcome of the L1–L3 sweep — beyond the applied edits — is the **catalogue of L4–L6
candidates and suspected bugs** below, several of which seeded the Mikado goals.

---

## Per-level narrative

### L1 — Readability (all 12 lanes)
Dead-code removal (grep/`deadcode`-verified, unexported only), magic-string/number →
named-constant extraction, builtin-shadow renames (`cap`, `new`, `copy`, `max`, `time`),
stale/misplaced/false-comment fixes, and pre-existing `gofmt` drift. Highlights:
- **domain-economy:** deleted 7 dead exported symbols (whole-repo grep-verified) +
  renames + false-comment removal — 10 files, −110 LOC.
- **adapters-cli:** deleted 4 dead functions, 3 ANSI-escape constants, 8 redundant
  section-comments — ~−96 LOC.
- **adapters-data:** `traitMarketplace`/`traitFuelStation` (3 sites) and
  `assignmentStatus{Active,Idle}` (10 GORM sites) constants.
- Builtin-shadow renames landed in app-manufacturing, app-ops, app-ship,
  domain-fleetops, domain-economy.

### L2 — Complexity / duplication (simple)
Named predicates for dense booleans; Extract-Function on flat Long Methods; within-file
copy-paste dedup. Highlights:
- **captain-infra-cmd:** `config.SetDefaults` (294-line Long Method) decomposed into a
  dispatcher + 9 `setXxxDefaults` helpers — **0 value changes** (41 structural
  insertions). This removed the pre-refactor **#2-worst** function (complexity **79**)
  from the top-20 entirely (highest `config` fn is now `setCaptainDefaults` at 33).
- **domain-economy:** `groupLegs`/`computableRates` (tour_rate, 3×/2× copy-paste),
  `roleTarget` (depot mutation), `nearestTargetWithCapacity` (fleet_assigner nesting).
- **app-ship:** `recordMarketScanMetric` collapsed a 5× metric-emit block.
- **app-manufacturing:** `isModeratePlusSupply` predicate (2 sites).
- **app-trading:** `minInt` → Go 1.24 builtin `min` (variadic).

### L3 — Responsibilities (within-package)
God-file splits by cohesive concern (pure line-range moves, verbatim, zero signature
change) and within-package dedup. Highlights:
- **captain-infra-cmd:** `captain/detectors.go` **1431 → 636 lines**, split into 5 files
  (`detectors_income/regime/scout/prometheus/credits.go`) mirroring the existing test
  partition — 32 funcs + 15 decls preserved verbatim, 0 duplicates.
- **domain-fleetops:** `navigation/ship.go` **946 → 532 lines** + 3 new cohesive files
  (assignment ops / state sync / reconstruct).
- **domain-economy:** `manufacturing/task.go` **808 → 520** + `task_transitions.go` (296).
- **adapters-grpc:** shared `arriveIfInTransit`/`clearCooldownIfSet` (ship-state
  scheduler); prior pass extracted `startContainerRunner`/`findContainerModelByID`
  across ~27 launch sites.
- Within-package dedup: `fleetBySymbol` (app-contract), `toConstructionMaterials` /
  `traitGrantsFuel` / `stripQueryString` (adapters-data).

### L4–L6 — Abstractions / patterns / architecture (Mikado)
Cross-package moves and structural changes were **not** performed inside the L1–L3
lanes (blast-radius + live-money risk). They were routed to the Mikado method as
independently-gated goals — see [Mikado goals](#mikado-goals-l4l6). Four landed;
one (`injected-metrics-facade`) was parked with a fully-explored map after both
park-rule triggers fired.

---

## Per-lane summary (12 merged, 0 dropped)

| Lane | Scope | Levels applied | Applied wins | Suspected bugs surfaced |
|---|---|---|---|---|
| `adapters-cli` | `adapters/cli` | L1,L2,L3 | 4 dead fns, 3 consts, 8 comments (−96 LOC); `anyKnobSet()` predicate | maskPassword no-op (security) |
| `adapters-data` | `adapters/{persistence,api}` | L1,L3 | 4 consts (2 groups), 3 helpers, 3 copy-paste sites | parseContractData copy-key |
| `adapters-grpc` | `adapters/grpc` | L1,L2,L3 | 2 timeout consts; `arrive/clearCooldown` helpers; (prior pass: ~27-site launch-tail dedup) | none |
| `adapters-misc` | `adapters/{metrics,capacity,expansion,flowfeed,graph,routing,telemetry}` | L1 | dead `mu` field; 2 poll-interval consts | none |
| `app-contract` | `application/contract` | L1,L3 | misplaced-doc fix; `fleetBySymbol` dedup | WARN/WARNING log inconsistency |
| `app-manufacturing` | `application/manufacturing` | L1,L2 | `cap→maxChains`; `isModeratePlusSupply` predicate | `shortID[:8]` panic on <8-char id |
| `app-ops` | 15 `application/*` pkgs | L1 | `cap→capBudget/hubCap` (bootstrap) | doc-only (Step-5 label, stale line-ref) |
| `app-ship` | `application/{ship,shipyard,fleet,autooutfit}` | L1,L2 | builtin-shadow renames; `recordMarketScanMetric` (5 sites) | none |
| `app-trading` | `application/{trading,ledger,liquidation,probebuy}` | L1,L2 | dead `factoryCommandTypes`; 2 scan consts; `minInt→min` | `originReason` discarded by all callers |
| `captain-infra-cmd` | `captain`,`infrastructure`,`cmd`,`pkg/utils` | L1,L2,L3 | gofmt 5 files; `SetDefaults`→dispatcher+9; `buildDetectorConfig`; `detectors.go` 1431→636 split | `(>3h)` label/const coupling |
| `domain-economy` | 9 `domain/*` pkgs (trading,market,goods,ledger,contract,manufacturing,shipyard,outfitting,gas) | L1,L2,L3 | 7 dead exports (−110 LOC); 3 helpers; `task.go` 808→520 split | `ErrInsufficientCargo` never matches; always-nil errors |
| `domain-fleetops` | 18 `domain/*` pkgs | L1,L2,L3 | 2 renames, doc fix, 5 comments; 3 helpers; `ship.go` 946→532 split | DRIFT<CRUISE travel-time table; discarded `NewCargo` errors; HealthMonitor stub; container STOPPING desync |

### Notable per-lane detail

- **captain-infra-cmd** delivered the two largest structural wins: the `SetDefaults`
  decomposition (removed the #2-worst function) and the `detectors.go` split
  (−795 lines in one file). Together these drive most of the prod-file-count increase
  at ~flat prod LOC. Commits: `ee402516`, `eb6e3aae`, `1b0fe6bf`, `16e498a9`.
- **domain-economy** and **domain-fleetops** L4 candidates *directly seeded* three of
  the five Mikado goals (shared Supply/Activity VOs; lift fuel-trait to domain;
  parallel lifecycle-status switches).
- **adapters-cli** recorded but did **not** execute the two biggest structural items on
  the money-path RPC file `daemon_client.go`: the god-file split (2355 LOC) and the
  39× `agentSymbol` optional-pointer dedup — deemed high-churn on a critical file,
  routed to a focused follow-up.
- **adapters-misc** L4 candidates (injected metrics facade; shared polling-collector
  base) became Mikado goals `injected-metrics-facade` (parked) and
  `metrics-polling-collector-base` (achieved).

### gofmt reconciliation (integration-level)
The final `style: gofmt [sp-1z4q]` commit reformatted 25 files flagged by
`gofmt -l internal cmd pkg/utils` at integration time — almost all pre-existing test-file
drift on Go 1.26. This intentionally includes `application/autooutfit/coordinator.go`
+ `coordinator_test.go` (which the **app-ship** lane deliberately left, preferring their
one-liner setter bodies) and the three `domain/*` test files **domain-fleetops** left
"for their lanes". The integration mandate is a gofmt-clean tree; gofmt is whitespace-only
and behavior-preserving, so this is a deliberate, authorized override of those two
lane-local preferences.

---

## Dropped lanes

**None.** All 12 planned lanes were completed and merged (`merge(rpp-l1l3)` ×12).

---

## Mikado goals (L4–L6)

Cross-package/architectural work, each explored via the Mikado method and landed only
when the dependency tree proved bounded and safe. Tree/exploration docs live under
`docs/mikado/`.

### Achieved (4)

1. **`has-fuel-trait-to-domain`** — `docs/mikado/has-fuel-trait-to-domain.mikado.md`
   Homed the "MARKETPLACE/FUEL_STATION ⇒ has fuel" trait rule into
   `internal/domain/shared` (`TraitGrantsFuel`/`TraitsGrantFuel` + consts + test);
   repointed `graph_builder.go` and `waypoint_converter.go`; deleted the adapter-local
   predicate/constants. Flat 3-leaf tree, no hidden prerequisites. Completes the
   adapters-data L4 candidate #1. *(Landed by 4 pre-existing branch commits; the final
   verification pass added 0 new commits.)* Measurable effect:
   `GraphBuilder.BuildSystemGraph` complexity **31 → 28**.
   > Scope note: the waypoint-**TYPE** filter `type != "FUEL_STATION"` (market_repository,
   > assign_scouting_fleet) is a *different* rule and was correctly left out of scope.

2. **`supply-activity-level-vo`** — `docs/mikado/supply-activity-level-vo.mikado.md`
   Extracted `SupplyLevel` + `ActivityLevel` value objects into `internal/domain/shared`
   (owning `Order()` + validation); `manufacturing`/`market` retain drift-proof
   `type X = shared.X` aliases; dropped the odd `trading → manufacturing` domain→domain
   import. Kills the `sp-9mkf` cross-package supply-enum drift class. 1 net commit this
   session (Leaf 4/GOAL); Leaves 1–3 pre-existing.
   > **Parked follow-up (out of assessed bound):** repoint the raw *string* fields
   > (`goods.SupplyChainNode`, `shipyard.ShipTypeAvailability`, `trading.ArbitrageLane`,
   > …) from `string` to the VO type (23 external files) and fully eliminate the
   > manufacturing/market aliases (57 refs). Behavior-preserving in principle (fields
   > carry no gorm/protobuf tags); blocker is blast radius on a live system. Recommend a
   > dedicated Mikado goal.

3. **`metrics-polling-collector-base`** — `docs/mikado/metrics-polling-collector-base.mikado.md`
   Extracted the shared lifecycle scaffolding of the 4 interval-polling metrics
   collectors into one embedded `pollingCollector` template
   (`adapters/metrics/polling_collector.go`): `ctx`/`cancelFunc`/`wg` +
   `startContext`/`startPolling` + promoted `Stop()`. Flat goal + 5 leaves. Net **−62
   LOC**. The `pollImmediately` bool preserves the real asymmetry (3 collectors poll once
   before ticking; container's two loops are tick-only) — preserved, not "fixed". 8
   commits. Completes adapters-misc L4 candidate #2.

4. **`lifecycle-status-mapping`** — `docs/mikado/lifecycle-status-mapping.mikado.md`
   Added a generic `shared.ProjectStatus[T any](sm, table, fallback)` primitive; the 3
   hand-written state→status switches (`container.Container.Status`,
   `storage.StorageOperation.Status`, `navigation.Route.Status`) now delegate via a
   per-aggregate table. Flat 7-leaf tree; characterization tests committed against the
   *original* switches first (bottom-up TDD). Kills the parallel-enum drift risk in
   money-bearing aggregates. 9 commits. Completes domain-fleetops candidate #3 +
   domain-economy candidate #3.

### Parked (1)

5. **`injected-metrics-facade`** — `docs/mikado/injected-metrics-facade.mikado.md`
   **PARKED** with a fully-explored, experiment-grounded map (the deliverable; 1
   doc-only commit, zero `.go` changes). Goal: replace the metrics package-global
   collector singletons + nil-safe free-functions with an injected
   `application/ports/telemetry.Recorder` facade. Design **proven feasible** (a built-
   then-reverted experiment compiled cycle-free). Parked because **both** park-rule
   triggers fired:
   - **Size:** ~16–19-node tree, over the ~12-node bound.
   - **Risk > assessed:** on a live money system whose failure mode is *silent metric
     loss*, 5 application test files assert real emission (swapping to a no-op-defaulted
     port breaks them unless each co-wires `SetRecorder`); `market_scanner` reaches
     through a getter with no free-function; the 19 `SetGlobalX` globals cannot be
     deleted by this goal (still consumed by adapters/{api,capacity,grpc}, captain,
     domain/{apibudget,dutycycle}); adapters/metrics already imports application
     (pre-existing coupling).
   **Recommended way forward:** split into 3 sequential ≤12-node, independently-green
   sub-goals — (a) port scaffold + forwarding adapter + daemon injection (byte-identical);
   (b) ~15 no-emission callers + market_scanner; (c) the 5 emission callers, each
   co-wiring its test.

---

## Suspected bugs — follow-up list

Surfaced during the sweep, **reported not fixed** (each would change behavior/output,
outside the behavior-preserving mandate). Ordered roughly by severity. Recommend one
bead per item.

1. **[SECURITY] `adapters/cli/config.go` `maskPassword` is a no-op** — returns the
   connection URL unchanged (its own `// TODO`), so `config show` prints the DB URL
   *including any embedded password*, unmasked. Pre-existing, known.
2. **[correctness] `domain/navigation` DRIFT flight-mode table is mis-calibrated** —
   `shared/flight_mode.go` gives DRIFT `TimeMultiplier` **26** vs CRUISE **31**, i.e.
   DRIFT computes *faster* travel than CRUISE, contradicting its own "slow" comment and
   the SpaceTraders API. Any DRIFT-leg travel-time estimate under-estimates badly.
3. **[correctness] `adapters/api/client.go` `parseContractData` copy-key** — both
   `deadlineToAccept` and `deadline` are read from `termsData["deadline"]`, so
   `DeadlineToAccept` always equals the fulfillment `Deadline` (should be the
   contract-level `data["deadlineToAccept"]`). Confirm against the OpenAPI schema first.
4. **[robustness] `domain/navigation/ship_cargo.go` discards `NewCargo` errors** —
   `ReceiveCargo`/`RemoveCargo` do `newCargo, _ := shared.NewCargo(...)`; on an
   inventory-sum mismatch the ship's cargo silently becomes nil.
5. **[robustness] `application/manufacturing/services/market_levels.go` `shortID(id)[:8]`
   has no length guard** — panics on a <8-char id; used at 30+ log-metadata sites. Safe
   today only because inputs are UUIDs/long IDs.
6. **[dead branch] `domain/goods.ErrInsufficientCargo` can never match** — the
   `errors.As` target at `run_factory_coordinator.go:1414` is never constructed anywhere;
   the recovery branch behind it is unreachable.
7. **[metric-inflation] `domain/daemon/health_monitor.go` `AttemptRecovery`** — past the
   attempts cap it increments `AbandonedShips` on *every* subsequent call, and the
   recovery body is a stub (`isShipStuck` always false). Moot if the dead-code candidate
   (safe-delete `HealthMonitor`) is confirmed.
8. **[latent, from `lifecycle-status-mapping`] `container.interrupted` is only ever
   assigned `false`** — no setter exists, so the `INTERRUPTED` daemon-crash-recovery
   status is currently unreachable. Candidate for its own bead.
9. **[architecture, from `injected-metrics-facade`] `domain/{apibudget,dutycycle}` import
   `adapters/metrics`** — a hexagonal-layering breach (domain → adapter). Candidate goal:
   `domain-metrics-inversion`.
10. **[diagnostic gap] `run_trade_route_coordinator_lanes.go:287`
    `repositionNeighborsWithinJumps`** returns `originReason` that its doc says is
    threaded back for diagnostics, but all 3 callers discard it with `_`.
11. **[always-nil errors] `domain/contract.FleetAssigner`** —
    `IsRebalancingNeeded`/`AssignShipsToTargets*` return an `error` that is always nil
    (API slack; callers still handle it).

### Non-bug observations (documentation / style — no runtime effect)
- **app-contract:** `delivery_executor.go` mixes `logger.Log("WARN",…)` (×3) and
  `"WARNING"` (×4) — an ops-grep could miss lines. (Log-string change ⇒ out of scope.)
- **app-manufacturing:** `collection_opportunity_finder.go` has ~14 `fmt.Printf` debug
  prints to stdout, inconsistent with the structured logger.
- **app-ship:** `strategies/refuel_strategy.go` `MinimalRefuelStrategy` /
  `AlwaysTopOffStrategy` are dead *exported* code (kept — exported API removal is a
  product call).
- **app-ops:** gas coordinator duplicate `// Step 5:` label; a stale `(lines 218-235)`
  comment.
- **captain-infra-cmd:** `briefing.go` prints literal `"(>3h)"` while the cutoff is the
  independent const `briefingStaleMarket = 3*time.Hour` — must be kept in sync manually.

### Dead-code safe-delete candidates (exported ⇒ need repo-wide decision, not lane edit)
- `domain/daemon/HealthMonitor` (entire file; stub detection, 0 external refs, no tests).
- `domain/container/ShipAssignmentManager` (in-memory manager, 0 refs; live path is the
  DB-backed `adapters/persistence/ship_assignment_repository.go`).
- `application/ship/strategies/{MinimalRefuelStrategy,AlwaysTopOffStrategy}`.

---

## Provenance / housekeeping
- Every lane and Mikado leaf was gated (`go build` + `go vet` + `go test -race`) before
  its commit; the integration branch is green at every merge and at final HEAD.
- The repo-root beads pre-commit hook auto-exports `.beads/issues.jsonl`; refactor
  commits used `--no-verify` to keep each commit surgical (`gobot/` `.go` only), matching
  the established branch pattern. The final worktree carries only the pre-existing,
  out-of-scope `.beads/issues.jsonl` drift (never staged into a code commit).
- Quantitative before/after deltas: see `quality-metrics.md`.
