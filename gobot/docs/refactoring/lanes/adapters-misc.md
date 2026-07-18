# Lane report: adapters-misc (RPP L1–L3)

- **Lane key:** `adapters-misc`
- **Scope (recursive):** `internal/adapters/{metrics,capacity,expansion,flowfeed,graph,routing,telemetry}`
- **Base:** `dc647939` (origin/main, green)
- **Range this pass:** L1–L3, behavior-preserving only.

## Headline

This scope is **exceptionally clean and heavily pre-refactored**. Across ~15k lines
(7 packages, ~55 `.go` files) the consistent idiom is already: intention-revealing
names, extracted named constants, guard clauses / early returns, small well-decomposed
functions, "pure core + thin adapter" seams (`bfsHops`, `growFrontierGraph`,
`pickReusableProbe`, `unchartedNeighborsFrom`, …), narrow injected ports, and dense
load-bearing WHY/bead comments.

A four-linter mechanical sweep over the **entire** scope
(`unused`, `unconvert`, `ineffassign`, `unparam`) surfaced exactly **one** dead
identifier in production code and **zero** redundant conversions / ineffectual
assignments. The only applied edits are one grep-verified dead-code removal and one
magic-number → named-constant extraction, both in `metrics/container_metrics.go`.
L2 and L3 were surveyed across the full scope and produced **no safe, value-positive
edits** — forcing extractions/splits here would be churn on pristine code and add
merge/review risk to a live money-earning system. Per the "skip pristine, prefer many
small safe wins over few risky large ones" guidance, they were intentionally left
untouched.

## Method

- Read in full (19 largest / most logic-heavy files): `expansion/adapters.go`,
  `expansion/reuse_relayer.go`, `expansion/off_gate_target.go`,
  `expansion/depth_objective.go`, `expansion/frontier_bearing.go`,
  `metrics/prometheus_collector.go`, `metrics/manufacturing_metrics.go`,
  `metrics/container_metrics.go`, `metrics/market_metrics.go`,
  `metrics/api_budget_tracker.go`, `routing/grpc_routing_client.go`,
  `graph/graph_service.go`, `telemetry/collector.go`, `flowfeed/handler.go`,
  `flowfeed/registry.go`, `capacity/sensor.go`, `capacity/sense_topology.go`,
  `capacity/sense_economics.go`, `capacity/sense_demand.go`,
  `capacity/sense_performance.go`, `capacity/sense_utilization.go`,
  `capacity/actuator_adapters.go`, `capacity/proposal_channel.go`.
- Linted **all** in-scope packages with `golangci-lint`
  (`unused`, `unconvert`, `ineffassign`, `unparam`).
- Gate before commit: `go build ./...` + `go vet ./...` +
  `go test -race -count=1` over all seven packages — all green.

## L1 — Readability (APPLIED)

Smells found:
- **Dead Code** — `ContainerMetricsCollector.mu sync.Mutex`
  (`metrics/container_metrics.go`) was never taken. `golangci-lint unused` flagged it;
  grep across the whole `metrics` package confirmed the only occurrence is the field
  declaration (the `t.mu`/`s.mu` hits are unrelated structs in `api_budget_tracker.go`
  / `duty_cycle_sampler.go`). The collector's two pollers update **disjoint**,
  internally-synchronized Prometheus vecs, so no lock is required — this is speculative
  leftover, not a missing-lock bug (see Suspected bugs).
- **Magic numbers** — `Start()` launched its two pollers with inline literals
  `10 * time.Second` / `30 * time.Second`, each shadowed by a comment restating the
  number, whereas the sibling collectors (`manufacturing`, `market`) use a named
  interval. Minor stale-comment risk.

Changes applied (`metrics/container_metrics.go`):
1. Safe-deleted the dead `mu` field.
2. Extracted `containerMetricsPollInterval = 10 * time.Second` and
   `shipMetricsPollInterval = 30 * time.Second` (identical values); replaced the two
   inline literals; trimmed the now-redundant numeric parenthetical from the two
   `Start()` comments (the constant names carry the cadence).

Counts: 1 file, +11/−5 lines, behavior-preserving.

Considered and **declined** (churn > value on clean code):
- `routing/grpc_routing_client.go` — the repeated `// Convert to protobuf request` /
  `// Call gRPC service` / `// Convert response` markers technically restate the next
  line, but they form a deliberate, consistent paragraph structure across five
  conversion methods; removing ~15 of them is noise, not clarity.
- `flowfeed/{handler,registry}.go` top-of-file `// gobot/…/x.go` path comments — a
  project-wide convention, not worth diverging on in one lane.
- `capacity/sense_utilization.go:55` `var report dutycycle.Report = …` explicit type —
  arguably documents intent; not a smell.

## L2 — Complexity (SURVEYED, no safe/valuable edits)

- No functions exceed a healthy size in a way that warrants extraction. The longest
  in scope, `graph/graph_service.go:GetWaypoint` (~65 lines), is the **intentional
  two-tier double-checked-cache** idiom whose two DB-read blocks are semantically
  distinct (pre- vs post-lock) and are explained by load-bearing comments; extracting
  them would obscure the concurrency intent, not clarify it.
- No within-package duplication worth extracting: the repeated
  `playerIDStr := strconv.Itoa(playerID)` per metrics method is idiomatic;
  `market_metrics.go`'s inline `systemSymbol+"-%"` LIKE-pattern is a trivial concat that
  reads better inline inside each `WHERE`.
- Complex booleans are already decomposed into named predicates
  (`betterOffGateTarget`, `isPromisingSystemType`, `reuseEligibleIdleHulls`, …).

## L3 — Responsibilities (SURVEYED, no safe/valuable edits)

- No god-files needing a within-package split. The one very large file,
  `metrics/prometheus_collector.go` (765 lines), is a **single cohesive responsibility**
  (the global-collector registry + its nil-safe delegating wrappers), already grouped
  Set/Get/Record per collector and maximally uniform; splitting it would be high-churn,
  low-value, and touch the public surface. Recorded instead as an L4–L6 candidate.
- No feature envy / inappropriate intimacy within packages — data and the methods that
  own it are already colocated (e.g. `coordinateCache`, `warehouseCapConfig`,
  `playerContract` all carry their own behavior).

## Suspected bugs (reported, NOT fixed)

None. The removed `ContainerMetricsCollector.mu` is **not** a latent concurrency bug:
the container poller and ship poller mutate different Prometheus vecs (each internally
synchronized) and share no mutable state, so the never-taken mutex was dead
speculative scaffolding rather than a missing guard.

## Cross-package / architectural candidates (L4–L6, deferred — see structured output)

1. **Replace the metrics package-global collector singletons + nil-safe free-function
   wrappers with an injected metrics facade (L6).** `prometheus_collector.go` holds
   ~20 `globalXCollector` vars, each with `SetGlobal…`/`GetGlobal…` plus a family of
   `RecordX` package functions that nil-check the global — a Service-Locator / global
   mutable-state pattern (~450 lines of near-identical boilerplate) that hides metrics
   coupling from every emitter across the daemon. Inverting to one injected facade
   removes the global state and the duplicated wrapper trios. Cross-cutting (every
   caller) → Mikado.
2. **Extract a shared polling-collector lifecycle base (L4).** ~8 collectors in `metrics`
   repeat identical scaffolding: `ctx`/`cancelFunc`/`wg` fields, `Start`/`Stop`, the
   `pollMetrics` ticker loop, and the `Register` "range over a []prometheus.Collector"
   loop. An embedded `pollingCollector` (template method) would dedupe it. Structural
   change to many public collector types → deliberate, not an L1–L3 edit.
3. **Route metrics aggregate reads through the persistence repository layer (L6).** The
   metrics collectors embed Postgres-dialect raw SQL directly
   (`split_part`, `EXTRACT(EPOCH …)`, `NOW()`, `::bigint` in `market_metrics.go` /
   `manufacturing_metrics.go`), bypassing the `persistence` repositories the rest of the
   system reads through. Consolidating behind repository query methods restores layering
   and removes dialect coupling from the adapter. Spans `metrics` + `persistence`.

## Gate (final)

`go build ./...` ✓ · `go vet ./...` ✓ ·
`go test -race -count=1 ./internal/adapters/{metrics,capacity,expansion,flowfeed,graph,routing,telemetry}/...` ✓
(metrics/capacity/expansion/flowfeed/routing/telemetry `ok`; graph has no test files).
