package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// --- sp-fihvy: depot stocker hull viability — home-reachability is a HARD PRECONDITION of the
// depot stocker hull, enforced at recovery/re-launch (this file) and at grow-time reclaim (the
// FindReclaimableForHome table in contract_scaler_ports_test.go). These tests drive the REAL
// *DaemonServer.launchDepotStocker (never a spy sink) because the sp-fihvy guard lives inside that
// method — a spy would bypass the very logic under test — backed by newRecoveryTestServer's real DB
// so eviction is proven durable (RemoveElement -> repository -> a fresh post-restart registry read).

// fakeGateRouter is the depotHomeRouter test double: a fixed routable/err verdict for every Routable
// call, recording each (from, to, playerID) query so a test can prove the SAME gate-graph
// routability notion was consulted — never a second reachability mechanism invented in the test.
type fakeGateRouter struct {
	routable bool
	err      error
	calls    []gateRouteQuery
}

type gateRouteQuery struct {
	from, to string
	playerID int
}

func (f *fakeGateRouter) Routable(ctx context.Context, fromSystem, toSystem string, playerID int) (bool, error) {
	f.calls = append(f.calls, gateRouteQuery{from: fromSystem, to: toSystem, playerID: playerID})
	return f.routable, f.err
}

// depotLaunchShipRepo is a minimal ship-repo fake for the launchDepotStocker viability + positioning
// seam: FindBySymbol serves a fixed hull, AssignFleet/ReleaseContainerClaim record + apply the two
// writes evictStrandedStocker (and positionDepotElementHull's re-dedicate) can issue. Embedding
// navigation.ShipRepository would panic on any unimplemented method reached via the nil interface, so
// every method this seam can reach is implemented directly rather than left to embed through.
type depotLaunchShipRepo struct {
	navigation.ShipRepository
	ships          map[string]*navigation.Ship
	assignedFleet  []assignFleetCall
	releasedClaims []string
}

func (r *depotLaunchShipRepo) FindBySymbol(ctx context.Context, symbol string, playerID shared.PlayerID) (*navigation.Ship, error) {
	return r.ships[symbol], nil
}

func (r *depotLaunchShipRepo) AssignFleet(ctx context.Context, shipSymbol, fleet string, playerID shared.PlayerID) error {
	r.assignedFleet = append(r.assignedFleet, assignFleetCall{symbol: shipSymbol, fleet: fleet})
	if ship, ok := r.ships[shipSymbol]; ok {
		ship.SetDedicatedFleet(fleet)
	}
	return nil
}

func (r *depotLaunchShipRepo) ReleaseContainerClaim(ctx context.Context, shipSymbol string, playerID shared.PlayerID, reason string) (bool, error) {
	r.releasedClaims = append(r.releasedClaims, shipSymbol)
	if ship, ok := r.ships[shipSymbol]; ok && ship.IsAssigned() {
		ship.ForceRelease(reason, shared.NewRealClock())
		return true, nil
	}
	return false, nil
}

// hasStocker reports whether shipSymbol is bound as a stocker element anywhere in reg — the
// depot-store durability check ("did the eviction actually persist?").
func hasStocker(reg *depot.Registry, shipSymbol string) bool {
	for _, d := range reg.Depots() {
		for _, st := range d.Stockers() {
			if st.ShipSymbol == shipSymbol {
				return true
			}
		}
	}
	return false
}

// TestLaunchDepotStocker_EvictsAForeignUnreachableHullInsteadOfRelaunching proves the root-cause fix
// (sp-fihvy): a stocker element bound to a hull that is NOT in, or gate-reachable to, the warehouse's
// home system (the live TORWIND-19/X1-ZK26 stranding — no gate route to home X1-UM5) is EVICTED on
// recovery/re-launch instead of being re-launched on the same stranded hull. The stale depot-store
// binding is removed, the hull is un-dedicated, and its work-claim is released, so the standing
// scaler's next tick re-grows the role through the now-hardened, home-scoped reclaim/buy tiers.
func TestLaunchDepotStocker_EvictsAForeignUnreachableHullInsteadOfRelaunching(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	ctx := context.Background()
	require.NoError(t, s.AddDepot(ctx, playerID, DepotSpec{
		ID:         "central",
		Warehouses: []ElementSpec{{Waypoint: "X1-UM5-I56", ShipSymbol: "WH-1"}},
		Stockers:   []ElementSpec{{Waypoint: "X1-UM5-I56", ShipSymbol: "TORWIND-19"}},
	}))

	// The stranded hull: parked foreign (X1-ZK26), already the seated "stocker"-dedicated hull, and
	// actively holding its (failing) coordinator's claim — the live bug's exact shape.
	stranded := homeReaderShip(t, "TORWIND-19", "X1-ZK26-A1", "HAULER", "stocker")
	require.NoError(t, stranded.AssignToContainer("stk-old-run", shared.NewRealClock()))
	shipRepo := &depotLaunchShipRepo{ships: map[string]*navigation.Ship{"TORWIND-19": stranded}}
	gate := &fakeGateRouter{routable: false} // no gate route X1-ZK26 -> X1-UM5
	s.shipRepo = shipRepo
	s.gateGraph = gate

	err := s.launchDepotStocker(ctx, "TORWIND-19", "X1-UM5-I56", playerID)
	require.NoError(t, err, "eviction is a corrective no-op, not an error")

	reg := freshRegistry(t, db, playerID)
	require.False(t, hasStocker(reg, "TORWIND-19"), "the stale stranded binding must be removed from the depot store")

	require.Len(t, shipRepo.assignedFleet, 1, "the stranded hull must be un-dedicated, exactly once")
	require.Equal(t, assignFleetCall{symbol: "TORWIND-19", fleet: ""}, shipRepo.assignedFleet[0])
	require.Contains(t, shipRepo.releasedClaims, "TORWIND-19", "the stranded hull's work-claim must be released")
	require.False(t, stranded.IsAssigned(), "the released hull must actually return to idle")

	require.Len(t, gate.calls, 1, "must consult the SAME gate-graph routability the stocker itself uses")
	require.Equal(t, gateRouteQuery{from: "X1-ZK26", to: "X1-UM5", playerID: playerID}, gate.calls[0])

	require.Empty(t, s.containers, "an evicted hull must NOT get a fresh coordinator launched on the same call")
}

// TestLaunchDepotStocker_HomeHullIsUnchangedAcrossRestartNoThrash proves RULINGS #2: a stocker
// already seated on a home-system hull is a byte-identical no-op across repeated recovery/re-launch
// calls (simulating two consecutive restarts replaying the same persisted depot registry) — no
// re-dedication, no claim release, no eviction; the binding STAYS seated. A same-system hull is
// trivially viable, so the guard never even queries the gate graph — zero Routable calls.
func TestLaunchDepotStocker_HomeHullIsUnchangedAcrossRestartNoThrash(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	ctx := context.Background()
	require.NoError(t, s.AddDepot(ctx, playerID, DepotSpec{
		ID:         "central",
		Warehouses: []ElementSpec{{Waypoint: "X1-UM5-I56", ShipSymbol: "WH-1"}},
		Stockers:   []ElementSpec{{Waypoint: "X1-UM5-I56", ShipSymbol: "TORWIND-18"}},
	}))

	// The home hull: parked in-system (X1-UM5), already the seated "stocker"-dedicated hull, already
	// flying its coordinator — the steady-state shape a restart must never disturb.
	home := homeReaderShip(t, "TORWIND-18", "X1-UM5-B7", "HAULER", "stocker")
	require.NoError(t, home.AssignToContainer("stk-run-1", shared.NewRealClock()))
	shipRepo := &depotLaunchShipRepo{ships: map[string]*navigation.Ship{"TORWIND-18": home}}
	gate := &fakeGateRouter{routable: true}
	s.shipRepo = shipRepo
	s.gateGraph = gate

	for i := 0; i < 2; i++ { // two consecutive calls == two simulated restarts replaying the registry
		require.NoError(t, s.launchDepotStocker(ctx, "TORWIND-18", "X1-UM5-I56", playerID))
	}

	reg := freshRegistry(t, db, playerID)
	require.True(t, hasStocker(reg, "TORWIND-18"), "the home hull's binding must STAY seated across restarts")
	require.Empty(t, shipRepo.assignedFleet, "an already-home, already-dedicated hull must never be re-dedicated (no thrash)")
	require.Empty(t, shipRepo.releasedClaims, "an already-home hull's live claim must never be released (no thrash)")
	require.True(t, home.IsAssigned(), "the hull keeps flying its own coordinator, undisturbed")
	require.Empty(t, gate.calls, "a same-system hull is trivially viable — it never queries the gate graph")
	require.Empty(t, s.containers, "an already-flying coordinator must not be relaunched")
}
