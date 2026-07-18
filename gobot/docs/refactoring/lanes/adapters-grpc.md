# Refactoring Lane Report — adapters-grpc

Lane key: `adapters-grpc`
Scope (recursive, edit-permitted): `gobot/internal/adapters/grpc/...`
RPP range: L1–L3, behavior-preserving only.
Baseline: `origin/main` green — `go build ./... && go vet ./internal/adapters/grpc/... && go test -race -count=1 ./internal/adapters/grpc/...` all exit 0 (tests ~17s).

## Executive summary

This is a **large (~63k LOC, ~70 production files) and already highly-polished lane.**
The team demonstrably practices RPP L1–L3 discipline continuously:

- All operational thresholds are **config-driven** (`RULINGS #5` — "the daemon never
  hardcodes the operational values"); a scan for magic numbers in conditionals found
  **zero**.
- Boundary predicates are already **extracted and named** (e.g. `capacityReconcilerDryRun`,
  `flowRemovalWanted`, `isTransientClaimError`), config-key lists are named vars. Shared
  launch tails (`startContainerRunner`, `findContainerModelByID` in `container_launch.go`)
  were factored by **this branch's prior refactoring pass** — see "Provenance" below.
- Guard clauses / early returns are the norm; deep nesting exists only inside genuinely
  complex state machines (`container_runner.execute`, `daemon_server`) and is heavily
  justified by load-bearing WHY/bead comments.
- **No dead code**: `deadcode ./...` flagged 10 grpc funcs, but every one is exercised by
  a test in-package (deadcode ignores `_test.go` reachability) — none are removable.

Net: genuine, safe L1–L3 wins are **sparse**. The clearest ones cluster in
`ship_state_scheduler.go`, which were applied. The rest of the lane is reported pristine.

## Provenance (two runs on this branch)

This worktree branch was resumed from a prior, interrupted run of this same lane (its
commits were already present at this session's start HEAD `fd7a7faa`). To avoid
misattribution, the split is:

- **Prior pass (4 commits, already on branch):** `refactor(L1)` remove redundant WHAT
  step-marker comments; `refactor(L2)` flatten a nested type-match into a named predicate;
  `refactor(L3)` ×2 extract shared `startContainerRunner` (launch tail) and
  `findContainerModelByID` (ListAll-then-scan) into `container_launch.go` and apply across
  ~27 coordinator-launch sites (biggest dedups: `container_ops_gas.go` −71,
  `container_ops_scouting.go` −74, `container_ops_ship.go` −67).
- **This session (3 commits):** the two `ship_state_scheduler.go` changes below + this report.

Both passes are on the branch, green, and behavior-preserving (final gate below).

## Changes applied — this session

### L1 — Readability (1 commit)
`ship_state_scheduler.go`: the file establishes a "name your durations with a doc comment"
convention at the top (`ClockDriftBuffer`, `SweeperInterval`) but left three
`context.WithTimeout` values inline as magic numbers. Brought them into the convention:

- `shipStateWriteTimeout = 10 * time.Second` (per-ship arrival/cooldown write; 2 sites)
- `stuckSweepTimeout = 30 * time.Second` (batch sweeper pass; 1 site)

Values byte-identical; behavior-preserving.

### L2 — Complexity (no commit)
**No safe wins found.** The long functions in this lane fall into two buckets, neither
safely extractable:
- **Config/wiring builders** (`StartTourRun` 171L, `NewDaemonServer` 390L, the
  `injectXxxConfig` functions): dominated by a single map/struct literal or a flat list of
  independent `if knob != 0 { config[k]=… }` guards. There is no cohesive sub-block to
  extract, and every line carries a load-bearing bead comment.
- **State machines** (`container_runner.execute`, `signalCompletionWithStatus`): the
  nesting is intrinsic control flow (restart-backoff / honest-completion) with continue/
  return interleaved and extensively documented. Extraction would obscure, not clarify, and
  risks behavior change in a live money path. Left intact.

### L3 — Responsibilities (1 commit)
`ship_state_scheduler.go`: the arrival and cooldown-clear `SaveWithRetry` mutation closures
were **copy-pasted** between the event-driven handlers (`handleArrival`,
`handleCooldownClear`) and the batch `sweepStuckShips`. Extracted to shared unexported
helpers `arriveIfInTransit` / `clearCooldownIfSet`; all four call sites now share one body
(making the sweeper's existing "same as the event-driven handler" comment literally true).
Identical logic; `sp-01wc` WHY-comments preserved at every call site.

## Counts

| Metric | This session | Cumulative branch (vs origin/main) |
|---|---|---|
| Commits | 3 (L1, L3, report) | 7 (6 refactor + report) |
| grpc production files touched | 1 (`ship_state_scheduler.go`) | ~28 |
| Levels applied | L1, L3 | L1, L2, L3 |
| Gate | build 0 / vet 0 / `go test -race` ok (~17s) after every commit | green at HEAD |

Note: a repo-root `issues.jsonl` / `.beads/issues.jsonl` (the `bd` issue-tracker state)
shows as modified in the branch/worktree. It is **outside this lane's scope** (not under
`gobot/`) and was **not authored by this session** — left untouched. Flagged so the
orchestrator can decide whether the prior run should have excluded it from a refactor commit.

## Smells surveyed and deliberately NOT changed (rationale)

- **Repeated config-map keys** (`"coordinator_id"` ×21, `"working_capital_reserve"` ×11,
  `"tick_interval_secs"` ×9, …): a string-keyed persistence protocol whose write side
  (`container_ops_*.go`) and read side (`command_factory_registry.go` `configReader`) must
  agree byte-for-byte. Extracting shared constants would be behavior-preserving and a mild
  drift-guard, but it is a **21+-site cross-file change for uncertain value** — deferred as
  too large/risky for a quality sweep on a live system.
- **Repeated error literal** `fmt.Errorf("unexpected response type")` ×11 in
  `container_ops_queries.go`: a sentinel would DRY it, but the codebase uses **zero
  `errors.New`** (consistent `fmt.Errorf` style) and error strings are **load-bearing**
  (note the deliberate `"ship_symbol is required"` vs `"ship symbol is required"` split —
  proto-field vs human-facing; ops greps these). Introducing a foreign pattern / churning
  error construction is not worth the small gain. Left as-is.
- **Redundant WHAT-comments** (`// Create query`, `// Execute via mediator`,
  `// Convert response` in `container_ops_queries.go`): technically L1-removable, but given
  the codebase's explicit strong-comment culture they read as intentional structural rhythm;
  purging them is low-value churn. Left as-is.
- **Parallel coordinator-launch methods** (`FrontierExpansionCoordinator`,
  `MarketFreshnessSizerCoordinator`, `WorkerRebalancerCoordinator`, …): near-identical by
  design and documented as "mirroring X". Deduplicating fights the intended
  recovery-safety parallelism → see L4–L6 candidate #1.

## L4–L6 candidates (architectural — NOT done here; for later Mikado)

1. **[L5] Coordinator config quadruplet → per-type descriptor/registration.**
   Every standing coordinator repeats a four-part parallel structure —
   `injectXxxConfig` / `resolveXxxConfig` / `buildXxxCommand` / `xxxConfigKeys` — plus a
   `command_factory_registry.go` switch arm and boot-list membership. Adding/renaming a
   coordinator is Shotgun Surgery across `internal/adapters/grpc` + `internal/application`.
   A per-type descriptor (config-keys + inject + build in one registered value) could
   collapse the duplication. Deliberately parallel today for creation/recovery symmetry, so
   it needs careful Mikado sequencing.
   Packages: `internal/adapters/grpc`, `internal/application`.

2. **[L4] Generic mediator send+type-assert helper.**
   The `resp,err := mediator.Send(…); if err {…}; typed,ok := resp.(*T); if !ok
   {"unexpected response type…"}` dance repeats across `container_ops_queries.go` (×11),
   `daemon_service_impl.go` (most handlers), and `container_ops_auto_outfit.go`. A Go-generic
   `sendTyped[R](ctx, med, msg) (R, error)` would collapse send+assert into one checked call
   while leaving each handler's proto mapping intact. Duplicate Code / boundary noise.
   Package: `internal/adapters/grpc` (helper could live in `internal/application/common`).

3. **[L6] `DaemonServer` god-object (SRP).**
   ~50+ launch/query methods hang off `*DaemonServer` across dozens of `container_ops_*.go`
   files; `daemon_service_impl.go` (2006 LOC) and `daemon_server.go` (1720 LOC) are the two
   largest files in the lane. Decomposing into cohesive sub-services (ship ops / container
   lifecycle / depot / scouting / trading / capacity) would shrink the god-object and the
   two mega-files. Mikado-scale; high churn on actively-developed files, so explicitly
   deferred out of an L1–L3 sweep. Package: `internal/adapters/grpc`.

## Suspected bugs (report only — NOT fixed)

None observed. The `daemon_service_impl.go` TODO stubs (`EstimatedTimeSeconds: 0`,
`FuelAdded: 0`, `CreditsCost: 0`, log-retrieval, default-system) are **documented
incomplete wiring**, not defects — left untouched (WHY/TODO comments are load-bearing).
