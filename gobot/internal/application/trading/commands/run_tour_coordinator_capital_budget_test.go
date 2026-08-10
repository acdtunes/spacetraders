package commands

import (
	"context"
	"errors"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// The TRADE half of the per-operation capital budget. Construction had no proportional cap at all
// and consumed everything above its 150k floor; the budget makes the courtesy mutual. Trade's
// cumulative cap IS its share of deployable capital, and GRACEFUL DEGRADATION hands trade the
// WHOLE pool whenever the construction drain is not running, so no capital idles when the gate is
// stopped.

type tourFakeCapitalWorkSensor struct {
	constructionWork bool
	err              error
	calls            int
}

func (s *tourFakeCapitalWorkSensor) TradeHasWork(_ context.Context, _ int) (bool, error) {
	return true, nil
}

func (s *tourFakeCapitalWorkSensor) ConstructionHasWork(_ context.Context, _ int) (bool, error) {
	s.calls++
	if s.err != nil {
		return false, s.err
	}
	return s.constructionWork, nil
}

// The budget can never exceed the deployable pool above the run's own reserve (RULINGS #4). The
// construction-idle rows are the LIVE ACCEPTANCE CASE: with the gate pipeline stopped the split
// must hand trade 100% rather than 40% with the other 60% idling.
func TestTradeCapitalBudget(t *testing.T) {
	cases := []struct {
		name     string
		sensor   *tourFakeCapitalWorkSensor
		reserve  int64
		treasury int64
		want     int64
	}{
		{
			// LIVE ACCEPTANCE CASE. deployable 140,000; construction idle -> trade holds
			// all of it. Anything less would be capital left idle by a stopped gate.
			name:    "construction idle hands trade the whole deployable pool",
			sensor:  &tourFakeCapitalWorkSensor{constructionWork: false},
			reserve: 300_000, treasury: 440_000,
			want: 140_000,
		},
		{
			// Same treasury with the gate running: trade takes its 40% of 140,000 = 56,000.
			// Construction's 84,000 is now genuinely reserved instead of being a floor it can
			// spend straight through.
			name:    "construction live clamps the cap to trade's share",
			sensor:  &tourFakeCapitalWorkSensor{constructionWork: true},
			reserve: 300_000, treasury: 440_000,
			want: 56_000,
		},
		{
			// The measured era-5 dip, live numbers: treasury 343,093 against a 300,000
			// reserve. A flat 25% of treasury would have been 85,773 — a cap the run's OWN
			// reserve bounces mid-tour (343,093 − 85,773 = 257,320, below 300,000), which is
			// exactly the observed "shrinking buy ... to respect working-capital floor" log.
			// Deriving the cap from the run's reserve makes it self-consistent.
			name:    "the cap can never exceed what the run's own reserve permits",
			sensor:  &tourFakeCapitalWorkSensor{constructionWork: false},
			reserve: 300_000, treasury: 343_093,
			want: 43_093,
		},
		{
			name:    "the same dip with the gate running splits 40/60",
			sensor:  &tourFakeCapitalWorkSensor{constructionWork: true},
			reserve: 300_000, treasury: 343_093,
			want: 17_237, // round(0.4 x 43,093)
		},
		{
			// FAIL CONSERVATIVE: a blind sensor read must not be read as "construction is
			// idle" and hand trade the whole pool.
			name:    "an unreadable sensor takes only trade's share",
			sensor:  &tourFakeCapitalWorkSensor{err: errors.New("container registry unreadable")},
			reserve: 300_000, treasury: 440_000,
			want: 56_000,
		},
		{
			// A treasury at or under the run's reserve deploys nothing. The cap collapses to
			// 0 and the planner reports infeasible rather than planning a tour the floor
			// would refuse buy by buy.
			name:    "a treasury at the reserve deploys nothing",
			sensor:  &tourFakeCapitalWorkSensor{constructionWork: true},
			reserve: 300_000, treasury: 300_000,
			want: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &RunTourCoordinatorHandler{workSensor: tc.sensor}
			logger := &laneLogCapturingLogger{}
			ctx := common.WithLogger(context.Background(), logger)

			got := h.tradeCapitalBudget(ctx, 1, tc.reserve, tc.treasury)
			if got != tc.want {
				t.Fatalf("tradeCapitalBudget(reserve=%d, treasury=%d) = %d, want %d — trade's share is %d%% of deployable capital",
					tc.reserve, tc.treasury, got, tc.want, common.TradeCapitalSharePct)
			}
			if deployable := common.CapitalDeployable(tc.treasury, tc.reserve); got > deployable {
				t.Fatalf("the budget resolved %d against only %d deployable above the reserve floor — a money guard was weakened (RULINGS #4)", got, deployable)
			}
			if tc.sensor.calls == 0 {
				t.Fatal("the construction-side hasWork sensor was never consulted — the budget is not in the resolution path at all")
			}
		})
	}
}

// A budget that cannot sense the other side must RESERVE for it. An unwired sensor is a blind
// read, not an observation of an idle gate, so it takes only trade's proportional share — the
// same answer a sensor error gets (RULINGS #4: never fail open on a money guard).
func TestTradeCapitalBudget_UnwiredSensorTakesTradesShare(t *testing.T) {
	h := &RunTourCoordinatorHandler{}
	ctx := common.WithLogger(context.Background(), &laneLogCapturingLogger{})
	if got := h.tradeCapitalBudget(ctx, 1, 300_000, 343_093); got != 17_237 {
		t.Fatalf("an unwired sensor must take only trade's %d%% share (17,237 of the 43,093 deployable), got %d", common.TradeCapitalSharePct, got)
	}
}

// End-to-end through the resolver the continuous tour loop actually calls: the budget must be
// what defaultMaxSpend RETURNS, not merely computed and dropped.
func TestDefaultMaxSpend_IsTheCapitalBudget(t *testing.T) {
	cases := []struct {
		name             string
		constructionWork bool
		want             int64
	}{
		// Deployable over the 300,000 reserve = 140,000.
		{name: "gate stopped hands trade the whole pool", constructionWork: false, want: 140_000},
		{name: "gate running clamps to trade's 40% share", constructionWork: true, want: 56_000},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &RunTourCoordinatorHandler{
				apiClient:  &tourSeqAPIClient{balances: []int{440_000}},
				workSensor: &tourFakeCapitalWorkSensor{constructionWork: tc.constructionWork},
			}
			ctx := common.WithLogger(auth.WithPlayerToken(context.Background(), "TOUR-FTQGP"), &laneLogCapturingLogger{})

			got, unreadable := h.defaultMaxSpend(ctx, 1, 300_000)
			if unreadable {
				t.Fatal("a readable treasury must not report unreadable")
			}
			if got != tc.want {
				t.Fatalf("defaultMaxSpend = %d, want %d", got, tc.want)
			}
		})
	}
}

// An unreadable treasury still fails CLOSED at the resolver, budget or no budget: the caller
// pauses and retries rather than spending on a stale or unlimited cap (RULINGS #4). There must be
// no path that returns a positive cap on a blind read.
func TestDefaultMaxSpend_UnreadableTreasuryStillFailsClosed(t *testing.T) {
	h := &RunTourCoordinatorHandler{
		apiClient:  &tourErrAPIClient{},
		workSensor: &tourFakeCapitalWorkSensor{constructionWork: false},
	}
	ctx := common.WithLogger(auth.WithPlayerToken(context.Background(), "TOUR-FTQGP"), &laneLogCapturingLogger{})

	got, unreadable := h.defaultMaxSpend(ctx, 1, 300_000)
	if !unreadable || got != 0 {
		t.Fatalf("an unreadable treasury must yield (0, unreadable=true), got (%d, %v)", got, unreadable)
	}
}
