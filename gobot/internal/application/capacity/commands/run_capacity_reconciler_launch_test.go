package commands

// sp-rt3b6 PART 1 — the reconciler must LAUNCH a hub's depot coordinators on the REUSE
// path, not merely re-tag the dedication (the buy path already auto-launches via the
// autosizer's StartWarehouse bridge; the reuse path did not, so reused hulls sat
// dedicated-but-IDLE and the hub stayed "uncovered" forever → latent unbounded buy).
// A hub launches ONLY when its executed reassigns FULLY staff it (warehouse+stocker+
// worker) with no tier-4 capital action — complete-hub-first, never a partial depot.

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
)

// spyDepotLauncher records every LaunchDepot dispatch (the sp-38xc lifecycle seam the
// reuse path drives). failHub makes that one hub's launch error.
type spyDepotLauncher struct {
	mu        sync.Mutex
	launched  []DepotLaunchSpec
	playerIDs []int
	failHub   string
}

func (s *spyDepotLauncher) LaunchDepot(_ context.Context, playerID int, spec DepotLaunchSpec) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.launched = append(s.launched, spec)
	s.playerIDs = append(s.playerIDs, playerID)
	if s.failHub != "" && spec.HubSymbol == s.failHub {
		return errors.New("launch boom")
	}
	return nil
}

func (s *spyDepotLauncher) specs() []DepotLaunchSpec {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]DepotLaunchSpec(nil), s.launched...)
}

func (s *spyDepotLauncher) hubs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, spec := range s.launched {
		out = append(out, spec.HubSymbol)
	}
	return out
}

// reassign builds one tier-1 reuse-idle reassignment carrying the role (via GapKind),
// hub, ship, and target waypoint — exactly what the ladder differ emits and CONVERGE
// executes onto Actuator.ReuseIdleHull.
func reassign(hub, ship string, role capacity.GapKind) capacity.Action {
	return capacity.Action{
		Tier: capacity.TierReuseIdle, Verb: capacity.VerbReassignHull,
		GapKind: role, HubSymbol: hub, ShipSymbol: ship, TargetWaypoint: hub,
	}
}

// fullyStaffedHub is the three reassigns that fully staff a 1-warehouse/1-stocker/
// 1-worker depot by reuse.
func fullyStaffedHub(hub string) []capacity.Action {
	return []capacity.Action{
		reassign(hub, hub+"-WH", capacity.GapWarehouseShort),
		reassign(hub, hub+"-ST", capacity.GapStockerShort),
		reassign(hub, hub+"-DL", capacity.GapWorkerShort),
	}
}

func oneWarehouseHub(symbol string) capacity.DesiredHub {
	return capacity.DesiredHub{HubSymbol: symbol, WarehouseCount: 1, StockerCount: 1, WorkerCount: 1}
}

// Behavior (the core fix, RED today): when the reuse path fully staffs a hub's depot
// roles, the reconciler LAUNCHES that hub's depot — a DepotLaunchSpec carrying each
// reassigned hull in its role (warehouse/stocker/delivery). Today nothing launches.
func TestCapacityReconciler_LaunchesDepotWhenReuseFullyStaffsHub(t *testing.T) {
	f := newLoopFixture()
	launcher := &spyDepotLauncher{}
	f.planner.desired = capacity.DesiredTopology{Hubs: []capacity.DesiredHub{oneWarehouseHub("X1-HUB-A")}}
	f.governor.result = capacity.GovernResult{Approved: fullyStaffedHub("X1-HUB-A")}

	h := f.handler()
	h.SetDepotLauncher(launcher)
	outcomes := runTicks(t, h, reconcilerCmd(), 1, nil)

	require.Len(t, launcher.specs(), 1, "a fully reuse-staffed hub must LAUNCH exactly one depot")
	spec := launcher.specs()[0]
	require.Equal(t, "X1-HUB-A", spec.HubSymbol)
	require.Equal(t, []DepotLaunchElement{{Waypoint: "X1-HUB-A", ShipSymbol: "X1-HUB-A-WH"}}, spec.Warehouses)
	require.Equal(t, []DepotLaunchElement{{Waypoint: "X1-HUB-A", ShipSymbol: "X1-HUB-A-ST"}}, spec.Stockers)
	require.Equal(t, []DepotLaunchElement{{Waypoint: "X1-HUB-A", ShipSymbol: "X1-HUB-A-DL"}}, spec.DeliveryHulls)
	require.Equal(t, []int{1}, launcher.playerIDs, "the launch must carry the reconciling player")
	require.Equal(t, []string{"X1-HUB-A"}, outcomes[0].DepotsLaunched)
}

// Behavior (complete-hub-first): with idle hulls enough for only ONE full hub, exactly
// one fully-staffed depot launches and the partially-staffed hub #2 gets NONE — never
// scatter limited hulls into non-functional partial depots.
func TestCapacityReconciler_LaunchesOnlyFullyStaffedHubNotPartial(t *testing.T) {
	f := newLoopFixture()
	launcher := &spyDepotLauncher{}
	f.planner.desired = capacity.DesiredTopology{Hubs: []capacity.DesiredHub{
		oneWarehouseHub("X1-HUB-A"),
		oneWarehouseHub("X1-HUB-B"),
	}}
	approved := fullyStaffedHub("X1-HUB-A")
	approved = append(approved, reassign("X1-HUB-B", "X1-HUB-B-WH", capacity.GapWarehouseShort)) // B only partly staffed
	f.governor.result = capacity.GovernResult{Approved: approved}

	h := f.handler()
	h.SetDepotLauncher(launcher)
	outcomes := runTicks(t, h, reconcilerCmd(), 1, nil)

	require.Equal(t, []string{"X1-HUB-A"}, launcher.hubs(),
		"only the fully-staffed hub launches; the partial hub #2 must NOT")
	require.Equal(t, []string{"X1-HUB-A"}, outcomes[0].DepotsLaunched)
}

// Behavior (idempotent / restart proxy): a tick that reassigns NOTHING (a hub already
// covered — the state after the depot persisted) launches NOTHING, even though the hub
// is still desired. This is the mechanism that stops relaunch: 0 reassigns → 0 launches.
func TestCapacityReconciler_NoDepotLaunchWithoutReassigns(t *testing.T) {
	f := newLoopFixture()
	launcher := &spyDepotLauncher{}
	f.planner.desired = capacity.DesiredTopology{Hubs: []capacity.DesiredHub{oneWarehouseHub("X1-HUB-A")}}
	f.governor.result = capacity.GovernResult{} // hub covered: no reassigns this tick

	h := f.handler()
	h.SetDepotLauncher(launcher)
	runTicks(t, h, reconcilerCmd(), 1, nil)

	require.Empty(t, launcher.specs(), "a covered hub (no reassigns) must launch nothing — the idempotent/restart-safe no-op")
}

// Behavior (fully-staffed-by-reuse gate): a hub with a tier-4 CAPITAL action is NOT
// fully staffed by reuse — even if its reassigns meet the counts, the presence of a
// capital draw means a partial hub whose remainder is being BOUGHT, so it must NOT
// launch (the buy path's own launch will cover it once the hull lands).
func TestCapacityReconciler_NoDepotLaunchWhenHubHasCapitalAction(t *testing.T) {
	f := newLoopFixture()
	launcher := &spyDepotLauncher{}
	f.planner.desired = capacity.DesiredTopology{Hubs: []capacity.DesiredHub{oneWarehouseHub("X1-HUB-A")}}
	f.governor.result = capacity.GovernResult{Approved: fullyStaffedHub("X1-HUB-A")}
	// The tick ALSO carries a tier-4 capital action for the hub (differ output).
	f.differ.actions = []capacity.Action{{Tier: capacity.TierCapital, Verb: capacity.VerbBuyHull, HubSymbol: "X1-HUB-A", HullDelta: 1}}

	h := f.handler()
	h.SetDepotLauncher(launcher)
	runTicks(t, h, reconcilerCmd(), 1, nil)

	require.Empty(t, launcher.specs(), "a hub with a tier-4 capital action is not fully staffed by reuse — no partial launch")
}

// Behavior (a hub with no desired warehouse cannot anchor a depot): a worker-only
// desired hub (0 warehouses) never launches a depot even if its worker reassign lands
// — NewContractDepot requires a warehouse anchor, so there is nothing to launch.
func TestCapacityReconciler_NoDepotLaunchForWarehouselessHub(t *testing.T) {
	f := newLoopFixture()
	launcher := &spyDepotLauncher{}
	f.planner.desired = capacity.DesiredTopology{Hubs: []capacity.DesiredHub{
		{HubSymbol: "X1-HUB-A", WarehouseCount: 0, StockerCount: 0, WorkerCount: 1},
	}}
	f.governor.result = capacity.GovernResult{Approved: []capacity.Action{
		reassign("X1-HUB-A", "X1-HUB-A-DL", capacity.GapWorkerShort),
	}}

	h := f.handler()
	h.SetDepotLauncher(launcher)
	runTicks(t, h, reconcilerCmd(), 1, nil)

	require.Empty(t, launcher.specs(), "a warehouse-less (worker-only) hub has no depot to anchor — never launch")
}

// Behavior (DryRun): observe-only records what it WOULD launch and dispatches NOTHING.
func TestCapacityReconciler_DryRunRecordsWouldLaunchDispatchesNothing(t *testing.T) {
	f := newLoopFixture()
	launcher := &spyDepotLauncher{}
	f.planner.desired = capacity.DesiredTopology{Hubs: []capacity.DesiredHub{oneWarehouseHub("X1-HUB-A")}}
	f.governor.result = capacity.GovernResult{Approved: fullyStaffedHub("X1-HUB-A")}
	cmd := reconcilerCmd()
	cmd.DryRun = true

	h := f.handler()
	h.SetDepotLauncher(launcher)
	outcomes := runTicks(t, h, cmd, 1, nil)

	require.Empty(t, launcher.specs(), "DryRun must dispatch NO launch")
	require.Equal(t, []string{"X1-HUB-A"}, outcomes[0].WouldLaunchDepots,
		"DryRun must RECORD the would-be launch for the operator to watch")
	require.Empty(t, outcomes[0].DepotsLaunched, "DryRun launches nothing")
}

// Behavior (byte-identical OFF): with NO launcher wired, the reuse path runs exactly as
// before — the reassigns still execute, but nothing launches and nothing panics.
func TestCapacityReconciler_NilDepotLauncherByteIdentical(t *testing.T) {
	f := newLoopFixture()
	f.planner.desired = capacity.DesiredTopology{Hubs: []capacity.DesiredHub{oneWarehouseHub("X1-HUB-A")}}
	f.governor.result = capacity.GovernResult{Approved: fullyStaffedHub("X1-HUB-A")}

	h := f.handler() // no SetDepotLauncher
	outcomes := runTicks(t, h, reconcilerCmd(), 1, nil)

	require.Len(t, f.actuator.calls(capacity.VerbReassignHull), 3, "the reassigns still execute (byte-identical CONVERGE)")
	require.Empty(t, outcomes[0].DepotsLaunched, "a nil launcher launches nothing")
	require.Empty(t, outcomes[0].WouldLaunchDepots)
}

// Behavior (idempotent across ticks): tick 1 fully staffs + launches the hub; tick 2,
// now with the hub covered (no reassigns), launches nothing. Exactly ONE launch across
// two ticks — no relaunch of an already-serving depot.
func TestCapacityReconciler_IdempotentAcrossTicksNoRelaunch(t *testing.T) {
	f := newLoopFixture()
	launcher := &spyDepotLauncher{}
	f.planner.desired = capacity.DesiredTopology{Hubs: []capacity.DesiredHub{oneWarehouseHub("X1-HUB-A")}}
	f.governor.result = capacity.GovernResult{Approved: fullyStaffedHub("X1-HUB-A")}

	h := f.handler()
	h.SetDepotLauncher(launcher)
	runTicks(t, h, reconcilerCmd(), 2, func(out capacity.TickOutcome) {
		if out.Sequence == 1 {
			f.governor.result = capacity.GovernResult{} // hub now covered → no reassigns next tick
		}
	})

	require.Equal(t, []string{"X1-HUB-A"}, launcher.hubs(),
		"the depot launches once; a covered hub emits no reassigns next tick so it is never relaunched")
}

// Behavior (launch failure is loud, not swallowed): a LaunchDepot error is surfaced on
// the tick outcome (a failed launch a captain must see), not silently dropped.
func TestCapacityReconciler_DepotLaunchFailureSurfacedOnOutcome(t *testing.T) {
	f := newLoopFixture()
	launcher := &spyDepotLauncher{failHub: "X1-HUB-A"}
	f.planner.desired = capacity.DesiredTopology{Hubs: []capacity.DesiredHub{oneWarehouseHub("X1-HUB-A")}}
	f.governor.result = capacity.GovernResult{Approved: fullyStaffedHub("X1-HUB-A")}

	h := f.handler()
	h.SetDepotLauncher(launcher)
	outcomes := runTicks(t, h, reconcilerCmd(), 1, nil)

	require.Contains(t, outcomes[0].Error, "launch boom", "a launch failure must surface on the outcome")
	require.Empty(t, outcomes[0].DepotsLaunched, "a failed launch is not recorded as launched")
}
