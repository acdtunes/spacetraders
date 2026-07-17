package commands

// sp-1wp8: the reposition ranking normalizes by WALL-CLOCK — candidates are ranked by
// projected fresh-credits-per-HOUR over the full time-to-value (jump + re-plan + the
// candidate plan's own projected duration), not by raw fresh profit. The 25k floor
// stays ABSOLUTE: rate reorders CHOICE among floor-clearing candidates, it never
// resurrects a sub-floor one. Candidates with no usable time estimate (cph<=0) drop
// the whole episode back to absolute-fresh ordering — never a divide-by-zero garbage
// ranking (the sp-1wp8 regression pin).

import (
	"context"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// rateRepositionFixture is repositionFixture plus a second neighbor X1-S3, so the
// ranking has two candidates to reorder.
func rateRepositionFixture() *tourFixture {
	fx := repositionFixture()
	fx.markets["X1-S3"] = []string{"X1-S3-A", "X1-S3-B"}
	fx.ask["X1-S3-A"] = map[string]int{"J": 100}
	fx.ask["X1-S3-B"] = map[string]int{"J": 400}
	fx.bid["X1-S3-B"] = map[string]int{"J": 400}
	fx.tv["X1-S3-A"] = map[string]int{"J": 1000}
	fx.tv["X1-S3-B"] = map[string]int{"J": 1000}
	fx.neighbors["X1-S1"] = []string{"X1-S2", "X1-S3"}
	return fx
}

// The 42-min-rifles shape (the bead's evidence): a fast-dense candidate (275k fresh,
// ~39min plan → ~360k/hr over jump+replan+plan) must WIN the reposition over a
// slow-big one (345k fresh, 90min plan → ~214k/hr), even though the slow one's
// absolute fresh profit is higher — and the ranking log must record that rate
// reordered the choice (the acceptance evidence: both orderings visible when they
// differ).
func TestTour_Reposition_RateRanking_FastDenseBeatsSlowBig(t *testing.T) {
	fx := rateRepositionFixture()
	homeCalls := 0
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		switch ship.CurrentSystem {
		case "X1-S1":
			homeCalls++
			if homeCalls == 1 {
				return roundTripS1()
			}
			return infeasibleTour()
		case "X1-S2":
			// Fast-dense: 275k over a ~2340s plan (cph 423,077) → ~360k/hr with the
			// jump+replan overhead. Only the pre-flight sees this; post-jump plans are
			// infeasible so the run exits after the rotation.
			return &routing.TourPlan{Feasible: true, ProjectedProfit: 275000, ProjectedCreditsPerHour: 423077}
		case "X1-S3":
			// Slow-big: more absolute fresh (345k) but a 5400s plan (cph 230,000)
			// → ~214k/hr. Profit-primary would pick THIS one; rate must not.
			return &routing.TourPlan{Feasible: true, ProjectedProfit: 345000, ProjectedCreditsPerHour: 230000}
		}
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	logger := &laneLogCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	resp, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-RATE", PlayerID: 1, ContainerID: "ctr-rate", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("rate-ranking run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.Repositions != 1 {
		t.Fatalf("expected one reposition, got %d", r.Repositions)
	}
	if len(fx.jumps) != 1 || fx.jumps[0] != "X1-S2" {
		t.Fatalf("rate ranking must choose the fast-dense X1-S2 (~360k/hr) over the bigger-but-slower X1-S3 (~214k/hr), got jumps %v", fx.jumps)
	}

	// Acceptance evidence: the ranking line shows BOTH orderings when they differ —
	// the chosen rate winner AND the profit-max candidate it out-ranked.
	var ranking string
	for _, e := range logger.entries {
		if strings.HasPrefix(e.message, "Reposition ranking from") {
			ranking = e.message
			break
		}
	}
	if ranking == "" {
		t.Fatalf("expected a 'Reposition ranking' log entry, got %+v", logger.entries)
	}
	if !strings.Contains(ranking, "rate-reorder") {
		t.Fatalf("when rate reorders the choice, the ranking line must say so (rate-reorder), got %q", ranking)
	}
	if !strings.Contains(ranking, "X1-S3") || !strings.Contains(ranking, "345000") {
		t.Fatalf("the rate-reorder note must name the profit-max candidate it out-ranked (X1-S3 fresh 345000), got %q", ranking)
	}
	if !strings.Contains(ranking, "rate=") {
		t.Fatalf("candidate tokens must carry their projected rate (rate=N/hr), got %q", ranking)
	}
}

// The floor is ABSOLUTE: a blazing rate on a sub-floor fresh profit never justifies
// the jump. A 20k-fresh candidate at a huge rate must stay excluded (below the 25k
// default floor) even when it is the only feasible candidate — the run exits honestly
// without jumping.
func TestTour_Reposition_RateCannotResurrectBelowFloorCandidate(t *testing.T) {
	fx := repositionFixture()
	homeCalls := 0
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		switch ship.CurrentSystem {
		case "X1-S1":
			homeCalls++
			if homeCalls == 1 {
				return roundTripS1()
			}
			return infeasibleTour()
		case "X1-S2":
			// 20k fresh in a 300s plan → ~93k/hr over the overhead — a great RATE that
			// must still lose to the absolute floor (20k < 25k).
			return &routing.TourPlan{Feasible: true, ProjectedProfit: 20000, ProjectedCreditsPerHour: 240000}
		}
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-RATE-FLOOR", PlayerID: 1, ContainerID: "ctr-rate-floor", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("floor run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if len(fx.jumps) != 0 {
		t.Fatalf("a below-floor candidate must never win on rate — expected no jump, got %v", fx.jumps)
	}
	if r.ExitReason != tourExitStarvation {
		t.Fatalf("expected an honest starvation exit, got %q", r.ExitReason)
	}
}

// The zero-time regression pin: when any floor-clearing candidate carries no usable
// time estimate (cph<=0 — a degenerate/mocked planner response), the episode falls
// back to ABSOLUTE fresh-profit ordering for the whole choice — comparing a real
// rate against a guess is not a ranking, and a divide-by-zero rate must never decide
// a jump.
func TestTour_Reposition_MissingTimeEstimateFallsBackToProfitOrdering(t *testing.T) {
	fx := rateRepositionFixture()
	homeCalls := 0
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		switch ship.CurrentSystem {
		case "X1-S1":
			homeCalls++
			if homeCalls == 1 {
				return roundTripS1()
			}
			return infeasibleTour()
		case "X1-S2":
			// Has a time estimate and a strong rate — but the episode must NOT rank by
			// rate, because the other candidate has none.
			return &routing.TourPlan{Feasible: true, ProjectedProfit: 30000, ProjectedCreditsPerHour: 400000}
		case "X1-S3":
			// No time estimate (cph 0) with the larger absolute fresh profit.
			return &routing.TourPlan{Feasible: true, ProjectedProfit: 90000}
		}
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-RATE-FALLBACK", PlayerID: 1, ContainerID: "ctr-rate-fallback", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("fallback run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.Repositions != 1 {
		t.Fatalf("expected one reposition, got %d", r.Repositions)
	}
	if len(fx.jumps) != 1 || fx.jumps[0] != "X1-S3" {
		t.Fatalf("with a missing time estimate the episode must fall back to absolute-fresh ordering (X1-S3 fresh 90000), got jumps %v", fx.jumps)
	}
}

// Pure winner-selection properties that the integration paths above cannot pin
// exactly (float tie shapes): equal rates break on absolute fresh profit; the
// profit-max is reported alongside the rate winner; sub-floor candidates never
// enter the comparison set.
func TestSelectRepositionWinner_EqualRateTieBreaksOnAbsoluteFresh(t *testing.T) {
	evaluated := []repositionScore{
		{system: "X1-SMALL", waypoint: "X1-SMALL-A", feasible: true, freshProfit: 100000, rate: 200000, hasRate: true},
		{system: "X1-BIG", waypoint: "X1-BIG-A", feasible: true, freshProfit: 200000, rate: 200000, hasRate: true},
	}
	winner, profitMax, rateMode := selectRepositionWinner(evaluated, 25000)
	if !rateMode {
		t.Fatalf("both candidates carry rates — rateMode must be true")
	}
	if winner == nil || winner.system != "X1-BIG" {
		t.Fatalf("equal-rate tie must break on absolute fresh profit (X1-BIG), got %+v", winner)
	}
	if profitMax == nil || profitMax.system != "X1-BIG" {
		t.Fatalf("profit-max must be X1-BIG, got %+v", profitMax)
	}
}

func TestSelectRepositionWinner_FloorBoundsTheComparisonSet(t *testing.T) {
	evaluated := []repositionScore{
		// Sub-floor with an enormous rate: must not enter the comparison at all.
		{system: "X1-THIN", waypoint: "X1-THIN-A", feasible: true, freshProfit: 9000, rate: 900000, hasRate: true},
		{system: "X1-SOLID", waypoint: "X1-SOLID-A", feasible: true, freshProfit: 60000, rate: 150000, hasRate: true},
	}
	winner, _, _ := selectRepositionWinner(evaluated, 25000)
	if winner == nil || winner.system != "X1-SOLID" {
		t.Fatalf("a sub-floor candidate must be excluded regardless of rate, got %+v", winner)
	}
}

func TestSelectRepositionWinner_NoFloorClearingCandidate_ReportsBestFeasible(t *testing.T) {
	evaluated := []repositionScore{
		{system: "X1-DEAD", waypoint: "X1-DEAD-A", feasible: false},
		{system: "X1-THIN", waypoint: "X1-THIN-A", feasible: true, freshProfit: 9000, rate: 900000, hasRate: true},
	}
	winner, _, rateMode := selectRepositionWinner(evaluated, 25000)
	if winner == nil || winner.system != "X1-THIN" || winner.freshProfit != 9000 {
		t.Fatalf("with nothing floor-clearing the best feasible must be reported (for the honest below-floor exit log), got %+v", winner)
	}
	if rateMode {
		t.Fatalf("no floor-clearing set → no rate comparison happened, rateMode must be false")
	}
}

// repositionCandidateRate inverts the solver's own cph to recover the plan's
// projected wall-clock (seconds = profit/cph×3600) and prices the candidate as
// fresh/(hops·jump + replan + plan) hours. No estimate (cph<=0) → ok=false.
func TestRepositionCandidateRate_InvertsSolverCphAndAddsOverhead(t *testing.T) {
	// planSeconds = 345000/230000×3600 = 5400s; 1 hop: hours = (352+60+5400)/3600 = 1.61444;
	// rate = 345000/1.61444 = 213,695.6/hr.
	plan := &routing.TourPlan{Feasible: true, ProjectedProfit: 345000, ProjectedCreditsPerHour: 230000}
	rate, ok := repositionCandidateRate(345000, plan, 1)
	if !ok {
		t.Fatalf("a plan with positive cph must yield a rate")
	}
	if rate < 213000 || rate > 214500 {
		t.Fatalf("expected ~213.7k/hr (345k over 1·jump+replan+5400s plan), got %.1f", rate)
	}

	// THE multi-hop deadhead pin (the far-ground under-charge fix): the SAME plan 3 gate hops away
	// is charged 3·352s, not 1·352 — hours = (1056+60+5400)/3600 = 1.81; rate = 345000/1.81 =
	// ~190.6k/hr, materially BELOW the 1-hop rate. If hops were ignored (single-hop charge), a
	// distant candidate's rate would be over-stated back to ~213.7k and the improvement gate would
	// be over-permissive for far grounds.
	rate3, ok := repositionCandidateRate(345000, plan, 3)
	if !ok {
		t.Fatalf("a 3-hop plan with positive cph must yield a rate")
	}
	if rate3 < 190000 || rate3 > 191500 {
		t.Fatalf("expected ~190.6k/hr (345k over 3·jump+replan+5400s plan), got %.1f", rate3)
	}
	if rate3 >= rate {
		t.Fatalf("a 3-hop candidate must be charged MORE deadhead than a 1-hop one (rate3 %.1f < rate1 %.1f)", rate3, rate)
	}

	// hops<1 defensive floor: charged as one hop, never a free deadhead.
	if r0, _ := repositionCandidateRate(345000, plan, 0); r0 != rate {
		t.Fatalf("hops<1 must be charged as one hop (got %.1f, want the 1-hop rate %.1f)", r0, rate)
	}

	if _, ok := repositionCandidateRate(90000, &routing.TourPlan{Feasible: true, ProjectedProfit: 90000}, 1); ok {
		t.Fatalf("cph=0 carries no time estimate — ok must be false (the divide-by-zero pin)")
	}
}
