# Lane report: app-manufacturing (RPP L1-L3)

Scope: `internal/application/manufacturing/...` (packages: `services`, `commands`, `types`).
Bead: sp-1z4q. Range: L1-L3, behavior-preserving.

## Verdict

The lane is **near-pristine**. This is a mature, heavily-bead-refactored, live money-earning
codebase with an exceptionally strong maintenance culture: essentially every magic number is a
named constant carrying a WHY-comment, functions are decomposed into intention-revealing helpers,
guard clauses / early returns are used throughout, pure functions are extracted and labelled, and
per-run config rides `ctx` (never struct fields) with the singleton-race reasoning documented at
each site. ~12,000 of ~15,000 production lines were read directly; the remainder were grepped for
every major smell signature (magic numbers, complex booleans, dead code, debug prints, builtin
shadows). Baseline was green (`go build ./...`, `go vet`, `go test -race ./...manufacturing/...`
all exit 0) and stayed green after every change.

Because the code is this clean, the disciplined outcome is a **small number of genuinely safe
wins** plus a candidate/observation ledger — not manufactured churn (which the behavior-preserving,
live-money constraints explicitly warn against).

## Changes applied

### L1 Readability (1 commit)
- **`commands/run_siting_coordinator_score.go`** — renamed `breachesInputCap`'s parameter
  `cap` -> `maxChains`. `cap` shadowed the predeclared `cap` builtin (the package uses the
  `min`/`max`/`cap` builtins elsewhere), and `maxChains` reveals the value's role (the
  per-input-market concentration limit). Unexported helper, positional call site unchanged.

### L2 Complexity (1 commit)
- **`services/market_locator.go`** — extracted the named predicate `isModeratePlusSupply(supply)`
  and applied it at the two **pure-boolean** MODERATE+ eligibility sites (`EligibleSourceMedianAsk`,
  `InputSourceEligibility`), replacing the dense inline arithmetic
  `manufacturing.SupplyLevel(s).Order()-manufacturing.SupplyLevelLimited.Order() >= 1`. The
  in-code comments already called this "the identical eligibility filter as the sibling locators",
  so naming it removes duplicated intent-obscuring arithmetic. The predicate wraps the **exact**
  comparison (behavior-preserving, including the unknown/empty-string semantics of `Order()`). The
  third site (`FindExportMarketBySupplyPriority`) was left unchanged: it needs the numeric
  `supplyScore` for ranking, so only its guard, not the computation, is equivalent — not worth a
  redundant predicate call there.

### L3 Responsibilities (surveyed, no safe change)
No behavior-preserving within-package L3 win was found:
- The two large files (`services/production_executor.go` ~1940, `commands/run_factory_coordinator.go`
  ~1960) are cohesive single-type files, and the package is already de-facto split by concern
  (`input_price_ceiling.go`, `input_source_selector.go`, `feeding_policy.go`, `unified_gate_fill.go`
  all hold `ProductionExecutor` helpers) — no god-file to split.
- The one real copy-paste (the 8-way market-scan scaffolding in `market_locator.go`) cannot be
  deduplicated without changing per-call error strings ops grep — recorded as a candidate below.
- No feature-envy or misplaced-responsibility within packages worth relocating.

## Counts
- Commits: 3 (L1, L2, this report).
- Production files touched: 2 (`run_siting_coordinator_score.go`, `market_locator.go`).
- Test files touched: 0 (no test change needed; assertions untouched).
- Net: +12 / -4 lines of production code (excluding this report).
- Gate after each commit: `go build ./...` = 0, `go vet ./...manufacturing/...` = 0,
  `go test -race -count=1 ./internal/application/manufacturing/...` = 0 (commands + services `ok`,
  types has no tests).

## Suspected bugs (reported only — NOT fixed)
1. **`services/market_levels.go` `shortID(id string) string { return id[:8] }`** — no length
   guard; panics if `id` has fewer than 8 characters. Used pervasively for log metadata across
   `task_activator.go`, `replenishment_planner.go`, `factory_supply_poller.go` (30+ call sites).
   Safe in practice because the inputs are UUIDs / long pipeline & task IDs, but a malformed/short
   id would crash the calling goroutine. A guarded `if len(id) < 8 { return id }` would be safer,
   but that changes emitted log values, so it is out of scope for a behavior-preserving sweep.

## Observations (out of scope for L1-L3, flagged for a product decision)
1. **`services/collection_opportunity_finder.go` debug prints** — ~14 `fmt.Printf("[CollectionFinder]
   ..." / "[StorageOpportunityFinder] ...")` statements write directly to **stdout**, inconsistent
   with the structured `common.LoggerFromContext(ctx).Log(...)` used everywhere else in the lane.
   They read as leftover debugging. Converting or removing them changes stdout output (which ops may
   grep) and is therefore a behavior change, not a refactor — needs an owner decision, then a
   normal TDD change.
2. **`commands/run_factory_coordinator.go:147` `FactoryWorkerCapProvider.WorkerCap(...) (cap int, ...)`**
   — the named return `cap` shadows the builtin. Left untouched: it is a documentation-only name in
   an **exported** interface implemented in another package; a rename is technically safe (interface
   satisfaction ignores names) but low value and cross-package-visible.
3. **`commands/run_construction_coordinator.go` `enqueueReplenishmentIfNeeded`** — inline `3`
   (retry count) and `15*time.Second` (create timeout). Extractable to named constants, but the `3`
   is echoed verbatim in two log strings ("attempt %d/3", "after 3 attempts") ops may grep, so
   extraction couples a const to log literals for marginal gain. Left as-is.

## L4-L6 candidates
See the structured `l456Candidates` field. Summary:
1. **(L4) market_locator.go scan/rank duplication** — 8 functions (`FindExportMarket`,
   `FindExportMarketBySupplyPriority`, `EligibleSourceMedianAsk`, `InputSourceEligibility`,
   `FindConstructionSource`, `FindExportMarketWithGoodSupply`, `FindBestExportMarket`,
   `FindFactoryForProduction`) repeat `FindAllMarketsInSystem` -> per-waypoint `GetMarketData` ->
   `FindGood` -> nil-guard. A shared internal market-scan iterator (callback per
   `(waypoint, tradeGood)`) + a small ranking helper would remove the copy-paste. Deferred from
   L1-L3 because each `FindAllMarketsInSystem` call wraps a **distinct, ops-greppable error string**
   and the bodies mix early-return with candidate accumulation — a safe extraction must preserve
   every error string and both control-flow shapes.
2. **(L5) primitive-obsession on supply/activity** — supply and activity flow through the whole
   `services` layer as raw `string`, compared against the local constant mirror in `market_levels.go`
   (`supplyScarce`, `activityWeak`, ...) that parallels the domain value objects
   `manufacturing.SupplyLevel` / `market.ActivityLevel`. Threading the typed value objects through
   the application layer would remove the const mirror and the `supplyOrEmpty` / `supplyOrModerate` /
   `activityOrEmpty` string shims (Object Calisthenics rule 3, at the app layer). Large, spans many
   files and the domain boundary — genuinely architectural.
