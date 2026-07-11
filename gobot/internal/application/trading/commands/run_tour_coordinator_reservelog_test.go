package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// sp-avt4: a reserve >= max_spend zeroes the Python solver's spend_cap BEFORE the
// market is ever looked at. Pre-fix, the solver's generic "no profitable allocation"
// reason was indistinguishable from genuine market death, costing 70+ min of
// misdiagnosis in the 2026-07-11 fleet-dark P0. The solver-side fix (tour_solver.py)
// now returns a distinct "reserve_exceeds_budget (spend_cap=0: max_spend X - reserve
// Y)" reason; these two tests pin the GO SEAM — that whatever InfeasibleReason the
// planner returns reaches `container logs` verbatim in the MESSAGE TEXT, not just
// metadata (ContainerRunner.Log only prints "message" to stdout — the sp-149h/sp-iqyq
// renderer defect the reposition ranking log already works around, per the doc
// comment at run_tour_coordinator_reposition.go:589-591). The fake planner stands in
// for the Python solver here — these tests do not re-verify the solver's own
// zeroed-budget detection (see tests/test_tour_solver.py for that), only that the Go
// coordinator never drops or mangles the reason string it receives.

// Path 1: a ONE-SHOT (default Iterations) run whose only planner call is infeasible.
// This is the "no-plan" fail-open exit (run_tour_coordinator.go ~line 701) — the
// simplest, most direct site Task 2 (sp-avt4) requires to name the cause.
func TestTour_NoPlanLog_SurfacesReserveExceedsBudgetVerbatim(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B"}},
		bid:     map[string]map[string]int{"X1-S1-B": {"G": 200}},
		ask:     map[string]map[string]int{"X1-S1-A": {"G": 100}, "X1-S1-B": {"G": 200}},
		tv:      map[string]map[string]int{"X1-S1-A": {"G": 1000}, "X1-S1-B": {"G": 1000}},
	}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{
		{Feasible: false, InfeasibleReason: "reserve_exceeds_budget (spend_cap=0: max_spend 50000 - reserve 50000)"},
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	logger := &laneLogCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	resp, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-RSV1", PlayerID: 1, ContainerID: "ctr-rsv1",
		MaxSpend: 50_000, WorkingCapitalReserve: 50_000,
		ModelArtifactPath: writeTourArtifact(t),
		// Iterations deliberately unset (0) -> one-shot fail-open path.
	})
	if err != nil {
		t.Fatalf("a zeroed-budget no-plan is a fail-open completion, not a Go error; got %v", err)
	}
	r := tourResponse(t, resp)

	if !r.TourUnavailable {
		t.Fatalf("expected TourUnavailable=true on a zero-plan one-shot run, got %+v", r)
	}
	if !strings.Contains(r.TourUnavailableReason, "reserve_exceeds_budget") {
		t.Fatalf("TourUnavailableReason = %q, want it to contain %q", r.TourUnavailableReason, "reserve_exceeds_budget")
	}

	var named *laneLogEntry
	for i := range logger.entries {
		if strings.Contains(logger.entries[i].message, "reserve_exceeds_budget") {
			named = &logger.entries[i]
			break
		}
	}
	if named == nil {
		t.Fatalf("expected a log entry naming reserve_exceeds_budget in its MESSAGE TEXT (metadata-only is invisible to `container logs`), got entries: %+v", logger.entries)
	}
	for _, want := range []string{"reserve_exceeds_budget", "max_spend 50000", "reserve 50000"} {
		if !strings.Contains(named.message, want) {
			t.Fatalf("expected the no-plan log message to contain %q, got: %s", want, named.message)
		}
	}
}

// Path 2: a CONTINUOUS (-1) run that trades once, then dies on a zeroed budget for
// tourStarvationLimit consecutive tours and exits honestly. This pins the
// sp-avt4 fix at the "Continuous tour stopping" site (run_tour_coordinator.go
// ~line 748-764): the LAST tour's InfeasibleReason must be appended to the stop
// message's TEXT (not left metadata-only), so a starved budget reads differently
// from genuine margin death in `container logs`.
func TestTour_ContinuousStarvationLog_NamesReserveExceedsBudget(t *testing.T) {
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
		// Repeats (fake clamps to the last plan) once the treasury has grown enough
		// that a fixed reserve now exceeds a re-resolved/explicit budget mid-run.
		{Feasible: false, InfeasibleReason: "reserve_exceeds_budget (spend_cap=0: max_spend 50000 - reserve 50000)"},
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	logger := &laneLogCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	resp, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-RSV2", PlayerID: 1, ContainerID: "ctr-rsv2",
		MaxSpend: 50_000, WorkingCapitalReserve: 50_000,
		Iterations: -1, ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("continuous margin-death must be a clean completion, not a Go error; got %v", err)
	}
	r := tourResponse(t, resp)

	if r.ExitReason != tourExitStarvation {
		t.Fatalf("exit reason = %q, want %q", r.ExitReason, tourExitStarvation)
	}
	if r.ToursCompleted != 1 {
		t.Fatalf("expected exactly 1 productive tour before the budget starved, got %d", r.ToursCompleted)
	}

	var stopped *laneLogEntry
	for i := range logger.entries {
		if strings.HasPrefix(logger.entries[i].message, "Continuous tour stopping") {
			stopped = &logger.entries[i]
			break
		}
	}
	if stopped == nil {
		t.Fatalf("expected a 'Continuous tour stopping' log entry, got %+v", logger.entries)
	}
	// The LAST tour's concrete reason must be named in the message text — a starved
	// budget must not read identically to genuine margin death.
	for _, want := range []string{"reserve_exceeds_budget", "max_spend 50000", "reserve 50000"} {
		if !strings.Contains(stopped.message, want) {
			t.Fatalf("expected the starvation-stop message to contain %q, got: %s", want, stopped.message)
		}
	}
	if stopped.metadata["reason"] == nil || !strings.Contains(stopped.metadata["reason"].(string), "reserve_exceeds_budget") {
		t.Fatalf("expected metadata[\"reason\"] to also carry reserve_exceeds_budget (structured payload for dashboards), got %v", stopped.metadata["reason"])
	}
}
