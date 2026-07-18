# Refactoring Lane Report — `captain-infra-cmd`

RPP pass L1–L3, behavior-preserving only. Bead tag `sp-1z4q`.

**Scope (recursive):** `gobot/internal/captain/…`, `gobot/internal/infrastructure/…`,
`gobot/cmd/…`, `gobot/pkg/utils/…`

**Gate (green before every commit + final):**
`cd gobot && go build ./... && go vet ./... && go test -race -count=1 ./internal/captain/... ./internal/infrastructure/... ./cmd/... ./pkg/utils/...`
— all three exit 0 (captain suite runs with `-race`, ~10.5s).

## Headline

This is an exceptionally clean, mature codebase with a strong, load-bearing
comment culture (WHY-comments + `sp-*` bead refs everywhere). The usual L1/L2
targets — dead code, bad names, magic numbers, redundant WHAT-comments, deep
nesting — are almost entirely **already done**. So this was a surgical quality
sweep, not a rewrite: a few genuinely safe wins, many files confirmed pristine
and left untouched.

Tooling used to bound the survey:
- `deadcode -test ./...` (golang.org/x/tools) over the whole module → **zero**
  dead code in scope.
- `grep TODO|FIXME|XXX|HACK` over scope → **zero** markers.
- `gofmt -l` over scope → 5 pre-existing drifted files (fixed at L1).
- Longest-function scan (awk) → identified the L2 targets.

## L1 — Readability

**Smells found**
- Pre-existing `gofmt` drift in 5 `internal/captain` files (const-block
  alignment, arg-list wrapping, comment spacing).
- No dead code, no TODO/FIXME, no misleading names, no extractable magic
  numbers (all already named consts), no redundant WHAT-comments worth removing
  (the comments are WHY/bead docs — deliberately preserved).

**Changes applied** — 1 commit (`ee402516`)
- `gofmt -w` on `briefing_source.go`, `briefing_test.go`, `gatecli_test.go`,
  `surveyor_nudge_test.go`, `wake_test.go`. Pure whitespace; no semantic change;
  no assertion/value/case change in the touched test files.

**Pristine, left untouched (representative):** `pkg/utils/*`, `wake.go`,
`state.go`, `watch.go`, `fixer.go`, `worktree.go` (behavior-critical git-merge
code — deliberately not risked), `briefing.go`, `config.go`, `validation.go`,
`supervise/supervisor.go`, `database/connection.go`, and the config schema
sub-files (`trade_fleet.go`, `manufacturing.go`, …) which are pure documented
structs.

## L2 — Complexity

**Smells found**
- `config.SetDefaults` — one 294-line Long Method: a flat sequence of
  `if cfg.X == 0 { cfg.X = default }` blocks fenced by `// <Section> defaults`
  WHAT-comments (textbook Extract Function: the comment names the function).
- `captain.Supervisor.Tick` — a 35-line inline `DetectorConfig{…}` literal
  buried mid-orchestration.

**Changes applied** — 2 commits
- `eb6e3aae` — `config.SetDefaults` decomposed into 9 focused helpers
  (`setDatabaseDefaults`, `setAPIDefaults`, `setRoutingDefaults`,
  `setDaemonDefaults`, `setLoggingDefaults`, `setMetricsDefaults`,
  `setCaptainDefaults`, `setTradeFleetDefaults`, `setBootstrapDefaults`);
  `SetDefaults` becomes a short dispatcher. Every default value and inline
  WHY/bead comment is byte-identical (verified diff: 41 structural insertions,
  **0 value changes**). Public `SetDefaults` signature/behavior unchanged.
- `1b0fe6bf` — extracted `Supervisor.buildDetectorConfig(prevCredits, regimePolicy)`
  from `Tick`; the `DetectorConfig` fields and their `sp-k7q5`/`sp-y0f6`
  comments moved verbatim. Unexported method; no externally-visible signature
  changed.

**Considered but deliberately skipped**
- `cmd/spacetraders-daemon/main.go` `run()` (1423-line composition root):
  flat, branchless DI wiring with interdependent ordering and only a
  registration-gate parser test — extracting from it is high-risk / low-reward
  without a characterization harness. Recorded as an L4 candidate below.

## L3 — Responsibilities

**Smells found**
- `captain/detectors.go` — 1431-line God-File. Already internally clean and
  topically ordered, and the **test** files were already split by concern
  (`detectors_income_granularity_test.go`, `detectors_scout_staleness_test.go`,
  `detectors_prometheus_alerts_test.go`, `detectors_trading_engine_scope_test.go`),
  giving a natural production partition.

**Changes applied** — 1 commit (`16e498a9`)
- Split into cohesive same-package files, mirroring the test partition:
  - `detectors_income.go` — income-stall (aggregate / per-engine / per-factory)
  - `detectors_regime.go` — price-tripwire regime detector (`sp-zlfv`)
  - `detectors_scout.go` — `sp-k7q5` planner-staleness detectors (layers 2/3)
  - `detectors_prometheus.go` — `sp-y0f6` Prometheus alert-firing poller
  - `detectors_credits.go` — credit-threshold crossing + `CurrentCredits`
- `detectors.go` (now **636** lines) retains the core: `DetectorConfig`,
  `RunDetectors`, ship-state + stream/crash detectors, `episodeTracker`, and all
  `default*` consts (they are the defaults *for* `DetectorConfig`'s fields, so
  they belong with it).
- **Move integrity:** done mechanically by slicing exact contiguous line ranges
  (no retyping). Verified: all 5 moved bodies present **verbatim** in the
  original; **32** funcs + **15** type/const/var decls preserved with **zero**
  duplicates; compiler + `-race` tests green.

## Counts

| Metric | Value |
|---|---|
| Commits (refactor) | 4 (+1 report) |
| Levels applied | L1, L2, L3 |
| Files edited | 7 (5 gofmt-only test/src + `config/defaults.go` + `captain/supervisor.go`) |
| Files created (L3 split) | 5 |
| `detectors.go` | 1431 → 636 lines |
| `SetDefaults` | 1 × 294-line fn → dispatcher + 9 helpers |
| Behavior changes | 0 (config keys, defaults, flags, log/error strings, wire all unchanged) |

## L4–L6 candidates (NOT done here — architectural, need a dedicated pass)

1. **Missing abstraction — shared Prometheus read client (L4).**
   Packages: `internal/captain`. `detectPrometheusAlerts`
   (`detectors_prometheus.go`) and `Briefing.readAlerts`/`promQuery`
   (`briefing_source.go`) each hand-roll a Prometheus HTTP GET + `/api/v1/alerts`
   decode (they already share the `prometheusAlertsAPIResponse` type). Extract a
   small unexported `promClient` so the timeout (5s vs 3s) and error policy
   (error-return vs fail-open-nil) live in one place. Deferred because the two
   call sites have *intentionally different* error semantics — reconciling them
   is a design decision, not a mechanical move.

2. **God-function — daemon composition root decomposition (L4/L5).**
   Packages: `cmd/spacetraders-daemon`. `run()` is 1423 lines of DI wiring.
   Decompose into per-subsystem builders (`registerShipHandlers`,
   `registerContractHandlers`, `wireCoordinators`, …). Genuinely blocked on a
   **characterization test** first: current coverage is only the
   `main_test.go` registration-gate AST parser, so there is no behavioral net
   for a large extraction with order dependencies. Mikado-shaped.

3. **Incomplete abstraction — detector default-consts should be `CaptainConfig`
   fields (L4, cross-package).** Packages: `internal/captain` +
   `internal/infrastructure/config`. The `default*` consts in `detectors.go`
   carry ~6 repeated notes: *"wired by the supervisor until CaptainConfig grows
   a tunable field (follow-up bead)."* Promoting them (factory-stall, crash-loop,
   pinned-hull, `sp-k7q5` staleness) to config surface removes the hardcoded
   defaults. **Caveat:** this *adds config keys* → behavior-affecting, so it is a
   product follow-up (already tracked by beads), not a pure refactor.

## Suspected bugs / latent issues (reported only — NOT fixed)

- **Display/threshold coupling (minor, latent).** `briefing.go`
  `renderCoverage` prints the literal `"(>3h)"`, while the actual staleness
  cutoff is `briefingStaleMarket = 3 * time.Hour` in `briefing_source.go`. They
  are independent literals that must agree; changing the const would silently
  make the label lie. Not a runtime bug today. A fix would derive the label from
  the const — but that changes wake-mail output text, so it is out of scope for
  a behavior-preserving pass.

No functional/behavioral bugs were observed in the surveyed code.
