package commands

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/application/system/gategraph"
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

type fakeRepositioner struct {
	moves []string
	// failWith maps a destination waypoint to the error its reposition returns; an absent/nil
	// entry succeeds. Lets a test model a source the strict resolver reports gate-UNROUTABLE
	// (the sp-e059j reachability fallback) or a transient non-unroutable blip. Zero value (nil
	// map) is byte-identical to the old always-succeed fake, so every prior test is unchanged.
	failWith map[string]error
}

func (f *fakeRepositioner) RepositionToWaypoint(_ context.Context, _, destination string, _ int) error {
	f.moves = append(f.moves, destination)
	if err, ok := f.failWith[destination]; ok {
		return err
	}
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

// REACHABILITY FALLBACK (sp-e059j): the engine deliberately ranks FAR multi-hop lanes, some of
// which are structurally unreachable for this hull's supply. When the top-$/hr lane's SOURCE is
// gate-UNROUTABLE, the episode must SKIP it and run the next viable lane — never error-return the
// unroutable verdict (the old single-pick did, re-picking the same deterministic top lane every
// cycle and looping forever with zero captured value). The reposition error carries a wrapped
// gategraph.ErrUnroutable, exactly as the strict resolver produces it.
func TestLongHaulWorker_UnroutableTopLane_SkipsToNextReachableLane(t *testing.T) {
	hull := longHaulHull(t, "LH-1", longHaulFleet, "HAULER", 60) // empty
	legs := &fakeLegExecutor{}
	repo := &fakeRepositioner{failWith: map[string]error{
		"X1-FARSRC-A1": fmt.Errorf("reposition of LH-1 to X1-FARSRC-A1 failed: %w from X1-RJ40 to X1-FARSRC within 25 jumps", gategraph.ErrUnroutable),
	}}
	lanes := []pricedLongHaulLane{
		workerLane("EXOTIC_MATTER", "X1-FARSRC-A1", "X1-SINK1-A1", 5000, 40, 300000), // top $/hr, UNROUTABLE source
		workerLane("PLATINUM", "X1-NEARSRC-A1", "X1-SINK2-A1", 5000, 40, 200000),     // reachable second
	}
	h := newWorker(hull, lanes, legs, repo, 50_000_000)

	didWork, err := h.runEpisode(tradeCtx(&tradeCaptureLogger{}), workerCmd())

	require.NoError(t, err, "an unroutable top lane must NOT error-return — that loops the same pick forever")
	require.True(t, didWork)
	require.Equal(t, []string{"X1-FARSRC-A1", "X1-NEARSRC-A1"}, repo.moves, "attempt the top source, skip on unroutable, then reach the second")
	require.Equal(t, []string{"PLATINUM"}, legs.legGoods(), "runs the FIRST REACHABLE lane, never the unroutable top pick")
}

// FALLBACK BOUNDARY (sp-e059j): ONLY an unroutable verdict triggers skip-to-next. A NON-unroutable
// reposition failure (a transient API blip) must still FAIL the episode so the next cycle retries
// — never silently swallowed (which would hide a real fault and could skip a genuinely reachable
// top lane on a momentary error). It stops at the failing lane; it does not fall through.
func TestLongHaulWorker_NonUnroutableRepositionError_FailsEpisodeWithoutFallingThrough(t *testing.T) {
	hull := longHaulHull(t, "LH-1", longHaulFleet, "HAULER", 60)
	legs := &fakeLegExecutor{}
	repo := &fakeRepositioner{failWith: map[string]error{
		"X1-FARSRC-A1": errors.New("API error (status 502): bad gateway"), // transient, NOT unroutable
	}}
	lanes := []pricedLongHaulLane{
		workerLane("EXOTIC_MATTER", "X1-FARSRC-A1", "X1-SINK1-A1", 5000, 40, 300000),
		workerLane("PLATINUM", "X1-NEARSRC-A1", "X1-SINK2-A1", 5000, 40, 200000),
	}
	h := newWorker(hull, lanes, legs, repo, 50_000_000)

	didWork, err := h.runEpisode(tradeCtx(&tradeCaptureLogger{}), workerCmd())

	require.Error(t, err, "a non-unroutable reposition error must propagate (retry next cycle), not be skipped")
	require.False(t, didWork)
	require.Empty(t, legs.legGoods(), "no leg runs when the top reposition fails transiently")
	require.Equal(t, []string{"X1-FARSRC-A1"}, repo.moves, "stops at the transient failure — must NOT fall through to the next lane")
}

// sp-0o9ub THE HEADLINE (extends sp-e059j): a long-haul worker whose top-$/hr lane's SOURCE is 7
// gate-hops away must REACH it and run the OUT leg — now via the CHEAP "plan over stored adjacency,
// verify only the chosen path" resolver (the latency fix) rather than the strict whole-frontier
// per-edge probe. Wired through the REAL long-haul reposition adapter (longHaulReposition) over the
// shared travel machinery, so this proves the resolver switch is applied end-to-end at the isolated
// large bound. The gate graph makes the default-cap strict Path, the STRICT PathWithinJumps, AND the
// RELAXED RepositionPath all error, so a run OUT leg can ONLY mean the stored-then-verify resolver
// reached the far source.
func TestLongHaulWorker_FarMultiHopSource_ReachesViaStoredThenVerify_RunsOutLeg(t *testing.T) {
	flying := newTravelShipAtGate(t, "LH-1", "X1-A-GATE") // the flying hull starts on the source-region gate
	onSource := newTravelShipAt(t, "LH-1", "X1-H-SRC")    // 7 hops later it lands ON the lane source
	mediator := &travelMediator{jumpResp: &navCmd.JumpShipResponse{Success: true, CooldownSeconds: 60}}
	trHandler := NewRunTradeRouteCoordinatorHandler(mediator, &travelShipRepo{ship: flying, reloadShip: onSource}, nil, nil, &travelFakeClock{}, nil)
	fake := &fakeGateGraph{
		storedThenVerifyResult: []string{"X1-A", "X1-B", "X1-C", "X1-D", "X1-E", "X1-F", "X1-G", "X1-H"}, // 7 jumps via the cheap stored-then-verify resolver
		pathErr:                errors.New("no jump-gate route from X1-A to X1-H within 5 jumps"),        // the before 5-cap failure (strict Path)
		pathWithinErr:          errors.New("the long-haul reposition must not use the STRICT fetch-through resolver (sp-0o9ub)"),
		repositionPathErr:      errors.New("a heavy long-haul reposition must not use the RELAXED resolver"),
	}
	trHandler.SetGateGraph(fake)

	hull := longHaulHull(t, "LH-1", longHaulFleet, "HAULER", 60) // empty, for the worker's hold sizing
	legs := &fakeLegExecutor{}
	lanes := []pricedLongHaulLane{
		workerLane("EXOTIC_MATTER", "X1-H-SRC", "X1-SINK-A1", 5000, 40, 300000), // the far 7-hop source
	}
	worker := NewRunLongHaulArbHandler(
		&fakeShipLoader{ship: hull},
		&fakeLaneDiscoverer{lanes: lanes},
		legs,
		longHaulReposition{inner: trHandler}, // the REAL long-haul adapter — stored-then-verify resolver, large isolated bound
		&fakeTreasuryReader{balance: 50_000_000},
	)

	didWork, err := worker.runEpisode(tradeCtx(&tradeCaptureLogger{}), workerCmd())

	require.NoError(t, err, "the far source must be reachable via the stored-then-verify resolver — no 'within 5 jumps' failure")
	require.True(t, didWork)
	require.Equal(t, []string{"EXOTIC_MATTER"}, legs.legGoods(), "the OUT leg fires once the far source is reached")
	require.Equal(t, 7, len(mediator.jumps), "the hull flew the 7-hop stored-then-verify route to the far source")
	// The isolated large bound reached the NEW resolver, and neither the strict nor relaxed resolver was used.
	require.Equal(t, longHaulRepositionJumps, fake.storedThenVerifyBound, "the long-haul bound must drive the stored-then-verify resolver")
	require.Equal(t, 0, fake.pathWithinBound, "the strict PathWithinJumps must NOT be consulted for the long-haul reposition")
	require.Equal(t, 0, fake.repositionBound, "the RELAXED RepositionPath must NOT be consulted for the long-haul reposition")
}

// sp-e059j (backhaul symmetry): the opportunistic backhaul applies the SAME reachability fallback as
// the OUT leg — a gate-UNROUTABLE near-source is skipped for the next viable near-source rather than
// deadheading on the first. The OUT leg runs a reachable lane; then two lanes are sourced in the sink
// system, the top unroutable and the second reachable — the backhaul must run the second, not forfeit it.
func TestLongHaulWorker_Backhaul_SkipsUnroutableNearSource_RunsNextReachable(t *testing.T) {
	hull := longHaulHull(t, "LH-1", longHaulFleet, "HAULER", 60)
	legs := &fakeLegExecutor{}
	repo := &fakeRepositioner{failWith: map[string]error{
		"X1-SINK-B1": fmt.Errorf("reposition failed: %w", gategraph.ErrUnroutable), // unroutable backhaul top
	}}
	lanes := []pricedLongHaulLane{
		workerLane("OUT_GOOD", "X1-OUTSRC-A1", "X1-SINK-A1", 5000, 40, 300000), // OUT leg (reachable), sink system X1-SINK
		workerLane("BACK_TOP", "X1-SINK-B1", "X1-OTHER-A1", 5000, 40, 250000),  // backhaul top: sourced in X1-SINK, UNROUTABLE
		workerLane("BACK_2ND", "X1-SINK-C1", "X1-OTHER2-A1", 5000, 40, 200000), // backhaul 2nd: sourced in X1-SINK, reachable
	}
	h := newWorker(hull, lanes, legs, repo, 50_000_000)

	didWork, err := h.runEpisode(tradeCtx(&tradeCaptureLogger{}), workerCmd())

	require.NoError(t, err)
	require.True(t, didWork)
	require.Equal(t, []string{"OUT_GOOD", "BACK_2ND"}, legs.legGoods(), "OUT leg, then the backhaul skips the unroutable near-source and runs the reachable second")
}
