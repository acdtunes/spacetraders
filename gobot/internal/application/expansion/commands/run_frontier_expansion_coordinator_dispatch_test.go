package commands

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// fakeExplorerDispatch is the double at the slice-A warp port (ExecuteWarpRoute, adapted). It records
// the dispatch so a test can prove the RIGHT explorer was warped to the RIGHT off-gate target — and,
// by call count, that it was NOT dispatched when it shouldn't be.
type fakeExplorerDispatch struct {
	calls      int
	lastShip   string
	lastTarget OffGateTarget
	err        error
}

func (f *fakeExplorerDispatch) DispatchExplorer(_ context.Context, _ int, shipSymbol string, target OffGateTarget) error {
	f.calls++
	f.lastShip = shipSymbol
	f.lastTarget = target
	return f.err
}

// newDedicatedExplorer builds an idle, warp-capable, "explorer"-dedicated hull — exactly what the
// autosizer's buy+dedicate-at-purchase produces (a SHIP_EXPLORER carrying MODULE_WARP_DRIVE_I, tagged
// to the "explorer" fleet so no coordinator poaches it).
func newDedicatedExplorer(t *testing.T, symbol, waypoint string) *navigation.Ship {
	t.Helper()
	loc, err := shared.NewWaypoint(waypoint, 0, 0)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(800, 800)
	require.NoError(t, err)
	cargo, err := shared.NewCargo(40, 0, nil)
	require.NoError(t, err)
	warp := navigation.NewShipModule("MODULE_WARP_DRIVE_I", 0, 0, navigation.ShipRequirements{})
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(1), loc, fuel, 800, 40, cargo, 9, "FRAME_EXPLORER", "EXPLORER", []*navigation.ShipModule{warp}, navigation.NavStatusInOrbit)
	require.NoError(t, err)
	ship.SetDedicatedFleet("explorer")
	return ship
}

// armedExhaustedFrontier wires a coordinator whose off-gate demand FIRES (empty queue past the
// debounce, a target selectable) with the given idle fleet and a recording dispatch port.
func armedExhaustedFrontier(t *testing.T, idle []*navigation.Ship, disp *fakeExplorerDispatch, target OffGateTarget, found bool) (*RunFrontierExpansionCoordinatorHandler, *RunFrontierExpansionCoordinatorCommand) {
	t.Helper()
	clock := &shared.MockClock{CurrentTime: time.Now()}
	h := newHandler(&fakePostRepo{}, &fakeFleetRepo{idle: idle}, &fakeLedgerRepo{}, clock)
	h.SetExpansionScanner(&fakeScanner{candidates: nil}) // empty queue → exhaustion → demand fires
	h.SetOffGateTargetSelector(&fakeOffGateSelector{found: found, target: target})
	h.SetExplorerDispatchPort(disp)
	cmd := testCmd()
	cmd.OffGateQueueExhaustionCycles = 1 // fire on the first empty-queue cycle
	return h, cmd
}

// Bead test (a) dispatch half: ARMED + off-gate demand fired + an idle dedicated explorer exists ⇒
// the explorer is warped, via ExecuteWarpRoute (the dispatch port), to the selected off-gate target.
func TestOffGateDispatch_WarpsIdleDedicatedExplorerToTarget(t *testing.T) {
	disp := &fakeExplorerDispatch{}
	target := OffGateTarget{SystemSymbol: "X1-OFF", FromSystem: "X1-EDGE", WarpFuelCost: 5}
	h, cmd := armedExhaustedFrontier(t, []*navigation.Ship{newDedicatedExplorer(t, "EXP-1", "X1-EDGE-A1")}, disp, target, true)

	require.NoError(t, h.ReconcileOnce(context.Background(), cmd))

	require.Equal(t, 1, disp.calls, "the bought+dedicated explorer must be dispatched exactly once when demand fires")
	require.Equal(t, "EXP-1", disp.lastShip, "the dedicated explorer hull is the one warped")
	require.Equal(t, "X1-OFF", disp.lastTarget.SystemSymbol, "warped to slice-B's selected off-gate target")
}

// No off-gate demand ⇒ NO dispatch (the explorer parks). Mutation guard for the demand-gate on
// dispatch: a two-candidate queue keeps the frontier serviceable so demand never fires.
func TestOffGateDispatch_NoDispatchWithoutDemand(t *testing.T) {
	disp := &fakeExplorerDispatch{}
	clock := &shared.MockClock{CurrentTime: time.Now()}
	h := newHandler(&fakePostRepo{}, &fakeFleetRepo{idle: []*navigation.Ship{newDedicatedExplorer(t, "EXP-1", "X1-EDGE-A1")}}, &fakeLedgerRepo{}, clock)
	h.SetExpansionScanner(&fakeScanner{candidates: []ExpansionCandidate{
		{SystemSymbol: "X1-V1", Hops: 1, Charted: false, Scanned: false},
		{SystemSymbol: "X1-V2", Hops: 1, Charted: false, Scanned: false},
	}}) // non-empty queue → no exhaustion → no off-gate demand
	h.SetOffGateTargetSelector(&fakeOffGateSelector{found: true, target: OffGateTarget{SystemSymbol: "X1-OFF"}})
	h.SetExplorerDispatchPort(disp)
	cmd := testCmd()

	require.NoError(t, h.ReconcileOnce(context.Background(), cmd))
	require.Equal(t, 0, disp.calls, "no off-gate demand ⇒ the explorer is NOT dispatched (it parks)")
}

// Demand fires but NO explorer has been bought yet (only a probe idle) ⇒ NO dispatch. The dispatch
// operates on a bought+dedicated explorer; there is nothing to warp until the autosizer buys one.
func TestOffGateDispatch_NoDispatchWhenNoExplorerExists(t *testing.T) {
	disp := &fakeExplorerDispatch{}
	h, cmd := armedExhaustedFrontier(t, []*navigation.Ship{newProbe(t, "P1", "X1-EDGE-A1")}, disp, OffGateTarget{SystemSymbol: "X1-OFF"}, true)

	require.NoError(t, h.ReconcileOnce(context.Background(), cmd))
	require.Equal(t, 0, disp.calls, "no dedicated explorer exists ⇒ nothing to dispatch")
}

// Demand fires but the selector found NO reachable target ⇒ NO dispatch (nowhere to warp).
func TestOffGateDispatch_NoDispatchWithoutTarget(t *testing.T) {
	disp := &fakeExplorerDispatch{}
	h, cmd := armedExhaustedFrontier(t, []*navigation.Ship{newDedicatedExplorer(t, "EXP-1", "X1-EDGE-A1")}, disp, OffGateTarget{}, false)

	require.NoError(t, h.ReconcileOnce(context.Background(), cmd))
	require.Equal(t, 0, disp.calls, "no reachable off-gate target ⇒ no dispatch")
}

// Bead test (i) HANDOFF: after chart-on-arrival grows the frontier (the scanner now surfaces gate-
// reachable candidates), off-gate demand STOPS firing, so the explorer is NOT re-dispatched — it
// PARKS. This is the loop closing: explorer charts → growFrontierGraph resumes → demand clears.
func TestOffGateDispatch_ParksWhenFrontierResumesAfterCharting(t *testing.T) {
	disp := &fakeExplorerDispatch{}
	clock := &shared.MockClock{CurrentTime: time.Now()}
	scanner := &fakeScanner{candidates: nil} // cycle 1: empty → demand fires
	h := newHandler(&fakePostRepo{}, &fakeFleetRepo{idle: []*navigation.Ship{newDedicatedExplorer(t, "EXP-1", "X1-EDGE-A1")}}, &fakeLedgerRepo{}, clock)
	h.SetExpansionScanner(scanner)
	h.SetOffGateTargetSelector(&fakeOffGateSelector{found: true, target: OffGateTarget{SystemSymbol: "X1-OFF"}})
	h.SetExplorerDispatchPort(disp)
	cmd := testCmd()
	cmd.OffGateQueueExhaustionCycles = 1
	ctx := context.Background()

	require.NoError(t, h.ReconcileOnce(ctx, cmd))
	require.Equal(t, 1, disp.calls, "cycle 1: exhausted frontier ⇒ explorer dispatched off-gate")

	// The explorer charted its target; growFrontierGraph now reaches the new cluster.
	scanner.candidates = []ExpansionCandidate{{SystemSymbol: "X1-NEW", Hops: 1, Charted: false, Scanned: false}}
	require.NoError(t, h.ReconcileOnce(ctx, cmd))
	require.Equal(t, 1, disp.calls, "cycle 2: frontier resumed (queue repopulated) ⇒ demand clears ⇒ explorer PARKS (no re-dispatch)")
}

// Bead test (i) HANDOFF, advance branch: if the frontier is STILL exhausted after the first charting,
// the (now-idle-again) explorer ADVANCES to the next off-gate target.
func TestOffGateDispatch_AdvancesToNextTargetWhileStillExhausted(t *testing.T) {
	disp := &fakeExplorerDispatch{}
	h, cmd := armedExhaustedFrontier(t, []*navigation.Ship{newDedicatedExplorer(t, "EXP-1", "X1-EDGE-A1")}, disp, OffGateTarget{SystemSymbol: "X1-OFF"}, true)
	ctx := context.Background()

	require.NoError(t, h.ReconcileOnce(ctx, cmd))
	require.NoError(t, h.ReconcileOnce(ctx, cmd)) // still exhausted → advance
	require.Equal(t, 2, disp.calls, "while the frontier stays exhausted the explorer advances to the next off-gate target")
}

// A dispatch-port error is logged and swallowed — a failed warp never aborts the whole reconcile pass.
func TestOffGateDispatch_DispatchErrorIsNonFatal(t *testing.T) {
	disp := &fakeExplorerDispatch{err: errors.New("warp API down")}
	h, cmd := armedExhaustedFrontier(t, []*navigation.Ship{newDedicatedExplorer(t, "EXP-1", "X1-EDGE-A1")}, disp, OffGateTarget{SystemSymbol: "X1-OFF"}, true)

	require.NoError(t, h.ReconcileOnce(context.Background(), cmd), "a dispatch failure must not fail the reconcile pass")
	require.Equal(t, 1, disp.calls)
}
