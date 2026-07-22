package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// --- sp-gvvph: RULINGS #7 — the COMMAND FRIGATE is never a depot hull. This complements the sp-fihvy/
// sp-fis8y home-reachability precondition with a second, reachability-INDEPENDENT non-viability: even a
// HOME-reachable frigate is a non-viable depot element, so a frigate that got seated as a
// warehouse/stocker hull is EVICTED rather than re-claimed on every restart recovery (the flagship must
// never be live-dedicated as a stationary depot hull). These tests drive the REAL *DaemonServer methods
// (never a spy sink) because the frigate rule lives inside depotElementHullViable / evictStrandedDepotElement,
// reusing the SAME fakeGateRouter / depotLaunchShipRepo doubles and gate-graph notion as the reachability tests.

// TestDepotElementHullViable_CommandFrigateIsNeverViableEvenAtHome proves change (b): the command
// frigate fails depotElementHullViable EVEN when parked at the depot in the home system (trivially
// reachable), while a regular home hull at the same waypoint stays viable — the byte-identical
// non-frigate control. A same-system frigate is rejected by the frigate rule BEFORE any reachability
// query, so the gate graph is never consulted: the rejection is the RULINGS #7 rule, not a routing verdict.
func TestDepotElementHullViable_CommandFrigateIsNeverViableEvenAtHome(t *testing.T) {
	s, _, playerID := newRecoveryTestServer(t)
	ctx := context.Background()

	frigate := homeReaderShip(t, "TORWIND-1", "X1-UM5-I56", commandRole, "warehouse") // flagship, parked AT the depot (home)
	regular := homeReaderShip(t, "WH-9", "X1-UM5-I56", "HAULER", "warehouse")         // a regular home hull at the same waypoint
	gate := &fakeGateRouter{routable: true}
	s.shipRepo = &depotLaunchShipRepo{ships: map[string]*navigation.Ship{"TORWIND-1": frigate, "WH-9": regular}}
	s.gateGraph = gate

	require.False(t, s.depotElementHullViable(ctx, "TORWIND-1", "X1-UM5-I56", playerID),
		"the command frigate is never a viable depot hull (sp-gvvph, RULINGS #7), even parked at the depot")
	require.True(t, s.depotElementHullViable(ctx, "WH-9", "X1-UM5-I56", playerID),
		"a regular home hull at the depot stays viable — the non-frigate path is byte-identical")
	require.Empty(t, gate.calls,
		"a same-system frigate is rejected by the frigate rule before any reachability query")
}

// TestLaunchDepotWarehouse_EvictsACommandFrigateSeatedAsDepotHull proves change (b) end-to-end plus the
// honest reason (change c): a command frigate seated as a warehouse element, parked AT home (so it is
// home-reachable — proving the eviction is the frigate rule, NOT a reachability failure), is EVICTED on
// recovery/re-launch — its stale depot-store binding removed, the hull un-dedicated, and its work-claim
// released with a reason that names sp-gvvph — instead of being re-launched (and re-seated) on the same
// flagship forever. A second, home-reachable anchor keeps the depot's >=1-warehouse invariant satisfiable
// across the eviction, and no fresh coordinator is launched: the scaler ramp re-grows the slot on a home
// hauler next tick.
func TestLaunchDepotWarehouse_EvictsACommandFrigateSeatedAsDepotHull(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	ctx := context.Background()
	require.NoError(t, s.AddDepot(ctx, playerID, DepotSpec{
		ID: "central",
		Warehouses: []ElementSpec{
			{Waypoint: "X1-UM5-I56", ShipSymbol: "WH-9"},      // a regular home-reachable anchor keeps the >=1-warehouse invariant
			{Waypoint: "X1-UM5-G49", ShipSymbol: "TORWIND-1"}, // the flagship, wrongly seated as a warehouse hull
		},
	}))

	// The flagship: parked AT home (X1-UM5, reachable), already seated "warehouse"-dedicated, actively
	// holding its coordinator's claim — the live shape where the frigate re-claims the slot on every restart.
	frigate := homeReaderShip(t, "TORWIND-1", "X1-UM5-B7", commandRole, "warehouse")
	require.NoError(t, frigate.AssignToContainer("wh-frigate-run", shared.NewRealClock()))
	shipRepo := &depotLaunchShipRepo{ships: map[string]*navigation.Ship{"TORWIND-1": frigate}}
	gate := &fakeGateRouter{routable: true} // reachable — so ONLY the frigate rule can evict it
	s.shipRepo = shipRepo
	s.gateGraph = gate

	err := s.launchDepotWarehouse(ctx, "TORWIND-1", "X1-UM5-G49", playerID)
	require.NoError(t, err, "eviction is a corrective no-op, not an error")

	reg := freshRegistry(t, db, playerID)
	require.False(t, hasWarehouse(reg, "TORWIND-1"), "the frigate's stale warehouse binding must be removed from the depot store")
	require.True(t, hasWarehouse(reg, "WH-9"), "the depot's home-reachable anchor warehouse must be untouched")

	require.Len(t, shipRepo.assignedFleet, 1, "the frigate must be un-dedicated, exactly once")
	require.Equal(t, assignFleetCall{symbol: "TORWIND-1", fleet: ""}, shipRepo.assignedFleet[0])
	require.Contains(t, shipRepo.releasedClaims, "TORWIND-1", "the frigate's work-claim must be released so it returns to free")
	require.False(t, frigate.IsAssigned(), "the released frigate must actually return to idle")

	require.NotEmpty(t, shipRepo.releaseReasons, "the claim release must record a reason")
	require.Contains(t, shipRepo.releaseReasons[0], "sp-gvvph",
		"the eviction reason must be honest: a frigate eviction is RULINGS #7 (sp-gvvph), not a home-reachability failure")

	require.Empty(t, gate.calls, "a same-system frigate is evicted by the frigate rule, never a reachability verdict")
	require.Empty(t, s.containers, "an evicted frigate must NOT get a fresh coordinator launched on the same call")
}
