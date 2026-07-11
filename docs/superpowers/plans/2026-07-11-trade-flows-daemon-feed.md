# Trade Flows Daemon Feed Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A read-only, in-memory registry of active trading flows (tour / trade-route / arb) that the gobot daemon serves at `GET /api/flows` on the existing metrics listener, so the visualizer's Trade Flows tab can draw live intent (future hops + tranches) the optimizer holds in memory but never writes to PG until a leg realizes.

**Architecture:** A dependency-light leaf package `internal/adapters/flowfeed` holds the registry and its JSON payload types. Trading executors publish a plan snapshot at plan adoption and at each leg boundary via a nil-safe package-level free function (the same singleton idiom executors already use for Prometheus counters). The shared container runner removes a flow at its terminal exit seam (never on a resumable/re-adoptable exit). One handler serializes a registry snapshot onto the metrics mux beside `/metrics`. Publishing is fire-and-forget state exposure only.

**Tech Stack:** Go (existing gobot patterns: `internal/adapters/metrics` singleton wiring, `net/http` + `httptest`, `container_runner` lifecycle, captain-gate).

**Spec:** `docs/superpowers/specs/2026-07-10-trade-flows-tab-design.md` §"Architecture / 1. Daemon flow feed" and §"Degradation" / §"Testing" — read it first; it is the authority on any ambiguity. This plan covers **Part 2 only** (the one Go surface); the visualizer server + web tab (spec §2, §3) are a separate, separable plan.

## Global Constraints

Every task's requirements implicitly include this section. Copy values verbatim.

- **READ-ONLY surface.** No trading logic reads the registry; no guard interacts with it (RULINGS #4 — this surface can never gate or relax a buy). Publishing is fire-and-forget; a missed or failed publish must never alter a trade path. The registry exposes no mutation to any decision code.
- **In-memory only.** The registry is process-memory; after a daemon restart it repopulates as executors re-adopt plans and re-publish. Durable intent (plan-time telemetry inserts) is explicitly **phase 2** (spec §"Phase 2" #1). Do not persist the registry.
- **Existing listener only.** The handler registers on the **existing metrics HTTP mux** (`internal/adapters/grpc/daemon_server.go`, beside the `/metrics` handler), localhost-bound via the existing `metrics:` config (`enabled` / `port` 9090 / `host` localhost / `path`). **No new listener, no new port, no auth change, no new config keys.** Same trust boundary as `/metrics`.
- **Degradation contract.** An empty fleet returns `{"flows":[]}` — a non-nil empty array, never `{"flows":null}`. A flow with no in-progress leg carries `"currentLeg": null`; a flow with no projection carries `"projected": null`.
- **PAYLOAD CONTRACT (field names are law — shared verbatim with the visualizer web plan).** Top-level object: `{"flows": [ <flow>, ... ], "generatedAt": <RFC3339 string>}`. Each `<flow>`:
  ```json
  {
    "containerId": "tour-run-TORWIND-54-beba64e7",
    "program": "tour" | "trade-route" | "arb",
    "ship": "TORWIND-54",
    "tourId": "tour-run-TORWIND-54-beba64e7" | null,
    "currentLeg": {"from": "X1-UU57-E21Z", "to": "X1-ZC66-C39A",
                   "departedAt": "<RFC3339>", "arrivesAt": "<RFC3339>"} | null,
    "cargo": [{"good": "EQUIPMENT", "units": 200}],
    "remainingHops": [{"waypoint": "X1-ZC66-F12F",
                       "tranches": [{"good": "ADVANCED_CIRCUITRY", "isBuy": false,
                                     "units": 100, "expectedUnitPrice": 4100}]}],
    "projected": {"profit": 312000, "ratePerHour": 445000} | null,
    "plannedAt": "<RFC3339>"
  }
  ```
  JSON field order is fixed by struct field order (Go `encoding/json` emits in declaration order); the golden test in Task 2 pins the exact bytes.
- **P0 COLLISION HOLD.** Tasks that **edit** `internal/application/trading/commands/run_tour_coordinator*.go` or `run_trade_route_coordinator*.go` are **held until beads `sp-4hl5` and `sp-1pli` have merged** — those lanes are editing these files now. Tasks 1–5 (registry, handler, daemon wiring, runner Remove seam, arb publish) touch none of those files and proceed immediately. Tasks 6–7 carry a visible **[HOLD]** banner; rebase onto the merged result and re-confirm the quoted line numbers before editing (the seams will have shifted).
- **RULINGS.md applies** (esp. #2 restart-resilient, #3 single-writer, #4 guards fail closed / never weakened). Container lifecycle contract **sp-7yej** applies: a flow is removed only at a *terminal* exit; resumable exits (ctx-cancel before re-adoption) keep the entry so the re-adopted container's re-publish is not lost (invariant #4, universal restart re-adoption).
- **Protocol v2 landing rules:** worktree-first; commit before gating (never `git add` `.beads/issues.jsonl`; use `git commit --no-verify` if a hook interferes); gate with `--provision --merge`; verify the merged SHA's numstat.
- **Commit scope tag:** use `(flows)` in commit subjects (e.g. `feat(flowfeed): ...`). If a bead id is filed for this work, substitute it (e.g. `(sp-xxxx)`).
- **Go test commands (package-scoped):**
  - flowfeed: `go test ./internal/adapters/flowfeed/...`
  - grpc: `go test ./internal/adapters/grpc/...`
  - trading: `go test ./internal/application/trading/...`
  - full compile: `go build ./...`

## File Structure (locked)

```
gobot/internal/adapters/flowfeed/registry.go            # Task 1: types + Registry + global singleton + free funcs
gobot/internal/adapters/flowfeed/registry_test.go       # Task 1
gobot/internal/adapters/flowfeed/handler.go             # Task 2: GET /api/flows http.Handler
gobot/internal/adapters/flowfeed/handler_test.go        # Task 2: golden-payload conformance (httptest)
gobot/internal/adapters/grpc/daemon_server.go           # Task 3: construct registry + SetGlobal + register route (EDIT)
gobot/internal/adapters/grpc/daemon_flows_wiring_test.go# Task 3
gobot/internal/adapters/grpc/container_runner.go        # Task 4: Remove at terminal exit seam (EDIT)
gobot/internal/adapters/grpc/container_runner_flow_test.go # Task 4
gobot/internal/application/trading/commands/flow_publish.go       # Tasks 5-7: pure Flow builders + cargo mapper
gobot/internal/application/trading/commands/flow_publish_test.go  # Tasks 5-7
gobot/internal/application/trading/commands/run_arb_coordinator.go            # Task 5: publish seam (EDIT — collision-free)
gobot/internal/application/trading/commands/run_tour_coordinator.go           # Task 6: publish seams (EDIT — [HOLD])
gobot/internal/application/trading/commands/run_trade_route_coordinator.go    # Task 7: publish seam (EDIT — [HOLD])
```

### Registry-placement decision (why `internal/adapters/flowfeed`)

The registry is an **observation sink**, the same category as the Prometheus collectors. Verified facts drove the choice: (1) `internal/application/**` importing `internal/adapters/**` is already sanctioned here — 42 such imports exist, and `run_tour_coordinator.go` already calls package-level `metrics.RecordTourExit(...)`; (2) the executors reach metrics via a **package-level global singleton + nil-safe free functions** (`SetGlobalTourCollector` in `daemon_server.go`, delegating `RecordTourExit`), not via an injected port — so a flow registry mirroring that idiom needs **no port/interface split**; (3) `adapters/grpc` already imports `application/trading/commands`, so placing the registry type in `adapters/grpc` would close an import cycle (**forbidden**); (4) a new leaf `adapters/flowfeed` importing only the stdlib is importable by executors, the runner, and the handler with zero cycle risk. The package therefore defines its **own** plain JSON-tagged payload types (decoupled from `domain/routing` and `domain/trading`); executors map their domain types into `flowfeed.Flow` at the publish seam, keeping `flowfeed` a stdlib-only leaf. The API-client hexagonal port (`domain/ports`) is the codebase's *other* idiom, reserved for decision-influencing dependencies — a read-only feed is not one, so it follows the metrics idiom, not the port idiom.

---

## Task 1: `flowfeed` registry package

**Files:**
- Create: `gobot/internal/adapters/flowfeed/registry.go`
- Test: `gobot/internal/adapters/flowfeed/registry_test.go`

**Interfaces:**
- Produces (payload types, JSON tags are the contract): `Tranche`, `Hop`, `CargoItem`, `Leg`, `Projection`, `Flow`, `Feed` (exact fields below).
- Produces (registry): `func New() *Registry`; `func (*Registry) Publish(Flow)`; `func (*Registry) Remove(containerID string)`; `func (*Registry) Snapshot() Feed`.
- Produces (process-wide singleton — how executors and the runner reach it): `func SetGlobal(*Registry)`; nil-safe free functions `func Publish(Flow)` and `func Remove(containerID string)` that delegate to the installed global (no-op when unset).
- Program constants: `ProgramTour = "tour"`, `ProgramTradeRoute = "trade-route"`, `ProgramArb = "arb"`.

- [ ] **Step 1: Write the failing test**

```go
// gobot/internal/adapters/flowfeed/registry_test.go
package flowfeed

import "testing"

func TestRegistry_PublishOverwritesByContainerID(t *testing.T) {
	r := New()
	r.Publish(Flow{ContainerID: "c1", Ship: "S1", Program: ProgramArb})
	r.Publish(Flow{ContainerID: "c1", Ship: "S1-updated", Program: ProgramArb})
	snap := r.Snapshot()
	if len(snap.Flows) != 1 {
		t.Fatalf("want 1 flow after overwrite, got %d", len(snap.Flows))
	}
	if snap.Flows[0].Ship != "S1-updated" {
		t.Errorf("want overwritten ship S1-updated, got %q", snap.Flows[0].Ship)
	}
}

func TestRegistry_RemoveDropsFlow(t *testing.T) {
	r := New()
	r.Publish(Flow{ContainerID: "c1"})
	r.Publish(Flow{ContainerID: "c2"})
	r.Remove("c1")
	snap := r.Snapshot()
	if len(snap.Flows) != 1 || snap.Flows[0].ContainerID != "c2" {
		t.Fatalf("want only c2 after remove, got %+v", snap.Flows)
	}
}

func TestRegistry_SnapshotEmptyIsNonNilSlice(t *testing.T) {
	snap := New().Snapshot()
	if snap.Flows == nil {
		t.Fatal("empty snapshot must be a non-nil slice so JSON is [], not null")
	}
	if len(snap.Flows) != 0 {
		t.Fatalf("want 0 flows, got %d", len(snap.Flows))
	}
}

func TestRegistry_SnapshotSortedByContainerID(t *testing.T) {
	r := New()
	r.Publish(Flow{ContainerID: "c3"})
	r.Publish(Flow{ContainerID: "c1"})
	r.Publish(Flow{ContainerID: "c2"})
	got := r.Snapshot().Flows
	want := []string{"c1", "c2", "c3"}
	for i, w := range want {
		if got[i].ContainerID != w {
			t.Fatalf("want sorted %v, got index %d = %q", want, i, got[i].ContainerID)
		}
	}
}

func TestRegistry_BlankContainerIDIgnored(t *testing.T) {
	r := New()
	r.Publish(Flow{ContainerID: ""})
	if len(r.Snapshot().Flows) != 0 {
		t.Fatal("blank container id must not be stored")
	}
}

func TestGlobalFreeFunctions_NilSafeAndDelegate(t *testing.T) {
	SetGlobal(nil)
	Publish(Flow{ContainerID: "x"}) // must not panic
	Remove("x")                     // must not panic

	r := New()
	SetGlobal(r)
	t.Cleanup(func() { SetGlobal(nil) })
	Publish(Flow{ContainerID: "c1"})
	if len(r.Snapshot().Flows) != 1 {
		t.Fatal("free-function Publish must delegate to the installed registry")
	}
	Remove("c1")
	if len(r.Snapshot().Flows) != 0 {
		t.Fatal("free-function Remove must delegate to the installed registry")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd gobot && go test ./internal/adapters/flowfeed/...`
Expected: FAIL — `no required module provides package .../flowfeed` / undefined `New`, `Flow`, `SetGlobal` (package does not yet exist).

- [ ] **Step 3: Write minimal implementation**

```go
// gobot/internal/adapters/flowfeed/registry.go

// Package flowfeed is an in-memory, read-only registry of active trading flows
// (tour / trade-route / arb) that the daemon exposes at GET /api/flows.
//
// RULINGS #4: this surface is pure state exposure. No trading logic reads it and
// no guard consults it; publishing is fire-and-forget and can never gate or relax
// a buy. The registry is process-memory and repopulates as executors re-adopt
// plans after a daemon restart (durable intent is phase 2).
package flowfeed

import (
	"sort"
	"sync"
	"time"
)

// Program identifies which trading executor published a flow.
const (
	ProgramTour       = "tour"
	ProgramTradeRoute = "trade-route"
	ProgramArb        = "arb"
)

// Tranche is one buy or sell intent at a hop.
type Tranche struct {
	Good              string `json:"good"`
	IsBuy             bool   `json:"isBuy"`
	Units             int    `json:"units"`
	ExpectedUnitPrice int    `json:"expectedUnitPrice"`
}

// Hop is a planned future stop with its intended tranches.
type Hop struct {
	Waypoint string    `json:"waypoint"`
	Tranches []Tranche `json:"tranches"`
}

// CargoItem is one good currently aboard the hull.
type CargoItem struct {
	Good  string `json:"good"`
	Units int    `json:"units"`
}

// Leg is the hull's current in-progress leg. Timestamps are best-effort from the
// executor's ship nav; the visualizer server overlays PG nav for position truth.
type Leg struct {
	From       string    `json:"from"`
	To         string    `json:"to"`
	DepartedAt time.Time `json:"departedAt"`
	ArrivesAt  time.Time `json:"arrivesAt"`
}

// Projection is the run's projected economics.
type Projection struct {
	Profit      int64   `json:"profit"`
	RatePerHour float64 `json:"ratePerHour"`
}

// Flow is one active trading run's published snapshot. Field order here IS the
// JSON field order of the payload contract.
type Flow struct {
	ContainerID   string      `json:"containerId"`
	Program       string      `json:"program"`
	Ship          string      `json:"ship"`
	TourID        *string     `json:"tourId"`
	CurrentLeg    *Leg        `json:"currentLeg"`
	Cargo         []CargoItem `json:"cargo"`
	RemainingHops []Hop       `json:"remainingHops"`
	Projected     *Projection `json:"projected"`
	PlannedAt     time.Time   `json:"plannedAt"`
}

// Feed is the top-level GET /api/flows payload.
type Feed struct {
	Flows       []Flow    `json:"flows"`
	GeneratedAt time.Time `json:"generatedAt"`
}

// Registry is a concurrency-safe, in-memory set of active flows keyed by
// container id.
type Registry struct {
	mu    sync.RWMutex
	flows map[string]Flow
	now   func() time.Time // injectable for deterministic tests
}

// New returns an empty registry.
func New() *Registry {
	return &Registry{flows: make(map[string]Flow), now: time.Now}
}

// Publish inserts or overwrites the flow for f.ContainerID. Overwrite is
// intentional: executors re-publish on every re-adoption and leg boundary, so the
// latest snapshot wins. A blank container id is ignored.
func (r *Registry) Publish(f Flow) {
	if f.ContainerID == "" {
		return
	}
	r.mu.Lock()
	r.flows[f.ContainerID] = f
	r.mu.Unlock()
}

// Remove drops the flow for the given container id (called at terminal exit).
func (r *Registry) Remove(containerID string) {
	r.mu.Lock()
	delete(r.flows, containerID)
	r.mu.Unlock()
}

// Snapshot returns the current feed. Flows are sorted by container id for a
// deterministic payload; an empty registry yields a non-nil empty slice so the
// JSON is {"flows":[]}, never {"flows":null}.
func (r *Registry) Snapshot() Feed {
	r.mu.RLock()
	flows := make([]Flow, 0, len(r.flows))
	for _, f := range r.flows {
		flows = append(flows, f)
	}
	r.mu.RUnlock()
	sort.Slice(flows, func(i, j int) bool { return flows[i].ContainerID < flows[j].ContainerID })
	return Feed{Flows: flows, GeneratedAt: r.now().UTC()}
}

// --- process-wide singleton (mirrors the metrics SetGlobal*Collector idiom) ---

var global *Registry

// SetGlobal installs the process-wide registry that the free functions delegate
// to. The daemon wires this once at construction.
func SetGlobal(r *Registry) { global = r }

// Publish is the nil-safe free function executors call. When no registry is
// installed (unit tests, or any path where the daemon did not wire one) it is a
// no-op — a missed publish can never touch the trade path.
func Publish(f Flow) {
	if global != nil {
		global.Publish(f)
	}
}

// Remove is the nil-safe free function the container runner calls at terminal exit.
func Remove(containerID string) {
	if global != nil {
		global.Remove(containerID)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd gobot && go test ./internal/adapters/flowfeed/...`
Expected: PASS (6 tests).

- [ ] **Step 5: Commit**

```bash
git add gobot/internal/adapters/flowfeed/registry.go gobot/internal/adapters/flowfeed/registry_test.go
git commit --no-verify -m "feat(flowfeed): in-memory active-flow registry + singleton (flows)"
```

---

## Task 2: `GET /api/flows` handler + golden-payload conformance test

**Files:**
- Create: `gobot/internal/adapters/flowfeed/handler.go`
- Test: `gobot/internal/adapters/flowfeed/handler_test.go`

**Interfaces:**
- Consumes: `*Registry` and its `Snapshot()` (Task 1).
- Produces: `func NewFlowsHandler(reg *Registry) http.Handler` — GET-only; sets `Content-Type: application/json`; JSON-encodes `reg.Snapshot()`. Non-GET → 405. Read-only: it never calls `Publish`/`Remove`.

- [ ] **Step 1: Write the failing test**

```go
// gobot/internal/adapters/flowfeed/handler_test.go
package flowfeed

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fixedClock pins GeneratedAt so the golden bytes are stable.
func fixedRegistry() *Registry {
	r := New()
	r.now = func() time.Time { return time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC) }
	return r
}

func TestFlowsHandler_EmptyFleetReturnsEmptyFlows(t *testing.T) {
	srv := httptest.NewServer(NewFlowsHandler(fixedRegistry()))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	got := strings.TrimSpace(string(body))
	want := `{"flows":[],"generatedAt":"2026-07-11T12:00:00Z"}`
	if got != want {
		t.Errorf("empty payload mismatch:\n got: %s\nwant: %s", got, want)
	}
}

func TestFlowsHandler_GoldenPayloadShape(t *testing.T) {
	reg := fixedRegistry()
	tourID := "tour-run-TORWIND-54-beba64e7"
	reg.Publish(Flow{
		ContainerID: tourID,
		Program:     ProgramTour,
		Ship:        "TORWIND-54",
		TourID:      &tourID,
		CurrentLeg: &Leg{
			From:       "X1-UU57-E21Z",
			To:         "X1-ZC66-C39A",
			DepartedAt: time.Date(2026, 7, 11, 11, 55, 0, 0, time.UTC),
			ArrivesAt:  time.Date(2026, 7, 11, 12, 3, 0, 0, time.UTC),
		},
		Cargo: []CargoItem{{Good: "EQUIPMENT", Units: 200}},
		RemainingHops: []Hop{{
			Waypoint: "X1-ZC66-F12F",
			Tranches: []Tranche{{Good: "ADVANCED_CIRCUITRY", IsBuy: false, Units: 100, ExpectedUnitPrice: 4100}},
		}},
		Projected: &Projection{Profit: 312000, RatePerHour: 445000},
		PlannedAt: time.Date(2026, 7, 11, 11, 54, 30, 0, time.UTC),
	})
	srv := httptest.NewServer(NewFlowsHandler(reg))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	got := strings.TrimSpace(string(body))
	want := `{"flows":[{"containerId":"tour-run-TORWIND-54-beba64e7","program":"tour",` +
		`"ship":"TORWIND-54","tourId":"tour-run-TORWIND-54-beba64e7",` +
		`"currentLeg":{"from":"X1-UU57-E21Z","to":"X1-ZC66-C39A",` +
		`"departedAt":"2026-07-11T11:55:00Z","arrivesAt":"2026-07-11T12:03:00Z"},` +
		`"cargo":[{"good":"EQUIPMENT","units":200}],` +
		`"remainingHops":[{"waypoint":"X1-ZC66-F12F","tranches":[{"good":"ADVANCED_CIRCUITRY",` +
		`"isBuy":false,"units":100,"expectedUnitPrice":4100}]}],` +
		`"projected":{"profit":312000,"ratePerHour":445000},` +
		`"plannedAt":"2026-07-11T11:54:30Z"}],"generatedAt":"2026-07-11T12:00:00Z"}`
	if got != want {
		t.Errorf("golden payload mismatch:\n got: %s\nwant: %s", got, want)
	}
}

func TestFlowsHandler_RejectsNonGet(t *testing.T) {
	srv := httptest.NewServer(NewFlowsHandler(fixedRegistry()))
	t.Cleanup(srv.Close)
	resp, err := http.Post(srv.URL, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", resp.StatusCode)
	}
}

func TestFlowsHandler_IsReadOnly(t *testing.T) {
	reg := fixedRegistry()
	reg.Publish(Flow{ContainerID: "c1"})
	h := NewFlowsHandler(reg)
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/flows", nil))
	}
	if n := len(reg.Snapshot().Flows); n != 1 {
		t.Fatalf("GET must not mutate the registry; want 1 flow, got %d", n)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd gobot && go test ./internal/adapters/flowfeed/... -run TestFlowsHandler`
Expected: FAIL — undefined `NewFlowsHandler`.

- [ ] **Step 3: Write minimal implementation**

```go
// gobot/internal/adapters/flowfeed/handler.go
package flowfeed

import (
	"encoding/json"
	"net/http"
)

// NewFlowsHandler returns the read-only GET /api/flows handler backed by reg. It
// serializes a registry snapshot and never mutates state (RULINGS #4). It shares
// the metrics listener's trust boundary — no auth is added here.
func NewFlowsHandler(reg *Registry) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(reg.Snapshot()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd gobot && go test ./internal/adapters/flowfeed/...`
Expected: PASS (all Task 1 + Task 2 tests). If the golden test fails on `ratePerHour`, confirm Go emits `445000` (whole float64 → no decimal); the fixture uses `445000` deliberately to match.

- [ ] **Step 5: Commit**

```bash
git add gobot/internal/adapters/flowfeed/handler.go gobot/internal/adapters/flowfeed/handler_test.go
git commit --no-verify -m "feat(flowfeed): GET /api/flows handler + golden-payload test (flows)"
```

---

## Task 3: Wire the registry + handler into the daemon

**Files:**
- Modify: `gobot/internal/adapters/grpc/daemon_server.go` — add a `flowRegistry *flowfeed.Registry` field, construct it + `flowfeed.SetGlobal(...)` unconditionally in `NewDaemonServer`, and register the route on the metrics mux inside `startMetricsServer` via a small helper.
- Test: `gobot/internal/adapters/grpc/daemon_flows_wiring_test.go`

**Interfaces:**
- Consumes: `flowfeed.New`, `flowfeed.SetGlobal`, `flowfeed.NewFlowsHandler` (Tasks 1–2).
- Produces: `func registerFlowsRoute(mux *http.ServeMux, reg *flowfeed.Registry)` (unexported, package `grpc`) — mounts `/api/flows` on the given mux. Used by `startMetricsServer` and unit-testable in isolation.

**Wiring facts (verified — confirm they still hold before editing):**
- `startMetricsServer` at `daemon_server.go:549` builds `mux := http.NewServeMux()` (:556) and registers `/metrics` at :557–562. The route registration goes **immediately after that block** (~:562). The mux serves only when `metricsConfig.Enabled` (early return at :551) — same trust boundary as `/metrics`.
- The `DaemonServer` struct's `// Metrics` field group is at :78–88 — add the field there.
- `NewDaemonServer` (`:136`) builds the struct literal at :191–215. The duty-cycle sampler and API budget tracker are wired **unconditionally, above** the `Enabled` block — do the same for the registry so executor publishes always have a target even when metrics serving is disabled.
- `import "github.com/andrescamacho/spacetraders-go/internal/adapters/flowfeed"` must be added (same module prefix as the existing metrics import on `daemon_server.go:20`).

- [ ] **Step 1: Write the failing test**

```go
// gobot/internal/adapters/grpc/daemon_flows_wiring_test.go
package grpc

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/flowfeed"
)

func TestRegisterFlowsRoute_ServesRegistrySnapshot(t *testing.T) {
	reg := flowfeed.New()
	reg.Publish(flowfeed.Flow{ContainerID: "arb-run-SHIP-1-abc", Program: flowfeed.ProgramArb, Ship: "SHIP-1"})

	mux := http.NewServeMux()
	registerFlowsRoute(mux, reg)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/flows")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"arb-run-SHIP-1-abc"`) {
		t.Errorf("route did not serve the published flow; body=%s", body)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd gobot && go test ./internal/adapters/grpc/... -run TestRegisterFlowsRoute`
Expected: FAIL — undefined `registerFlowsRoute`.

- [ ] **Step 3: Write minimal implementation**

Add the import beside the existing metrics import (`daemon_server.go:20`):

```go
	"github.com/andrescamacho/spacetraders-go/internal/adapters/flowfeed"
```

Add the field to the `DaemonServer` struct in the `// Metrics` group (~:88):

```go
	// Metrics
	// ... existing collector fields ...
	flowRegistry *flowfeed.Registry
```

In `NewDaemonServer`, **before** the struct literal (with the other unconditional wiring), construct the registry and install it as the process-wide singleton:

```go
	// Read-only active-flow feed: constructed unconditionally so trading executors
	// always have a publish target (the HTTP route is only served when metrics are
	// enabled). RULINGS #4: exposure only — no decision code reads this.
	flowRegistry := flowfeed.New()
	flowfeed.SetGlobal(flowRegistry)
```

Add `flowRegistry: flowRegistry,` to the `&DaemonServer{...}` struct literal (:191–215).

Add the route helper (anywhere in the file, e.g. just above `startMetricsServer`):

```go
// registerFlowsRoute mounts the read-only GET /api/flows handler on the metrics
// mux, beside /metrics (same localhost trust boundary; no auth change).
func registerFlowsRoute(mux *http.ServeMux, reg *flowfeed.Registry) {
	mux.Handle("/api/flows", flowfeed.NewFlowsHandler(reg))
}
```

In `startMetricsServer`, immediately after the `/metrics` `mux.Handle(...)` block (~:562, before the `net.Listen` call):

```go
	registerFlowsRoute(mux, s.flowRegistry)
```

- [ ] **Step 4: Run test to verify it passes, and confirm the whole module compiles**

Run: `cd gobot && go test ./internal/adapters/grpc/... -run TestRegisterFlowsRoute && go build ./...`
Expected: PASS + clean build.

- [ ] **Step 5: Runtime smoke (verifies SetGlobal + serving path end-to-end)**

Start the daemon locally with metrics enabled, then:

Run: `curl -s localhost:9090/api/flows`
Expected: `{"flows":[],"generatedAt":"..."}` (empty fleet at boot). This confirms the same `flowRegistry` instance is both installed via `SetGlobal` and served by the route.

- [ ] **Step 6: Commit**

```bash
git add gobot/internal/adapters/grpc/daemon_server.go gobot/internal/adapters/grpc/daemon_flows_wiring_test.go
git commit --no-verify -m "feat(flowfeed): serve /api/flows on the metrics mux + install global registry (flows)"
```

---

## Task 4: Remove a flow at the shared runner's terminal exit seam

**Files:**
- Modify: `gobot/internal/adapters/grpc/container_runner.go` — call `flowfeed.Remove(...)` from `releaseShipAssignments`, guarded so it fires only on terminal reasons.
- Test: `gobot/internal/adapters/grpc/container_runner_flow_test.go`

**Why here and not in the executors:** `releaseShipAssignments(reason string)` (`container_runner.go:1099`) is the **single shared chokepoint** for a container's end-of-life. It is called with terminal reasons `"completed"` (:600), `"failed"` (:496, :557), `"claim_failed"` (:265), `"stopped"` (:318), and with the resumable reason `"canceled"` (:441, :471). The `isStopping` resumable path (:428–431) does **not** call it at all. A `defer flowfeed.Remove(...)` inside each executor's `Handle` would fire on resumable exits too, blanking a container that is about to be **re-adopted** with the same id (sp-7yej invariant #4) — the re-adopted container would show no flow until its next publish. Gating on `reason != "canceled"` inside this one function removes on every terminal path and preserves the entry on the resumable one. The container id at this seam is `r.containerEntity.ID()`.

**Interfaces:**
- Produces: `func flowRemovalWanted(reason string) bool` (unexported) — returns `false` only for `"canceled"`. Extracted so the terminal-vs-resumable contract is unit-tested directly; `releaseShipAssignments` uses it.
- Consumes: `flowfeed.Remove` (Task 1), `r.containerEntity.ID()`.

- [ ] **Step 1: Write the failing test**

```go
// gobot/internal/adapters/grpc/container_runner_flow_test.go
package grpc

import "testing"

// The sp-7yej lifecycle contract: a flow is removed on every TERMINAL exit reason,
// and preserved on the resumable "canceled" reason (the container re-adopts with the
// same id and re-publishes).
func TestFlowRemovalWanted_TerminalVsResumable(t *testing.T) {
	terminal := []string{"completed", "failed", "claim_failed", "stopped"}
	for _, reason := range terminal {
		if !flowRemovalWanted(reason) {
			t.Errorf("reason %q is terminal — want removal", reason)
		}
	}
	if flowRemovalWanted("canceled") {
		t.Error(`reason "canceled" is resumable (re-adopted) — must NOT remove the flow`)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd gobot && go test ./internal/adapters/grpc/... -run TestFlowRemovalWanted`
Expected: FAIL — undefined `flowRemovalWanted`.

- [ ] **Step 3: Write minimal implementation**

Add the helper (near `releaseShipAssignments`):

```go
// flowRemovalWanted reports whether a container exit for the given reason is
// terminal and should drop the container's flow from the read-only feed. Only the
// resumable "canceled" reason (ctx-cancel before re-adoption) preserves the entry,
// so the re-adopted container keeps its flow until it re-publishes (sp-7yej inv-4).
func flowRemovalWanted(reason string) bool {
	return reason != "canceled"
}
```

Insert the removal at the top of `releaseShipAssignments` (before the `r.shipRepo == nil` early return at :1100, so it fires regardless of ship-repo state):

```go
func (r *ContainerRunner) releaseShipAssignments(reason string) {
	if flowRemovalWanted(reason) {
		flowfeed.Remove(r.containerEntity.ID())
	}
	if r.shipRepo == nil {
		return
	}
	// ... existing body unchanged ...
```

Add the import beside the existing `metrics` import in `container_runner.go`:

```go
	"github.com/andrescamacho/spacetraders-go/internal/adapters/flowfeed"
```

- [ ] **Step 4: Run test to verify it passes, and confirm the package compiles**

Run: `cd gobot && go test ./internal/adapters/grpc/... && go build ./...`
Expected: PASS + clean build (existing runner tests still green).

- [ ] **Step 5: Commit**

```bash
git add gobot/internal/adapters/grpc/container_runner.go gobot/internal/adapters/grpc/container_runner_flow_test.go
git commit --no-verify -m "feat(flowfeed): drop flow at terminal container exit, keep on resumable (flows)"
```

---

## Task 5: Arb publish point (collision-free)

**Files:**
- Create: `gobot/internal/application/trading/commands/flow_publish.go` — pure `flowfeed.Flow` builders + a ship-cargo mapper (shared by Tasks 5–7).
- Test: `gobot/internal/application/trading/commands/flow_publish_test.go`
- Modify: `gobot/internal/application/trading/commands/run_arb_coordinator.go` — one publish call at the adoption seam. **This file is NOT under the P0 collision hold** — proceed now.

**Seam facts (verified):** `RunArbCoordinatorHandler.execute` (`run_arb_coordinator.go:267`) settles the lane at :283–288 (`cmd.Good`/`BuyAt`/`SellAt` validated) and loads the hull at :292–295 (`ship, err := h.legs.loadShip(...)`). Publish **right after the ship loads** (so `cargo` is available). Arb's position during flight comes from the visualizer's nav join, so the daemon publishes `currentLeg: null` and expresses intent as the sell hop. `RunArbCoordinatorCommand` fields (verified, :38–75): `ShipSymbol`, `Good`, `BuyAt`, `SellAt`, `MaxUnits`, `ContainerID`, `QuotedDestBid`. Ship cargo: `ship.Cargo()` returns `*shared.Cargo` with `Inventory []*shared.CargoItem`, each `{Symbol, Units}`.

**Interfaces:**
- Produces (in `flow_publish.go`): `func shipCargoItems(ship *navigation.Ship) []flowfeed.CargoItem`; `func buildArbFlow(cmd *RunArbCoordinatorCommand, cargo []flowfeed.CargoItem, now time.Time) flowfeed.Flow`.
- Consumes: `flowfeed.Publish` at the seam.

- [ ] **Step 1: Write the failing test**

```go
// gobot/internal/application/trading/commands/flow_publish_test.go
package commands

import (
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/flowfeed"
)

func TestBuildArbFlow_MapsLaneIntent(t *testing.T) {
	cmd := &RunArbCoordinatorCommand{
		ContainerID:   "arb-run-SHIP-1-abc",
		ShipSymbol:    "SHIP-1",
		Good:          "IRON_ORE",
		BuyAt:         "X1-AA-A1",
		SellAt:        "X1-AA-B2",
		MaxUnits:      120,
		QuotedDestBid: 55,
	}
	cargo := []flowfeed.CargoItem{{Good: "IRON_ORE", Units: 40}}
	now := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)

	f := buildArbFlow(cmd, cargo, now)

	if f.Program != flowfeed.ProgramArb {
		t.Errorf("program = %q, want arb", f.Program)
	}
	if f.ContainerID != "arb-run-SHIP-1-abc" || f.Ship != "SHIP-1" {
		t.Errorf("id/ship mismatch: %+v", f)
	}
	if f.TourID != nil {
		t.Errorf("arb tourId must be null, got %v", *f.TourID)
	}
	if f.CurrentLeg != nil {
		t.Errorf("arb currentLeg must be null (nav join owns position), got %+v", f.CurrentLeg)
	}
	if f.Projected != nil {
		t.Errorf("arb projected must be null at adoption, got %+v", f.Projected)
	}
	if len(f.RemainingHops) != 1 || f.RemainingHops[0].Waypoint != "X1-AA-B2" {
		t.Fatalf("want one sell hop at X1-AA-B2, got %+v", f.RemainingHops)
	}
	tr := f.RemainingHops[0].Tranches[0]
	if tr.Good != "IRON_ORE" || tr.IsBuy || tr.Units != 120 || tr.ExpectedUnitPrice != 55 {
		t.Errorf("sell tranche mismatch: %+v", tr)
	}
	if len(f.Cargo) != 1 || f.Cargo[0].Good != "IRON_ORE" || f.Cargo[0].Units != 40 {
		t.Errorf("cargo mismatch: %+v", f.Cargo)
	}
	if !f.PlannedAt.Equal(now) {
		t.Errorf("plannedAt = %v, want %v", f.PlannedAt, now)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd gobot && go test ./internal/application/trading/... -run TestBuildArbFlow`
Expected: FAIL — undefined `buildArbFlow`.

- [ ] **Step 3: Write minimal implementation**

```go
// gobot/internal/application/trading/commands/flow_publish.go
package commands

import (
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/flowfeed"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// shipCargoItems maps the hull's current hold into the flow-feed cargo shape,
// skipping empty slots. Returns a non-nil (possibly empty) slice.
func shipCargoItems(ship *navigation.Ship) []flowfeed.CargoItem {
	items := []flowfeed.CargoItem{}
	if ship == nil {
		return items
	}
	cargo := ship.Cargo()
	if cargo == nil {
		return items
	}
	for _, it := range cargo.Inventory {
		if it == nil || it.Units <= 0 {
			continue
		}
		items = append(items, flowfeed.CargoItem{Good: it.Symbol, Units: it.Units})
	}
	return items
}

// buildArbFlow maps a one-shot arb run's intent into a flow-feed snapshot. Arb's
// live position comes from the visualizer nav join, so currentLeg is null; the
// daemon's unique contribution is the sell hop (buy here now, sell that good at
// SellAt for up to MaxUnits near QuotedDestBid).
func buildArbFlow(cmd *RunArbCoordinatorCommand, cargo []flowfeed.CargoItem, now time.Time) flowfeed.Flow {
	return flowfeed.Flow{
		ContainerID: cmd.ContainerID,
		Program:     flowfeed.ProgramArb,
		Ship:        cmd.ShipSymbol,
		TourID:      nil,
		CurrentLeg:  nil,
		Cargo:       cargo,
		RemainingHops: []flowfeed.Hop{{
			Waypoint: cmd.SellAt,
			Tranches: []flowfeed.Tranche{{
				Good:              cmd.Good,
				IsBuy:             false,
				Units:             cmd.MaxUnits,
				ExpectedUnitPrice: cmd.QuotedDestBid,
			}},
		}},
		Projected: nil,
		PlannedAt: now,
	}
}
```

Add the publish call in `run_arb_coordinator.go`, immediately after the hull loads (after the `if err != nil { return err }` at :295):

```go
	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return err
	}
	// Expose this run's intent on the read-only flow feed (fire-and-forget; a missed
	// publish never touches the trade path — RULINGS #4).
	flowfeed.Publish(buildArbFlow(cmd, shipCargoItems(ship), time.Now().UTC()))
```

Ensure `run_arb_coordinator.go` imports `flowfeed` and `time` (add `time` only if not already imported).

- [ ] **Step 4: Run test to verify it passes, and confirm the package + module compile**

Run: `cd gobot && go test ./internal/application/trading/... -run TestBuildArbFlow && go build ./...`
Expected: PASS + clean build.

- [ ] **Step 5: Commit**

```bash
git add gobot/internal/application/trading/commands/flow_publish.go gobot/internal/application/trading/commands/flow_publish_test.go gobot/internal/application/trading/commands/run_arb_coordinator.go
git commit --no-verify -m "feat(flowfeed): arb run publishes intent to the flow feed (flows)"
```

---

## Task 6: Tour publish points — plan adoption + leg boundary

> ### ⛔ [HOLD: rebase after sp-4hl5 / sp-1pli land]
> This task **edits `run_tour_coordinator.go`**, which sp-4hl5 / sp-1pli are editing now. Do not start until both have merged. After rebasing onto the merged result, **re-confirm every quoted line number below** (they will have shifted) by locating the named functions and statements, then edit. The new `buildTourFlow` code (added to the already-created `flow_publish.go`) is collision-free and can be written first; only the two seam insertions are hold-sensitive.

**Files:**
- Modify: `gobot/internal/application/trading/commands/flow_publish.go` — add `buildTourFlow` (append; collision-free).
- Modify: `gobot/internal/application/trading/commands/flow_publish_test.go` — add tests (append; collision-free).
- Modify: `gobot/internal/application/trading/commands/run_tour_coordinator.go` — two publish calls **([HOLD])**.

**Seam facts (verified against current HEAD — re-confirm post-rebase):**
- **Adoption:** `runOneTour` (`run_tour_coordinator.go:771`) obtains the plan from `planAndReserve` at :792 and passes the feasibility check at :796. Publish at ~:797 (after `if !feasible { return ... }`, plan known feasible). The hull `ship` was loaded at :783.
- **Leg boundary:** `executePlan` (`run_tour_coordinator.go:892`) loops `for legIdx, leg := range plan.Legs` (:904). Travel dispatches the hull at :909 (`h.legs.travel(...)`); the hull is then in transit toward `leg.Waypoint`. Publish **immediately after a successful travel call** (before `dock` at :919) so `currentLeg` = the leg now being flown. `ship` is reloaded at :905; `ship.ArrivalTime()` returns `*time.Time` after travel.
- **Types (verified, `domain/routing/tour.go`):** `TourPlan{Legs []TourLeg; ProjectedProfit int64; ProjectedCreditsPerHour float64; ...}`; `TourLeg{Waypoint string; System string; Trades []TourTrade; ...}`; `TourTrade{Good string; Units int; ExpectedUnitPrice int; IsBuy bool; ...}`. `RunTourCoordinatorCommand.ContainerID` is the tour id (:136).
- `buildTourFlow` derives `currentLeg.From` from the plan (`Legs[idx-1].Waypoint`, or empty for the first leg where the nav join owns the origin), so the seam does not need to capture the pre-travel waypoint.

**Interfaces:**
- Produces: `func buildTourFlow(cmd *RunTourCoordinatorCommand, plan *routing.TourPlan, currentLegIdx int, arrivesAt time.Time, cargo []flowfeed.CargoItem, now time.Time) flowfeed.Flow` — `currentLegIdx < 0` means "adopted, not yet flying a leg" (`currentLeg` null, `remainingHops` = all legs); `currentLegIdx >= 0` sets `currentLeg` to that leg and `remainingHops` to the legs after it.
- Consumes: `shipCargoItems` (Task 5), `flowfeed.Publish`.

- [ ] **Step 1: Write the failing test** (append to `flow_publish_test.go`)

```go
func tourPlanFixture() *routing.TourPlan {
	return &routing.TourPlan{
		Legs: []routing.TourLeg{
			{Waypoint: "X1-AA-A1", Trades: []routing.TourTrade{{Good: "IRON", Units: 50, ExpectedUnitPrice: 30, IsBuy: true}}},
			{Waypoint: "X1-AA-B2", Trades: []routing.TourTrade{{Good: "IRON", Units: 50, ExpectedUnitPrice: 42, IsBuy: false}}},
			{Waypoint: "X1-AA-C3", Trades: []routing.TourTrade{{Good: "GOLD", Units: 10, ExpectedUnitPrice: 900, IsBuy: true}}},
		},
		ProjectedProfit:         600,
		ProjectedCreditsPerHour: 5400,
	}
}

func TestBuildTourFlow_AdoptionHasNoCurrentLegAndAllHops(t *testing.T) {
	cmd := &RunTourCoordinatorCommand{ContainerID: "tour-run-SHIP-9-xyz", ShipSymbol: "SHIP-9"}
	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)

	f := buildTourFlow(cmd, tourPlanFixture(), -1, time.Time{}, nil, now)

	if f.Program != flowfeed.ProgramTour {
		t.Errorf("program = %q, want tour", f.Program)
	}
	if f.TourID == nil || *f.TourID != "tour-run-SHIP-9-xyz" {
		t.Errorf("tourId must equal the container id, got %v", f.TourID)
	}
	if f.CurrentLeg != nil {
		t.Errorf("adoption currentLeg must be null, got %+v", f.CurrentLeg)
	}
	if len(f.RemainingHops) != 3 {
		t.Fatalf("adoption remainingHops = %d, want 3 (all legs)", len(f.RemainingHops))
	}
	if f.Projected == nil || f.Projected.Profit != 600 || f.Projected.RatePerHour != 5400 {
		t.Errorf("projected mismatch: %+v", f.Projected)
	}
}

func TestBuildTourFlow_LegBoundarySetsCurrentLegAndTrimsHops(t *testing.T) {
	cmd := &RunTourCoordinatorCommand{ContainerID: "tour-run-SHIP-9-xyz", ShipSymbol: "SHIP-9"}
	arrives := time.Date(2026, 7, 11, 10, 8, 0, 0, time.UTC)
	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)

	// Flying leg index 1 (the second leg): from Legs[0].Waypoint to Legs[1].Waypoint.
	f := buildTourFlow(cmd, tourPlanFixture(), 1, arrives, nil, now)

	if f.CurrentLeg == nil {
		t.Fatal("leg-boundary currentLeg must be set")
	}
	if f.CurrentLeg.From != "X1-AA-A1" || f.CurrentLeg.To != "X1-AA-B2" {
		t.Errorf("currentLeg from/to = %s/%s, want X1-AA-A1/X1-AA-B2", f.CurrentLeg.From, f.CurrentLeg.To)
	}
	if !f.CurrentLeg.DepartedAt.Equal(now) || !f.CurrentLeg.ArrivesAt.Equal(arrives) {
		t.Errorf("currentLeg timestamps mismatch: %+v", f.CurrentLeg)
	}
	if len(f.RemainingHops) != 1 || f.RemainingHops[0].Waypoint != "X1-AA-C3" {
		t.Fatalf("remainingHops after leg 1 = %+v, want [X1-AA-C3]", f.RemainingHops)
	}
	tr := f.RemainingHops[0].Tranches[0]
	if tr.Good != "GOLD" || !tr.IsBuy || tr.Units != 10 || tr.ExpectedUnitPrice != 900 {
		t.Errorf("hop tranche mismatch: %+v", tr)
	}
}

func TestBuildTourFlow_FirstLegHasEmptyFrom(t *testing.T) {
	cmd := &RunTourCoordinatorCommand{ContainerID: "c", ShipSymbol: "S"}
	f := buildTourFlow(cmd, tourPlanFixture(), 0, time.Time{}, nil, time.Now())
	if f.CurrentLeg == nil || f.CurrentLeg.From != "" || f.CurrentLeg.To != "X1-AA-A1" {
		t.Errorf("first leg: want From empty (nav owns origin), To=X1-AA-A1, got %+v", f.CurrentLeg)
	}
}
```

Add the `routing` import to `flow_publish_test.go` (`github.com/andrescamacho/spacetraders-go/internal/domain/routing`).

- [ ] **Step 2: Run test to verify it fails**

Run: `cd gobot && go test ./internal/application/trading/... -run TestBuildTourFlow`
Expected: FAIL — undefined `buildTourFlow`.

- [ ] **Step 3: Write minimal implementation** (append to `flow_publish.go`; add the `routing` import)

```go
// buildTourFlow maps a tour plan snapshot into a flow-feed Flow. currentLegIdx < 0
// means the plan was just adopted (no leg in progress): currentLeg is null and
// remainingHops is every planned leg. currentLegIdx >= 0 means the hull is flying
// that leg: currentLeg describes it (From derived from the previous leg, empty for
// the first leg where the nav join owns the origin) and remainingHops is the legs
// after it.
func buildTourFlow(cmd *RunTourCoordinatorCommand, plan *routing.TourPlan, currentLegIdx int, arrivesAt time.Time, cargo []flowfeed.CargoItem, now time.Time) flowfeed.Flow {
	tourID := cmd.ContainerID
	var currentLeg *flowfeed.Leg
	remaining := plan.Legs
	if currentLegIdx >= 0 && currentLegIdx < len(plan.Legs) {
		from := ""
		if currentLegIdx > 0 {
			from = plan.Legs[currentLegIdx-1].Waypoint
		}
		currentLeg = &flowfeed.Leg{
			From:       from,
			To:         plan.Legs[currentLegIdx].Waypoint,
			DepartedAt: now,
			ArrivesAt:  arrivesAt,
		}
		remaining = plan.Legs[currentLegIdx+1:]
	}
	hops := make([]flowfeed.Hop, 0, len(remaining))
	for _, leg := range remaining {
		tranches := make([]flowfeed.Tranche, 0, len(leg.Trades))
		for _, tr := range leg.Trades {
			tranches = append(tranches, flowfeed.Tranche{
				Good:              tr.Good,
				IsBuy:             tr.IsBuy,
				Units:             tr.Units,
				ExpectedUnitPrice: tr.ExpectedUnitPrice,
			})
		}
		hops = append(hops, flowfeed.Hop{Waypoint: leg.Waypoint, Tranches: tranches})
	}
	return flowfeed.Flow{
		ContainerID:   cmd.ContainerID,
		Program:       flowfeed.ProgramTour,
		Ship:          cmd.ShipSymbol,
		TourID:        &tourID,
		CurrentLeg:    currentLeg,
		Cargo:         cargo,
		RemainingHops: hops,
		Projected:     &flowfeed.Projection{Profit: plan.ProjectedProfit, RatePerHour: plan.ProjectedCreditsPerHour},
		PlannedAt:     now,
	}
}
```

**[HOLD] seam edits in `run_tour_coordinator.go`** (after rebase, re-confirm line numbers):

Adoption — after the feasibility check in `runOneTour` (~:797), where `plan` and `ship` are in scope:

```go
	// Publish the adopted tour plan to the read-only flow feed (RULINGS #4).
	flowfeed.Publish(buildTourFlow(cmd, plan, -1, time.Time{}, shipCargoItems(ship), time.Now().UTC()))
```

Leg boundary — in `executePlan`, immediately after the successful `h.legs.travel(...)` call (~:909), where `plan`, `legIdx`, and the reloaded `ship` are in scope:

```go
	arrivesAt := time.Time{}
	if at := ship.ArrivalTime(); at != nil {
		arrivesAt = *at
	}
	flowfeed.Publish(buildTourFlow(cmd, plan, legIdx, arrivesAt, shipCargoItems(ship), time.Now().UTC()))
```

Ensure `run_tour_coordinator.go` imports `flowfeed` and `time`.

- [ ] **Step 4: Run tests to verify they pass, and confirm the module compiles**

Run: `cd gobot && go test ./internal/application/trading/... && go build ./...`
Expected: PASS (new + all existing tour tests) + clean build.

- [ ] **Step 5: Commit**

```bash
git add gobot/internal/application/trading/commands/flow_publish.go gobot/internal/application/trading/commands/flow_publish_test.go gobot/internal/application/trading/commands/run_tour_coordinator.go
git commit --no-verify -m "feat(flowfeed): tour publishes plan at adoption + each leg boundary (flows)"
```

---

## Task 7: Trade-route publish point — lane commitment

> ### ⛔ [HOLD: rebase after sp-4hl5 / sp-1pli land]
> This task **edits `run_trade_route_coordinator.go`**, which sp-4hl5 / sp-1pli are editing now. Do not start until both have merged. After rebasing, **re-confirm the quoted line numbers** by locating the named function and statements. The `buildTradeRouteFlow` code (appended to `flow_publish.go`) is collision-free and can be written first; only the seam insertion is hold-sensitive.

**Files:**
- Modify: `gobot/internal/application/trading/commands/flow_publish.go` — add `buildTradeRouteFlow` (append; collision-free).
- Modify: `gobot/internal/application/trading/commands/flow_publish_test.go` — add tests (append; collision-free).
- Modify: `gobot/internal/application/trading/commands/run_trade_route_coordinator.go` — one publish call **([HOLD])**.

**Seam facts (verified against current HEAD — re-confirm post-rebase):**
- The trade-route coordinator re-scans lanes every outer iteration and commits **one lane per circuit** (no multi-leg plan struct). In `execute` (`run_trade_route_coordinator.go:455`), the committed lane is stamped onto the response at :608–611 (`response.Good`, `response.SourceWaypoint`, `response.DestWaypoint`, `response.Circuits++`) right before `runCircuit` at :630. Publish at ~:611 with the selected `lane`.
- **Types (verified, `domain/trading/arbitrage_lane.go`):** `ArbitrageLane{Good string; SourceWaypoint string; DestWaypoint string; SourceAsk int; DestBid int; SpreadPerUnit int; VolumeCap int; CappedSpread int; ...}`. `SourceWaypoint` = "from", `DestWaypoint` = "to", `DestBid` = sell price, `VolumeCap` = per-circuit units, `CappedSpread` = per-circuit profit.
- Circuit rate: `laneCircuitRatePerHour(lane trading.ArbitrageLane, shipCapacity int, targetDest string) float64` (`run_trade_route_coordinator_travel.go:583`). The seam supplies `shipCapacity` (from the hull already loaded in `execute`) and `cmd.TargetDest`; keep the builder pure by passing the computed rate in.
- At lane commitment the hull has not yet departed on this circuit, so `arrivesAt` is the zero time — the visualizer nav join supplies live position.

**Interfaces:**
- Produces: `func buildTradeRouteFlow(cmd *RunTradeRouteCoordinatorCommand, lane trading.ArbitrageLane, ratePerHour float64, cargo []flowfeed.CargoItem, arrivesAt time.Time, now time.Time) flowfeed.Flow`.
- Consumes: `shipCargoItems` (Task 5), `laneCircuitRatePerHour` (existing), `flowfeed.Publish`.

- [ ] **Step 1: Write the failing test** (append to `flow_publish_test.go`; add the `trading` import `github.com/andrescamacho/spacetraders-go/internal/domain/trading`)

```go
func TestBuildTradeRouteFlow_MapsCommittedLane(t *testing.T) {
	cmd := &RunTradeRouteCoordinatorCommand{ShipSymbol: "SHIP-7"}
	lane := trading.ArbitrageLane{
		Good:           "FUEL",
		SourceWaypoint: "X1-AA-SRC",
		DestWaypoint:   "X1-AA-DST",
		DestBid:        88,
		VolumeCap:      60,
		CappedSpread:   1800,
	}
	cargo := []flowfeed.CargoItem{{Good: "FUEL", Units: 60}}
	now := time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC)

	f := buildTradeRouteFlow(cmd, lane, 12000, cargo, time.Time{}, now)

	if f.Program != flowfeed.ProgramTradeRoute {
		t.Errorf("program = %q, want trade-route", f.Program)
	}
	if f.Ship != "SHIP-7" {
		t.Errorf("ship = %q, want SHIP-7", f.Ship)
	}
	if f.TourID != nil {
		t.Errorf("trade-route tourId must be null, got %v", *f.TourID)
	}
	if f.CurrentLeg == nil || f.CurrentLeg.From != "X1-AA-SRC" || f.CurrentLeg.To != "X1-AA-DST" {
		t.Fatalf("currentLeg from/to mismatch: %+v", f.CurrentLeg)
	}
	if len(f.RemainingHops) != 1 || f.RemainingHops[0].Waypoint != "X1-AA-DST" {
		t.Fatalf("want sell hop at X1-AA-DST, got %+v", f.RemainingHops)
	}
	tr := f.RemainingHops[0].Tranches[0]
	if tr.Good != "FUEL" || tr.IsBuy || tr.Units != 60 || tr.ExpectedUnitPrice != 88 {
		t.Errorf("sell tranche mismatch: %+v", tr)
	}
	if f.Projected == nil || f.Projected.Profit != 1800 || f.Projected.RatePerHour != 12000 {
		t.Errorf("projected mismatch: %+v", f.Projected)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd gobot && go test ./internal/application/trading/... -run TestBuildTradeRouteFlow`
Expected: FAIL — undefined `buildTradeRouteFlow`.

- [ ] **Step 3: Write minimal implementation** (append to `flow_publish.go`; add the `trading` import)

```go
// buildTradeRouteFlow maps the lane a circuit just committed into a flow-feed Flow.
// A trade-route circuit is a single source->dest round trip; currentLeg carries the
// lane's from/to (position truth comes from the nav join, so arrivesAt is best-effort)
// and the sole remaining hop is the sell at the destination.
func buildTradeRouteFlow(cmd *RunTradeRouteCoordinatorCommand, lane trading.ArbitrageLane, ratePerHour float64, cargo []flowfeed.CargoItem, arrivesAt time.Time, now time.Time) flowfeed.Flow {
	return flowfeed.Flow{
		ContainerID: cmd.ContainerID,
		Program:     flowfeed.ProgramTradeRoute,
		Ship:        cmd.ShipSymbol,
		TourID:      nil,
		CurrentLeg: &flowfeed.Leg{
			From:       lane.SourceWaypoint,
			To:         lane.DestWaypoint,
			DepartedAt: now,
			ArrivesAt:  arrivesAt,
		},
		Cargo: cargo,
		RemainingHops: []flowfeed.Hop{{
			Waypoint: lane.DestWaypoint,
			Tranches: []flowfeed.Tranche{{
				Good:              lane.Good,
				IsBuy:             false,
				Units:             lane.VolumeCap,
				ExpectedUnitPrice: lane.DestBid,
			}},
		}},
		Projected: &flowfeed.Projection{Profit: int64(lane.CappedSpread), RatePerHour: ratePerHour},
		PlannedAt: now,
	}
}
```

**[HOLD] seam edit in `run_trade_route_coordinator.go`** (after rebase, re-confirm line numbers) — at ~:611, right after `response.Circuits++`, where the committed `lane` and the loaded hull are in scope. Supply `shipCapacity` from the hull's hold-capacity accessor already used nearby in `execute` (confirm the exact expression on rebase):

```go
	response.Circuits++
	// Publish the committed circuit lane to the read-only flow feed (RULINGS #4).
	flowfeed.Publish(buildTradeRouteFlow(cmd, lane, laneCircuitRatePerHour(lane, shipCapacity, cmd.TargetDest), shipCargoItems(ship), time.Time{}, time.Now().UTC()))
```

Ensure `run_trade_route_coordinator.go` imports `flowfeed` and `time`.

- [ ] **Step 4: Run tests to verify they pass, and confirm the module compiles**

Run: `cd gobot && go test ./internal/application/trading/... && go build ./...`
Expected: PASS (new + all existing trade-route tests) + clean build.

- [ ] **Step 5: Commit**

```bash
git add gobot/internal/application/trading/commands/flow_publish.go gobot/internal/application/trading/commands/flow_publish_test.go gobot/internal/application/trading/commands/run_trade_route_coordinator.go
git commit --no-verify -m "feat(flowfeed): trade-route publishes committed lane to the flow feed (flows)"
```

---

## Task 8: Full-suite verification + gate

**Files:** none (verification + landing only).

- [ ] **Step 1: Full compile + all touched packages green**

```bash
cd gobot && go build ./... \
  && go test ./internal/adapters/flowfeed/... ./internal/adapters/grpc/... ./internal/application/trading/...
```
Expected: clean build; all PASS. If Go's build cache emits a phantom `package X is not in std` error under parallel jobs, run `go clean -cache` and retry — it is environmental, not a code defect.

- [ ] **Step 2: End-to-end runtime smoke (the on-screen acceptance the spec §Testing calls for, daemon side)**

Start the daemon locally with `metrics.enabled: true`. With no active trading containers:

Run: `curl -s localhost:9090/api/flows`
Expected: `{"flows":[],"generatedAt":"..."}`.

Launch one arb run (`spacetraders workflow arb-run --ship <S> --good <G> --buy-at <A> --sell-at <B> ...`), then:

Run: `curl -s localhost:9090/api/flows`
Expected: one flow with `"program":"arb"`, the hull symbol, a `remainingHops` sell hop at `<B>`, and `"currentLeg":null`. After the run completes, re-run the curl and confirm the flow **disappears** (Task 4 terminal-removal path). This closes the publish→serve→remove loop against a live daemon.

- [ ] **Step 3: Gate via captain-gate** (Protocol v2 landing; see memory "captain-gate: commit first")

All work is committed (Steps in Tasks 1–7). From the worktree, with the branch pushed:

```bash
captain-gate --repo spacetraders --worktree <this-worktree-path> --branch <feature-branch> --provision --merge
```
Then verify the merged SHA's numstat matches the intended surface (the `flowfeed` package + the four edited files: `daemon_server.go`, `container_runner.go`, `run_arb_coordinator.go`, and — if Tasks 6–7 landed in this branch — `run_tour_coordinator.go` / `run_trade_route_coordinator.go`):

```bash
git show --numstat <merged-sha>
```
Expected: only the intended files changed; no `.beads/issues.jsonl` staged in any commit.

> **Landing note:** Tasks 1–5 are collision-free and can gate as one branch immediately. Tasks 6–7 are held until sp-4hl5 / sp-1pli merge; they may ride a second branch/gate after rebase. The visualizer tab (spec §2–3) builds against the Task 2 golden payload as a fixture and degrades gracefully until this feed deploys, so the two lanes remain separable (spec §"Delivery notes").

---

## Self-review (done at write time)

**1. Spec §"1. Daemon flow feed" coverage:**
- *In-process plan registry, keyed by container id, publish at adoption + each leg boundary* → Task 1 (registry) + Tasks 5/6/7 (arb adoption; tour adoption + leg boundary; trade-route lane commitment).
- *Tour publishes hops + tranches + projections; trade-route publishes circuit legs; arb publishes its one-shot leg* → `buildTourFlow` / `buildTradeRouteFlow` / `buildArbFlow` respectively.
- *No trading logic reads the registry, no guard interacts (RULINGS #4)* → registry exposes only `Publish`/`Remove`/`Snapshot`; publish is a nil-safe free function; stated in Global Constraints and every builder comment.
- *HTTP handler `GET /api/flows` on the existing metrics mux, localhost, no auth change* → Tasks 2–3.
- *Payload shape* → Task 1 types + Task 2 golden test pin it byte-for-byte.
- *Restart behavior: in-memory, repopulates as executors re-adopt* → registry is process-memory (Task 1); `Publish` overwrites (idempotent on re-adoption); `Remove` gated so resumable/re-adoptable exits keep the entry (Task 4).

**2. Spec §"Degradation" + §"Testing" coverage:**
- *Empty fleet → `{"flows":[]}`* → `TestRegistry_SnapshotEmptyIsNonNilSlice` + `TestFlowsHandler_EmptyFleetReturnsEmptyFlows`.
- *Hull trading without a published plan → position-only (no dashed path)* → arb publishes `currentLeg:null`; the feed simply omits a flow for any container that has not published, and the web overlays nav position.
- *Registry unit tests: publish/replace/remove per lifecycle; handler tests: payload shape, empty fleet, read-only GET* → Tasks 1, 2, 4.
- *Export-proof rigor (metrics Gather() lesson)* → mirrored as httptest golden-payload assertions (the handler's analog of "registered ≠ exported": the type must actually serialize to the contract, not merely compile).

**3. Placeholder scan:** No `TBD`/`similar to Task N`/"add error handling". Every code step is complete. The two intentional, flagged verify-on-rebase points (Tasks 6–7 line numbers, and the `shipCapacity` expression in Task 7) are HOLD-banner consequences, not placeholders — each names the exact function and statement to relocate, with the current-HEAD line number.

**4. Type consistency:** `flowfeed.Flow` field names/order are identical across the registry (Task 1), the golden test (Task 2), and all three builders (Tasks 5–7). `Publish`/`Remove`/`Snapshot`/`SetGlobal`/`New` signatures are stable from Task 1 onward. `shipCargoItems` (Task 5) is reused unchanged by Tasks 6–7. Program constants (`ProgramTour`/`ProgramTradeRoute`/`ProgramArb`) are the single source for the `program` field.

**5. Ambiguity resolved:** The payload's `currentLeg.{departedAt,arrivesAt}` are not stored on any domain plan/lane struct. Resolution: the daemon populates `from`/`to` (intent it owns) and best-effort timestamps from the hull's nav at the leg-boundary publish (`departedAt` = publish time, `arrivesAt` = `ship.ArrivalTime()`); the visualizer server's `/api/flows/live` overlays PG nav for authoritative position (spec §2). Where the daemon cannot cheaply know an in-progress leg (arb pre-departure), it emits `currentLeg:null` rather than fabricate one.
