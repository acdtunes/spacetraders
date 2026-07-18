# Refactoring Lane Report — app-trading (RPP L1–L3)

Bead: sp-1z4q · Branch: `worktree-wf_80d457b9-963-7`

Scope (recursive, edit-authorized):
- `internal/application/trading/...` (commands, queries, services)
- `internal/application/ledger/...` (commands, queries)
- `internal/application/liquidation/...`
- `internal/application/probebuy/...`

Size surveyed: 35 production `.go` files, ~15k LOC (plus 79 test files).

## Headline

This scope is **exceptionally clean already**. Constants are extracted with rich
WHY-comments, magic numbers are named, files are split by topic, dead free-functions are
absent, and the long money-path functions are already written as linear guard-clause +
compose-method narratives. The high-value/low-risk surface for L1–L3 was therefore small.
Every applied change is provably behavior-preserving and was gate-verified. On a live
money-earning system I deliberately preferred a few certain wins over speculative
extraction of critical trading functions.

Tooling used to survey: `go vet`, `deadcode` (0 findings in scope), `golangci-lint`
(`unused`, `ineffassign`, `unparam`, `unconvert`, `misspell`), plus manual reads of the
densest functions in every package.

## Gate (final)

`cd gobot && go build ./... && go vet <scope> && go test -race -count=1 <scope>` — all exit 0.
6 packages `ok`, 0 failures, race clean. (`ledger/queries` has no test files.) Baseline was
green before any edit and remained green after each commit.

## L1 — Readability (committed: 3b531e5c)

Smells found:
- **Dead code (1, applied):** `var factoryCommandTypes` in
  `commands/run_worker_rebalancer_coordinator.go` — unused (`unused` linter + repo-wide
  grep confirm zero references). It is an orphan superseded by the identical map in the
  adapter layer (`adapters/grpc/container_ops_worker_rebalancer.go`), where factory-type
  filtering now happens before the `ActiveFactoryContainers` query DTO reaches the app
  layer. Deleted the var + its comment; bead ref `sp-f5pr` is preserved on the live
  `ActiveFactoryContainer` type directly below.
- **Magic numbers (2, applied):** the bare `Limit: 50` / `Limit: 500` on the two probe
  ledger scans in `probebuy/guarded_probe_buyer.go` → named `recentShipPurchaseScan` /
  `windowProbeSpendScan`, with a comment documenting the find-latest-vs-cover-window
  asymmetry. Same values → byte-identical queries.
- **Dead code (2, intentionally kept + recorded):**
  - `(*RunTradeRouteCoordinatorHandler).purchase` (`run_trade_route_coordinator_actions.go`)
    is unused, but it is the deliberate plain-buy twin of the *used* `sell()` wrapper and
    a sibling comment references it. Kept for API symmetry/readability.
  - `const defaultMaxLightsPerSystem = 0` (`run_worker_rebalancer_coordinator.go`) is
    unused, but it carries the only prose explaining the `max_lights_per_system` knob's
    default semantics; deleting it would lose live documentation. Kept.

Not-smells (verified, left alone): all other numeric literals are either in named const
blocks, the universally-understood `3600` s/hr conversion, or documented API error codes
(`4000`). `misspell`, `unconvert`, `ineffassign` reported nothing in scope. No redundant
WHAT-comments were stripped: the few section-marker comments in long handlers (e.g.
`record_transaction.go`) aid navigation and the repo has a strong, load-bearing comment
culture, so removing them was judged net-negative.

## L2 — Complexity (committed: 3e0124d8)

Smells found:
- **Duplicate code / builtin shadow (1, applied):** `minInt(a,b int) int` in
  `services/tour_deposit_candidates.go` duplicated Go's builtin `min` (already used
  elsewhere, e.g. `run_stocker_coordinator.pick`; `go.mod` = 1.24). Flattened the single
  call site `minInt(x, minInt(y, z))` → `min(x, y, z)` (identical result; `min` is
  associative/variadic) and deleted the helper.

Assessed and deliberately NOT changed (recorded rationale):
- **Long functions** `run_tour_coordinator.execute` (372 LOC), `run_trade_route_coordinator.execute`
  (247), `flyVisits` (222), `run_stocker_coordinator.pick` (206), `run_arb_coordinator.execute`
  / `guardAndBuy` (200/198). These are already top-guard-clause + flat filter-funnel
  narratives with per-block WHY-comments and counter/`continue` bookkeeping that feeds
  their verdict logs. Extracting sub-blocks would fragment the narrative and thread the
  counters awkwardly — it would *reduce* clarity, and it touches live trading money paths.
  Left as-is; noted as an L4 candidate for a dedicated, Mikado-guarded pass if ever wanted.
- `unparam` hits: nearly all are test helpers (a param that always receives one value — a
  readability choice, not a smell). The two production hits (`planAndReserve`'s always-nil
  error; `freshListings`'s always-`maxListingAge` arg) are signature changes that ripple to
  tests and are defensible convention/future-proofing — skipped.

## L3 — Responsibilities

No within-package edits warranted.
- **God-file split:** already done. The tour coordinator is spread across 8 topic files;
  the trade-route coordinator across ~7 (`_lanes`, `_circuit`, `_travel`, `_actions`,
  `_guards`, `_log`, ...). The residual large files (`run_tour_coordinator.go` 2305,
  `run_stocker_coordinator.go` 1264) hold cohesive core lifecycle methods; further
  same-package splitting would not raise cohesion.
- **Feature envy / dedup within a package:** shared logic is already extracted to
  package-level helpers used widely (`freshListings` 7 files, `rankLanesByCircuitRate` 10,
  `laneCircuitRatePerHour` 4). No copy-paste blocks found to consolidate within a package.
- `residualKnobs` in `warehousecap.go` is a textbook already-applied extraction ("so the
  source-side and receipt adapters resolve the ramp identically and can never drift").

## Pristine packages (no edits, or minimal)

- `ledger/commands`, `ledger/queries` — pristine (named constants, guard clauses,
  load-bearing WHY-comments; single-writer balance derivation is well-documented).
- `liquidation` — pristine.
- `probebuy` — pristine (only the 2 query-limit constants added).
- `trading/queries` — pristine (read-only lane reader, well-commented fail-closed semantics).

## Counts

- Commits: 2 code (L1, L2) + this report.
- Production files edited: 3 (`guarded_probe_buyer.go`, `run_worker_rebalancer_coordinator.go`,
  `tour_deposit_candidates.go`).
- Net: −1 dead var, −1 redundant helper func, +2 named constants, 1 call simplified.
- Test files: unchanged (no assertions/values/cases altered).

## L4–L6 candidates (NOT applied — architectural, for a later Mikado pass)

1. **L4 — cross-package trivial-helper duplication.** `derefString` is defined identically
   in both `trading/queries/profitable_lane_reader.go` and `trading/services/tour_snapshot.go`;
   sibling string-set helpers (`toSet`, `stringSet`, `dedupeStrings`) recur across
   queries/services/commands. Consolidating into `pkg/utils` (already a dependency) would
   remove the drift risk. Cross-package → out of L1–L3 scope.
2. **L4 — decompose `run_tour_coordinator.execute` (372 LOC) and the 2305-LOC file.** The
   continuous-run orchestration vs. per-tour execution could be separated, but only under a
   dedicated Mikado plan given the money-path risk.
3. **L5 — parallel continuous-run coordinator scaffolding.** arb/tour/stocker/trade-route
   coordinators each re-implement the same shape: starvation limit, `*Exit*` reason enum,
   operation-context stamping, and the -1/N/0 iteration loop. A shared Template-Method /
   loop strategy could reduce this structural duplication. Package: `trading/commands`.

## Suspected bugs (RECORDED ONLY — not fixed)

1. **`(*RunTradeRouteCoordinatorHandler).repositionNeighborsWithinJumps`
   (`run_trade_route_coordinator_lanes.go:287`)** returns a second value `originReason`
   whose doc says it is "threaded back unchanged for the caller's empty-discovery
   diagnostics and stranded detector," yet all three callers discard it with `_`
   (`run_tour_coordinator_reposition.go:408,469`, `run_tour_coordinator_candidates.go:82`).
   Either a latent diagnostic gap (callers should surface the reason but don't) or a stale
   comment. Left untouched — resolving it would either change behavior (surface the reason)
   or reword a WHY-comment, both outside a behavior-preserving sweep.
