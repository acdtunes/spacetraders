package commands

// run_tour_coordinator_reposition_budget_test.go — a spend budget that resolves to ZERO is a
// SOLVENCY verdict, not a market one. The solver's own money guard is
// spend_cap = max(0, max_spend − working_capital_reserve); at spend_cap 0 it refuses every
// tour before the market is looked at. The reposition pre-flight deliberately clears the hull's
// hold (planAtCandidate), so the solver's held-cargo exemption can never apply there — which
// makes EVERY candidate's verdict predetermined. Those two facts together turned an empty
// deployable pool into "all N candidate(s) solver-infeasible" and then into an honest-looking
// "margins died" exit, parking hulls on a ground nobody had priced.

import (
	"context"

	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// THE DISCRIMINATOR. max_spend 0 carries two OPPOSITE meanings and the guard must tell them
// apart: with no live treasury source wired it is the "no explicit cumulative cap" sentinel
// (defaultMaxSpend's own documented contract — the per-buy floor is the guard), while with a
// source wired it is a number a live read produced, and a zero one means the deployable pool is
// empty. Only the second may suppress a pre-flight; reading the first as a refusal would park
// every un-capped run.
func TestBudgetDeniesEverySpend_TellsResolvedZeroFromNoCap(t *testing.T) {
	cases := []struct {
		name       string
		wireAPI    bool
		cmdSpend   int64
		maxSpend   int64
		reserve    int64
		wantDenied bool
	}{
		{
			name:    "no treasury source: 0 is the no-cap sentinel, never a refusal",
			wireAPI: false, cmdSpend: 0, maxSpend: 0, reserve: 150_000,
			wantDenied: false,
		},
		{
			name:    "live source, budget resolved to zero: every spend is refused",
			wireAPI: true, cmdSpend: 0, maxSpend: 0, reserve: 150_000,
			wantDenied: true,
		},
		{
			name:    "live source, budget resolved positive: nothing is predetermined",
			wireAPI: true, cmdSpend: 0, maxSpend: 33_841, reserve: 150_000,
			wantDenied: false,
		},
		{
			// The explicit --max-spend path keeps the solver's CASH contract: max_spend is a
			// ceiling and the reserve a keep-back, so a reserve at or above the ceiling zeroes
			// the cap just as surely.
			name:    "explicit max-spend at or below the reserve zeroes the cap",
			wireAPI: true, cmdSpend: 50_000, maxSpend: 50_000, reserve: 50_000,
			wantDenied: true,
		},
		{
			name:    "explicit max-spend above the reserve leaves real headroom",
			wireAPI: true, cmdSpend: 200_000, maxSpend: 200_000, reserve: 50_000,
			wantDenied: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := repositionFixture()
			var h *RunTourCoordinatorHandler
			if tc.wireAPI {
				h = newTourHandlerWithAPI(t, fx, &tourFakeRoutingClient{}, &tourFakeTelemetry{}, &tourSeqAPIClient{balances: []int{135_364}})
			} else {
				h = newTourHandler(t, fx, &tourFakeRoutingClient{}, &tourFakeTelemetry{})
			}
			cmd := &RunTourCoordinatorCommand{ShipSymbol: "BUDGET-DISC", PlayerID: 1, MaxSpend: tc.cmdSpend}

			got := h.budgetDeniesEverySpend(cmd, tourPlanBudget{maxHops: 6, maxSpend: tc.maxSpend, reserve: tc.reserve})

			if got != tc.wantDenied {
				t.Fatalf("budgetDeniesEverySpend = %v, want %v (api=%v cmd.MaxSpend=%d maxSpend=%d reserve=%d)",
					got, tc.wantDenied, tc.wireAPI, tc.cmdSpend, tc.maxSpend, tc.reserve)
			}
		})
	}
}

// THE LIVE SHAPE (TORWINDSTG-45): treasury 135,364 sits BELOW the 150,000 working-capital
// reserve, so the deployable pool is empty and trade's share of it is 0. The money guard is
// RIGHT to refuse — nothing here weakens it — but the reposition must not then spend a solver
// call per candidate to collect a verdict arithmetic already fixed, and must not record those
// verdicts as though the GROUNDS were poor. Zero pre-flights, and the refusal names its cause.
func TestReposition_ExhaustedDeployableCapital_PreflightsNothingAndNamesTheCause(t *testing.T) {
	fx := repositionFixture()
	planner := &tourFakeRoutingClient{infeasibleOnZeroSpend: true}
	h := newTourHandlerWithAPI(t, fx, planner, &tourFakeTelemetry{}, &tourSeqAPIClient{balances: []int{135_364}})
	logger := &tradeCaptureLogger{}
	ctx := common.WithLogger(context.Background(), logger)
	cmd := &RunTourCoordinatorCommand{ShipSymbol: "RPZ-BROKE", PlayerID: 1}
	episode := repositionEpisode{}

	repositioned, err := h.maybeReposition(ctx, cmd, &RunTourCoordinatorResponse{}, &episode, map[string]int{},
		tourPlanBudget{maxHops: 6, maxSpend: 0, reserve: 150_000})

	if err != nil {
		t.Fatalf("an empty deployable pool is a verdict, not an operational error: %v", err)
	}
	if repositioned {
		t.Fatalf("a hull that cannot buy anywhere must not burn a jump")
	}
	if planner.calls != 0 {
		t.Fatalf("the zeroed budget predetermines every candidate's verdict - want 0 pre-flight planner calls, got %d (positions=%v)", planner.calls, planner.positions)
	}
	if !logger.loggedContaining("Reposition", "spend budget") {
		t.Fatalf("the refusal must name the budget as its cause in the MESSAGE TEXT, not read as a market verdict; messages=%v", logger.messages)
	}
}

// The reposition rescue is UNTOUCHED whenever real headroom exists: the same fixture with a
// resolved budget still pre-flights its candidates. This is the falsifier for a guard that
// simply refused everything.
func TestReposition_ResolvedBudget_StillPreflightsCandidates(t *testing.T) {
	fx := repositionFixture()
	planner := &tourFakeRoutingClient{planFn: func(routing.TourShipState) *routing.TourPlan { return infeasibleTour() }}
	h := newTourHandlerWithAPI(t, fx, planner, &tourFakeTelemetry{}, &tourSeqAPIClient{balances: []int{4_000_000}})
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	cmd := &RunTourCoordinatorCommand{ShipSymbol: "RPZ-SOLVENT", PlayerID: 1}
	episode := repositionEpisode{}

	if _, err := h.maybeReposition(ctx, cmd, &RunTourCoordinatorResponse{}, &episode, map[string]int{},
		tourPlanBudget{maxHops: 6, maxSpend: 1_000_000, reserve: 150_000}); err != nil {
		t.Fatalf("maybeReposition: %v", err)
	}

	if planner.calls == 0 {
		t.Fatalf("a solvent budget must still price its candidates - got 0 planner calls")
	}
}

// THE OPERATOR-SET ZERO — the other side of the classification. An explicit --max-spend at or
// below its reserve is two constants a captain set: a ceiling and a keep-back that no amount of
// re-planning will move. The bounded wait that rescues a DYNAMIC budget (re-resolved from a live
// treasury each pass, so it can recover) would there only delay an honest exit behind three
// pointless backoffs. That path keeps today's behaviour, and the solver's reason already names the
// cause for the operator.
func TestTour_ExplicitMaxSpendBelowReserve_ExitsWithoutWaitingForARecoveryThatCannotCome(t *testing.T) {
	fx := repositionFixture()
	planner := &tourFakeRoutingClient{planFn: func(routing.TourShipState) *routing.TourPlan { return infeasibleTour() }}
	h := newTourHandlerWithAPI(t, fx, planner, &tourFakeTelemetry{}, &tourSeqAPIClient{balances: []int{4_000_000}})

	ctx := auth.WithPlayerToken(context.Background(), "TOUR-OPCAP")
	resp, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-OPCAP", PlayerID: 1, ContainerID: "ctr-opcap", Iterations: -1,
		MaxSpend: 50_000, WorkingCapitalReserve: 50_000,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("an operator-set zero cap must not error the run: %v", err)
	}
	r := tourResponse(t, resp)

	if r.ExitReason != tourExitStarvation {
		t.Fatalf("exit reason = %q, want %q - waiting cannot change two constants a captain set", r.ExitReason, tourExitStarvation)
	}
}

// THE EXIT CLASSIFICATION. A tour refused by the live-treasury budget flew zero trades for a
// SOLVENCY reason, and the loop already knows the two diagnoses demand opposite responses: a
// dead margin means leave, denied capital means wait. Routing a plan-time refusal into the
// starvation streak parks the hull on a treasury dip that moves every minute — and the
// destination it would rotate to is one it equally cannot buy at. It must take the same
// bounded wait a buy-time floor refusal takes, and exit under the capital-denied reason if the
// treasury never recovers.
func TestTour_ExhaustedDeployableCapital_ExitsCapitalDeniedNotStarvation(t *testing.T) {
	fx := repositionFixture()
	planner := &tourFakeRoutingClient{infeasibleOnZeroSpend: true}
	h := newTourHandlerWithAPI(t, fx, planner, &tourFakeTelemetry{}, &tourSeqAPIClient{balances: []int{135_364}})
	h.SetCapitalWorkSensor(&tourFakeCapitalWorkSensor{constructionWork: false})

	ctx := auth.WithPlayerToken(context.Background(), "TOUR-BROKE")
	resp, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-BROKE", PlayerID: 1, ContainerID: "ctr-broke", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("an empty deployable pool must not error the run: %v", err)
	}
	r := tourResponse(t, resp)

	if r.ExitReason != tourExitCapitalDenied {
		t.Fatalf("exit reason = %q, want %q - the budget refused every plan, the market was never priced", r.ExitReason, tourExitCapitalDenied)
	}
	if fx.buys != 0 {
		t.Fatalf("the money guard must still refuse every spend (RULINGS #4 untouched), got %d buys", fx.buys)
	}
	if len(fx.jumps) != 0 {
		t.Fatalf("a hull with no deployable capital must not deadhead to a ground it equally cannot buy at, jumps=%v", fx.jumps)
	}
}
