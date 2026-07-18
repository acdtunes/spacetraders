# Mikado: lifecycle-status-mapping (L5)

**Goal (business value):** Make `shared.LifecycleStateMachine` the single
authority for state->local-status projection. Give it one generic/table-driven
projection primitive and rewrite the three hand-written `LifecycleState`
switches — `container.Container.Status()`, `storage.StorageOperation.Status()`,
`navigation.Route.Status()` — to delegate to it. End-state: adding a lifecycle
state edits one authority (+ one table row per aggregate) instead of three
hand-written switches, and none of the three `Status()` methods contains a
standalone lifecycle-state switch. Eliminates 3-way shotgun surgery across three
money-adjacent aggregates.

**Status:** achieved
**START (baseline SHA on refactor/sp-1z4q-rpp):** c91d39a846191f3ce0773939a39b9a960538f1e5
**Affected packages:** `internal/domain/shared`, `internal/domain/container`, `internal/domain/storage`, `internal/domain/navigation`

## Baseline facts (pre-exploration)

- Go 1.24.0 — generics fully available. No import cycles: `container`, `storage`,
  `navigation` each already import `shared`; `shared` imports none of them.
- The three switches are pure `lifecycle.Status()` -> local-enum projections with a
  `default:` fallback. Transcribed mapping tables (verbatim from the current switches):
  - **Container** (`container.go:250`) — pre-guards `stopping`->STOPPING, `interrupted`->INTERRUPTED
    run BEFORE the switch (they are NOT lifecycle states; left untouched). Switch:
    Pending->Pending, Running->Running, Completed->Completed, Failed->Failed,
    Stopped->Stopped; default->Pending.
  - **Storage** (`operation.go:207`): Pending->Pending, Running->Running,
    Completed->Completed, Stopped->Stopped, Failed->Failed; default->Pending.
  - **Route** (`route.go:160`): Pending->PLANNED, Running->EXECUTING,
    Completed->COMPLETED, Failed->FAILED, Stopped->ABORTED; default->PLANNED.
- The three target enums differ (Route uses PLANNED/EXECUTING/ABORTED strings; Container
  adds STOPPING/INTERRUPTED extension states), so the primitive must be parameterized
  per aggregate — a generic `ProjectStatus[T]` over a per-aggregate `map[LifecycleStatus]T`
  table + typed fallback. Direct-key lookup replicates the switch (missing key == default).
- **Reachability (for characterization):**
  - `container` package has NO `_test.go` file — `Status()` is uncharacterized in-package.
  - `container.interrupted` is only ever set to `false` (never true) — INTERRUPTED is a
    dormant recovery state, unreachable via public API. Container `default:` also
    unreachable (lifecycle is always one of the 5 valid states). Left verbatim.
  - Route has NO stop transition and NO FromData/reconstruct path — lifecycle can only
    reach Pending/Running/Completed/Failed, so Route's Stopped->ABORTED row is dead but
    preserved verbatim. Route `default:` likewise unreachable.
  - Storage can reach all 5 via transitions (Start/Complete/Fail) AND via
    `StorageOperationFromData` round-trip.

## Scope guard (OUT of scope — behavior-preserving)

- `StorageOperationFromData`'s REVERSE switch (status string -> LifecycleStatus) is a
  different projection (persistence rehydrate), NOT one of the three named forward
  switches. Left untouched.
- Container-specific `stopping`/`interrupted` pre-guards stay verbatim (not lifecycle states).

## Tree

- [x] GOAL: three `Status()` methods delegate to `shared.ProjectStatus`; no standalone lifecycle switch remains in any of them
    - [x] Leaf A: Add `shared.ProjectStatus[T any](sm *LifecycleStateMachine, table map[LifecycleStatus]T, fallback T) T` primitive (`shared/lifecycle_state_machine.go`) — commit a5ceeb39
    - [x] Leaf B: Characterize `container.Container.Status()` reachable rows + STOPPING guard (new `container_status_test.go`) — commit 1cfdf986
    - [x] Leaf C: Characterize `storage.StorageOperation.Status()` all 5 rows (new `operation_status_test.go`) — commit 398eed38
    - [x] Leaf D: Characterize `navigation.Route.Status()` reachable rows (new `route_status_test.go`) — commit eff745f4
    - [x] Leaf E: Rewrite `container.Container.Status()` -> `containerStatusByLifecycle` table + `ProjectStatus` (keep the two pre-guards) — commit 1f577d0c
    - [x] Leaf F: Rewrite `storage.StorageOperation.Status()` -> `operationStatusByLifecycle` table + `ProjectStatus` — commit 9f781792
    - [x] Leaf G: Rewrite `navigation.Route.Status()` -> `routeStatusByLifecycle` table + `ProjectStatus` — commit b219a906

## Completion

GOAL achieved. All 7 leaves ticked; each was gated (`go build ./...` + `go vet` +
`gofmt -l` + `-race` over the 4 domain pkgs) and committed green, with each rewrite
landing AFTER its characterization test was committed against the original switch.

**End-state criteria — both hold:**
1. `shared.LifecycleStateMachine` owns the single state->status projection primitive
   `ProjectStatus[T]`; all three aggregates call it (container.go:272, operation.go:219,
   route.go:173). Adding a lifecycle state is now a one-row edit to each aggregate's
   table (or a fallback tweak), not a hand-edited switch per aggregate.
2. None of the three `Status()` methods contains a standalone lifecycle switch —
   confirmed by grep (`switch .*lifecycle.Status()` -> zero matches across the three files).

**Behavior-preserving proof:** each per-aggregate table is a byte-for-byte transcription
of the switch arms it replaced (direct-key lookup == switch; missing key == fallback ==
the old `default:` arm). Dead-but-preserved rows kept verbatim: Route `Stopped->ABORTED`
(unreachable — no stop transition) and Container `INTERRUPTED`/`default` (never set true).
No log/error strings, config keys, CLI surface, wire/DB schema, or the reverse
`StorageOperationFromData` rehydrate switch were touched.

**FULL gate:** `go test -race -count=1 ./...` -> exit 0, **0 FAIL**, 71 ok + 36 no-test
packages (107 total). Baseline discipline held throughout: never committed red, never
weakened a test.

**Suspected issue recorded (NOT actioned — out of behavior-preserving scope):**
`container.interrupted` is only ever assigned `false` anywhere in the codebase, so the
`INTERRUPTED` status is currently unreachable — the daemon-crash-recovery state appears
to have no setter. Candidate for a separate bead (behavioral fix, not a refactor).

## Exploration log

**Naive full-goal attempt (throwaway, reverted).** Added `shared.ProjectStatus[T]`
and rewrote all three `Status()` methods to table+primitive in one pass, then ran the
targeted gate: `go build ./...`, `go vet` (4 pkgs), `gofmt -l` (4 files), and
`go test -race -count=1` over shared/container/storage/navigation. ALL GREEN, zero
diagnostics. **No blocking prerequisites emerged** — the three packages already import
`shared`, `shared` imports none of them (no cycle), Go 1.24 supports the generic, and
the existing suites already pass unchanged because the tables are byte-for-byte
transcriptions of the switches (direct-key lookup == switch, missing key == default).

Conclusion: the tree is a flat, 7-leaf fan-out under the goal (no nesting). Leaves are
independent and trivially safe. Reverted the throwaway code and now execute leaf-by-leaf
so each rewrite lands AFTER a committed characterization test that pins the current
projection — the map is executed, not just drawn. Well within the ~12-node bound.

**Characterization reachability confirmed during exploration:**
- Container reachable via public API: Pending (new), Running (Start), Completed
  (Start+Complete), Failed (Start+Fail), Stopped (Stop-from-Pending), STOPPING
  (Start+Stop). INTERRUPTED + `default:` unreachable — preserved verbatim, not asserted.
- Storage reachable via transitions: all 5 (Start/Complete/Fail/Stop). `default:` is
  unreachable (lifecycle is always one of 5); the fallback arm is covered by the
  `ProjectStatus` primitive's own unit test instead.
- Route reachable via transitions: Planned (new), Executing (StartExecution), Completed
  (StartExecution+CompleteSegment), Failed (FailRoute). Stopped->ABORTED unreachable (no
  stop transition, no FromData) — preserved verbatim, not asserted.
