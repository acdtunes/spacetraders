package commands

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// sp-m5kv acceptance (1): a continuous (--iterations) tour completes a manifest and
// starts the NEXT one from the hull's new position with no captain input. Two feasible
// plans; the coordinator must call the planner once per tour and the SECOND call must
// see the hull where the first tour left it (X1-S1-B), not the launch waypoint.
func TestTour_ContinuousRePlansFromNewPosition(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B", "X1-S1-C"}},
		bid:     map[string]map[string]int{"X1-S1-B": {"G1": 200}, "X1-S1-C": {"G2": 120}},
		ask:     map[string]map[string]int{"X1-S1-A": {"G1": 100}, "X1-S1-B": {"G1": 200, "G2": 50}, "X1-S1-C": {"G2": 120}},
		tv:      map[string]map[string]int{"X1-S1-A": {"G1": 1000}, "X1-S1-B": {"G1": 1000, "G2": 1000}, "X1-S1-C": {"G2": 1000}},
	}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{
		{Feasible: true, Legs: []routing.TourLeg{
			leg("X1-S1-A", "X1-S1", buy("G1", 40, 100)),
			leg("X1-S1-B", "X1-S1", sell("G1", 40, 200)),
		}},
		{Feasible: true, Legs: []routing.TourLeg{
			leg("X1-S1-B", "X1-S1", buy("G2", 40, 50)),
			leg("X1-S1-C", "X1-S1", sell("G2", 40, 120)),
		}},
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-CT", PlayerID: 1, ContainerID: "ctr-ct", Iterations: 2, ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("continuous tour returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if planner.calls != 2 {
		t.Fatalf("expected exactly 2 planner calls (one per tour, no re-plans), got %d", planner.calls)
	}
	// The heart of the acceptance: the second tour plans from the NEW position.
	if planner.positions[0] != "X1-S1-A" || planner.positions[1] != "X1-S1-B" {
		t.Fatalf("plan positions = %v, want [X1-S1-A X1-S1-B] (second tour re-plans from where the first ended)", planner.positions)
	}
	if r.ToursCompleted != 2 {
		t.Fatalf("expected 2 tours completed, got %d (%+v)", r.ToursCompleted, r)
	}
	if r.ExitReason != tourExitIterations {
		t.Fatalf("exit reason = %q, want %q", r.ExitReason, tourExitIterations)
	}
	// Both goods traded across the two tours (buy+sell each = 4 trades).
	if r.TradesExecuted != 4 {
		t.Fatalf("expected 4 trades across the two tours, got %d", r.TradesExecuted)
	}
	if ok, reason := r.CompletionOutcome(); !ok {
		t.Fatalf("expected honest completion, got veto: %s", reason)
	}
	if fx.cargo["G1"] != 0 || fx.cargo["G2"] != 0 {
		t.Fatalf("expected the hull empty after two full tours, got %+v", fx.cargo)
	}
}

// A -1 (infinite) tour flies until margins die: the first tour trades, then the planner
// returns infeasible. After tourStarvationLimit consecutive no-plans the run stops
// HONESTLY — the container COMPLETES (ExitReason=starvation), it is NOT a fail-open
// "tour unavailable" (a productive run happened) and NOT a veto.
func TestTour_ContinuousStopsHonestlyOnMarginDeath(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B"}},
		bid:     map[string]map[string]int{"X1-S1-B": {"G": 200}},
		ask:     map[string]map[string]int{"X1-S1-A": {"G": 100}, "X1-S1-B": {"G": 200}},
		tv:      map[string]map[string]int{"X1-S1-A": {"G": 1000}, "X1-S1-B": {"G": 1000}},
	}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{
		{Feasible: true, Legs: []routing.TourLeg{
			leg("X1-S1-A", "X1-S1", buy("G", 40, 100)),
			leg("X1-S1-B", "X1-S1", sell("G", 40, 200)),
		}},
		{Feasible: false, InfeasibleReason: "no_profitable_tour"}, // margins died
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-INF", PlayerID: 1, ContainerID: "ctr-inf", Iterations: -1, ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("margin-death must be a clean completion, not a Go error; got %v", err)
	}
	r := tourResponse(t, resp)

	if r.ToursCompleted != 1 {
		t.Fatalf("expected exactly 1 productive tour before margins died, got %d", r.ToursCompleted)
	}
	if r.ExitReason != tourExitStarvation {
		t.Fatalf("exit reason = %q, want %q", r.ExitReason, tourExitStarvation)
	}
	if r.TourUnavailable {
		t.Fatalf("a run that earned then hit margin-death is NOT tour-unavailable: %+v", r)
	}
	if !r.Completed {
		t.Fatalf("margin-death after productive tours is an HONEST completion, got %+v", r)
	}
	if ok, reason := r.CompletionOutcome(); !ok {
		t.Fatalf("empty-hold margin-death must complete honestly, got veto: %s", reason)
	}
	// The no-plan streak: 1 productive plan + tourStarvationLimit confirming no-plans.
	if planner.calls != 1+tourStarvationLimit {
		t.Fatalf("expected %d planner calls (1 productive + %d starvation confirmations), got %d", 1+tourStarvationLimit, tourStarvationLimit, planner.calls)
	}
}

// The sp-m5kv honest-completion boundary, GOOD path: a tour ending with held cargo is
// NOT a strand mid-run — the next tour re-plans from the hull's current cargo (the
// planner SEES the held load) and sells it, so the final exit is clean. This is the
// laden-exit→manual-rescue class the feature kills.
func TestTour_ContinuousHeldCargoCarriesForwardAndSellsCleanly(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B"}},
		bid:     map[string]map[string]int{"X1-S1-B": {"G": 200}},
		ask:     map[string]map[string]int{"X1-S1-A": {"G": 100}, "X1-S1-B": {"G": 200}},
		tv:      map[string]map[string]int{"X1-S1-A": {"G": 1000}, "X1-S1-B": {"G": 1000}},
		sellCap: map[string]int{"G": 20}, // tour 1's sink absorbs only 20 of the 40 bought
	}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{
		{Feasible: true, Legs: []routing.TourLeg{
			leg("X1-S1-A", "X1-S1", buy("G", 40, 100)),
			leg("X1-S1-B", "X1-S1", sell("G", 40, 200)), // absorbs 20 → 20 held
		}},
		{Feasible: true, Legs: []routing.TourLeg{
			leg("X1-S1-B", "X1-S1", sell("G", 20, 200)), // next tour liquidates the carried-forward load
		}},
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-HELD", PlayerID: 1, ContainerID: "ctr-held", Iterations: 2, ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("carry-forward tour returned error: %v", err)
	}
	r := tourResponse(t, resp)

	// The second plan must have SEEN the 20 units held from tour 1 as its cargo input.
	if got := planner.cargos[1]["G"]; got != 20 {
		t.Fatalf("second tour planned from cargo G=%d, want 20 (held load carried forward as planner input)", got)
	}
	if ok, reason := r.CompletionOutcome(); !ok {
		t.Fatalf("held cargo sold by the next tour must complete clean, got veto: %s", reason)
	}
	if r.CargoStranded {
		t.Fatalf("a mid-run held load that the next tour sells is NOT stranded: %+v", r)
	}
	if fx.cargo["G"] != 0 {
		t.Fatalf("expected the carried-forward load fully sold, got %d aboard", fx.cargo["G"])
	}
}

// The sp-m5kv honest-completion boundary, VETO path: the FINAL exit while still holding
// cargo BOUGHT this run vetoes success — the honest-exit contract does not bend across
// iterations. Here tour 1 strands 20 G, tour 2 trades a different good and never clears
// it, so the run ends laden with tour-bought cargo → CompletionOutcome false.
func TestTour_ContinuousFinalLadenExitVetoes(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B", "X1-S1-C"}},
		bid:     map[string]map[string]int{"X1-S1-B": {"G": 200}, "X1-S1-C": {"G2": 120}},
		ask:     map[string]map[string]int{"X1-S1-A": {"G": 100}, "X1-S1-B": {"G": 200, "G2": 50}, "X1-S1-C": {"G2": 120}},
		tv:      map[string]map[string]int{"X1-S1-A": {"G": 1000}, "X1-S1-B": {"G": 1000, "G2": 1000}, "X1-S1-C": {"G2": 1000}},
		sellCap: map[string]int{"G": 20}, // G never fully clears
	}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{
		{Feasible: true, Legs: []routing.TourLeg{
			leg("X1-S1-A", "X1-S1", buy("G", 40, 100)),
			leg("X1-S1-B", "X1-S1", sell("G", 40, 200)), // 20 stranded
		}},
		{Feasible: true, Legs: []routing.TourLeg{ // tour 2 trades G2, ignores the stranded G
			leg("X1-S1-B", "X1-S1", buy("G2", 40, 50)),
			leg("X1-S1-C", "X1-S1", sell("G2", 40, 120)),
		}},
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-VETO", PlayerID: 1, ContainerID: "ctr-veto", Iterations: 2, ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("a stranded continuous run vetoes via CompletionOutcome, not a Go error; got %v", err)
	}
	r := tourResponse(t, resp)

	ok, reason := r.CompletionOutcome()
	if ok {
		t.Fatalf("expected a final-laden veto after two tours, got clean completion: %+v", r)
	}
	// The veto names the tour-bought good, its stranded units, and the final location.
	if !strings.Contains(reason, "20 G") || !strings.Contains(reason, "X1-S1-C") {
		t.Fatalf("veto reason must name stranded units+good+final location, got %q", reason)
	}
	if r.ToursCompleted != 2 {
		t.Fatalf("both tours were productive, expected ToursCompleted=2, got %d", r.ToursCompleted)
	}
}

// A stop/shutdown (ctx cancel) during a continuous run must exit RESUMABLE: the
// coordinator returns the ctx error so the container runner routes it through its
// ctx.Err() path (re-adopted at next boot), NEVER letting the cancel be misread as
// margin-death starvation and COMPLETE a -1 container (the sp-ovkn trap — a COMPLETED
// row is dropped from the recovery set and the hull is lost). Here the planner cancels
// the context after tour 1; the loop's boundary check must return ctx.Canceled.
func TestTour_ContinuousStopExitsResumableNotCompleted(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B"}},
		bid:     map[string]map[string]int{"X1-S1-B": {"G": 200}},
		ask:     map[string]map[string]int{"X1-S1-A": {"G": 100}, "X1-S1-B": {"G": 200}},
		tv:      map[string]map[string]int{"X1-S1-A": {"G": 1000}, "X1-S1-B": {"G": 1000}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	planner := &tourFakeRoutingClient{
		plans: []*routing.TourPlan{{Feasible: true, Legs: []routing.TourLeg{
			leg("X1-S1-A", "X1-S1", buy("G", 40, 100)),
			leg("X1-S1-B", "X1-S1", sell("G", 40, 200)),
		}}},
		cancel: cancel, cancelOnCall: 1, // stop arrives during the first tour
	}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-STOP", PlayerID: 1, ContainerID: "ctr-stop", Iterations: -1, ModelArtifactPath: writeTourArtifact(t),
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a stopped continuous run must return the ctx error (runner exits resumable), got %v", err)
	}
	r := tourResponse(t, resp)
	if r.Completed {
		t.Fatalf("a stopped -1 run must NOT report Completed (that COMPLETES the row and loses the hull), got %+v", r)
	}
	if r.ExitReason == tourExitStarvation {
		t.Fatalf("a ctx cancel must not be misread as starvation, got exit reason %q", r.ExitReason)
	}
	if r.ToursCompleted != 1 {
		t.Fatalf("tour 1 completed before the stop; expected ToursCompleted=1, got %d", r.ToursCompleted)
	}
}

// Regression: with Iterations unset (0) the coordinator runs EXACTLY one tour — the
// original one-shot behavior — so every pre-sp-m5kv caller and test is unchanged.
func TestTour_DefaultIterationsRunsExactlyOneTour(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B"}},
		bid:     map[string]map[string]int{"X1-S1-B": {"G": 200}},
		ask:     map[string]map[string]int{"X1-S1-A": {"G": 100}, "X1-S1-B": {"G": 200}},
		tv:      map[string]map[string]int{"X1-S1-A": {"G": 1000}, "X1-S1-B": {"G": 1000}},
	}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{{
		Feasible: true, Legs: []routing.TourLeg{
			leg("X1-S1-A", "X1-S1", buy("G", 40, 100)),
			leg("X1-S1-B", "X1-S1", sell("G", 40, 200)),
		},
	}}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-1SHOT", PlayerID: 1, ContainerID: "ctr-1shot", ModelArtifactPath: writeTourArtifact(t),
		// Iterations deliberately unset (0)
	})
	if err != nil {
		t.Fatalf("one-shot tour returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if planner.calls != 1 {
		t.Fatalf("Iterations=0 must run exactly one tour (one planner call), got %d", planner.calls)
	}
	if r.ToursCompleted != 1 {
		t.Fatalf("expected 1 tour completed, got %d", r.ToursCompleted)
	}
	if !r.Completed {
		t.Fatalf("expected a completed one-shot tour, got %+v", r)
	}
}

// A finite Iterations=N flies exactly N tours then completes.
func TestTour_FiniteIterationsRunsExactlyN(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B"}},
		bid:     map[string]map[string]int{"X1-S1-B": {"G": 200}},
		ask:     map[string]map[string]int{"X1-S1-A": {"G": 100}, "X1-S1-B": {"G": 200}},
		tv:      map[string]map[string]int{"X1-S1-A": {"G": 1000}, "X1-S1-B": {"G": 1000}},
	}
	// One reusable round-trip plan; the fake returns it for every call (buy at A, sell
	// at B). Each tour starts wherever the last ended, so travel is a no-op when the
	// hull is already at A — the plan still trades, keeping every tour productive.
	roundTrip := &routing.TourPlan{Feasible: true, Legs: []routing.TourLeg{
		leg("X1-S1-A", "X1-S1", buy("G", 40, 100)),
		leg("X1-S1-B", "X1-S1", sell("G", 40, 200)),
	}}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{roundTrip}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-N", PlayerID: 1, ContainerID: "ctr-n", Iterations: 3, ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("finite tour returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.ToursCompleted != 3 {
		t.Fatalf("Iterations=3 must fly exactly 3 tours, got %d", r.ToursCompleted)
	}
	if planner.calls != 3 {
		t.Fatalf("expected 3 planner calls, got %d", planner.calls)
	}
	if r.ExitReason != tourExitIterations {
		t.Fatalf("exit reason = %q, want %q", r.ExitReason, tourExitIterations)
	}
}
