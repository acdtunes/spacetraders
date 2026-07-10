package commands

// sp-m9co: restart-boundary tour deaths. Two coupled classes in the continuous-tour
// loop, both exercised here with the shared reposition fixture/harness.
//
//   Class A — a recovered continuous (-1) tour whose FIRST post-restart plan is
//   infeasible must ROUTE INTO the sp-zhii rank-and-reposition rescue (it re-enters at
//   ToursCompleted==0 having lost its pre-restart productive standing), NOT complete the
//   container as tour-unavailable. A finite/one-shot (iterations=1) run is unchanged.
//
//   Class B — the reposition pre-flight priced the candidate with the DRAINED hull's
//   clogging cargo aboard; the solver seats launch cargo in every hold slot
//   (occ=[total_initial]*n), so a hull whose hold is full of cargo no candidate can sink
//   pre-flights INFEASIBLE at every ground alike (the 2B-0691151d shape: three
//   healthy-prerank systems all "infeasible"). The pre-flight must measure the CANDIDATE's
//   fresh potential with an available hold. Plus: the ranking log must distinguish
//   solver-infeasible from feasible-but-below-floor.

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// --- Class A -------------------------------------------------------------------------

// A recovered continuous (-1) tour whose FIRST plan is infeasible must NOT die on the
// drained ground: with a healthy jump-reachable neighbour it 3-strikes into the SAME
// rank-and-reposition rescue as margins-death and rotates to the fresh ground, instead of
// completing the container tour-unavailable (the sp-m9co Class A restart-boundary death:
// tour-run-TORWIND-39/37 died at iteration 1 seconds after recovery).
func TestTour_RecoveredContinuous_Iteration1Infeasible_RepositionsInsteadOfDying(t *testing.T) {
	fx := repositionFixture() // hull on home X1-S1 (empty hold); X1-S2 a fresh jump-reachable ground
	s2Calls := 0
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		switch ship.CurrentSystem {
		case "X1-S1":
			return infeasibleTour() // the recovered hull's FIRST post-restart plan is infeasible
		case "X1-S2":
			s2Calls++
			if s2Calls <= 2 {
				return roundTripS2() // pre-flight (clears the floor) + first productive re-plan after the jump
			}
			return infeasibleTour() // then the fresh ground dries too → honest exit
		}
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-REC", PlayerID: 1, ContainerID: "ctr-rec", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("recovered continuous run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.TourUnavailable {
		t.Fatalf("a recovered continuous (-1) tour whose FIRST plan is infeasible must ROTATE to a fresh ground, not complete tour-unavailable (sp-m9co Class A): %+v", r)
	}
	if r.Repositions != 1 {
		t.Fatalf("expected exactly one reposition off the drained home ground, got %d (%+v)", r.Repositions, r)
	}
	if len(fx.jumps) != 1 || fx.jumps[0] != "X1-S2" {
		t.Fatalf("expected the hull to jump to the healthy neighbour X1-S2, got %v", fx.jumps)
	}
	if !plannerVisitedSystem(planner.positions, "X1-S2") {
		t.Fatalf("the reposition ranking must have priced a tour AT the candidate X1-S2, positions=%v", planner.positions)
	}
	if shared.ExtractSystemSymbol(fx.location) != "X1-S2" {
		t.Fatalf("the hull must end at the fresh ground X1-S2, got %q", fx.location)
	}
}

// Regression (preserve): a FINITE single-tour run (iterations=1) whose only plan is
// infeasible exits tour-unavailable exactly as pre-sp-m9co — it never ranks or jumps. The
// Class A reposition-on-iteration-1 routing is scoped to CONTINUOUS (-1) runs only.
func TestTour_SingleTour_Iteration1Infeasible_StillExitsUnavailable(t *testing.T) {
	fx := repositionFixture()
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-ONE", PlayerID: 1, ContainerID: "ctr-one", Iterations: 1, // single tour, NOT continuous
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("single-tour unavailable must be a clean completion, got %v", err)
	}
	r := tourResponse(t, resp)

	if !r.TourUnavailable || r.ExitReason != tourExitUnavailable {
		t.Fatalf("a single-tour (iterations=1) run whose only plan is infeasible must exit tour-unavailable exactly as pre-sp-m9co, got %+v", r)
	}
	if r.Repositions != 0 || len(fx.jumps) != 0 {
		t.Fatalf("a finite/one-shot run must NEVER reposition, got %d repositions / jumps %v", r.Repositions, fx.jumps)
	}
	if plannerVisitedSystem(planner.positions, "X1-S2") {
		t.Fatalf("a single-tour run must not even RANK a candidate, positions=%v", planner.positions)
	}
}

// A recovered continuous (-1) tour that reposition-rescues on iteration-1 infeasibility but
// finds the destination ALSO dead exits HONESTLY (not tour-unavailable, not a Go error), and
// the exit detail NAMES BOTH the origin and the destination — the dual-system honest exit is
// preserved on the ToursCompleted==0 recovered path.
func TestTour_RecoveredContinuous_Iteration1Infeasible_BothGroundsDead_ExitsHonestlyNamingBoth(t *testing.T) {
	fx := repositionFixture()
	s2Calls := 0
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		switch ship.CurrentSystem {
		case "X1-S1":
			return infeasibleTour() // home dead from the first post-restart plan
		case "X1-S2":
			s2Calls++
			if s2Calls == 1 {
				return roundTripS2() // pre-flight clears the floor → the jump commits
			}
			return infeasibleTour() // but the destination is dead on arrival → dies there too
		}
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-2DEAD", PlayerID: 1, ContainerID: "ctr-2dead", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("both-grounds-dead must be an honest completion, not an error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.TourUnavailable {
		t.Fatalf("a recovered continuous run that rotated then died must NOT read tour-unavailable: %+v", r)
	}
	if r.Repositions != 1 || len(fx.jumps) != 1 || fx.jumps[0] != "X1-S2" {
		t.Fatalf("expected exactly one reposition to X1-S2 before the honest exit, got %d repositions / jumps %v", r.Repositions, fx.jumps)
	}
	if r.ExitReason != tourExitStarvation {
		t.Fatalf("exit reason = %q, want %q", r.ExitReason, tourExitStarvation)
	}
	if !strings.Contains(r.ExitDetail, "X1-S1") || !strings.Contains(r.ExitDetail, "X1-S2") {
		t.Fatalf("the honest exit must NAME BOTH the origin X1-S1 and destination X1-S2 (dual-system detail), got %q", r.ExitDetail)
	}
	if ok, _ := r.CompletionOutcome(); !ok {
		t.Fatalf("a both-dead episode is an honest completion, got a veto")
	}
}

// --- Class B -------------------------------------------------------------------------

// THE 2B-0691151d reproduction. The recovered hull is CLOGGED with cargo no ground in the
// tour graph can sink. The reposition pre-flight must price the candidate as the hull WILL
// arrive — with an available hold — measuring the FRESH ground's true potential, not carry
// the clogging cargo that seats every hold slot in the solver (occ=[total_initial]*n) and
// makes a healthy ground read infeasible. Before the fix, three healthy grounds all
// pre-flighted infeasible and the hull died on its home ground.
func TestTour_Reposition_LadenHull_PreFlightMeasuresFreshGroundNotBlockedByCargo(t *testing.T) {
	fx := repositionFixture()
	// 80 ADVANCED_CIRCUITRY + 80 LAB_INSTRUMENTS bought at a system that no longer buys them,
	// modelled as one unsellable good clogging the hold. Nowhere in the graph bids for it.
	fx.cargo = map[string]int{"SLAG": 90}

	homeCalls, s2Calls := 0, 0
	blockedByCargo := func(ship routing.TourShipState) bool {
		return ship.Cargo["SLAG"] > 0 // models tour_solver occ=total_initial: a clogged hold ⇒ no fresh arb
	}
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		switch ship.CurrentSystem {
		case "X1-S1":
			homeCalls++
			if homeCalls == 1 {
				return roundTripS1() // one productive tour, then home dies (reaches the margins-death reposition)
			}
			return infeasibleTour()
		case "X1-S2":
			s2Calls++
			// The fresh ground is HEALTHY, but reads INFEASIBLE while the synthetic pre-flight
			// still carries the clogging cargo — exactly the sp-m9co Class B defect.
			if blockedByCargo(ship) {
				return infeasibleTour()
			}
			return roundTripS2()
		}
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-LADEN", PlayerID: 1, ContainerID: "ctr-laden", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("laden-hull reposition run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.Repositions != 1 || len(fx.jumps) != 1 || fx.jumps[0] != "X1-S2" {
		t.Fatalf("a laden hull's reposition pre-flight must measure the FRESH ground (empty-hold), not be blocked by the cargo it is escaping — expected 1 jump to X1-S2, got %d repositions / jumps %v (%+v)", r.Repositions, fx.jumps, r)
	}
	// The candidate pre-flight must be priced with an AVAILABLE hold: the synthetic state
	// clears the clogging cargo so the ranking sees the ground's real potential.
	sawEmptyHoldPreflight := false
	for i, pos := range planner.positions {
		if shared.ExtractSystemSymbol(pos) == "X1-S2" && len(planner.cargos[i]) == 0 {
			sawEmptyHoldPreflight = true
			break
		}
	}
	if !sawEmptyHoldPreflight {
		t.Fatalf("the candidate pre-flight must be priced with an EMPTY hold (clogging cargo cleared); recorded cargos=%v positions=%v", planner.cargos, planner.positions)
	}
}

// repoRankCapturingLogger records ranking log lines so the log-distinction test can inspect
// the exact greppable message text (metadata is dropped by the container-log renderer).
type repoRankCapturingLogger struct {
	mu      sync.Mutex
	entries []string
}

func (l *repoRankCapturingLogger) Log(_, message string, _ map[string]interface{}) {
	l.mu.Lock()
	l.entries = append(l.entries, message)
	l.mu.Unlock()
}

// The ranking log must DISTINGUISH a solver-infeasible candidate (the ground itself cannot be
// toured) from a feasible-but-below-floor one (tourable, but not worth the jump) — the
// pre-sp-m9co line conflated them ("chosen none (none cleared the floor)" even when every
// candidate was infeasible), which cost diagnosis time on the 2B episode.
func TestTour_RepositionRanking_DistinguishesInfeasibleFromBelowFloor(t *testing.T) {
	floor := int64(25000)

	// Mixed: one solver-infeasible, one feasible-but-below-floor (the best feasible).
	logger := &repoRankCapturingLogger{}
	evaluated := []repositionScore{
		{system: "X1-DEAD", waypoint: "X1-DEAD-A", prerank: 62840, feasible: false},                   // solver said no
		{system: "X1-THIN", waypoint: "X1-THIN-A", prerank: 38712, freshProfit: 9000, feasible: true}, // tourable but thin
	}
	best := &evaluated[1] // best feasible — still below the floor
	logRepositionRanking(logger, "TOUR-LOG", "X1-HOME", evaluated, best, floor)

	if len(logger.entries) != 1 {
		t.Fatalf("expected one ranking log line, got %d", len(logger.entries))
	}
	line := logger.entries[0]
	if !strings.Contains(line, "X1-DEAD(prerank=62840,infeasible)") {
		t.Fatalf("a solver-infeasible candidate must be marked 'infeasible', got %q", line)
	}
	if !strings.Contains(line, "X1-THIN(prerank=38712,fresh=9000,below-floor)") {
		t.Fatalf("a feasible-but-below-floor candidate must be marked 'below-floor' with its fresh profit, distinct from infeasible, got %q", line)
	}
	if strings.Contains(line, "none cleared the floor") {
		t.Fatalf("the summary must not conflate the two cases with the old 'none cleared the floor' text, got %q", line)
	}
	if !strings.Contains(line, "X1-THIN") || !strings.Contains(line, "9000") || !strings.Contains(line, "25000") {
		t.Fatalf("the below-floor summary must name the best feasible candidate, its fresh profit and the floor, got %q", line)
	}

	// All-infeasible: the summary must say so, distinct from a below-floor verdict.
	logger2 := &repoRankCapturingLogger{}
	evaluated2 := []repositionScore{
		{system: "X1-A", prerank: 5, feasible: false},
		{system: "X1-B", prerank: 4, feasible: false},
	}
	logRepositionRanking(logger2, "TOUR-LOG", "X1-HOME", evaluated2, nil, floor)
	line2 := logger2.entries[0]
	if !strings.Contains(line2, "solver-infeasible") {
		t.Fatalf("when NO candidate is feasible the summary must say all are solver-infeasible (distinct from below-floor), got %q", line2)
	}
}
