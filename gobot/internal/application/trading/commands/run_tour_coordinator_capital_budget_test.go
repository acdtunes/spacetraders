package commands

import (
	"context"
	"errors"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// sp-ftqgp: the TRADE half of the per-operation capital budget. Trade was already the polite side
// — it caps itself at 25% of live treasury — while construction had no proportional cap at all and
// consumed everything above its 150k floor. The budget makes the courtesy mutual: trade's
// cumulative cap is clamped to its share of deployable capital, and GRACEFUL DEGRADATION hands
// trade the WHOLE pool whenever the construction drain is not running, so no capital idles when
// the gate is stopped.

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

// The budget only ever LOWERS the cap (RULINGS #4), and it never lowers it when trade holds the
// whole deployable pool. The construction-idle rows are the LIVE ACCEPTANCE CASE for this bead:
// the gate pipeline is stopped in production, so the split must hand trade 100% rather than 60%
// with the other 40% idling.
func TestApplyCapitalBudget(t *testing.T) {
	cases := []struct {
		name     string
		sensor   *tourFakeCapitalWorkSensor
		reserve  int64
		treasury int64
		spendCap int64
		want     int64
	}{
		{
			// LIVE ACCEPTANCE CASE. deployable 140,000; construction idle -> trade holds all
			// of it, which is above the 110,000 (25%) cap, so the cap is untouched. Anything
			// less than the full cap here would be capital left idle by a stopped gate.
			name:    "construction idle leaves the whole dynamic cap intact",
			sensor:  &tourFakeCapitalWorkSensor{constructionWork: false},
			reserve: 300_000, treasury: 440_000, spendCap: 110_000,
			want: 110_000,
		},
		{
			// Same treasury with the gate running: trade takes its 60% of 140,000 = 84,000.
			// Construction's 56,000 is now genuinely reserved instead of being a floor it can
			// spend straight through.
			name:    "construction live clamps the cap to trade's share",
			sensor:  &tourFakeCapitalWorkSensor{constructionWork: true},
			reserve: 300_000, treasury: 440_000, spendCap: 110_000,
			want: 84_000,
		},
		{
			// The measured era-5 dip, live numbers: treasury 343,093 against a 300,000
			// reserve. The 25% cap of 85,773 was a cap the run's OWN reserve would bounce
			// mid-tour (343,093 − 85,773 = 257,320, below 300,000) — which is exactly the
			// observed "shrinking buy ... to respect working-capital floor" log. Deriving the
			// budget from the run's reserve makes the cumulative cap self-consistent.
			name:    "the cap can no longer exceed what the run's own reserve permits",
			sensor:  &tourFakeCapitalWorkSensor{constructionWork: false},
			reserve: 300_000, treasury: 343_093, spendCap: 85_773,
			want: 43_093,
		},
		{
			name:    "the same dip with the gate running splits 60/40",
			sensor:  &tourFakeCapitalWorkSensor{constructionWork: true},
			reserve: 300_000, treasury: 343_093, spendCap: 85_773,
			want: 25_856, // round(0.6 x 43,093)
		},
		{
			// FAIL CONSERVATIVE: a blind sensor read must not be read as "construction is
			// idle" and hand trade the whole pool.
			name:    "an unreadable sensor takes only trade's share",
			sensor:  &tourFakeCapitalWorkSensor{err: errors.New("container registry unreadable")},
			reserve: 300_000, treasury: 440_000, spendCap: 110_000,
			want: 84_000,
		},
		{
			// A treasury at or under the run's reserve deploys nothing. The cap collapses to
			// 0 and the planner reports infeasible rather than planning a tour the floor
			// would refuse buy by buy.
			name:    "a treasury at the reserve deploys nothing",
			sensor:  &tourFakeCapitalWorkSensor{constructionWork: true},
			reserve: 300_000, treasury: 300_000, spendCap: 75_000,
			want: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &RunTourCoordinatorHandler{workSensor: tc.sensor}
			logger := &laneLogCapturingLogger{}
			ctx := common.WithLogger(context.Background(), logger)

			got := h.applyCapitalBudget(ctx, 1, tc.reserve, tc.treasury, tc.spendCap)
			if got != tc.want {
				t.Fatalf("applyCapitalBudget(reserve=%d, treasury=%d, cap=%d) = %d, want %d — trade's share is %d%% of deployable capital (sp-ftqgp)",
					tc.reserve, tc.treasury, tc.spendCap, got, tc.want, common.TradeCapitalSharePct)
			}
			if got > tc.spendCap {
				t.Fatalf("the budget RAISED the cap from %d to %d — a money guard was weakened (RULINGS #4)", tc.spendCap, got)
			}
			if tc.sensor.calls == 0 {
				t.Fatal("the construction-side hasWork sensor was never consulted — the budget is not in the resolution path at all")
			}
		})
	}
}

// With no sensor wired the dynamic cap is exactly what it was before this bead. This is the
// contract every existing tour test relies on (none wires a sensor).
func TestApplyCapitalBudget_UnwiredSensorKeepsTheDynamicCap(t *testing.T) {
	h := &RunTourCoordinatorHandler{}
	ctx := common.WithLogger(context.Background(), &laneLogCapturingLogger{})
	// Deployable is only 43,093 here, so a wired sensor would clamp; unwired must not.
	if got := h.applyCapitalBudget(ctx, 1, 300_000, 343_093, 85_773); got != 85_773 {
		t.Fatalf("an unwired sensor must leave the 25%% dynamic cap untouched, got %d want 85773", got)
	}
}

// End-to-end through the resolver the continuous tour loop actually calls: the budget must be
// applied to the value defaultMaxSpend RETURNS, not merely computed and dropped.
func TestDefaultMaxSpend_AppliesTheCapitalBudget(t *testing.T) {
	cases := []struct {
		name             string
		constructionWork bool
		want             int64
	}{
		// 25% of 440,000 = 110,000; deployable over the 300,000 reserve = 140,000.
		{name: "gate stopped leaves the 25% cap intact", constructionWork: false, want: 110_000},
		{name: "gate running clamps to trade's 60% share", constructionWork: true, want: 84_000},
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
// pauses and retries rather than spending on a stale or unlimited cap (RULINGS #4). The budget
// must not have introduced a path that returns a positive cap on a blind read.
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
