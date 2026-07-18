# Mikado: metrics-polling-collector-base (L4)

**Goal (business value):** Remove the verbatim lifecycle-scaffolding duplication
across the four polling metrics collectors (container, financial, manufacturing,
market). Extract the shared `ctx`/`cancelFunc`/`wg` + `Start(ctx)`/`Stop()` +
`time.NewTicker` poll loop into one embedded `pollingCollector` template inside
`internal/adapters/metrics`; each collector supplies only its poll callback(s) and
interval(s). End-state: only `pollingCollector` constructs the ticker/goroutine and
owns the shutdown policy; the four collectors no longer each declare
`ctx`/`cancelFunc`/`wg` or duplicate `Start`/`Stop`.

**Status:** achieved
**START (baseline SHA on refactor/sp-1z4q-rpp):** a01190434b4e5a54ed03dd69b394981ae57833b5
**Affected packages:** `internal/adapters/metrics`

## Baseline facts (pre-exploration)

The verbatim-duplicated scaffolding across the 4 collectors:

- **Fields** (identical block in all 4): `ctx context.Context`,
  `cancelFunc context.CancelFunc`, `wg sync.WaitGroup`.
- **`Stop()`** (byte-identical in all 4): `if c.cancelFunc != nil { c.cancelFunc() }; c.wg.Wait()`.
- **`Start(ctx)`** each begins with `c.ctx, c.cancelFunc = context.WithCancel(ctx)`
  then `c.wg.Add(1); go <loop>(interval)` — the WHAT differs per collector (below).
- **Poll loop** each: `defer c.wg.Done(); ticker := time.NewTicker(interval);
  defer ticker.Stop(); [optional initial poll]; for { select { case <-c.ctx.Done():
  return; case <-ticker.C: c.update...() } }`.

Behavioral variations that MUST be preserved (not "verbatim"):

- **Initial poll**: financial/manufacturing/market call their `update...()` ONCE
  immediately before the ticker loop; **container does NOT** (both its loops are
  tick-only). => template needs a `pollImmediately bool`.
- **Loop count**: container starts TWO loops (`collectContainerMetrics` always,
  `collectShipMetrics` only `if c.shipRepo != nil`); the other 3 start ONE.
- **Interval source**: container uses two package consts
  (`containerMetricsPollInterval`, `shipMetricsPollInterval`); financial hardcodes
  `60 * time.Second`; manufacturing/market use the `c.pollInterval` field.

Blast radius (verified by repo-wide grep):

- External callers (`internal/adapters/grpc/daemon_server.go`) touch ONLY the
  exported `Start(ctx)`/`Stop()`/`Register()`/`Record*` — signatures unchanged. Safe.
- The loop-wrapper methods (`pollMetrics`, `pollProfitLoss`,
  `collectContainerMetrics`, `collectShipMetrics`) are referenced ONLY inside each
  collector's own `Start` — safe to delete after rewiring.
- The `update...()` callbacks (`updateAllMetrics`, `updateProfitLoss`,
  `updateContainerMetrics`, `updateShipMetrics`) are KEPT (they become the
  callbacks). A test calls `collector.updateProfitLoss()` directly — unaffected.
- Every `c.ctx`/`c.wg`/`c.cancelFunc` reference in financial/manufacturing/market
  lives inside the lifecycle methods being rewritten (zero survivors). Container's
  only survivor is `c.shipRepo.FindAllByPlayer(c.ctx, ...)` in `updateShipMetrics`,
  which keeps working via embedded-field promotion (same package).
- No pre-existing `pollingCollector` identifier; new file introduces no collision.

## Template design (`polling_collector.go`)

`pollingCollector` struct { ctx, cancelFunc, wg } with:
- `startContext(ctx)` — `p.ctx, p.cancelFunc = context.WithCancel(ctx)` (call once).
- `startPolling(interval, pollImmediately, poll func())` — `p.wg.Add(1)` then a
  goroutine owning `time.NewTicker`, optional initial `poll()`, and the
  `ctx.Done()/ticker.C` select loop. This is the ONLY place ticker/goroutine live.
- `Stop()` — promoted; identical to the 4 old copies.

Each collector: embed `pollingCollector`; delete its own ctx/cancelFunc/wg fields,
its `Stop()`, and its loop-wrapper method(s); reduce `Start` to
`c.startContext(ctx)` + one/two `c.startPolling(...)` calls.

## Tree

- [x] GOAL: one embedded `pollingCollector` owns ticker/goroutine/shutdown; 4 collectors carry no duplicated ctx/cancelFunc/wg/Start/Stop scaffolding
    - [x] Leaf 1: Add `polling_collector.go` with `pollingCollector` { ctx, cancelFunc, wg } + `startContext` + `startPolling(interval, pollImmediately, poll)` + `Stop()` (initially unused — legal in Go)
    - [x] Leaf 2: Convert `ManufacturingMetricsCollector` — embed `pollingCollector`; drop ctx/cancelFunc/wg + `Stop` + `pollMetrics`; `Start` = startContext + startPolling(c.pollInterval, true, c.updateAllMetrics)
    - [x] Leaf 3: Convert `MarketMetricsCollector` — same shape as Leaf 2 (initial poll = true)
    - [x] Leaf 4: Convert `FinancialMetricsCollector` — embed; drop fields + `Stop` + `pollProfitLoss`; `Start` = startContext + startPolling(60*time.Second, true, c.updateProfitLoss)
    - [x] Leaf 5: Convert `ContainerMetricsCollector` — embed; drop fields + `Stop` + `collectContainerMetrics` + `collectShipMetrics`; `Start` = startContext + startPolling(containerMetricsPollInterval, false, c.updateContainerMetrics) + (if shipRepo!=nil) startPolling(shipMetricsPollInterval, false, c.updateShipMetrics)

## Exploration log

**EXPLORE (naive full attempt, then revert):** Applied all five changes at once —
add `polling_collector.go` + convert all 4 collectors (embed, drop
ctx/cancelFunc/wg + Stop + loop-wrapper methods, thin Start via
startContext/startPolling, drop the now-unused `sync` import from each). Then ran
the targeted gate:

- `gofmt -l` on the 5 touched/created production files -> empty (all clean). The
  only gofmt hit was `container_metrics_test.go`, which is UNTOUCHED pre-existing
  baseline state — left alone.
- `go build ./...` -> exit 0
- `go vet ./internal/adapters/metrics/...` -> exit 0
- `go test -race -count=1 -p 4 ./internal/adapters/metrics/...` -> ok

**Result: NO hidden prerequisites discovered.** The dependency graph is exactly the
five leaves above, all trivially safe:
- No pre-existing `pollingCollector` / `startContext` / `startPolling` identifier —
  new file + promoted methods introduce no collision.
- Field promotion covers the one surviving lifecycle-field reference outside the
  rewritten methods: container `updateShipMetrics`'s
  `c.shipRepo.FindAllByPlayer(c.ctx, ...)` resolves to the embedded `ctx`.
- The loop-wrapper methods are referenced only inside each collector's own Start;
  deleting them after rewiring leaves no dangling reference.
- The `update...()` callbacks are kept, so the test that calls
  `updateProfitLoss()` directly is unaffected.
- Each collector loses its only `sync` use (the `wg` field) => `sync` import
  dropped from all 4; `context` stays (Start signature / `context.Background()`).

Reverted the code (kept this doc), then executed the leaves bottom-up as gated
commits below.

## Behavior preservation

The template reproduces each old loop byte-for-byte in effect:
- `Stop()` is the identical cancel-then-Wait, now promoted from the embedded type.
- `startPolling` does `wg.Add(1)` synchronously in Start (same happens-before as the
  old `c.wg.Add(1); go ...`), then the goroutine owns `time.NewTicker` + the
  `ctx.Done()/ticker.C` select — identical to the old wrappers.
- The `pollImmediately bool` preserves the ONE behavioral difference: financial /
  manufacturing / market kept their pre-loop initial poll (`true`); container's two
  loops stay tick-only (`false`).
- Container still starts two loops with the ship loop gated on `shipRepo != nil`,
  same intervals (`containerMetricsPollInterval` / `shipMetricsPollInterval`).
No proto/SQL/CLI/config/log-string/wire changes; exported Start/Stop signatures
unchanged (daemon_server.go callers untouched).

## Completion

Goal ACHIEVED. Executed all 5 leaves bottom-up as individually-gated commits
(each: `gofmt -l` clean + `go build ./...` + `go vet ./internal/adapters/metrics/...`
+ `go test -race -count=1 -p 4 ./internal/adapters/metrics/...` green), then the
GOAL node.

End-state verified by repo-wide grep:
- `time.NewTicker` / `context.WithCancel` / `wg.Add` / `go func()` for the four
  polling collectors now live ONLY in `polling_collector.go`. (The one other hit,
  `duty_cycle_sampler.go`, is the out-of-scope `DutyCycleSampler` — the goal scoped
  to exactly these 4, not the ~8 nominated; left untouched.)
- The four collectors carry zero `cancelFunc` / `wg sync.WaitGroup` /
  `ctx context.Context` field declarations (the only remaining mentions are the
  explanatory `// Lifecycle scaffolding ...` comments) and zero `Stop()` /
  `pollMetrics` / `pollProfitLoss` / `collect*Metrics` methods.
- Net LOC: template +62; the 4 collectors −31 each (≈ −124), so the package is a
  net −62 LOC with a single home for ticker/shutdown policy.

FULL gate green: `go build ./...` exit 0, `go vet ./...` exit 0,
`go test -race -count=1 ./...` -> 70 packages ok, 0 FAIL, 37 no-test-files.

Behavior preservation confirmed (see "Behavior preservation" above): exported
`Start`/`Stop` signatures unchanged (daemon_server.go callers untouched); initial-
poll cadence preserved per-collector via `pollImmediately`; container's two loops +
`shipRepo != nil` gate + both intervals unchanged; no proto/SQL/CLI/config/
log-string/wire changes; no WHY-comment or bead-reference deletions.

## Suspected issues (recorded, NOT changed — behavior-preserving refactor only)

None found. (No latent bugs observed in the extracted scaffolding; the pre-existing
per-collector initial-poll asymmetry between container and the other three is
intentional and was preserved verbatim, not "fixed".)

Note: `internal/adapters/metrics/container_metrics_test.go` is flagged by `gofmt -l`
at BASELINE (pre-existing, untouched by this goal) — left alone to keep the diff
scoped.
