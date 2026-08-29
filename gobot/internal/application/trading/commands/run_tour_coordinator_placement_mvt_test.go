package commands

// run_tour_coordinator_placement_mvt_test.go — sp-6x2gp. The placement charge is the fleet's
// departure trigger: a hull leaves its ground only when a foreign candidate out-earns staying by
// enough to pay for the crossing. These tests pin the three things that changed — the charge is
// horizon-normalized rather than an additive β·D, the deadhead is priced with the measured jump toll
// and a per-crossing base rather than the lane ranker's cold-start fallback, and the armed path
// applies the anti-herd occupancy cap the legacy body always did.

import (
	"context"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/placement"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// stayVsForeignPlanner returns a rich clean-hold E_s at home and a configurable E_x abroad, with the
// laden 3-strike at home going infeasible so the run reaches the margins-death reposition.
func stayVsForeignPlanner(homeCPH, foreignCPH float64) *tourFakeRoutingClient {
	homeCalls := 0
	return &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		switch ship.CurrentSystem {
		case "X1-S1":
			if ship.Cargo == nil {
				return feasiblePlan(homeCPH, int64(homeCPH))
			}
			homeCalls++
			if homeCalls == 1 {
				return roundTripS1()
			}
			return infeasibleTour()
		case "X1-S2":
			return feasiblePlan(foreignCPH, int64(foreignCPH))
		}
		return infeasibleTour()
	}}
}

// A crossing costs the fraction of the horizon it consumes, not a flat β·D toll. The retired charge
// subtracted β·D_x from a $/hr score: at β=150,000/hr and the deadhead this fixture produces it came
// to a few percent of E_x, so a candidate barely richer than home won and the hull left. The
// horizon-normalized charge makes the same candidate lose, because ~40% of the horizon is spent not
// trading. This is the behaviour change the dwell measurement asked for, so it is pinned as a
// verdict, not as arithmetic.
func TestTour_Placement_ModestlyRicherForeignGroundNoLongerWinsTheCrossing(t *testing.T) {
	fx := repositionFixture()
	// Home 300k/hr clean-hold; foreign 400k/hr — 1.33x richer. Under β·D that candidate won; the
	// crossing keeps only ~59% of it, so 400k·0.59 = 238k < 300k and the hull holds its ground.
	h := newTourHandler(t, fx, stayVsForeignPlanner(300000, 400000), &seededTelemetry{rows: betaSeedRows(150000)})
	logger := &tradeCaptureLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	resp, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-MVT1", PlayerID: 1, ContainerID: "ctr-mvt1", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.Repositions != 0 || len(fx.jumps) != 0 {
		t.Fatalf("a 1.33x-richer foreign ground must NOT be worth the crossing, got %d repositions / jumps %v", r.Repositions, fx.jumps)
	}
	if got := shared.ExtractSystemSymbol(fx.location); got != "X1-S1" {
		t.Fatalf("the hull must hold X1-S1, got %q", got)
	}
	if !logger.loggedContaining("Placement decision", "stay X1-S1") {
		t.Fatalf("the decision log must name the stay verdict:\n%s", strings.Join(logger.messages, "\n"))
	}
}

// The trigger still fires — a genuinely better ground wins. The charge lengthens dwell; it must not
// weld a hull to a system it has exhausted, or the fleet ratchets down instead of up.
func TestTour_Placement_DecisivelyRicherForeignGroundStillWinsTheCrossing(t *testing.T) {
	fx := repositionFixture()
	// Foreign 900k/hr vs home 300k/hr: 900k·0.59 = 535k > 300k, so the crossing pays for itself.
	h := newTourHandler(t, fx, stayVsForeignPlanner(300000, 900000), &seededTelemetry{rows: betaSeedRows(150000)})
	logger := &tradeCaptureLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	resp, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-MVT2", PlayerID: 1, ContainerID: "ctr-mvt2", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.Repositions != 1 {
		t.Fatalf("a 3x-richer foreign ground must still win the crossing, got %d repositions (%+v)", r.Repositions, r)
	}
	if !containsSystem(fx.jumps, "X1-S2") {
		t.Fatalf("the hull must jump to X1-S2, jumps=%v", fx.jumps)
	}
}

// The horizon is the operator's lever on dwell (RULINGS #5) and it must reach the verdict. The same
// fixture that stays at the default H=60min leaves at a long horizon, because a long horizon amortises
// the crossing over more trading time and so charges it less.
func TestTour_Placement_HorizonKnobMovesTheStayJumpVerdict(t *testing.T) {
	for _, tc := range []struct {
		name            string
		horizonMinutes  int
		wantRepositions int
	}{
		{"default horizon (60m) holds the hull", 0, 0},
		{"a 6h horizon amortises the crossing and releases it", 360, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := repositionFixture()
			h := newTourHandler(t, fx, stayVsForeignPlanner(300000, 400000), &seededTelemetry{rows: betaSeedRows(150000)})
			resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
				ShipSymbol: "TOUR-H", PlayerID: 1, ContainerID: "ctr-h", Iterations: -1,
				PlacementHorizonMinutes: tc.horizonMinutes,
				ModelArtifactPath:       writeTourArtifact(t),
			})
			if err != nil {
				t.Fatalf("run returned error: %v", err)
			}
			if r := tourResponse(t, resp); r.Repositions != tc.wantRepositions {
				t.Fatalf("Repositions = %d, want %d", r.Repositions, tc.wantRepositions)
			}
		})
	}
}

// ANTI-HERD on the armed path. The only reachable candidate is already at its per-system hull cap, so
// placement must exclude it before scoring and hold the hull — the legacy body and the rate-floor path
// both apply this gate, and arming placement must not quietly retire it.
func TestTour_Placement_ExcludesHerdSaturatedCandidate(t *testing.T) {
	fx := repositionFixture()
	fx.activeHulls = []activeHull{{"X1-S2", tradeFleet}} // S2 is at the cap of 1 set below
	// Home is a POOR clean-hold and the foreign ground is rich: without the herd gate this jumps.
	h := newTourHandler(t, fx, stayVsForeignPlanner(1000, 900000), &seededTelemetry{rows: betaSeedRows(150000)})
	logger := &tradeCaptureLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	resp, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-MVTHERD", PlayerID: 1, ContainerID: "ctr-mvtherd", Iterations: -1,
		RepositionReachMaxHullsPerSystem: 1,
		ModelArtifactPath:                writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.Repositions != 0 || len(fx.jumps) != 0 {
		t.Fatalf("a herd-saturated candidate must never be scored or jumped to, got %d repositions / jumps %v", r.Repositions, fx.jumps)
	}
	if got := shared.ExtractSystemSymbol(fx.location); got != "X1-S1" {
		t.Fatalf("the hull must hold X1-S1 when its only candidate is herd-excluded, got %q", got)
	}
	if !logger.loggedContaining("Placement: excluded 1 herd-saturated candidate") {
		t.Fatalf("the exclusion must be visible in the decision log:\n%s", strings.Join(logger.messages, "\n"))
	}
}

// The deadhead charge reads the MEASURED per-hop toll — the same estimator the tour solver plans
// against — and falls back to the fitted constant only while that estimator is silent. The retired
// formula charged the lane ranker's cold-start fallback (352s) and omitted the per-crossing base
// entirely, which priced a one-hop crossing at 412s against a measured fleet median inter-stay gap of
// roughly 24 minutes.
func TestPlacementDeadheadHours_UsesMeasuredTollOverTheFallback(t *testing.T) {
	cmd := &RunTourCoordinatorCommand{PlayerID: 1}

	silent := &RunTourCoordinatorHandler{jumpTolls: &fakeJumpTollReader{seconds: 0}}
	wantFallback := (placementCrossingBaseSeconds + placementCrossingPerHopSeconds + repositionReplanAllowanceSeconds) / secondsPerHour
	if got := silent.placementDeadheadHours(context.Background(), cmd, 1); got != wantFallback {
		t.Fatalf("a silent estimator must charge base+fallback+replan = %v h, got %v", wantFallback, got)
	}
	if wantFallback <= crossSystemHopSeconds/secondsPerHour {
		t.Fatalf("the fallback charge must exceed the retired lane-ranker constant, else nothing changed")
	}

	measured := &RunTourCoordinatorHandler{jumpTolls: &fakeJumpTollReader{seconds: 900}}
	wantMeasured := (placementCrossingBaseSeconds + 900 + repositionReplanAllowanceSeconds) / secondsPerHour
	if got := measured.placementDeadheadHours(context.Background(), cmd, 1); got != wantMeasured {
		t.Fatalf("a readable estimator must be charged at %v h, got %v", wantMeasured, got)
	}

	// Hops scale only the toll, never the per-crossing base — a 2-hop trip is one crossing.
	twoHop := measured.placementDeadheadHours(context.Background(), cmd, 2)
	if want := (placementCrossingBaseSeconds + 1800 + repositionReplanAllowanceSeconds) / secondsPerHour; twoHop != want {
		t.Fatalf("2 hops must add one more toll and no second base: want %v h, got %v", want, twoHop)
	}
}

// The resolver applies the 0/absent → 60min rule and converts to hours, so the horizon default lives
// in one place and a captain's minutes never reach Score unconverted.
func TestPlacementHorizonHours_ResolvesTheArmedDefault(t *testing.T) {
	if got := placementHorizonHours(0); got != placement.ResidencyHorizonHoursDefault {
		t.Fatalf("an absent horizon must resolve to the armed 60-min default (%v h), got %v", placement.ResidencyHorizonHoursDefault, got)
	}
	if got := placementHorizonHours(-5); got != placement.ResidencyHorizonHoursDefault {
		t.Fatalf("a negative horizon must resolve to the default, got %v", got)
	}
	if got := placementHorizonHours(30); got != 0.5 {
		t.Fatalf("30 minutes must resolve to 0.5h, got %v", got)
	}
}
