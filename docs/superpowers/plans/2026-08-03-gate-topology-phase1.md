# Gate Topology (Phase 1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Introduce a single era-invariant seam that resolves gate-construction topology by market *role* — never by waypoint symbol — and wire it into the feedstock delivery path so a good can only ever be dispatched to a market that imports it.

**Architecture:** A new `GateTopology` service in `internal/application/manufacturing/services` wraps a narrow two-method view of `MarketLocator` plus the `supplyChainMap` recipe DAG. It answers four questions: is this good raw, what are its inputs, which waypoint *exports* it (terminal factory), and which waypoint *imports* it (feed target). `deliverInputs` then resolves its destination through `FeedTarget`, which fails closed when no importer exists.

**Tech Stack:** Go 1.x, standard `testing`. No new dependencies.

## Global Constraints

Copied verbatim from `docs/superpowers/specs/2026-08-03-gate-construction-two-fleet-design.md`:

- **ZERO waypoint-symbol literals.** No system or waypoint symbol may be hardcoded. Every location is resolved at runtime by role from market import/export data.
- **Goods are invariant; locations are not.** FAB_MATS, ADVANCED_CIRCUITRY and recipe inputs may be named directly. Waypoints may not.
- **No feature flag, no default-off, no arm seam. Ship ARMED.** Standing Admiral order.
- **Money guards untouched.** The 50k `common.ImmutableReserveFloor` is not read, moved, or weakened by this phase (RULINGS #4). This phase spends nothing.
- **Fail closed on dispatch.** A good with no resolvable importer is never dispatched anywhere (RULINGS #4 posture).
- Do not touch protected paths: `gobot/internal/captain/**`, `cmd/captain-gate/**`, `city/agents/**`.

**Test output must be filtered.** This repo has ~4550 tests across ~107 packages; a raw `go test ./...` will exhaust an agent's context. Always use the filtered forms given in each task. This shell is zsh — `PIPESTATUS` does not work; capture `$?` directly without piping when you need an exit code.

## File Structure

| File | Responsibility |
|---|---|
| `internal/application/manufacturing/services/gate_topology.go` (create) | The `GateTopology` seam: recipe predicates + role-based waypoint resolution |
| `internal/application/manufacturing/services/gate_topology_test.go` (create) | Unit tests incl. the era-invariance source guard |
| `internal/application/manufacturing/services/production_executor_dock.go` (modify, `deliverInputs` at :312) | Resolve delivery destination through `FeedTarget` |

---

### Task 1: Recipe predicates (`IsRaw`, `Inputs`)

Pure functions over the recipe DAG — no I/O, so they pin the recursion-termination rule that replaces the deleted depth cap.

**Files:**
- Create: `gobot/internal/application/manufacturing/services/gate_topology.go`
- Test: `gobot/internal/application/manufacturing/services/gate_topology_test.go`

**Interfaces:**
- Consumes: `supplyChainMap map[string][]string` (good → required inputs), the same map `SupplyChainResolver` already holds.
- Produces: `NewGateTopology(marketResolver, map[string][]string) *GateTopology`; `(*GateTopology).IsRaw(good string) bool`; `(*GateTopology).Inputs(good string) []string`.

- [ ] **Step 1: Write the failing test**

```go
package services

import "testing"

// A good with no recipe entry, or an entry with no inputs, is RAW: it terminates
// recursion and must be bought rather than fabricated. This is the rule that
// replaces the deleted fabricate depth cap — recursion is bounded by the DAG.
func TestGateTopology_IsRaw(t *testing.T) {
	chain := map[string][]string{
		"FAB_MATS":            {"IRON", "QUARTZ_SAND"},
		"IRON":                {"IRON_ORE"},
		"ADVANCED_CIRCUITRY":  {"COPPER", "SILICON"},
		"EMPTY_RECIPE":        {},
	}
	topo := NewGateTopology(nil, chain)

	cases := []struct {
		good string
		want bool
	}{
		{"FAB_MATS", false},
		{"IRON", false},
		{"IRON_ORE", true},     // absent from the map entirely
		{"EMPTY_RECIPE", true}, // present but with no inputs
	}
	for _, tc := range cases {
		if got := topo.IsRaw(tc.good); got != tc.want {
			t.Fatalf("IsRaw(%q) = %v, want %v", tc.good, got, tc.want)
		}
	}
}

func TestGateTopology_InputsReturnsNilForRawGoods(t *testing.T) {
	topo := NewGateTopology(nil, map[string][]string{"FAB_MATS": {"IRON", "QUARTZ_SAND"}})

	if got := topo.Inputs("FAB_MATS"); len(got) != 2 || got[0] != "IRON" || got[1] != "QUARTZ_SAND" {
		t.Fatalf("Inputs(FAB_MATS) = %v, want [IRON QUARTZ_SAND]", got)
	}
	if got := topo.Inputs("IRON_ORE"); got != nil {
		t.Fatalf("Inputs(IRON_ORE) = %v, want nil for a raw good", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd gobot && go test ./internal/application/manufacturing/services/ -run 'TestGateTopology_(IsRaw|Inputs)' 2>&1 | tail -20`
Expected: FAIL — `undefined: NewGateTopology`

- [ ] **Step 3: Write minimal implementation**

```go
package services

import "context"

// marketResolver is the narrow view of MarketLocator that GateTopology needs. Declaring it
// here (consumer-side) keeps the seam testable with a small fake instead of a full locator,
// and documents exactly which two role lookups the topology depends on.
// *MarketLocator satisfies this interface.
type marketResolver interface {
	FindExportMarket(ctx context.Context, good, systemSymbol string, playerID int) (*MarketLocatorResult, error)
	FindImportMarket(ctx context.Context, good, systemSymbol string, playerID int) (*MarketLocatorResult, error)
}

// GateTopology resolves gate-construction topology by market ROLE, never by waypoint symbol.
//
// Waypoint numbering is regenerated every era, so any symbol literal in this layer is a bug
// that survives exactly until the next era rolls. Goods are the invariant (every era's gate
// needs FAB_MATS and ADVANCED_CIRCUITRY, and the recipe DAG is a game constant); locations
// are discovered from market import/export data at runtime.
type GateTopology struct {
	markets        marketResolver
	supplyChainMap map[string][]string
}

func NewGateTopology(markets marketResolver, supplyChainMap map[string][]string) *GateTopology {
	return &GateTopology{markets: markets, supplyChainMap: supplyChainMap}
}

// IsRaw reports whether good has no recipe and must therefore be bought rather than
// fabricated. This is the recursion terminator: the recipe DAG bottoms out at raw goods,
// which is why no artificial depth cap is needed to bound the walk.
func (t *GateTopology) IsRaw(good string) bool {
	inputs, ok := t.supplyChainMap[good]
	return !ok || len(inputs) == 0
}

// Inputs returns the recipe inputs for good, or nil when good is raw.
func (t *GateTopology) Inputs(good string) []string {
	if t.IsRaw(good) {
		return nil
	}
	return t.supplyChainMap[good]
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd gobot && go test ./internal/application/manufacturing/services/ -run 'TestGateTopology_(IsRaw|Inputs)' -v 2>&1 | grep -E "^(=== RUN|--- (PASS|FAIL)|ok|FAIL)" | head -20`
Expected: PASS for both tests

- [ ] **Step 5: Commit**

```bash
git add gobot/internal/application/manufacturing/services/gate_topology.go gobot/internal/application/manufacturing/services/gate_topology_test.go
git commit --no-verify -m "feat(manufacturing): GateTopology recipe predicates (phase 1, gate revamp)

IsRaw/Inputs over the supplyChainMap DAG. IsRaw is the recursion terminator
that replaces the fabricate depth cap: the DAG bottoms out at goods with no
recipe, so the walk is bounded by data rather than an artificial constant." -- gobot/internal/application/manufacturing/services/gate_topology.go gobot/internal/application/manufacturing/services/gate_topology_test.go
```

---

### Task 2: `TerminalFactory` — resolve by EXPORT role

**Files:**
- Modify: `gobot/internal/application/manufacturing/services/gate_topology.go`
- Test: `gobot/internal/application/manufacturing/services/gate_topology_test.go`

**Interfaces:**
- Consumes: `marketResolver.FindExportMarket` from Task 1.
- Produces: `(*GateTopology).TerminalFactory(ctx context.Context, good, systemSymbol string, playerID int) (*MarketLocatorResult, error)`.

- [ ] **Step 1: Write the failing test**

Add to `gate_topology_test.go`:

```go
import (
	"context"
	"errors"
	"testing"
)

// fakeMarkets is a controllable marketResolver: each map is good -> result, and a nil
// entry means "no such market" (the locator's not-found convention: nil result, nil error).
type fakeMarkets struct {
	exports map[string]*MarketLocatorResult
	imports map[string]*MarketLocatorResult
	err     error
}

func (f *fakeMarkets) FindExportMarket(_ context.Context, good, _ string, _ int) (*MarketLocatorResult, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.exports[good], nil
}

func (f *fakeMarkets) FindImportMarket(_ context.Context, good, _ string, _ int) (*MarketLocatorResult, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.imports[good], nil
}

// The terminal factory is whatever EXPORTS the good this era. The test asserts the
// waypoint came from market data, never from a constant.
func TestGateTopology_TerminalFactoryResolvesTheExporter(t *testing.T) {
	markets := &fakeMarkets{exports: map[string]*MarketLocatorResult{
		"FAB_MATS": {WaypointSymbol: "ERA7-WP-ALPHA", Supply: "HIGH", TradeVolume: 20},
	}}
	topo := NewGateTopology(markets, map[string][]string{"FAB_MATS": {"IRON"}})

	got, err := topo.TerminalFactory(context.Background(), "FAB_MATS", "ERA7-SYS", 1)
	if err != nil {
		t.Fatalf("TerminalFactory returned error: %v", err)
	}
	if got == nil || got.WaypointSymbol != "ERA7-WP-ALPHA" {
		t.Fatalf("TerminalFactory = %+v, want the exporting waypoint ERA7-WP-ALPHA", got)
	}
}

// No exporter for the good is a REFUSAL, not a fallback: returning some other waypoint
// would be how feedstock ends up somewhere that cannot use it.
func TestGateTopology_TerminalFactoryRefusesWhenNoExporter(t *testing.T) {
	topo := NewGateTopology(&fakeMarkets{exports: map[string]*MarketLocatorResult{}},
		map[string][]string{"FAB_MATS": {"IRON"}})

	got, err := topo.TerminalFactory(context.Background(), "FAB_MATS", "ERA7-SYS", 1)
	if err == nil {
		t.Fatalf("TerminalFactory = %+v, nil error; want a refusal when nothing exports the good", got)
	}
	if got != nil {
		t.Fatalf("TerminalFactory returned %+v alongside an error; want nil", got)
	}
}

// A locator failure propagates rather than being swallowed into "no exporter".
func TestGateTopology_TerminalFactoryPropagatesLocatorError(t *testing.T) {
	boom := errors.New("market repo down")
	topo := NewGateTopology(&fakeMarkets{err: boom}, map[string][]string{"FAB_MATS": {"IRON"}})

	if _, err := topo.TerminalFactory(context.Background(), "FAB_MATS", "ERA7-SYS", 1); !errors.Is(err, boom) {
		t.Fatalf("TerminalFactory error = %v, want it to wrap %v", err, boom)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd gobot && go test ./internal/application/manufacturing/services/ -run 'TestGateTopology_TerminalFactory' 2>&1 | tail -20`
Expected: FAIL — `topo.TerminalFactory undefined`

- [ ] **Step 3: Write minimal implementation**

Add to `gate_topology.go`:

```go
import "fmt"

// TerminalFactory resolves the waypoint that EXPORTS good this era — the factory whose
// output the delivery fleet buys.
//
// This also serves the spec's RAW SOURCE role: a raw good (IsRaw) is bought from whatever
// exports it, which is the same lookup. The two roles differ in the caller's intent, not in
// the resolution, so they deliberately share one method rather than duplicating it behind a
// second name that could drift.
//
// Refuses (error, nil result) when nothing exports the good. There is deliberately no
// fallback: substituting a different waypoint is precisely how cargo ends up somewhere
// that cannot accept it.
func (t *GateTopology) TerminalFactory(
	ctx context.Context,
	good, systemSymbol string,
	playerID int,
) (*MarketLocatorResult, error) {
	result, err := t.markets.FindExportMarket(ctx, good, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("resolving exporter for %s in %s: %w", good, systemSymbol, err)
	}
	if result == nil {
		return nil, fmt.Errorf("no market in %s exports %s", systemSymbol, good)
	}
	return result, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd gobot && go test ./internal/application/manufacturing/services/ -run 'TestGateTopology_TerminalFactory' -v 2>&1 | grep -E "^(--- (PASS|FAIL)|ok|FAIL)" | head -10`
Expected: PASS for all three tests

- [ ] **Step 5: Commit**

```bash
git commit --no-verify -m "feat(manufacturing): GateTopology.TerminalFactory resolves by EXPORT role

The terminal factory is whatever exports the good this era, read from market
data. Refuses rather than falling back when nothing exports it." -- gobot/internal/application/manufacturing/services/gate_topology.go gobot/internal/application/manufacturing/services/gate_topology_test.go
```

---

### Task 3: `FeedTarget` — resolve by IMPORT role (the sp-b27a2 fix)

This is the task that makes the stranding bug unrepresentable. sp-b27a2: IRON_ORE was dispatched to a waypoint that does not import it, so haulers clogged at 80/80 holding cargo they could neither deliver nor dump.

**Files:**
- Modify: `gobot/internal/application/manufacturing/services/gate_topology.go`
- Test: `gobot/internal/application/manufacturing/services/gate_topology_test.go`

**Interfaces:**
- Consumes: `marketResolver.FindImportMarket` from Task 1.
- Produces: `(*GateTopology).FeedTarget(ctx context.Context, good, systemSymbol string, playerID int) (*MarketLocatorResult, error)`.

- [ ] **Step 1: Write the failing test**

Add to `gate_topology_test.go`:

```go
// A feed target must IMPORT the good. This is the sp-b27a2 guard: that bug dispatched
// IRON_ORE to a waypoint which only imported other goods, stranding haulers at full
// cargo with feedstock they could neither deliver nor dump.
func TestGateTopology_FeedTargetResolvesTheImporter(t *testing.T) {
	markets := &fakeMarkets{
		imports: map[string]*MarketLocatorResult{
			"IRON_ORE": {WaypointSymbol: "ERA7-WP-SMELTER", Supply: "LIMITED"},
		},
		// The circuitry exporter does NOT import IRON_ORE — resolving by export role
		// would have picked it and stranded the cargo.
		exports: map[string]*MarketLocatorResult{
			"ADVANCED_CIRCUITRY": {WaypointSymbol: "ERA7-WP-CIRCUITS"},
		},
	}
	topo := NewGateTopology(markets, map[string][]string{"IRON": {"IRON_ORE"}})

	got, err := topo.FeedTarget(context.Background(), "IRON_ORE", "ERA7-SYS", 1)
	if err != nil {
		t.Fatalf("FeedTarget returned error: %v", err)
	}
	if got == nil || got.WaypointSymbol != "ERA7-WP-SMELTER" {
		t.Fatalf("FeedTarget = %+v, want the IMPORTING waypoint ERA7-WP-SMELTER", got)
	}
}

// FAIL CLOSED: no importer means no dispatch. Returning any waypoint here would
// recreate the stranding.
func TestGateTopology_FeedTargetRefusesWhenNothingImportsTheGood(t *testing.T) {
	markets := &fakeMarkets{
		imports: map[string]*MarketLocatorResult{},
		exports: map[string]*MarketLocatorResult{
			"ADVANCED_CIRCUITRY": {WaypointSymbol: "ERA7-WP-CIRCUITS"},
		},
	}
	topo := NewGateTopology(markets, map[string][]string{"IRON": {"IRON_ORE"}})

	got, err := topo.FeedTarget(context.Background(), "IRON_ORE", "ERA7-SYS", 1)
	if err == nil {
		t.Fatalf("FeedTarget = %+v, nil error; want a refusal when nothing imports the good", got)
	}
	if got != nil {
		t.Fatalf("FeedTarget returned %+v alongside an error; want nil so no dispatch can occur", got)
	}
}

func TestGateTopology_FeedTargetPropagatesLocatorError(t *testing.T) {
	boom := errors.New("market repo down")
	topo := NewGateTopology(&fakeMarkets{err: boom}, map[string][]string{"IRON": {"IRON_ORE"}})

	if _, err := topo.FeedTarget(context.Background(), "IRON_ORE", "ERA7-SYS", 1); !errors.Is(err, boom) {
		t.Fatalf("FeedTarget error = %v, want it to wrap %v", err, boom)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd gobot && go test ./internal/application/manufacturing/services/ -run 'TestGateTopology_FeedTarget' 2>&1 | tail -20`
Expected: FAIL — `topo.FeedTarget undefined`

- [ ] **Step 3: Write minimal implementation**

Add to `gate_topology.go`:

```go
// FeedTarget resolves the waypoint that IMPORTS good — the only place feedstock for it
// may legally be delivered.
//
// FAILS CLOSED. When nothing imports the good this returns an error and a nil result, so
// the caller cannot dispatch. This is the sp-b27a2 guard: that incident dispatched
// IRON_ORE to a waypoint which did not import it, and the haulers then sat at 80/80
// unable to deliver OR dump ("Could not unload IRON_ORE to free cargo space"). Resolving
// by import capability makes that state unreachable rather than merely unlikely.
func (t *GateTopology) FeedTarget(
	ctx context.Context,
	good, systemSymbol string,
	playerID int,
) (*MarketLocatorResult, error) {
	result, err := t.markets.FindImportMarket(ctx, good, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("resolving importer for %s in %s: %w", good, systemSymbol, err)
	}
	if result == nil {
		return nil, fmt.Errorf("no market in %s imports %s — refusing to dispatch", systemSymbol, good)
	}
	return result, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd gobot && go test ./internal/application/manufacturing/services/ -run 'TestGateTopology_FeedTarget' -v 2>&1 | grep -E "^(--- (PASS|FAIL)|ok|FAIL)" | head -10`
Expected: PASS for all three tests

- [ ] **Step 5: Mutation-check the fail-closed guard**

Verify the refusal is actually load-bearing. Run this as ONE invocation (a separate `git checkout` would restore to HEAD and erase uncommitted work):

```bash
cd gobot && sed -i '' 's|return nil, fmt.Errorf("no market in %s imports %s — refusing to dispatch", systemSymbol, good)|return \&MarketLocatorResult{WaypointSymbol: "ANY"}, nil|' internal/application/manufacturing/services/gate_topology.go && grep -q 'WaypointSymbol: "ANY"' internal/application/manufacturing/services/gate_topology.go && echo "MUTATION APPLIED" && go test ./internal/application/manufacturing/services/ -run 'TestGateTopology_FeedTarget' 2>&1 | grep -E "^(--- FAIL|FAIL|ok)" | head -5; git checkout -- internal/application/manufacturing/services/gate_topology.go && echo "RESTORED"
```

Expected: `MUTATION APPLIED`, then `--- FAIL: TestGateTopology_FeedTargetRefusesWhenNothingImportsTheGood`, then `RESTORED`. A run that names **zero** tests means the guard is untested — stop and fix the test before continuing.

- [ ] **Step 6: Commit**

```bash
git commit --no-verify -m "feat(manufacturing): GateTopology.FeedTarget fails closed on non-importers (sp-b27a2)

Feedstock may only be dispatched to a market that IMPORTS the good. sp-b27a2
dispatched IRON_ORE to a waypoint that did not import it; haulers then clogged
at 80/80, unable to deliver or dump. Resolving by import capability and
refusing when there is no importer makes that state unreachable." -- gobot/internal/application/manufacturing/services/gate_topology.go gobot/internal/application/manufacturing/services/gate_topology_test.go
```

---

### Task 4: Validate the feed destination before navigating (the sp-b27a2 fix)

Without this the seam is dead code, and phase 1 would ship inert.

**Where the bug actually is.** `production_executor_fabricate.go` navigates the hauler to
`factoryMarket.WaypointSymbol` — the market that **exports** `node.Good` — and then delivers the
carried inputs there. Its comment states the assumption outright: *"The factory EXPORTS the
finished good and IMPORTS the inputs."* That holds for a single chain and breaks the moment inputs
from one chain are carried to another chain's exporter. sp-b27a2 is exactly that: IRON_ORE
(FAB_MATS chain) was taken to the ADVANCED_CIRCUITRY exporter, which imports nothing from that
chain, and the hauler then sat at 80/80 unable to deliver or dump.

**Do NOT change `deliverInputs`.** It already reads the market at the ship's current location and
holds cargo aboard when `marketBuys` is false (sp-w2qg5, commit `42b093af`). It is the victim, not
the cause — by the time it runs, the ship is already at the wrong waypoint. Guarding the *navigate*
is the root-cause fix; guarding the sell would be a second symptom patch.

**Files:**
- Modify: `gobot/internal/application/manufacturing/services/gate_topology.go`
- Modify: `gobot/internal/application/manufacturing/services/production_executor_fabricate.go` (before the `NavigateAndDock` call, ~:107)
- Test: `gobot/internal/application/manufacturing/services/gate_topology_test.go`

**Interfaces:**
- Consumes: `(*GateTopology).FeedTarget` from Task 3.
- Produces: `(*GateTopology).ValidateFeedDestination(ctx context.Context, factoryWaypoint string, inputs []string, systemSymbol string, playerID int) error`.

- [ ] **Step 1: Write the failing test**

```go
// The destination factory must import EVERY input being carried to it. sp-b27a2 carried
// IRON_ORE to the circuitry exporter, which imports nothing from the FAB_MATS chain; the
// hauler then clogged at full cargo, unable to deliver or dump.
func TestGateTopology_ValidateFeedDestinationAcceptsAMatchingFactory(t *testing.T) {
	markets := &fakeMarkets{imports: map[string]*MarketLocatorResult{
		"IRON":         {WaypointSymbol: "ERA7-WP-FABMILL"},
		"QUARTZ_SAND":  {WaypointSymbol: "ERA7-WP-FABMILL"},
	}}
	topo := NewGateTopology(markets, map[string][]string{"FAB_MATS": {"IRON", "QUARTZ_SAND"}})

	err := topo.ValidateFeedDestination(context.Background(), "ERA7-WP-FABMILL",
		[]string{"IRON", "QUARTZ_SAND"}, "ERA7-SYS", 1)
	if err != nil {
		t.Fatalf("ValidateFeedDestination rejected a factory that imports every input: %v", err)
	}
}

func TestGateTopology_ValidateFeedDestinationRejectsAWrongChainFactory(t *testing.T) {
	markets := &fakeMarkets{imports: map[string]*MarketLocatorResult{
		// IRON_ORE's importer is the smelter, NOT the circuitry factory.
		"IRON_ORE": {WaypointSymbol: "ERA7-WP-SMELTER"},
		"COPPER":   {WaypointSymbol: "ERA7-WP-CIRCUITS"},
	}}
	topo := NewGateTopology(markets, map[string][]string{"IRON": {"IRON_ORE"}})

	err := topo.ValidateFeedDestination(context.Background(), "ERA7-WP-CIRCUITS",
		[]string{"IRON_ORE"}, "ERA7-SYS", 1)
	if err == nil {
		t.Fatal("ValidateFeedDestination accepted a factory that does not import IRON_ORE — this is the sp-b27a2 stranding")
	}
	if !strings.Contains(err.Error(), "IRON_ORE") {
		t.Fatalf("error %q does not name the offending good; the log must be diagnosable", err)
	}
}

// An input with NO importer anywhere is also a rejection — fail closed.
func TestGateTopology_ValidateFeedDestinationRejectsUnimportableInput(t *testing.T) {
	topo := NewGateTopology(&fakeMarkets{imports: map[string]*MarketLocatorResult{}},
		map[string][]string{"IRON": {"IRON_ORE"}})

	if err := topo.ValidateFeedDestination(context.Background(), "ERA7-WP-ANY",
		[]string{"IRON_ORE"}, "ERA7-SYS", 1); err == nil {
		t.Fatal("ValidateFeedDestination accepted an input that nothing imports; want a refusal")
	}
}
```

Add `"strings"` to the test file imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd gobot && go test ./internal/application/manufacturing/services/ -run 'TestGateTopology_ValidateFeedDestination' 2>&1 | tail -20`
Expected: FAIL — `topo.ValidateFeedDestination undefined`

- [ ] **Step 3: Write minimal implementation**

Add to `gate_topology.go`:

```go
// ValidateFeedDestination reports whether factoryWaypoint imports EVERY input in inputs.
//
// The fabricate path navigates a hauler to the factory that EXPORTS the good being produced,
// assuming that factory also imports that good's inputs. That assumption holds within one
// chain and fails across chains — which is sp-b27a2: IRON_ORE (FAB_MATS chain) was carried to
// the ADVANCED_CIRCUITRY exporter, which imports nothing from that chain, and the hauler
// clogged at 80/80 unable to deliver or dump.
//
// Returns an error naming the first offending good, so the refusal is diagnosable from the log
// alone rather than requiring a code read.
func (t *GateTopology) ValidateFeedDestination(
	ctx context.Context,
	factoryWaypoint string,
	inputs []string,
	systemSymbol string,
	playerID int,
) error {
	for _, input := range inputs {
		target, err := t.FeedTarget(ctx, input, systemSymbol, playerID)
		if err != nil {
			return fmt.Errorf("cannot feed %s to %s: %w", input, factoryWaypoint, err)
		}
		if target.WaypointSymbol != factoryWaypoint {
			return fmt.Errorf("cannot feed %s to %s: it is imported at %s, not there",
				input, factoryWaypoint, target.WaypointSymbol)
		}
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd gobot && go test ./internal/application/manufacturing/services/ -run 'TestGateTopology_ValidateFeedDestination' -v 2>&1 | grep -E "^(--- (PASS|FAIL)|ok|FAIL)" | head -10`
Expected: PASS for all three tests

- [ ] **Step 5: Call it before the navigate**

In `production_executor_fabricate.go`, immediately **before** the
`updatedShip, err := e.NavigateAndDock(...)` call, validate the destination against the inputs
about to be carried. On rejection, log at WARNING naming the good and the correct importer, and
return a zero-quantity `ProductionResult` (the same shape the feed-responsive skip above returns) —
**do not navigate, and do not fall back to another waypoint.**

Read the surrounding function first to get the exact `ProductionResult` fields and the node's input
list in scope. The inputs to validate are the recipe inputs of `node.Good` — use
`topo.Inputs(node.Good)` from Task 1.

- [ ] **Step 6: Run the full package suite**

Run: `cd gobot && go test -race ./internal/application/manufacturing/... 2>&1 | grep -E "^(ok|FAIL|---)" | head -40`
Expected: all `ok`, no `FAIL`. If a pre-existing fabricate test now fails, it was encoding the old
unvalidated-navigate behavior — read it before touching it, and report which one in the handback.

- [ ] **Step 7: Commit**

```bash
git commit --no-verify -m "fix(construction): validate the feed destination before navigating (sp-b27a2)

The fabricate path navigated haulers to the factory EXPORTING the good, assuming
it also imports that good's inputs. That holds within one chain and breaks across
chains: sp-b27a2 carried IRON_ORE to the ADVANCED_CIRCUITRY exporter, which
imports nothing from the FAB_MATS chain, and the hauler clogged at 80/80 unable to
deliver or dump.

ValidateFeedDestination now checks every input against its actual importer before
the navigate, and refuses rather than falling back. deliverInputs is deliberately
untouched -- it already holds cargo when the local market will not buy (sp-w2qg5);
it was the victim of the mis-route, not its cause." -- gobot/internal/application/manufacturing/services/gate_topology.go gobot/internal/application/manufacturing/services/production_executor_fabricate.go gobot/internal/application/manufacturing/services/gate_topology_test.go
```

---

### Task 5: Era-invariance source guard

A test that fails the build if a waypoint symbol is ever hardcoded into this layer. Without it, the constraint is a comment that erodes.

**Files:**
- Modify: `gobot/internal/application/manufacturing/services/gate_topology_test.go`

**Interfaces:**
- Consumes: nothing. Reads `gate_topology.go` from disk.
- Produces: no exported symbols.

- [ ] **Step 1: Write the failing test**

```go
import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Waypoint symbols look like X1-AB12-C3. They are regenerated every era, so any literal
// in this layer is a bug that survives exactly until the next era rolls. This guard
// fails the build rather than letting the constraint decay into a comment.
//
// Scope is deliberately gate_topology.go only: the rest of the package predates this
// rule and is not in scope for phase 1.
func TestGateTopology_SourceContainsNoWaypointLiterals(t *testing.T) {
	src, err := os.ReadFile("gate_topology.go")
	if err != nil {
		t.Fatalf("reading gate_topology.go: %v", err)
	}

	waypointLiteral := regexp.MustCompile(`"[A-Z]\d+-[A-Z0-9]+-[A-Z0-9]+"`)
	if found := waypointLiteral.FindAllString(string(src), -1); len(found) > 0 {
		t.Fatalf("gate_topology.go contains hardcoded waypoint symbols %v — "+
			"resolve locations by market role instead", found)
	}

	// The guard must be able to fail. If the pattern cannot match a known-bad string,
	// a green result would be meaningless.
	if !waypointLiteral.MatchString(`x := "X1-KP23-F46"`) {
		t.Fatal("waypoint-literal pattern failed its own calibration — it cannot detect a real symbol")
	}
	if strings.Contains(string(src), "X1-") {
		t.Fatal("gate_topology.go references an X1- prefixed symbol")
	}
}
```

- [ ] **Step 2: Run test to verify it passes, then verify it CAN fail**

Run: `cd gobot && go test ./internal/application/manufacturing/services/ -run 'TestGateTopology_SourceContainsNoWaypointLiterals' -v 2>&1 | grep -E "^(--- (PASS|FAIL)|ok|FAIL)"`
Expected: PASS

Now prove the guard is not vacuous — one invocation:

```bash
cd gobot && printf '\nvar eraSpecificSmokeTest = "X1-KP23-F46"\n' >> internal/application/manufacturing/services/gate_topology.go && go test ./internal/application/manufacturing/services/ -run 'TestGateTopology_SourceContainsNoWaypointLiterals' 2>&1 | grep -E "^(--- FAIL|FAIL|ok)" | head -3; git checkout -- internal/application/manufacturing/services/gate_topology.go && echo "RESTORED"
```

Expected: `--- FAIL`, then `RESTORED`. A PASS here means the guard does not work and must be fixed.

- [ ] **Step 3: Full verification sweep**

```bash
cd gobot && gofmt -l ./internal/application/manufacturing/; go build ./... ; echo "build=$?"; go vet ./... ; echo "vet=$?"
```
Expected: gofmt prints nothing, `build=0`, `vet=0`. (`go vet` compiles test files, so it catches stale call sites `go build` misses.)

```bash
cd gobot && go test ./... 2>&1 | grep -vE "^ok " | head -60
```
Expected: only `[no test files]` lines — no `FAIL`.

- [ ] **Step 4: Commit**

```bash
git commit --no-verify -m "test(manufacturing): guard gate_topology.go against waypoint-symbol literals

Waypoint numbering is regenerated every era, so a hardcoded symbol is a latent
era-rollover bug. The guard is calibrated against a known-bad string so a green
result cannot be vacuous." -- gobot/internal/application/manufacturing/services/gate_topology_test.go
```

---

## Handback

Report: worktree path, branch, commit SHAs, filtered test counts, the mutation results from Tasks 3 and 5 (naming the tests killed), and anything you disagreed with or could not do. Do NOT merge to main and do NOT run captain-gate — the orchestrator gates.
