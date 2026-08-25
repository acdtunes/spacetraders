package commands

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
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
