# Lane report: app-ship (RPP L1-L3)

Scope (recursive): `internal/application/ship/...`, `internal/application/shipyard/...`,
`internal/application/fleet/...`, `internal/application/autooutfit/...`
(~12.7k LOC production, ~25k with tests).

Bead: sp-1z4q. Branch: `worktree-wf_80d457b9-963-9`.

## Headline

This scope is **already very mature and heavily refactored**. The team practices
aggressive Extract-Method, narrow ports, named constants, and load-bearing WHY/bead
comments throughout. Handlers are thin orchestrators delegating to well-named
single-responsibility helpers (e.g. `route_executor.go`, `navigate_route.go`,
`purchase_ship.go`, `outfitting.go`, `fleet_autosizer_*.go` are exemplary). Consequently
the safe L1-L3 wins were **sparse and small**. Per the "many small safe wins over few
risky large ones" directive, I applied only provably behavior-preserving changes and
recorded the larger opportunities as candidates rather than editing them.

Baseline was fully green (build + vet + `go test -race` across all four clusters) and
remained green after every commit.

## L1 — Readability (applied)

Smells found:
- **Builtin shadowing** (misleading names): local/param names shadowing Go predeclared
  identifiers.
  - `internal/application/ship/market_scanner.go`: `pricesChanged(old, new *TradeGood)` —
    `new` shadows the builtin. The sole caller already uses `oldGood`/`newGood` locals.
  - `internal/application/fleet/commands/fleet_autosizer_guards.go`: local `cap` in
    `guardPriceCeiling` and `guardTreasuryPct` shadows the `cap` builtin (2 sites).

Changes applied (commit `refactor(L1)`):
- `market_scanner.go`: `pricesChanged` params `old,new` -> `oldGood,newGood` (+ all body refs).
- `fleet_autosizer_guards.go`: `cap` -> `premiumCap` (guardPriceCeiling), `cap` -> `treasuryCap`
  (guardTreasuryPct).

Counts: 2 files, ~3 identifiers renamed. Local-scope only; no signature/logic/output changes.

Note on comment hygiene: the strong comment culture here is almost entirely WHY/bead
documentation (load-bearing) — I found no redundant WHAT-comments worth removing except the
5 `// Record ... scan in metrics` lines, which were subsumed by the L2 extraction below.

## L2 — Complexity / duplication (applied)

Smells found:
- **Duplicate Code**: `MarketScanner.ScanAndSaveMarket` repeated the identical
  `if collector := metrics.GetGlobalMarketCollector(); collector != nil { collector.RecordScan(...) }`
  block **5 times** (4 error paths + success path).

Changes applied (commit `refactor(L2)`):
- `market_scanner.go`: extracted `recordMarketScanMetric(playerID, waypointSymbol, startTime, scanErr)`;
  replaced all 5 sites. `ScanAndSaveMarket` shed ~15 lines. Behavior-preserving: identical
  collector call at each site; `time.Since(startTime)` still measured at the same logical point;
  the global collector is nil in unit tests so the block was already a no-op there.

Counts: 1 file, 1 helper extracted, 5 call sites collapsed.

## L3 — Responsibilities (surveyed; no low-risk edits)

Surveyed for god-files, feature envy, within-package copy-paste, and responsibility bloat.
Findings:
- **No god-files.** Largest production file is `route_executor.go` (914 LOC) but it is highly
  cohesive around `RouteExecutor` and already decomposed into ~20 focused helpers.
- **No clear feature envy** — data and behavior are well co-located.
- The fleet demand providers (`fleet_autosizer_{lights,heavies,explorer,warehouse}.go`) share a
  parallel *shape* (Class/Demand/compute*/unreadable*) but each has genuinely distinct domain
  math; a forced generic would obscure, not clarify. Left as-is.
- The **one** real within-package copy-paste is `ship_event_bus.go` (below) — deferred as a
  candidate because it is concurrency-critical with no direct unit test.

No L3 edits were made: the only substantial dedup carries real risk disproportionate to a
quality sweep. Recorded as an L4 candidate instead.

## L4-L6 candidates (recorded, NOT edited)

1. **`ship_event_bus.go` — generic topic registry (L4, missing abstraction).**
   Five near-identical `Publish*/Subscribe*/Unsubscribe*` triplets (arrived, workerCompleted,
   tasksBecameReady, transportRequested, transferCompleted). Each Unsubscribe is byte-identical
   modulo the map field + key; each Publish is an identical non-blocking broadcast; each Subscribe
   an identical make-buffered-chan-and-append. A generic `eventTopic[T]` (with per-topic buffer
   size + key derivation) would collapse the copy-paste. Deferred from L3 because the bus is on the
   live arrival/coordination hot path and has **no direct unit test** (only referenced by name in
   `arrival_wait_test.go`); this needs a dedicated effort that writes bus unit tests first, then
   refactors under them. Package: `internal/application/ship`.

2. **`cargo_transaction.go` — unify `liveBidForFloor` / `liveAskForCeiling` (L4).**
   The two are structurally identical (refresh -> GetMarketData -> FindGood -> return, fail-closed)
   and differ only in the final `g.PurchasePrice()` vs `g.SellPrice()`. A shared helper taking a
   price-selector `func(*market.TradeGood) int` would dedup. LOW priority: the mirror is deliberate
   and each half carries a distinct load-bearing bead (sp-lbbm sell floor / sp-9mkf buy ceiling), so
   any unification must preserve both doc comments. Package: `internal/application/ship/commands/cargo`.

## Other observations (recorded, NOT acted on)

- **Dead exported code (Speculative Generality), not deleted.**
  `internal/application/ship/strategies/refuel_strategy.go`: `MinimalRefuelStrategy` and
  `AlwaysTopOffStrategy` (+ their constructors/methods) are **unreachable** across the whole repo
  (confirmed with `deadcode -test ./...`). They are documented, exported alternatives of the
  `RefuelStrategy` Strategy pattern (also listed in `route_executor.go`'s header). My mandate only
  authorizes deleting *unexported* dead code, and removing exported+documented API is a
  product/architecture call — left intact. Decision for a human: keep the documented extensibility
  or remove the two unused strategies.

- **Pre-existing gofmt non-compliance (not "fixed").**
  `internal/application/autooutfit/coordinator.go` and `coordinator_test.go` are gofmt-non-compliant
  on `origin/main` (deliberate one-liner setter bodies + comment-interspersed const alignment).
  These are pre-existing and not caused by me; running `gofmt -w` would expand the intentional
  one-liners — not a refactoring win — so I did not touch those two files. (All files I *did* touch
  are gofmt-clean.)

- **`jump_ship.go` `Handle` is long (~295 LOC, numbered steps 1-11).**
  A candidate for step extraction, but step 8 uses `defer` for claim/release scoped to `Handle`, and
  `ship` is reassigned across steps — naive extraction would change defer scoping. Left as-is; not a
  safe sweep edit.

## Suspected bugs

None found. The code is defensive and well-reasoned; error paths fail-closed with explicit logging.

## Gate (final)

`cd gobot && go build ./... && go vet <scope> && go test -race -count=1 <scope>` — all exit 0.
Scope = `./internal/application/ship/... ./internal/application/shipyard/...
./internal/application/fleet/... ./internal/application/autooutfit/...`.

Commits (all gated green, `--no-verify` used to keep the beads `issues.jsonl` export hook out of
code commits):
- `refactor(L1): app-ship — rename builtin-shadowing locals ...`
- `refactor(L2): app-ship — dedup market-scan metric recording ...`
