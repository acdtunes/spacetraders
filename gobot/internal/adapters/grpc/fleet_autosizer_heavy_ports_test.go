package grpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

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

type fakeTourTelemetryReader struct {
	rows     []trading.TourLegTelemetry
	err      error
	gotSince time.Time
}

func (f *fakeTourTelemetryReader) ListByPlayer(ctx context.Context, playerID int, since time.Time) ([]trading.TourLegTelemetry, error) {
	f.gotSince = since
	return f.rows, f.err
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
	src := &autosizerHeavySources{shipRepo: shipRepo, laneReader: lanes, clock: shared.NewRealClock()}

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
	src := &autosizerHeavySources{shipRepo: shipRepo, laneReader: lanes, clock: shared.NewRealClock()}

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
	src := &autosizerHeavySources{
		shipRepo:   &fakeHeavyShipRepo{all: ships},
		laneReader: &fakeLaneCounter{readable: false},
		clock:      shared.NewRealClock(),
	}
	_, readable, err := src.UnservedLaneCount(context.Background(), 1)
	require.NoError(t, err)
	require.False(t, readable, "an unreadable lane surface must fail closed")

	// (b) the lane read errors outright.
	src.laneReader = &fakeLaneCounter{err: errors.New("market surface down")}
	_, readable, _ = src.UnservedLaneCount(context.Background(), 1)
	require.False(t, readable)

	// (c) the ship read (system discovery) errors.
	src = &autosizerHeavySources{
		shipRepo:   &fakeHeavyShipRepo{err: errors.New("db down")},
		laneReader: &fakeLaneCounter{count: 9, readable: true},
		clock:      shared.NewRealClock(),
	}
	_, readable, err = src.UnservedLaneCount(context.Background(), 1)
	require.Error(t, err)
	require.False(t, readable)
}

// --- FleetTourRate -----------------------------------------------------------------------------

// The port reads a telemetry window and returns the computed fleet-average, marginal, and decline —
// in the (fleetAvg, marginal, declining, readable) order the heavy provider consumes.
func TestFleetTourRate_ComputesFleetAvgMarginalAndDecline(t *testing.T) {
	base := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	h := func(n int) time.Time { return base.Add(time.Duration(n) * time.Hour) }
	rows := []trading.TourLegTelemetry{
		// Ship A tour a1: net 100k / 1h = 100k/hr (completes h1).
		{TourID: "a1", ShipSymbol: "A", IsBuy: true, RealizedUnits: 100, RealizedUnitPrice: 1000, PlannedAt: h(0), RealizedAt: h(0), PlayerID: 1},
		{TourID: "a1", ShipSymbol: "A", IsBuy: false, RealizedUnits: 100, RealizedUnitPrice: 2000, PlannedAt: h(0), RealizedAt: h(1), PlayerID: 1},
		// Ship B tour b1: net 60k / 1h = 60k/hr (completes h3).
		{TourID: "b1", ShipSymbol: "B", IsBuy: true, RealizedUnits: 100, RealizedUnitPrice: 1000, PlannedAt: h(2), RealizedAt: h(2), PlayerID: 1},
		{TourID: "b1", ShipSymbol: "B", IsBuy: false, RealizedUnits: 100, RealizedUnitPrice: 1600, PlannedAt: h(2), RealizedAt: h(3), PlayerID: 1},
	}
	tele := &fakeTourTelemetryReader{rows: rows}
	src := &autosizerHeavySources{tourRates: tele, clock: shared.NewRealClock()}

	fleetAvg, marginal, declining, readable, err := src.FleetTourRate(context.Background(), 1)
	require.NoError(t, err)
	require.True(t, readable)
	require.Equal(t, 80000.0, fleetAvg)
	require.Equal(t, 60000.0, marginal)
	require.True(t, declining)
	require.True(t, tele.gotSince.Before(time.Now()), "the port reads a trailing window, not all history")
}

// RULINGS #4: a telemetry read failure fails the realized-rate signal CLOSED (RateReadable=false),
// so the heavy realized-rate/payback guards block on their own.
func TestFleetTourRate_UnreadableTelemetry_FailsClosed(t *testing.T) {
	// (a) repo error.
	src := &autosizerHeavySources{tourRates: &fakeTourTelemetryReader{err: errors.New("db down")}, clock: shared.NewRealClock()}
	_, _, _, readable, err := src.FleetTourRate(context.Background(), 1)
	require.Error(t, err)
	require.False(t, readable)

	// (b) no computable rate (empty window).
	src = &autosizerHeavySources{tourRates: &fakeTourTelemetryReader{rows: nil}, clock: shared.NewRealClock()}
	_, _, _, readable, err = src.FleetTourRate(context.Background(), 1)
	require.NoError(t, err)
	require.False(t, readable, "an empty telemetry window has no computable rate → fail closed")
}
