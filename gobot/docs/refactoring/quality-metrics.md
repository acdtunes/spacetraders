# RPP L1–L6 Quality Metrics — Before / After (`sp-1z4q`)

Quantitative before/after snapshot of the `gobot` module across the RPP pass.
Both snapshots use **identical measurement methods** (see notes) so deltas are
apples-to-apples.

- **Before:** `dc647939` (origin/main baseline) — `scratchpad/before-metrics.json`
- **After (metrics snapshot):** `4336f34f` (the last Go-affecting commit, `style: gofmt`)
  — `scratchpad/after-metrics.json`. The final branch HEAD is the subsequent docs commit
  on top; it is docs-only (Go tree byte-identical), so every metric here is unchanged by it.
- **Gate (re-verified green on the final HEAD):** `gofmt -l` clean · `go build` 0 ·
  `go vet` 0 · `go test -race -count=1 ./...` 0 (107 packages, 0 FAIL / 0 panic / 0 DATA RACE).

## Lines of code

| Metric | Before | After | Δ | Read |
|---|---:|---:|---:|---|
| Production LOC | 183,811 | 183,501 | **−310** | net shrink despite splits — dead-code deletion + dedup |
| Production files | 616 | 628 | **+12** | god-files split into cohesive units |
| Test LOC | 128,563 | 128,875 | **+312** | characterization tests added by Mikado goals |
| Test files | 637 | 642 | **+5** | new lifecycle/VO/trait characterization tests |
| Total LOC | 312,374 | 312,376 | **+2** | ~flat overall |
| Total `.go` files | 1,253 | 1,270 | **+17** | extract/move signature |
| Test:prod LOC ratio | 0.699 | 0.702 | **+0.003** | slightly more test coverage |

**Interpretation:** the classic behavior-preserving refactor signature — production LOC
*down* while production file count is *up*. Code was redistributed out of god-files
(e.g. `detectors.go` −795, `navigation/ship.go` −414, `manufacturing/task.go` −288
into split files) and dead code was deleted (domain-economy −110, adapters-cli −96),
while extraction added a little test scaffolding (+312 test LOC).

## Cyclomatic complexity (gocyclo, includes tests)

| Metric | Before | After | Δ |
|---|---:|---:|---:|
| Average complexity | 3.07 | 3.06 | **−0.01** |
| Functions with complexity > 15 | 102 | 101 | **−1** |
| Total functions measured | — | 10,849 | — |

**Top-20 worst — notable movements:**

| Function | Before | After | Note |
|---|---:|---:|---|
| `config.SetDefaults` | **79** (#2 worst) | *gone from top-20* | decomposed into dispatcher + 9 helpers; highest `config` fn now `setCaptainDefaults` (33) |
| `api.GraphBuilder.BuildSystemGraph` | 31 (#12) | **28** (#20) | has-fuel trait rule repointed to `shared.TraitsGrantFuel` (−3 branches) |
| `main.run` | 90 (#1) | 90 (#1) | unchanged — daemon composition root (recorded L4/L5 Mikado candidate; needs characterization harness first) |
| `RunFleetCoordinatorHandler.Handle` | 65 (#2 after) | 65 | unchanged — recorded L4 state-object candidate |

The aggregate barely moves because the biggest win (`SetDefaults` 79 → several sub-33
functions) dropped *below* the 15 threshold and so no longer contributes any over-15
count; it is visible only as the disappearance of the #2-worst entry. The largest
remaining functions (`main.run` 90, `Handle` 65) were deliberately **not** touched by
the L1–L3 sweep — they are recorded Mikado candidates blocked on characterization tests.

## Tech-debt markers

| Marker | Before | After | Δ |
|---|---:|---:|---:|
| TODO | 10 | 10 | 0 |
| FIXME | 0 | 0 | 0 |
| HACK | 0 | 0 | 0 |
| **Total** | **10** | **10** | **0** |

No new tech-debt markers introduced. (Suspected bugs were recorded in
`refactoring-log.md`, not as in-code TODOs.)

## Largest non-test, non-generated files (top 15)

| # | Before (file · lines) | After (file · lines) |
|---:|---|---|
| 1 | run_scout_post_coordinator.go · 3525 | run_scout_post_coordinator.go · 3525 |
| 2 | cli/daemon_client.go · 2355 | cli/daemon_client.go · 2347 |
| 3 | api/ship_repository.go · 2317 | api/ship_repository.go · 2317 |
| 4 | trading/run_tour_coordinator.go · 2305 | trading/run_tour_coordinator.go · 2305 |
| 5 | grpc/daemon_service_impl.go · 2006 | grpc/daemon_service_impl.go · 2006 |
| 6 | mfg/run_factory_coordinator.go · 1959 | mfg/run_factory_coordinator.go · 1959 |
| 7 | mfg/production_executor.go · 1939 | mfg/production_executor.go · 1939 |
| 8 | api/client.go · 1870 | api/client.go · 1870 |
| 9 | grpc/daemon_server.go · 1729 | grpc/daemon_server.go · 1720 |
| 10 | contract/run_fleet_coordinator.go · 1661 | contract/run_fleet_coordinator.go · 1661 |
| 11 | run_frontier_expansion_coordinator.go · 1655 | run_frontier_expansion_coordinator.go · 1655 |
| 12 | cli/ship.go · 1626 | cli/ship.go · 1626 |
| 13 | spacetraders-daemon/main.go · 1545 | spacetraders-daemon/main.go · 1545 |
| 14 | grpc/command_factory_registry.go · 1473 | grpc/command_factory_registry.go · 1473 |
| 15 | **captain/detectors.go · 1431** | **contract/idle_arb.go · 1281** |

**Notable:** `captain/detectors.go` dropped from **1431 → 636 lines** (−795, split into 5
files by the captain-infra-cmd lane) and left the top-15 entirely; `idle_arb.go` (1281,
unchanged) shifted up into rank #15. The other large money-path files (scout-post
coordinator 3525, ship_repository 2317, tour coordinator 2305, daemon_service_impl 2006)
were deliberately **not** split — each is a recorded L3/L4 candidate deferred as too
high-churn for a broad sweep on a live system.

## Static analysis (golangci-lint v1.64.8)

| Linter | Before | After | Δ |
|---|---:|---:|---:|
| errcheck | 39 | 39 | 0 |
| staticcheck | 13 | 13 | 0 |
| unused | 12 | **8** | **−4** |
| ineffassign | 4 | 4 | 0 |
| gosimple | 3 | 3 | 0 |
| **Total issues** | **71** | **67** | **−4** |

Both runs exit 1 (issues present) with the same tool version. The entire improvement is
in **`unused` (−4)**, from deleting dead predicates/constants/`Stop`-methods/switch-arms
during the sweep. The residual 67 (39 errcheck + 13 staticcheck + 8 unused + 4
ineffassign + 3 gosimple) are pre-existing findings outside the behavior-preserving
scope of this pass — candidates for a dedicated lint-cleanup follow-up.

## Summary of deltas

| Dimension | Direction | Magnitude |
|---|---|---|
| Production LOC | ↓ better | −310 |
| God-file decomposition | ↑ better | detectors.go −795; ship.go −414; task.go −288; +17 files |
| Worst-function complexity | ↓ better | SetDefaults 79 → decomposed; BuildSystemGraph 31→28 |
| Average complexity | ↓ better | 3.07 → 3.06 |
| Functions > 15 | ↓ better | 102 → 101 |
| Lint issues | ↓ better | 71 → 67 (−4 `unused`) |
| Tech-debt markers | → flat | 10 → 10 |
| Test coverage (LOC) | ↑ better | +312 |
| Behavior | → preserved | full `-race` suite green, 107 pkgs, 0 FAIL |

### Measurement methods (identical before/after)
- **LOC:** `find . -name '*.go' -not -path './vendor/*'`, split on `_test.go`; LOC = raw
  `wc -l`. No vendor dir.
- **gocyclo:** `gocyclo -ignore 'pkg/proto|vendor' -avg .` (path `.`, **not** `./...`
  which yields `Average: NaN`); `functions_over_15` = `gocyclo -over 15 . | wc -l`
  (strictly-greater-than-15). Includes `_test.go` functions.
- **largest files:** non-test, non-generated (excludes `*_test.go`, files whose first 3
  lines contain `Code generated`/`DO NOT EDIT`, and `./pkg/proto/`).
- **tech-debt:** `grep -rE 'TODO|FIXME|HACK' --include='*.go' .` line count.
- **lint:** `golangci-lint run` (v1.64.8); total reconciled two ways (header-line count ==
  Σ per-linter tags == 67).
