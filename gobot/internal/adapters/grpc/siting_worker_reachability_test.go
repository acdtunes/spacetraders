package grpc

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// reachStubShipRepo serves a fixed idle-worker roster to FindIdleLightHaulers via
// FindAllByPlayer; every other repo method comes from the embedded interface (never called).
type reachStubShipRepo struct {
	navigation.ShipRepository
	ships []*navigation.Ship
	err   error
}

func (r *reachStubShipRepo) FindAllByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*navigation.Ship, error) {
	return r.ships, r.err
}

// reachFakeGateGraph is the stored-adjacency reach stand-in: hopsFrom maps a source system to
// the ferry-jump count to reach the candidate; an absent source is unroutable (no path in).
type reachFakeGateGraph struct {
	hopsFrom map[string]int
}

func (g *reachFakeGateGraph) RepositionPath(ctx context.Context, fromSystem, toSystem string, maxJumps int) ([]string, error) {
	h, ok := g.hopsFrom[fromSystem]
	if !ok {
		return nil, errors.New("unroutable")
	}
	return make([]string, h+1), nil // len(path)-1 == hops
}

// newIdleWorkerAt builds an idle HAULER hull parked at the given waypoint (its system is the
// waypoint minus the last -suffix), the idle manufacturing-worker shape the locator recognizes.
// NOTE: symbols must NOT end in "-1" — that is the command-hull convention IsCommandHull excludes,
// so such a hull is (correctly) kept out of the manufacturing worker pool.
func newIdleWorkerAt(t *testing.T, symbol, waypointSymbol string, playerID int) *navigation.Ship {
	t.Helper()
	cargo, err := shared.NewCargo(40, 0, nil)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(100, 100)
	require.NoError(t, err)
	waypoint, err := shared.NewWaypoint(waypointSymbol, 0, 0)
	require.NoError(t, err)
	ship, err := navigation.NewShip(
		symbol, shared.MustNewPlayerID(playerID), waypoint, fuel, 100, 40, cargo, 30,
		"FRAME_LIGHT_FREIGHTER", "HAULER", nil, navigation.NavStatusDocked,
	)
	require.NoError(t, err)
	return ship
}

// An idle worker already in the candidate system is fully staffable → signal 1.0.
func TestWorkerReachability_InSystemIdleWorkerIsFullyStaffable(t *testing.T) {
	repo := &reachStubShipRepo{ships: []*navigation.Ship{
		newIdleWorkerAt(t, "WKR-A", "X1-CAND-A1", 1),
	}}
	p := newSitingWorkerReachabilityProvider(repo, &reachFakeGateGraph{})
	sig, err := p.Reachability(context.Background(), 1, "X1-CAND")
	require.NoError(t, err)
	if sig != 1.0 {
		t.Errorf("in-system idle worker signal = %v, want 1.0", sig)
	}
}

// No idle worker anywhere in the fleet → nothing can man the site → signal 0.0.
func TestWorkerReachability_NoIdleWorkerAnywhereIsUnstaffable(t *testing.T) {
	repo := &reachStubShipRepo{ships: nil}
	p := newSitingWorkerReachabilityProvider(repo, &reachFakeGateGraph{})
	sig, err := p.Reachability(context.Background(), 1, "X1-CAND")
	require.NoError(t, err)
	if sig != 0.0 {
		t.Errorf("no-worker signal = %v, want 0.0", sig)
	}
}

// A worker two ferry-jumps away decays with distance: signal = 1/(1+hops) = 1/3.
func TestWorkerReachability_FerryDistanceDecays(t *testing.T) {
	repo := &reachStubShipRepo{ships: []*navigation.Ship{
		newIdleWorkerAt(t, "WKR-A", "X1-FAR-A1", 1),
	}}
	graph := &reachFakeGateGraph{hopsFrom: map[string]int{"X1-FAR": 2}}
	p := newSitingWorkerReachabilityProvider(repo, graph)
	sig, err := p.Reachability(context.Background(), 1, "X1-CAND")
	require.NoError(t, err)
	if sig != 1.0/3.0 {
		t.Errorf("2-hop signal = %v, want %v", sig, 1.0/3.0)
	}
}

// An idle worker exists but no ferry path reaches the candidate (the C81/GS93 graph-blocked
// case) → the site is unstaffable → signal 0.0.
func TestWorkerReachability_NoFerryPathIsUnstaffable(t *testing.T) {
	repo := &reachStubShipRepo{ships: []*navigation.Ship{
		newIdleWorkerAt(t, "WKR-A", "X1-ISLAND-A1", 1),
	}}
	graph := &reachFakeGateGraph{hopsFrom: map[string]int{}} // X1-ISLAND cannot route in
	p := newSitingWorkerReachabilityProvider(repo, graph)
	sig, err := p.Reachability(context.Background(), 1, "X1-CAND")
	require.NoError(t, err)
	if sig != 0.0 {
		t.Errorf("no-ferry-path signal = %v, want 0.0", sig)
	}
}

// The NEAREST idle-worker pool sets the distance: with workers 3 and 1 jumps out, the 1-jump
// pool wins → signal = 1/(1+1) = 0.5.
func TestWorkerReachability_NearestPoolWins(t *testing.T) {
	repo := &reachStubShipRepo{ships: []*navigation.Ship{
		newIdleWorkerAt(t, "W-FAR", "X1-FAR-A1", 1),
		newIdleWorkerAt(t, "W-NEAR", "X1-NEAR-A1", 1),
	}}
	graph := &reachFakeGateGraph{hopsFrom: map[string]int{"X1-FAR": 3, "X1-NEAR": 1}}
	p := newSitingWorkerReachabilityProvider(repo, graph)
	sig, err := p.Reachability(context.Background(), 1, "X1-CAND")
	require.NoError(t, err)
	if sig != 0.5 {
		t.Errorf("nearest-pool signal = %v, want 0.5", sig)
	}
}

// A ship-repo read error propagates so SCORE treats reachability as neutral (no penalty) —
// a transient read never nukes the portfolio.
func TestWorkerReachability_RepoErrorPropagates(t *testing.T) {
	repo := &reachStubShipRepo{err: errors.New("db down")}
	p := newSitingWorkerReachabilityProvider(repo, &reachFakeGateGraph{})
	_, err := p.Reachability(context.Background(), 1, "X1-CAND")
	if err == nil {
		t.Errorf("repo error must propagate, got nil")
	}
}
