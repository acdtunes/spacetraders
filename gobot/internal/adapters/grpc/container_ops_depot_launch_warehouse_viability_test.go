package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// --- sp-fis8y: generalizes sp-fihvy's depot-stocker home-reachability precondition to the
// WAREHOUSE grow — the live TORWIND-19 stranding (IN_ORBIT X1-ZK26, no gate route to home X1-UM5)
// hit the WAREHOUSE role, not the stocker: the scaler grew the 5th warehouse on the stranded hull
// and the container failed ("could not reach home waypoint X1-UM5-G49 after 3 attempts"). These
// tests mirror container_ops_depot_launch_viability_test.go's stocker tests exactly, reusing the
// SAME fakeGateRouter / depotLaunchShipRepo doubles and the SAME depotElementHullViable guard
// (renamed, role-neutral) — never a second reachability mechanism invented for the warehouse.

// hasWarehouse reports whether shipSymbol is bound as a warehouse element anywhere in reg — the
// depot-store durability check ("did the eviction actually persist?"), mirroring hasStocker.
func hasWarehouse(reg *depot.Registry, shipSymbol string) bool {
	for _, d := range reg.Depots() {
		for _, wh := range d.Warehouses() {
			if wh.ShipSymbol == shipSymbol {
				return true
			}
		}
	}
	return false
}

// TestLaunchDepotWarehouse_EvictsAForeignUnreachableHullInsteadOfRelaunching proves the
// generalized fix (sp-fis8y): a warehouse element bound to a hull that is NOT in, or
// gate-reachable to, the depot's home system (the live TORWIND-19/X1-ZK26 stranding — no gate
// route to home X1-UM5) is EVICTED on recovery/re-launch instead of being re-launched (and
// failed) on the same stranded hull. A second, home-reachable warehouse anchor ("WH-1") keeps the
// depot's >=1-warehouse invariant satisfiable across the eviction.
func TestLaunchDepotWarehouse_EvictsAForeignUnreachableHullInsteadOfRelaunching(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	ctx := context.Background()
	require.NoError(t, s.AddDepot(ctx, playerID, DepotSpec{
		ID: "central",
		Warehouses: []ElementSpec{
			{Waypoint: "X1-UM5-I56", ShipSymbol: "WH-1"},
			{Waypoint: "X1-UM5-G49", ShipSymbol: "TORWIND-19"},
		},
	}))

	// The stranded hull: parked foreign (X1-ZK26), already the seated "warehouse"-dedicated hull,
	// and actively holding its (failing) coordinator's claim — the live bug's exact shape.
	stranded := homeReaderShip(t, "TORWIND-19", "X1-ZK26-A1", "HAULER", "warehouse")
	require.NoError(t, stranded.AssignToContainer("wh-old-run", shared.NewRealClock()))
	shipRepo := &depotLaunchShipRepo{ships: map[string]*navigation.Ship{"TORWIND-19": stranded}}
	gate := &fakeGateRouter{routable: false} // no gate route X1-ZK26 -> X1-UM5
	s.shipRepo = shipRepo
	s.gateGraph = gate

	err := s.launchDepotWarehouse(ctx, "TORWIND-19", "X1-UM5-G49", playerID)
	require.NoError(t, err, "eviction is a corrective no-op, not an error")

	reg := freshRegistry(t, db, playerID)
	require.False(t, hasWarehouse(reg, "TORWIND-19"), "the stale stranded binding must be removed from the depot store")
	require.True(t, hasWarehouse(reg, "WH-1"), "the depot's home-reachable anchor warehouse must be untouched")

	require.Len(t, shipRepo.assignedFleet, 1, "the stranded hull must be un-dedicated, exactly once")
	require.Equal(t, assignFleetCall{symbol: "TORWIND-19", fleet: ""}, shipRepo.assignedFleet[0])
	require.Contains(t, shipRepo.releasedClaims, "TORWIND-19", "the stranded hull's work-claim must be released")
	require.False(t, stranded.IsAssigned(), "the released hull must actually return to idle")

	require.Len(t, gate.calls, 1, "must consult the SAME gate-graph routability the stocker guard uses")
	require.Equal(t, gateRouteQuery{from: "X1-ZK26", to: "X1-UM5", playerID: playerID}, gate.calls[0])

	require.Empty(t, s.containers, "an evicted hull must NOT get a fresh coordinator launched on the same call")
}

// TestLaunchDepotWarehouse_HomeHullIsUnchangedAcrossRestartNoThrash proves RULINGS #2: a warehouse
// already seated on a home-system hull is a byte-identical no-op across repeated recovery/re-launch
// calls (simulating two consecutive restarts replaying the same persisted depot registry) — no
// re-dedication, no claim release, no eviction; the binding STAYS seated. A same-system hull is
// trivially viable, so the guard never even queries the gate graph — zero Routable calls.
func TestLaunchDepotWarehouse_HomeHullIsUnchangedAcrossRestartNoThrash(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	ctx := context.Background()
	require.NoError(t, s.AddDepot(ctx, playerID, DepotSpec{
		ID:         "central",
		Warehouses: []ElementSpec{{Waypoint: "X1-UM5-I56", ShipSymbol: "WH-1"}},
	}))

	// The home hull: parked in-system (X1-UM5), already the seated "warehouse"-dedicated hull,
	// already flying its coordinator — the steady-state shape a restart must never disturb.
	home := homeReaderShip(t, "WH-1", "X1-UM5-B7", "HAULER", "warehouse")
	require.NoError(t, home.AssignToContainer("wh-run-1", shared.NewRealClock()))
	shipRepo := &depotLaunchShipRepo{ships: map[string]*navigation.Ship{"WH-1": home}}
	gate := &fakeGateRouter{routable: true}
	s.shipRepo = shipRepo
	s.gateGraph = gate

	for i := 0; i < 2; i++ { // two consecutive calls == two simulated restarts replaying the registry
		require.NoError(t, s.launchDepotWarehouse(ctx, "WH-1", "X1-UM5-I56", playerID))
	}

	reg := freshRegistry(t, db, playerID)
	require.True(t, hasWarehouse(reg, "WH-1"), "the home hull's binding must STAY seated across restarts")
	require.Empty(t, shipRepo.assignedFleet, "an already-home, already-dedicated hull must never be re-dedicated (no thrash)")
	require.Empty(t, shipRepo.releasedClaims, "an already-home hull's live claim must never be released (no thrash)")
	require.True(t, home.IsAssigned(), "the hull keeps flying its own coordinator, undisturbed")
	require.Empty(t, gate.calls, "a same-system hull is trivially viable — it never queries the gate graph")
	require.Empty(t, s.containers, "an already-flying coordinator must not be relaunched")
}
