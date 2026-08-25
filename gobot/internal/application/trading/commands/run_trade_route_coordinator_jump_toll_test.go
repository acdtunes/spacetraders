package commands

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// --- sp-3x143: the cross-system travel path MEASURES every hop it flies ---

// tollAdvancingClock advances on Sleep, which is what makes the measurement bracket
// deterministic: the wait budget the coordinator sleeps IS the elapsed wall clock the
// recorder should see. (travelFakeClock returns real time.Now() and cannot show this.)
type tollAdvancingClock struct{ now time.Time }

func (c *tollAdvancingClock) Now() time.Time { return c.now }
func (c *tollAdvancingClock) Sleep(d time.Duration) {
	c.now = c.now.Add(d)
}

type tollRecordingRepo struct {
	samples []trading.JumpTollSample
	ships   []string
	from    []string
	to      []string
	err     error
}

func (r *tollRecordingRepo) RecordJumpToll(
	_ context.Context, _ int, shipSymbol, fromSystem, toSystem string, sample trading.JumpTollSample,
) error {
	r.samples = append(r.samples, sample)
	r.ships = append(r.ships, shipSymbol)
	r.from = append(r.from, fromSystem)
	r.to = append(r.to, toSystem)
	return r.err
}

func (r *tollRecordingRepo) RecentJumpTolls(
	_ context.Context, _ int, _ time.Time, _ int,
) ([]trading.JumpTollSample, error) {
	return nil, nil
}

// Every hop of a multi-hop path is measured, and what is measured is the ECONOMIC cost —
// wall clock from dispatching the jump to the hull being action-ready — not the cooldown the
// API reported. With a 900s cooldown the coordinator sleeps its 1.25x budget, so the sample
// must read 1125s while carrying the raw 900 alongside it as the hop's distance signal.
func TestFlyJumpPath_RecordsTheMeasuredWaitForEveryHop(t *testing.T) {
	logger := &tradeCaptureLogger{}
	clock := &tollAdvancingClock{now: time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)}
	repo := &tollRecordingRepo{}
	mediator := &travelMediator{jumpResp: &navCmd.JumpShipResponse{Success: true, CooldownSeconds: 900}}

	h := &RunTradeRouteCoordinatorHandler{mediator: mediator, clock: clock}
	h.SetJumpTollRecorder(repo)

	ship := newTravelShipAtGate(t, "HAULER-1", "X1-AAA-GATE")
	require.NoError(t, h.flyJumpPath(tradeCtx(logger), ship, []string{"X1-AAA", "X1-BBB", "X1-CCC"}, "X1-CCC", 1))

	require.Len(t, repo.samples, 2, "one sample per gate hop")
	for i, s := range repo.samples {
		require.Equal(t, 1125, s.WaitSeconds, "hop %d must record the wall clock the hull actually lost", i)
		require.Equal(t, 900, s.CooldownSeconds, "hop %d must carry the API cooldown as the distance signal", i)
	}
	require.Equal(t, []string{"HAULER-1", "HAULER-1"}, repo.ships)
	require.Equal(t, []string{"X1-AAA", "X1-BBB"}, repo.from)
	require.Equal(t, []string{"X1-BBB", "X1-CCC"}, repo.to)
	require.False(t, repo.samples[0].RecordedAt.IsZero(), "a sample with no timestamp cannot be windowed or decayed")
	require.True(t, repo.samples[1].RecordedAt.After(repo.samples[0].RecordedAt),
		"consecutive hops must be stamped in the order they were flown")
}

// The recorder is an optional port: unwired (every existing caller and test) the travel path
// is byte-for-byte what it was, and a recorder that ERRORS can never fail a leg. A hull has
// already moved by the time the sample is written — refusing the leg over a telemetry write
// would strand it.
func TestFlyJumpPath_TollRecordingIsOptionalAndNeverFailsALeg(t *testing.T) {
	logger := &tradeCaptureLogger{}
	mediator := &travelMediator{jumpResp: &navCmd.JumpShipResponse{Success: true, CooldownSeconds: 60}}
	ship := newTravelShipAtGate(t, "HAULER-1", "X1-AAA-GATE")

	unwired := &RunTradeRouteCoordinatorHandler{mediator: mediator, clock: &tollAdvancingClock{now: time.Now()}}
	require.NoError(t, unwired.flyJumpPath(tradeCtx(logger), ship, []string{"X1-AAA", "X1-BBB"}, "X1-BBB", 1))

	failing := &tollRecordingRepo{err: errors.New("sample table unwritable")}
	wired := &RunTradeRouteCoordinatorHandler{mediator: mediator, clock: &tollAdvancingClock{now: time.Now()}}
	wired.SetJumpTollRecorder(failing)
	require.NoError(t, wired.flyJumpPath(tradeCtx(logger), ship, []string{"X1-AAA", "X1-BBB"}, "X1-BBB", 1),
		"a telemetry write failure must never fail a leg the hull has already flown")
	require.Len(t, failing.samples, 1)
}

// A jump that FAILED produced no hop, so it must produce no sample: the estimator's
// population is hops actually flown, and a crashed leg's elapsed time is not a hop's cost.
func TestFlyJumpPath_RecordsNothingWhenTheJumpFails(t *testing.T) {
	logger := &tradeCaptureLogger{}
	repo := &tollRecordingRepo{}
	mediator := &travelMediator{jumpErr: errors.New("gate under construction")}

	h := &RunTradeRouteCoordinatorHandler{mediator: mediator, clock: &tollAdvancingClock{now: time.Now()}}
	h.SetJumpTollRecorder(repo)

	ship := newTravelShipAtGate(t, "HAULER-1", "X1-AAA-GATE")
	require.Error(t, h.flyJumpPath(tradeCtx(logger), ship, []string{"X1-AAA", "X1-BBB"}, "X1-BBB", 1))
	require.Empty(t, repo.samples)
}

// --- sp-80mha: the lane RATE ranker prices its gate hops off the SAME measured toll ---

// fixedTollReader is the estimator stub: a fleet whose measured per-hop toll is a known
// constant, or (0) one that has not measured enough hops to have an opinion.
type fixedTollReader struct{ seconds int }

func (r fixedTollReader) PerHopTollSeconds(context.Context, int) int { return r.seconds }

// The circuit time model charges the MEASURED per-hop toll when the estimator speaks, and the
// fitted crossSystemHopSeconds when it is silent — so the cold case is byte-identical to the
// frozen constant and the warm case prices what jumping actually costs. 352 sits BELOW the
// measured 1028, so the live default moves the crossing charge UP.
func TestEstimatedCircuitSeconds_ChargesTheMeasuredTollAndFallsBackToTheFittedHop(t *testing.T) {
	require.Equal(t, 4000.0+2.0*1028.0, estimatedCircuitSeconds(true, 1028),
		"a crossing circuit must pay the round trip at the MEASURED toll: 4000 + 2x1028 = 6056s")
	require.Equal(t, 4000.0+2.0*352.0, estimatedCircuitSeconds(true, 0),
		"a silent estimator must leave the crossing charge at the fitted 352/hop: 4704s, today's behavior exactly")
	require.Equal(t, 4000.0, estimatedCircuitSeconds(false, 1028),
		"a same-system circuit crosses nothing and must never pay a toll, measured or fitted")
	require.Greater(t, estimatedCircuitSeconds(true, 1028), estimatedCircuitSeconds(true, 0),
		"the fitted 352 under-prices the measured 1028 level, so the live default must RAISE the charge")
}

// The direction pin at the ranking surface: a cross lane whose value lead clears the fitted
// surcharge but not the measured one must WIN at the silent fallback and LOSE once the
// estimator speaks — with the exact rates asserted on both sides.
func TestLaneRateRanking_MeasuredTollRepricesTheCrossingCharge(t *testing.T) {
	cross := trading.ArbitrageLane{Good: "CROSS", SourceWaypoint: "X1-HOME-1", DestWaypoint: "X1-FAR-9", SpreadPerUnit: 1000, VolumeCap: 400, CappedSpread: 400000}
	home := trading.ArbitrageLane{Good: "HOME", SourceWaypoint: "X1-HOME-2", DestWaypoint: "X1-HOME-3", SpreadPerUnit: 800, VolumeCap: 400, CappedSpread: 320000}

	require.Equal(t, 400000.0/(4704.0/3600.0), laneCircuitRatePerHour(cross, 400, "", laneImpactModel{}, 0),
		"silent estimator: the cross lane rates at the fitted 4704s circuit")
	require.Equal(t, 400000.0/(6056.0/3600.0), laneCircuitRatePerHour(cross, 400, "", laneImpactModel{}, 1028),
		"measured 1028: the cross lane rates at the 6056s circuit")
	require.Equal(t, 320000.0/(4000.0/3600.0), laneCircuitRatePerHour(home, 400, "", laneImpactModel{}, 1028),
		"the home lane's baseline never moves with the toll")

	atFallback := rankLanesByCircuitRate([]trading.ArbitrageLane{cross, home}, 400, "", laneImpactModel{}, 0)
	require.Equal(t, "CROSS", atFallback[0].Good,
		"at the fitted 352/hop the cross lane's 306k/hr beats home's 288k/hr")
	atMeasured := rankLanesByCircuitRate([]trading.ArbitrageLane{cross, home}, 400, "", laneImpactModel{}, 1028)
	require.Equal(t, "HOME", atMeasured[0].Good,
		"at the measured 1028/hop the same cross lane earns 237.8k/hr and must lose to home's 288k/hr")
}

// End-to-end through the handler: a wired estimator reaches scanLanes' ranking, and an unwired
// one leaves selection exactly where the frozen constant put it.
func TestScanLanes_MeasuredTollFlipsSelectionAwayFromCrossing(t *testing.T) {
	newFixture := func() (*msMediator, *msMarketRepo) {
		return &msMediator{connections: map[string][]string{"X1-HOME": {"X1-NEAR"}}},
			&msMarketRepo{
				waypointsBySystem: map[string][]string{
					"X1-HOME": {"X1-HOME-A1", "X1-HOME-A2", "X1-HOME-B1"},
					"X1-NEAR": {"X1-NEAR-B2"},
				},
				goods: map[string]msGood{
					// GOOD_A: same-system, spread 500 (600-100), value 30,000 → 27,000/hr baseline.
					"X1-HOME-A1": {symbol: "GOOD_A", bid: 50, ask: 100, volume: 60, tradeType: market.TradeTypeExport},
					"X1-HOME-A2": {symbol: "GOOD_A", bid: 600, ask: 650, volume: 60, tradeType: market.TradeTypeImport},
					// GOOD_B: cross-system, spread 620, value 37,200 → 28,469/hr at the fitted
					// 4704s circuit (wins) but 22,114/hr at the measured 6056s one (loses).
					"X1-HOME-B1": {symbol: "GOOD_B", bid: 50, ask: 100, volume: 60, tradeType: market.TradeTypeExport},
					"X1-NEAR-B2": {symbol: "GOOD_B", bid: 720, ask: 770, volume: 60, tradeType: market.TradeTypeImport},
				},
			}
	}

	mediator, marketRepo := newFixture()
	silent := NewRunTradeRouteCoordinatorHandler(mediator, nil, marketRepo, nil, nil, nil)
	lanes, err := silent.scanLanes(context.Background(), "X1-HOME", 1, 0, "")
	require.NoError(t, err)
	require.Equal(t, "GOOD_B", lanes[0].Good,
		"unwired estimator: the cross lane must still win at the fitted surcharge — today's selection exactly")

	mediator, marketRepo = newFixture()
	measured := NewRunTradeRouteCoordinatorHandler(mediator, nil, marketRepo, nil, nil, nil)
	measured.SetJumpTollReader(fixedTollReader{seconds: 1028})
	lanes, err = measured.scanLanes(context.Background(), "X1-HOME", 1, 0, "")
	require.NoError(t, err)
	require.Equal(t, "GOOD_A", lanes[0].Good,
		"measured 1028/hop: the same cross lane no longer pays for its gates and the home lane must win")
	require.Equal(t, "GOOD_B", lanes[1].Good)
	require.Equal(t, 620, lanes[1].SpreadPerUnit,
		"ranking-only: the demoted lane's real economics survive unmutated")
}

// The resolver is fail-open at every layer: no reader, a silent reader, and a nonsensical
// negative reading all resolve to 0 — the "no measurement" value every consumer maps to its
// fitted fallback.
func TestTradeRoute_PerHopTollResolutionFailsOpen(t *testing.T) {
	unwired := &RunTradeRouteCoordinatorHandler{}
	require.Equal(t, 0, unwired.measuredPerHopTollSeconds(context.Background(), 1))

	wired := &RunTradeRouteCoordinatorHandler{}
	wired.SetJumpTollReader(fixedTollReader{seconds: 1028})
	require.Equal(t, 1028, wired.measuredPerHopTollSeconds(context.Background(), 1))

	negative := &RunTradeRouteCoordinatorHandler{}
	negative.SetJumpTollReader(fixedTollReader{seconds: -60})
	require.Equal(t, 0, negative.measuredPerHopTollSeconds(context.Background(), 1),
		"a crossing that gave time back would make every cross lane look free — clamp to no-opinion")
}

// The selection log carries the rate the ranker actually scored, so a measured-toll regime is
// readable in the captain's log exactly as the fitted one was.
func TestLaneSelectionOneLiner_CarriesTheMeasuredSurchargedRate(t *testing.T) {
	cross := trading.ArbitrageLane{Good: "DEEP", SourceWaypoint: "X1-AAA-1", DestWaypoint: "X1-BBB-2", SpreadPerUnit: 1700, VolumeCap: 480}

	got := laneSelectionOneLiner(cross, 480, "", laneImpactModel{}, 1028)
	require.Contains(t, got, fmt.Sprintf("rate=%.0f/hr", 816000.0/(6056.0/3600.0)),
		"the one-liner must carry the rate at the MEASURED circuit time, not the fitted one")
}
