package grpc

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	tradingQueries "github.com/andrescamacho/spacetraders-go/internal/application/trading/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// heavySourcesFor wires the demand source over the shared unserved-lane reader, the way the
// composition root does.
func heavySourcesFor(shipRepo navigation.ShipRepository, lanes tradingQueries.ProfitableLaneCounter) *autosizerHeavySources {
	return &autosizerHeavySources{shipRepo: shipRepo, unserved: tradingQueries.NewUnservedLaneReader(shipRepo, lanes)}
}

// --- fakes for the heavy-demand seam ports ----------------------------------------------------

type fakeHeavyShipRepo struct {
	navigation.ShipRepository
	all []*navigation.Ship
	err error
}

func (r *fakeHeavyShipRepo) FindAllByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*navigation.Ship, error) {
	return r.all, r.err
}

type fakeLaneCounter struct {
	count      int
	readable   bool
	err        error
	gotSystems []string
}

func (f *fakeLaneCounter) CountProfitableLanes(ctx context.Context, playerID int, systems []string) (int, bool, error) {
	f.gotSystems = systems
	return f.count, f.readable, f.err
}

// tradeShipAt builds a trade-dedicated hull parked at waypoint (its system is the discovery signal).
func tradeShipAt(t *testing.T, symbol string, playerID int, waypoint string) *navigation.Ship {
	t.Helper()
	cargo, err := shared.NewCargo(40, 0, nil)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(100, 100)
	require.NoError(t, err)
	wp, err := shared.NewWaypoint(waypoint, 0, 0)
	require.NoError(t, err)
	ship, err := navigation.NewShip(
		symbol, shared.MustNewPlayerID(playerID), wp, fuel, 100, 40, cargo, 30,
		"FRAME_HEAVY_FREIGHTER", "HAULER", nil, navigation.NavStatusDocked,
	)
	require.NoError(t, err)
	ship.SetDedicatedFleet("trade")
	return ship
}

// --- UnservedLaneCount -------------------------------------------------------------------------

// The port discovers the player's trading systems from its hulls, asks the lane reader how many
// profitable lanes those systems rank, and returns the count BEYOND the current heavy pool.
func TestUnservedLaneCount_ReadableCountBeyondHeavies(t *testing.T) {
	shipRepo := &fakeHeavyShipRepo{all: []*navigation.Ship{
		tradeShipAt(t, "TR-1", 1, "X1-AA-1"),
		tradeShipAt(t, "TR-2", 1, "X1-BB-1"),
	}}
	lanes := &fakeLaneCounter{count: 5, readable: true}
	src := heavySourcesFor(shipRepo, lanes)

	unserved, readable, err := src.UnservedLaneCount(context.Background(), 1)
	require.NoError(t, err)
	require.True(t, readable)
	require.Equal(t, 3, unserved, "5 profitable lanes − 2 trade hulls = 3 unserved")
	require.ElementsMatch(t, []string{"X1-AA", "X1-BB"}, lanes.gotSystems, "systems discovered from hull locations")
}

// More heavies than profitable lanes → no unserved demand (clamped at 0), still readable.
func TestUnservedLaneCount_MoreHeaviesThanLanes_ZeroButReadable(t *testing.T) {
	shipRepo := &fakeHeavyShipRepo{all: []*navigation.Ship{
		tradeShipAt(t, "TR-1", 1, "X1-AA-1"),
		tradeShipAt(t, "TR-2", 1, "X1-AA-2"),
		tradeShipAt(t, "TR-3", 1, "X1-AA-3"),
	}}
	lanes := &fakeLaneCounter{count: 2, readable: true}
	src := heavySourcesFor(shipRepo, lanes)

	unserved, readable, err := src.UnservedLaneCount(context.Background(), 1)
	require.NoError(t, err)
	require.True(t, readable)
	require.Equal(t, 0, unserved)
}

// RULINGS #4: an unreadable lane surface fails CLOSED — never a wrongly-inferred zero-or-positive
// demand feeding a heavy buy.
func TestUnservedLaneCount_GenuineReadFailure_FailsClosed(t *testing.T) {
	ships := []*navigation.Ship{tradeShipAt(t, "TR-1", 1, "X1-AA-1")}

	// (a) the lane reader reports its surface unreadable.
	src := heavySourcesFor(&fakeHeavyShipRepo{all: ships}, &fakeLaneCounter{readable: false})
	_, readable, err := src.UnservedLaneCount(context.Background(), 1)
	require.NoError(t, err)
	require.False(t, readable, "an unreadable lane surface must fail closed")

	// (b) the lane read errors outright.
	src = heavySourcesFor(&fakeHeavyShipRepo{all: ships}, &fakeLaneCounter{err: errors.New("market surface down")})
	_, readable, _ = src.UnservedLaneCount(context.Background(), 1)
	require.False(t, readable)

	// (c) the ship read (system discovery) errors.
	src = heavySourcesFor(&fakeHeavyShipRepo{err: errors.New("db down")}, &fakeLaneCounter{count: 9, readable: true})
	_, readable, err = src.UnservedLaneCount(context.Background(), 1)
	require.Error(t, err)
	require.False(t, readable)
}
