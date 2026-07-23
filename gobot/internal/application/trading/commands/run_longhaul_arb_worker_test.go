package commands

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// ---- worker port fakes -----------------------------------------------------

type fakeShipLoader struct {
	ship *navigation.Ship
	err  error
}

func (f *fakeShipLoader) LoadShip(_ context.Context, _ string, _ int) (*navigation.Ship, error) {
	return f.ship, f.err
}

type fakeLaneDiscoverer struct {
	lanes []pricedLongHaulLane
	err   error
}

func (f *fakeLaneDiscoverer) DiscoverLanes(_ context.Context, _ int) ([]pricedLongHaulLane, error) {
	return f.lanes, f.err
}

type fakeLegExecutor struct {
	legs   []directedLegCommand
	result directedLegResult
	err    error
}

func (f *fakeLegExecutor) RunLeg(_ context.Context, cmd directedLegCommand) (directedLegResult, error) {
	f.legs = append(f.legs, cmd)
	return f.result, f.err
}

func (f *fakeLegExecutor) legGoods() []string {
	out := make([]string, 0, len(f.legs))
	for _, l := range f.legs {
		out = append(out, l.Good)
	}
	return out
}

type fakeRepositioner struct{ moves []string }

func (f *fakeRepositioner) RepositionToWaypoint(_ context.Context, _, destination string, _ int) error {
	f.moves = append(f.moves, destination)
	return nil
}

type fakeTreasuryReader struct {
	balance int64
	err     error
}

func (f *fakeTreasuryReader) LiveTreasury(_ context.Context) (int64, error) {
	return f.balance, f.err
}

// ---- builders --------------------------------------------------------------

func workerLane(good, srcWp, sinkWp string, ask, optimalUnits int, perHour float64) pricedLongHaulLane {
	return pricedLongHaulLane{
		longHaulCandidate: longHaulCandidate{
			Lane: trading.ArbitrageLane{
				Good: good, SourceWaypoint: srcWp, DestWaypoint: sinkWp,
				SourceAsk: ask, DestBid: ask + 5000, SpreadPerUnit: 5000, VolumeCap: 1000,
			},
			GateHops: 3,
		},
		OptimalUnits:           optimalUnits,
		RealizedCreditsPerHour: perHour,
	}
}

func longHaulHullHolding(t *testing.T, symbol, good string, units, cargoCap int) *navigation.Ship {
	t.Helper()
	loc, err := shared.NewWaypoint("X1-XD86-A1", 1, 1)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(400, 400)
	require.NoError(t, err)
	cargo, err := shared.NewCargo(cargoCap, units, []*shared.CargoItem{{Symbol: good, Units: units}})
	require.NoError(t, err)
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(1), loc, fuel, 400, cargoCap, cargo, 30, "FRAME_FREIGHTER", "HAULER", nil, navigation.NavStatusInOrbit)
	require.NoError(t, err)
	ship.SetDedicatedFleet(longHaulFleet)
	return ship
}

func newWorker(hull *navigation.Ship, lanes []pricedLongHaulLane, legs *fakeLegExecutor, repo *fakeRepositioner, balance int64) *RunLongHaulArbHandler {
	return NewRunLongHaulArbHandler(
		&fakeShipLoader{ship: hull},
		&fakeLaneDiscoverer{lanes: lanes},
		legs,
		repo,
		&fakeTreasuryReader{balance: balance},
	)
}

func workerCmd() *RunLongHaulArbCommand {
	return &RunLongHaulArbCommand{ShipSymbol: "LH-1", PlayerID: 1, ContainerID: "lh-c-1", PerHaulCap: 1_000_000}
}

// ---- tests -----------------------------------------------------------------

// FRESH EPISODE + BACKHAUL (design §4): the worker discovers, sizes the top lane, repositions
// to its SOURCE (so the reused executor's location-guarded buy fires), runs the OUT leg, then
// re-ranks for a lane SOURCED near the sink and runs the opportunistic BACKHAUL.
func TestLongHaulWorker_FreshEpisode_OutLegThenBackhaul(t *testing.T) {
	hull := longHaulHull(t, "LH-1", longHaulFleet, "HAULER", 60) // empty
	legs := &fakeLegExecutor{}
	repo := &fakeRepositioner{}
	lanes := []pricedLongHaulLane{
		workerLane("LASER_RIFLES", "X1-ZC66-AX1B", "X1-XD86-A1", 5000, 40, 200000), // out (top)
		workerLane("HOLOGRAPHICS", "X1-XD86-B2", "X1-HOME-A1", 6000, 30, 100000),   // backhaul (sourced in the sink system)
	}
	h := newWorker(hull, lanes, legs, repo, 50_000_000)

	didWork, err := h.runEpisode(tradeCtx(&tradeCaptureLogger{}), workerCmd())

	require.NoError(t, err)
	require.True(t, didWork)
	require.Equal(t, []string{"X1-ZC66-AX1B", "X1-XD86-B2"}, repo.moves, "reposition to the OUT source, then the BACKHAUL source")
	require.Equal(t, []string{"LASER_RIFLES", "HOLOGRAPHICS"}, legs.legGoods(), "out leg then opportunistic backhaul")
	require.Equal(t, 40, legs.legs[0].Units)
	require.Equal(t, int64(1_000_000), legs.legs[0].PerHaulCap, "the per-haul cap is threaded to the reused executor")
}

// No lane clears the floor+envelope this scan → the worker idles (no leg, no move, backs off).
func TestLongHaulWorker_NoLane_Idles(t *testing.T) {
	hull := longHaulHull(t, "LH-1", longHaulFleet, "HAULER", 60)
	legs := &fakeLegExecutor{}
	repo := &fakeRepositioner{}
	h := newWorker(hull, nil, legs, repo, 50_000_000)

	didWork, err := h.runEpisode(tradeCtx(&tradeCaptureLogger{}), workerCmd())

	require.NoError(t, err)
	require.False(t, didWork)
	require.Empty(t, legs.legs)
	require.Empty(t, repo.moves)
}

// MID-TRIP RE-DERIVE (stateless recovery): a hull holding cargo from an interrupted episode
// RESUMES selling it (a leg for that good) — it never re-derives a fresh buy on a laden hull,
// and doesn't reposition (the reused executor's resume travels to the sink itself).
func TestLongHaulWorker_HoldingCargo_ResumesSellNeverRebuys(t *testing.T) {
	hull := longHaulHullHolding(t, "LH-1", "LASER_RIFLES", 40, 60)
	legs := &fakeLegExecutor{}
	repo := &fakeRepositioner{}
	lanes := []pricedLongHaulLane{
		workerLane("QUANTUM_DRIVES", "X1-A-A1", "X1-B-A1", 5000, 40, 300000), // a richer fresh lane it must IGNORE
		workerLane("LASER_RIFLES", "X1-ZC66-AX1B", "X1-XD86-A1", 5000, 40, 200000),
	}
	h := newWorker(hull, lanes, legs, repo, 50_000_000)

	didWork, err := h.runEpisode(tradeCtx(&tradeCaptureLogger{}), workerCmd())

	require.NoError(t, err)
	require.True(t, didWork)
	require.Equal(t, []string{"LASER_RIFLES"}, legs.legGoods(), "resumes the HELD good, never the richer fresh lane")
	require.Equal(t, 40, legs.legs[0].Units, "sells the whole held tranche")
	require.Empty(t, repo.moves, "resume does not reposition to a fresh source")
}

// Fail-closed: an unreadable treasury sizes every lane to zero, so the worker trades nothing
// this episode (RULINGS #4) — no leg, no spend.
func TestLongHaulWorker_UnreadableTreasury_TradesNothing(t *testing.T) {
	hull := longHaulHull(t, "LH-1", longHaulFleet, "HAULER", 60)
	legs := &fakeLegExecutor{}
	repo := &fakeRepositioner{}
	h := NewRunLongHaulArbHandler(
		&fakeShipLoader{ship: hull},
		&fakeLaneDiscoverer{lanes: []pricedLongHaulLane{workerLane("LASER_RIFLES", "X1-ZC66-AX1B", "X1-XD86-A1", 5000, 40, 200000)}},
		legs,
		repo,
		&fakeTreasuryReader{err: errors.New("token expired")},
	)

	didWork, err := h.runEpisode(tradeCtx(&tradeCaptureLogger{}), workerCmd())

	require.NoError(t, err)
	require.False(t, didWork)
	require.Empty(t, legs.legs, "no leg on an unreadable treasury")
}
