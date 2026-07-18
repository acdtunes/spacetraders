# Refactoring Lane Report: domain-fleetops (sp-1z4q)

RPP pass L1-L3 over `internal/domain/{navigation, shared, capacity, storage, system,
scouting, routing, placement, ports, player, dutycycle, daemon, container, captain,
buffer, apibudget, absorption, telemetry}`. Behavior-preserving only; every commit
gated on `go build && go vet && go test -race` over the full lane scope (all green).

## Overall assessment

This domain tree is in unusually good shape. The newer packages — capacity (planner,
ladder, emitter, signals), scouting, placement, buffer, absorption, telemetry,
dutycycle, apibudget, routing, captain — are pristine: small intention-named
functions, named constants, exhaustive WHY/bead comments, deterministic ordering
documented at every sort. They were surveyed and deliberately left untouched.
The smells found concentrate in the older navigation/storage/container/daemon files.

## L1 — Readability (1 commit)

Smells found / fixed:
- Misleading local name: `shared/flight_mode.go` `TravelTime` used `time` as a local
  variable (shadows the stdlib package name mentally; the package is not imported
  there, but the name misleads). Renamed to `travelSeconds`.
- Builtin-shadowing param: `daemon/health_monitor.go` `SetMaxRecoveryAttempts(max int)`
  shadowed the Go builtin. Renamed to `attempts`.
- Stale doc comment: `navigation/ship_fuel_service.go` `ShouldPreventDriftMode` doc
  listed a `segment` parameter that no longer exists (signature takes
  `segmentFlightMode` + `fromWaypointHasFuel`). Doc updated to match the signature.
- Redundant WHAT-comments that literally restate the next line removed in
  `container/container.go`, `container/ship_assignment.go`, `storage/storage_ship.go`
  (5 comments total). All WHY/bead comments preserved verbatim.
- Dead code scan: no unused unexported functions or constants found in any lane
  package (scripted reference count). Exported dead code found — see candidates.
- Magic numbers: none needing extraction; policy constants are already named and
  documented (capacity, scouting) or carried in config per RULINGS #5.
- gofmt: fixed pre-existing formatting drift in files touched (const-block alignment
  in container.go, doc-list reflow in operation.go). Note: three files OUTSIDE this
  lane also fail `gofmt -l` on Go 1.26 (`domain/contract/source_predictor_test.go`,
  `domain/outfitting/selection_test.go`, `domain/trading/trade_circuit_test.go`) —
  left for their lanes.

## L2 — Complexity (1 commit)

- `storage/operation.go`: `NewStorageOperation` / `NewWarehouseOperation` duplicated
  the identity validation (id / playerID / waypoint, identical messages) and the
  3x slice-copy pattern. Extracted `validateOperationIdentity` + `copyStrings`.
  Error strings byte-identical; the warehouse-specific messages and the deliberate
  empty-extractors comment kept.
- `apibudget/report.go`: 85-line `ComputeReport` had a distinct 30-line per-hull
  stanza (stats build + deterministic sort + hulls-to-ceiling arithmetic). Extracted
  `perHullBreakdown`; behavior identical including the 0-not-+Inf guard.
- `container/ship_assignment.go`: `Release` / `ForceRelease` duplicated the 5-line
  release-stamp body. Extracted `markReleased`.
- Considered and skipped (already clear): `dutycycle.ComputeReport` (linear
  accumulate-then-sort), `heuristic_planner.go` (already composed), `ladder.go`
  (already composed), `Container.Status()`/`StorageOperation.Status()` mapping
  switches (see candidate 3).

## L3 — Responsibilities, within-package (1 commit)

- `navigation/ship.go` was a 946-line god-file. Split along its own section banners
  into cohesive files (pure moves, all code/comments verbatim, zero signature
  changes):
  - `ship.go` (532): identity, invariants, validation, getters, nav state machine,
    fuel management, state queries.
  - `ship_assignment_ops.go` (150): Assignment Management — container claims and
    the sp-i1ku captain-reservation family.
  - `ship_state_sync.go` (147): the DB-as-Source-of-Truth section — flight mode,
    arrival/cooldown, sp-vp9k transit origin, repository setters.
  - `ship_reconstruct.go` (84): `ReconstructShip` + the sp-60ff
    PersistedVersion/SetPersistedVersion pair.
  - `ship_cargo_reservation.go` (+53): Ship's sp-1vhv reservation-override methods
    moved next to `IsDefaultReservedCargo`, so the whole do-not-sell feature lives
    in one file.
- No within-package feature envy or copy-paste duplication found elsewhere;
  `storage/operation.go`'s DTO block (ToData/FromData) was considered for a file
  split and left — the file is 400 lines and cohesive.

## Counts

- Commits: 4 (L1, L2, L3, report)
- Production .go files modified: 9; new files created: 3
- Net: ship.go -414 lines into 3 new cohesive files; 3 helpers extracted; 2 renames;
  1 doc fix; 5 WHAT-comments removed

## L4-L6 candidates (report only — NOT touched)

1. Safe-delete dead scaffolding (L4): `daemon/HealthMonitor` (entire file — its
   stuck-ship detection `isShipStuck` is a stub that always returns false, TODOs
   unresolved, zero references anywhere in gobot outside its own package, and the
   package has no tests) and `container/ShipAssignmentManager` (in-memory manager,
   zero references — the live path is the DB-backed
   `adapters/persistence/ship_assignment_repository.go`). Both exported, so removal
   needs a repo-wide decision + wiring audit, not a lane edit.
2. Duplicate ship-assignment model across packages (L4): `navigation.ShipAssignment`
   (immutable VO, sp-i1ku owner field) vs `container.ShipAssignment` (mutable entity
   with clock) plus duplicated `AssignmentStatus` consts ("active"/"idle") in both
   packages. Consolidating is a cross-package move with persistence-layer impact.
3. Parallel lifecycle-status mapping switches (L5): `container.Container.Status()`,
   `storage.StorageOperation.Status()`, and `navigation.Route.Status()` each
   hand-map `shared.LifecycleStatus` to a package-local status enum with the same
   switch shape. A shared mapping abstraction on `LifecycleStateMachine` would
   collapse three parallel switches; touching `shared` + three consumers is beyond
   within-package scope.

## Suspected bugs (observed, NOT fixed — behavior preserved)

1. `shared/flight_mode.go`: `flightModeConfigs` gives DRIFT `TimeMultiplier` 26 vs
   CRUISE 31 — DRIFT computes FASTER travel than CRUISE, contradicting its own
   "Slow, minimal fuel" comment and SpaceTraders API semantics (DRIFT is the slow
   mode). Anything estimating travel time for DRIFT legs from this table
   under-estimates badly. Suspect stale calibration constants.
2. `navigation/ship_cargo.go`: `ReceiveCargo` and `RemoveCargo` discard the error
   from `shared.NewCargo` (`newCargo, _ :=`). If reconstruction ever failed
   (inventory-sum mismatch), the ship's cargo would silently become nil.
3. `daemon/health_monitor.go` `AttemptRecovery`: once the attempts cap is reached it
   increments `AbandonedShips` on EVERY subsequent call (metric inflation), and the
   recovery body is a stub. Moot if candidate 1 (dead code) is confirmed and deleted.
4. `container/container.go` `ResetForRestart` calls `CanRestart` (requires FAILED),
   but `Container.Stop()` on a PENDING container calls `lifecycle.Stop()` while
   `Status()` still reports PENDING-derived states — harmless today, but the
   STOPPING flag/lifecycle duality is easy to desync; noted for the L4+ pass.

## Verification

Final gate (after report commit): `go build ./... && go vet ./... &&
go test -race -count=1 <all 18 lane package trees>` — exit 0, 11 test packages ok,
7 with no test files. Byte-for-byte behavior preservation asserted by: renames of
locals/params only, comment-only edits, verbatim code moves, and helper extractions
with identical error strings and arithmetic.
