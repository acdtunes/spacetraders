# Refactoring lane report — app-contract

- **Lane:** app-contract
- **Scope (recursive):** `gobot/internal/application/contract/...`
- **RPP range:** L1–L3 (behavior-preserving quality sweep)
- **Branch:** worktree-wf_80d457b9-963-8
- **Baseline:** origin/main (dc64793), fully green
- **Gate:** `go build ./... && go vet ./internal/application/contract/... && go test -race -count=1 ./internal/application/contract/...` — all exit 0
- **Commits:** 2 (one L1, one L3)
- **Production files touched:** 2 of ~30

## Headline

This is an exceptionally clean, mature codebase. Nearly every file is already
fully decomposed into small, intention-named functions; magic numbers are
extracted into documented named constants; and the comment culture is
load-bearing (WHY-comments + `sp-xxxx` bead references everywhere). The sweep
therefore found **very few** genuine L1–L3 wins — most files are pristine and
were deliberately left untouched (per "skip clean files; prefer small safe
wins"). No file was churned for its own sake.

Sub-packages surveyed and their state:

| Package | State |
|---|---|
| `contract/` (root) | pristine except 1 L3 dedup (fixed) |
| `contract/commands/` | pristine except 1 L1 misplaced-doc (fixed) |
| `contract/services/` | pristine |
| `contract/queries/` | pristine |
| `contract/depotstore/` | pristine |
| `contract/types/` | pristine |

## L1 — Readability

**Smells found:** 1.

- **Misplaced/orphaned doc comment** in `commands/run_fleet_coordinator.go`.
  The doc block for `calculateInFlightCargo` had drifted (botched-merge artifact)
  to sit *above* `scopeCandidatesToContractHome`, so that function carried an
  irrelevant in-flight-cargo doc stacked above its own, while
  `calculateInFlightCargo` had no doc at all.

**Change applied:** relocated the `calculateInFlightCargo` doc block down to its
actual function. Pure comment move — every load-bearing WHY/bead line preserved
verbatim; zero code/behavior change. (commit `refactor(L1)`)

**Deliberately NOT changed (comment hygiene):** a handful of tiny step-comments
that lightly restate the next line (e.g. `// Get system graph` above
`GetGraph(...)` in `balance_ship_position.go`). These are part of a *consistent,
intentional* house style used throughout the codebase; piecemeal removal would
add inconsistency and churn with no real readability gain, and risks the strong
comment culture the lane rules protect. No dead code, `TODO/FIXME/HACK/XXX`
markers, or unclear identifiers were found (grep-verified: zero tech-debt
markers in production files).

## L2 — Complexity

**Smells found:** 1 genuine, not safely actionable in a behavior-preserving sweep.

- **`RunFleetCoordinatorHandler.Handle`** (`commands/run_fleet_coordinator.go`)
  is a ~580-line infinite reconcile loop — a real Long Method. **Not extracted:**
  the loop body closes over many mutable loop-carried variables (`result`,
  `errMon`, `gov`, `liquidationCooldown`, `activeWorkerContainerID`,
  `previousShipSymbol`, `workerCompletedCh`), so any extraction is a state-object
  refactor (L4), not a mechanical L2 Extract-Function. Given this is a live
  money-earning coordinator and the mandate is provably behavior-preserving, the
  risk/reward said leave it. Recorded as an L4 candidate below.

No other long methods warranted extraction: the large files (`idle_arb.go` 1281,
`services/delivery_executor.go` 886) are already composed of small, single-purpose
methods.

**Changes applied:** none (none warranted).

## L3 — Responsibilities

**Smells found:** 1.

- **Duplicated fleet-snapshot builder** within `contract/ship_pool_manager.go`:
  `FilterUnrelatedCargo` and `FilterToHomeSystem` each built an identical
  `FindAllByPlayer -> map[symbol]*Ship` snapshot with the same
  `"failed to fetch ships: %w"` wrap.

**Change applied:** extracted one unexported `fleetBySymbol(ctx, playerID,
shipRepo)` helper (same package) and pointed both callers at it. The propagated
error text and the returned map are byte-identical, so behavior is preserved;
net −8 lines of copy-paste. (commit `refactor(L3)`)

## Cross-package / architectural candidates (L4–L6 — recorded, NOT edited)

1. **Overlapping contract-lifecycle services (L4/L6).**
   `services/ContractLifecycleService` and `services/ContractMarketService` both
   orchestrate negotiate/accept/fulfill and carry a byte-identical `FulfillContract`
   method (plus near-identical accept paths). Which service owns the lifecycle is
   an architectural decision (merge or clearly split responsibilities) — out of
   scope for a within-package sweep; Mikado-gated.

2. **`RunFleetCoordinatorHandler.Handle` god-method (L4).**
   The ~580-line loop orchestrates discovery, orphan-reclaim, negotiation,
   sourcing-defer, home-locality scoping, cargo filtering, auto-liquidation,
   depot routing, spawn, homing and completion. Decomposing cleanly needs a
   per-pass state object (extract loop-carried vars into a struct) — an L4
   Parameter/State-Object refactor, too invasive to do behavior-preservingly here.

3. **Cross-package fleet-snapshot/candidate-filter duplication (L4).**
   The "FindAllByPlayer + build symbol set + filter candidates" shape recurs in
   `ship_selector.go` (`SelectClosestShip`), `commands/rebalance_fleet.go`
   (`getCoordinatorShips`) and the now-deduped `ship_pool_manager.go` filters —
   but across package boundaries (contract root vs commands). Unifying needs a
   shared fleet-snapshot abstraction and a cross-package move (L4), Mikado-gated.

## Suspected issues (report only — NOT fixed)

- **Inconsistent log-level string** in `services/delivery_executor.go`: the
  fail-open telemetry paths use both `logger.Log("WARN", ...)` (3×) and
  `logger.Log("WARNING", ...)` (4×). Not a functional defect, but since ops greps
  log output, a `"WARN"` line could be missed by a `"WARNING"` filter. **Left
  untouched** because changing emitted log-level strings is explicitly out of
  scope for this sweep (ops greps them); flagging for the code owner to normalize
  intentionally.

No logic bugs were observed; the code is uniformly careful (fail-open/never-skip
invariants, atomic claims, CAS-retry releases are all consistently applied).
