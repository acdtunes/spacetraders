package parkedsensing

import (
	"context"
	"errors"
	"testing"

	expansionCmd "github.com/andrescamacho/spacetraders-go/internal/application/expansion/commands"
)

// offgate_test.go pins the warp-expansion slice.
//
// THE STATE IT EXISTS FOR, measured live: our 56-system ledger has 50 outbound gate edges to 21
// systems we do not hold, and ALL 50 are under construction — every construction site sitting in a
// system on the far side, unreachable to supply. Zero traversable. Gate expansion is correct and
// finished; the box has hard walls.
//
// EVERY FIXTURE HERE MUST HAVE NO GATE ROUTE, or the slice never fires and the test proves nothing.
// That is the exact failure mode that nearly cost five lanes today, so the harness below builds the
// sealed shape by default and the one test that needs a gate route opens it explicitly.

// --- fakes -------------------------------------------------------------------

type fakeOffGateSelector struct {
	target expansionCmd.OffGateTarget
	found  bool
	err    error
	calls  int
	params expansionCmd.OffGateSelectionParams
}

func (f *fakeOffGateSelector) SelectTarget(_ context.Context, _ int, p expansionCmd.OffGateSelectionParams) (expansionCmd.OffGateTarget, bool, error) {
	f.calls++
	f.params = p
	if f.err != nil {
		// Adversarial: a usable target alongside the error, so a driver that leaks the error
		// dispatches a warp and the test catches it rather than merely misreporting.
		return expansionCmd.OffGateTarget{SystemSymbol: "X1-GHOST"}, true, f.err
	}
	return f.target, f.found, nil
}

type fakeDemandSink struct {
	emitted []expansionCmd.OffGateDemandSignal
}

func (f *fakeDemandSink) EmitOffGateDemand(_ int, s expansionCmd.OffGateDemandSignal) {
	f.emitted = append(f.emitted, s)
}

func (f *fakeDemandSink) last() (expansionCmd.OffGateDemandSignal, bool) {
	if len(f.emitted) == 0 {
		return expansionCmd.OffGateDemandSignal{}, false
	}
	return f.emitted[len(f.emitted)-1], true
}

type fakeExplorerFinder struct {
	symbol string
	found  bool
	err    error
}

func (f *fakeExplorerFinder) IdleExplorer(_ context.Context, _ int) (string, bool, error) {
	if f.err != nil {
		return "EXPLORER-GHOST", true, f.err // adversarial: a hull alongside the error
	}
	return f.symbol, f.found, nil
}

type warpCall struct {
	ship   string
	target string
}

type fakeWarpDispatcher struct {
	calls []warpCall
	err   error
}

func (f *fakeWarpDispatcher) DispatchExplorer(_ context.Context, _ int, ship string, t expansionCmd.OffGateTarget) error {
	f.calls = append(f.calls, warpCall{ship, t.SystemSymbol})
	return f.err
}

// --- harness -----------------------------------------------------------------

type offGateHarness struct {
	*expandHarness
	selector  *fakeOffGateSelector
	sink      *fakeDemandSink
	explorers *fakeExplorerFinder
	warp      *fakeWarpDispatcher
}

// sealedPocket is the live shape: a system we hold and staff, and a target with charting work that
// NO gate edge reaches. The gate adjacency is deliberately EMPTY — that is what "every way out is
// under construction" looks like to the reach search, which only ever sees traversable edges.
func sealedPocket() *offGateHarness {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-HOME", Verdict: VerdictInScope},
		{System: "X1-SEALED", Verdict: VerdictPending, UnchartedCount: 5},
	}
	h.gates.adjacency = map[string][]string{"X1-HOME": {}, "X1-SEALED": {}}
	h.ledger.slots = []QueuedSlot{staffedYardRow("X1-HOME", "X1-HOME-YARD")}

	og := &offGateHarness{
		expandHarness: h,
		selector: &fakeOffGateSelector{
			found: true,
			target: expansionCmd.OffGateTarget{
				SystemSymbol: "X1-FARAWAY", FromSystem: "X1-HOME", WarpFuelCost: 240, Value: 2,
			},
		},
		sink:      &fakeDemandSink{},
		explorers: &fakeExplorerFinder{},
		warp:      &fakeWarpDispatcher{},
	}
	return og
}

func (o *offGateHarness) run(t *testing.T) (ExpandReport, error) {
	t.Helper()
	for i := range o.ledger.systems {
		o.ledger.systems[i].CatalogKnown = !o.unswept[o.ledger.systems[i].System]
	}
	p := o.ports()
	p.OffGate = OffGatePorts{
		Select: o.selector, Demand: o.sink, Explorer: o.explorers, Warp: o.warp,
	}
	return AdvanceExpansion(context.Background(), p, 1, ExpandKnobs{
		Enabled: true, MinBudgetRate: 0.05, Whitelist: o.whitelist,
	}, 1.0)
}

// --- the test that matters ---------------------------------------------------

// A sealed frontier, a known gate-unreachable system, and an idle explorer: the explorer is WARPED
// to that system, and the tick returns without waiting on it.
func TestAdvanceExpansion_SealedFrontierWarpsTheIdleExplorerToTheOffGateTarget(t *testing.T) {
	h := sealedPocket()
	h.explorers.symbol, h.explorers.found = "EXPLORER-1", true

	rep, err := h.run(t)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !rep.OffGateDemanded {
		t.Fatalf("no off-gate demand raised — X1-SEALED needs charting and no gate reaches it")
	}
	if len(h.warp.calls) != 1 {
		t.Fatalf("warp dispatched %d times, want exactly 1: %v", len(h.warp.calls), h.warp.calls)
	}
	got := h.warp.calls[0]
	if got.ship != "EXPLORER-1" || got.target != "X1-FARAWAY" {
		t.Fatalf("warped %s to %s, want EXPLORER-1 to X1-FARAWAY", got.ship, got.target)
	}
	if rep.OffGateWarped != 1 || rep.OffGateTarget != "X1-FARAWAY" {
		t.Fatalf("report = warped %d target %q, want 1 / X1-FARAWAY", rep.OffGateWarped, rep.OffGateTarget)
	}
	// The demand still reaches the buy side even when we already own the hull: the autosizer's
	// hard cap of 1 is what stops a second purchase, not our silence.
	signal, emitted := h.sink.last()
	if !emitted || !signal.Demanded || signal.ExplorerCount != 1 || !signal.HasTarget {
		t.Fatalf("bridge signal = %+v, want demanded/1/has-target", signal)
	}
}

// GATE ALWAYS WINS. One reachable gate target suppresses off-gate demand entirely — warp is the
// expensive fallback, never the default.
//
// The fixture is adverse in the way that matters: the off-gate selector would happily return a
// target (it is the same fake that fires in every other test here) and an idle explorer IS owned, so
// the only thing standing between this tick and a 769k-hull warp is the gate-reachability test.
func TestAdvanceExpansion_AReachableGateTargetSuppressesOffGateDemand(t *testing.T) {
	h := sealedPocket()
	h.explorers.symbol, h.explorers.found = "EXPLORER-1", true
	// Open one gate: X1-HOME now borders the target that needs charting.
	h.gates.adjacency["X1-HOME"] = []string{"X1-SEALED"}

	rep, err := h.run(t)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rep.OffGateDemanded {
		t.Fatalf("off-gate demand raised while X1-SEALED is one gate hop from X1-HOME — a probe that "+
			"walks a gate for free must always beat a 769k warp; report %+v", rep)
	}
	if len(h.warp.calls) != 0 {
		t.Fatalf("warped %v with a gate route available", h.warp.calls)
	}
	if h.selector.calls != 0 {
		t.Fatalf("off-gate selector consulted %d times with a gate route available — the gate test "+
			"must short-circuit before any off-gate work", h.selector.calls)
	}
	// The bridge is still written, with NO demand — see the next test for why that matters.
	signal, emitted := h.sink.last()
	if !emitted || signal.Demanded {
		t.Fatalf("bridge signal = %+v (emitted=%v), want an explicit no-demand write", signal, emitted)
	}
}

// THE ANTI-DORMANCY PROPERTY. The bridge is written on EVERY tick, including ticks that raise no
// demand — because it reads UNREADABLE until its first write for a player and the autosizer fails
// CLOSED on unreadable.
//
// A driver that wrote only when demand fired would leave the buy side permanently blind on any fleet
// that has not yet exhausted its gates, which is exactly the retirement this slice exists to undo: a
// complete, tested machine that never receives a single call.
func TestAdvanceExpansion_TheDemandBridgeIsWrittenEvenWhenNothingIsDemanded(t *testing.T) {
	h := sealedPocket()
	// No charting work at all — the quietest possible tick.
	h.ledger.systems = []ExpandSystem{{System: "X1-HOME", Verdict: VerdictInScope, CatalogKnown: true}}

	if _, err := h.run(t); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(h.sink.emitted) == 0 {
		t.Fatalf("nothing written to the demand bridge — it reads unreadable until first written, so " +
			"the autosizer's explorer pass would fail closed forever and this slice would be dormant")
	}
	if signal, _ := h.sink.last(); signal.Demanded {
		t.Fatalf("demand raised on a tick with no charting work at all: %+v", signal)
	}
}

// NOTHING IS WARPED BEFORE AN EXPLORER IS OWNED — the ordinary state until the autosizer buys one.
// The demand is still raised, because that signal is precisely what asks for the purchase.
func TestAdvanceExpansion_NoExplorerOwnedRaisesDemandAndWarpsNothing(t *testing.T) {
	h := sealedPocket()
	h.explorers.found = false // the autosizer has not bought one

	rep, err := h.run(t)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !rep.OffGateDemanded {
		t.Fatalf("no demand raised — the signal IS the purchase request; without it the autosizer never buys")
	}
	if len(h.warp.calls) != 0 {
		t.Fatalf("warped %v with no explorer owned", h.warp.calls)
	}
	if rep.OffGateWarped != 0 {
		t.Fatalf("OffGateWarped = %d, want 0", rep.OffGateWarped)
	}
}

// NO REACHABLE OFF-GATE TARGET: demand stands (the frontier really is exhausted) but nothing is
// warped — a hull must never be launched at a destination the selector could not name.
func TestAdvanceExpansion_DemandWithoutATargetWarpsNothing(t *testing.T) {
	h := sealedPocket()
	h.explorers.symbol, h.explorers.found = "EXPLORER-1", true
	h.selector.found = false // every off-gate candidate out of warp range

	rep, err := h.run(t)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !rep.OffGateDemanded {
		t.Fatalf("demand must stand — the gate frontier is exhausted whether or not a target is in range")
	}
	if len(h.warp.calls) != 0 {
		t.Fatalf("warped %v with no target selected", h.warp.calls)
	}
}

// A FAILING SELECTOR NEVER LAUNCHES A HULL. The fake returns a usable-looking target alongside its
// error, so a driver that leaks the error warps to X1-GHOST and this fails.
func TestAdvanceExpansion_AFailingSelectorRaisesDemandButNeverWarps(t *testing.T) {
	h := sealedPocket()
	h.explorers.symbol, h.explorers.found = "EXPLORER-1", true
	h.selector.err = errors.New("universe roster unreadable")

	rep, err := h.run(t)
	if err != nil {
		t.Fatalf("a failed selection must not fail the tick: %v", err)
	}
	if len(h.warp.calls) != 0 {
		t.Fatalf("warped %v off a failed selection", h.warp.calls)
	}
	if !rep.OffGateDemanded {
		t.Fatalf("demand must still stand when only the target lookup failed")
	}
}

// AN UNREADABLE EXPLORER FLEET NEVER WARPS. Same adversarial shape: a hull is returned alongside the
// error, so a leaked error hands EXPLORER-GHOST to the warp executor.
func TestAdvanceExpansion_AnUnreadableExplorerFleetWarpsNothing(t *testing.T) {
	h := sealedPocket()
	h.explorers.err = errors.New("ships table unhappy")

	if _, err := h.run(t); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(h.warp.calls) != 0 {
		t.Fatalf("warped %v off an unreadable fleet read", h.warp.calls)
	}
}

// A REFUSED WARP IS NON-FATAL AND LEAVES NOTHING STUCK. The adapter fails closed before moving
// anything, so the hull is where it was and the next tick re-derives and retries for free.
func TestAdvanceExpansion_ARefusedWarpDoesNotFailTheTick(t *testing.T) {
	h := sealedPocket()
	h.explorers.symbol, h.explorers.found = "EXPLORER-1", true
	h.warp.err = errors.New("no arrival waypoint known — fail closed")

	rep, err := h.run(t)
	if err != nil {
		t.Fatalf("a refused warp must not fail the tick: %v", err)
	}
	if rep.OffGateWarped != 0 {
		t.Fatalf("OffGateWarped = %d after a refused warp, want 0", rep.OffGateWarped)
	}
}

// The selector is handed the explorer's real fuel bound. 800 is the SHIP_EXPLORER's fuel capacity
// read live from the shipyard API, so a target admitted here is one a full tank can reach; a bound
// pulled from nowhere would offer targets the warp executor then refuses as would-strand.
func TestAdvanceExpansion_OffGateSelectionIsBoundedByTheExplorersFuelCapacity(t *testing.T) {
	h := sealedPocket()

	if _, err := h.run(t); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.selector.calls != 1 {
		t.Fatalf("selector called %d times, want 1", h.selector.calls)
	}
	if h.selector.params.WarpRangeFuel != 800 {
		t.Fatalf("WarpRangeFuel = %d, want 800 (SHIP_EXPLORER fuelCapacity)", h.selector.params.WarpRangeFuel)
	}
	if h.selector.params.ValueWeight <= 0 || h.selector.params.FuelWeight <= 0 {
		t.Fatalf("ranking weights = value %d fuel %d, want both positive so value and distance both count",
			h.selector.params.ValueWeight, h.selector.params.FuelWeight)
	}
}

// An UNWIRED slice does nothing and breaks nothing — every gate pass still runs. This is what lets
// the existing expansion tests construct ExpandPorts without the off-gate collaborators.
func TestAdvanceExpansion_AnUnwiredOffGateSliceIsInert(t *testing.T) {
	h := sealedPocket()
	h.explorers.symbol, h.explorers.found = "EXPLORER-1", true

	for i := range h.ledger.systems {
		h.ledger.systems[i].CatalogKnown = true
	}
	p := h.ports() // OffGate left zero-valued
	rep, err := AdvanceExpansion(context.Background(), p, 1, ExpandKnobs{
		Enabled: true, MinBudgetRate: 0.05, Whitelist: h.whitelist,
	}, 1.0)
	if err != nil {
		t.Fatalf("an unwired off-gate slice must not fail the tick: %v", err)
	}
	if rep.OffGateDemanded || rep.OffGateWarped != 0 {
		t.Fatalf("unwired slice acted: %+v", rep)
	}
	if len(h.warp.calls) != 0 || len(h.sink.emitted) != 0 {
		t.Fatalf("unwired slice reached its collaborators")
	}
}

// selectiveFailGates answers normally except for the systems named in failOn.
//
// The shared fakeGates fails EVERY call, which makes readNeighbours abort the tick before the reach
// search ever runs — so it cannot reach the failure this test is about. The reach search only goes
// to the store for a system the tick's neighbour map does NOT cover (an intermediate discovered
// mid-search), and that is the read this fake breaks.
type selectiveFailGates struct {
	adjacency map[string][]string
	failOn    map[string]bool
	calls     int
}

func (f *selectiveFailGates) Neighbours(_ context.Context, system string) ([]string, error) {
	f.calls++
	if f.failOn[system] {
		// Adversarial: a populated list alongside the error, so a caller that swallows it walks on
		// into a reach answer it did not actually read.
		return []string{"X1-GHOST"}, errors.New("gate store unhappy for " + system)
	}
	return f.adjacency[system], nil
}

// Mapped reports every system as already mapped — neutral for the gate-priority tier.
func (f *selectiveFailGates) Mapped(_ context.Context, _ string) (bool, error) { return true, nil }

// AN UNREADABLE GATE GRAPH NEVER RAISES EXPLORER DEMAND. A database hiccup must not present as an
// exhausted frontier — that misreading is the expensive one: on a fleet with the treasury to afford
// it, it buys a 769k hull to escape a pocket it was never actually sealed in.
//
// WHAT THIS ACTUALLY PINS, stated precisely because the two are easy to confuse. The guarantee is
// END-TO-END, and it is enforced UPSTREAM: the seed passes consult the same gateReach memo before
// the off-gate slice runs and propagate the read failure themselves, so the tick is already dead by
// the time off-gate would be reached. The explicit error check at the off-gate call site is
// defence-in-depth on top of that, and a mutation removing it SURVIVES this test — correctly, since
// the property holds without it. It is kept anyway: relying on an upstream pass to fail first is a
// dependency on an accident of ordering, not a guard.
func TestAdvanceExpansion_AnUnreadableGateGraphNeverRaisesExplorerDemand(t *testing.T) {
	h := sealedPocket()
	h.explorers.symbol, h.explorers.found = "EXPLORER-1", true
	// X1-HOME borders X1-MID, which the ledger does not hold — so the reach search must go to the
	// store for X1-MID's neighbours, and that read fails.
	gates := &selectiveFailGates{
		adjacency: map[string][]string{"X1-HOME": {"X1-MID"}, "X1-SEALED": {}},
		failOn:    map[string]bool{"X1-MID": true},
	}

	for i := range h.ledger.systems {
		h.ledger.systems[i].CatalogKnown = !h.unswept[h.ledger.systems[i].System]
	}
	p := h.ports()
	p.Gates = gates
	p.OffGate = OffGatePorts{Select: h.selector, Demand: h.sink, Explorer: h.explorers, Warp: h.warp}

	_, err := AdvanceExpansion(context.Background(), p, 1, ExpandKnobs{
		Enabled: true, MinBudgetRate: 0.05, Whitelist: h.whitelist,
	}, 1.0)
	if err == nil {
		t.Fatalf("the tick succeeded on an unreadable gate graph — an unread graph must never present " +
			"as an exhausted frontier")
	}
	if len(h.warp.calls) != 0 {
		t.Fatalf("warped %v off a reach search that failed to read", h.warp.calls)
	}
	for _, signal := range h.sink.emitted {
		if signal.Demanded {
			t.Fatalf("explorer demand raised off an unreadable reach search: %+v", signal)
		}
	}
}
