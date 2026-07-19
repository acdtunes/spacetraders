package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// The Python routing-service catches any exception inside OptimizeTradeTour and
// returns a STRUCTURED feasible=false response whose infeasible_reason is
// "internal_error: <exc>" (handlers/tour_handler.py) — it is not a gRPC transport
// error, so the Go side sees a "successful" RPC. Routing that response through the
// same clean "tour unavailable" fail-open path as a legitimate "no_profitable_tour"
// masks a live planner outage as container success=true.
//
// A planner internal_error is a real OUTAGE and must terminalize the container FAILED via
// the honest-completion veto (CompletionOutcome false), surfacing the reason verbatim —
// never a clean success. Transport errors ("planner error:") and genuine infeasibility
// ("no_profitable_tour") still fail open (asserted separately below), so only the
// structured internal_error is reclassified.
func TestTour_PlannerInternalErrorVetoesNotMasked(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		markets: map[string][]string{"X1-S1": {"X1-S1-A"}},
		bid:     map[string]map[string]int{}, ask: map[string]map[string]int{"X1-S1-A": {"G": 100}},
		tv: map[string]map[string]int{"X1-S1-A": {"G": 1000}},
	}
	tel := &tourFakeTelemetry{}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{
		{Feasible: false, InfeasibleReason: "internal_error: stock_sources"},
	}}
	h := newTourHandler(t, fx, planner, tel)

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-IE", PlayerID: 1, ContainerID: "ctr-ie", ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("a planner internal_error vetoes via CompletionOutcome, not a Go error; got %v", err)
	}
	r := tourResponse(t, resp)

	ok, reason := r.CompletionOutcome()
	if ok {
		t.Fatalf("planner internal_error must veto (container FAILED), got clean completion: %+v", r)
	}
	if !strings.Contains(reason, "internal_error") || !strings.Contains(reason, "stock_sources") {
		t.Fatalf("veto reason must surface the planner internal error verbatim, got %q", reason)
	}
	// The masking is the defect: it must NOT be reported as a clean fail-open no-op.
	if r.TourUnavailable {
		t.Fatalf("internal_error must not be masked as a clean 'tour unavailable' success, got %+v", r)
	}
	if fx.buys != 0 || fx.sells != 0 {
		t.Fatalf("a failed plan must not trade, got %d buys / %d sells", fx.buys, fx.sells)
	}
}

// Guardrail (no regression): a genuine infeasibility still fails OPEN cleanly — the
// single-lane fallback stands, container success=true. Only a planner internal_error is
// reclassified as a failure, so this legit "no profitable tour" path is untouched.
func TestTour_NoProfitableTourStillFailsOpenCleanly(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		markets: map[string][]string{"X1-S1": {"X1-S1-A"}},
		bid:     map[string]map[string]int{}, ask: map[string]map[string]int{"X1-S1-A": {"G": 100}},
		tv: map[string]map[string]int{"X1-S1-A": {"G": 1000}},
	}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{
		{Feasible: false, InfeasibleReason: "no_profitable_tour"},
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-NP", PlayerID: 1, ContainerID: "ctr-np", ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("legit infeasibility fails open with a nil error, got %v", err)
	}
	r := tourResponse(t, resp)
	if ok, _ := r.CompletionOutcome(); !ok {
		t.Fatalf("no_profitable_tour is a clean fail-open completion, not a veto: %+v", r)
	}
	if !r.TourUnavailable {
		t.Fatalf("expected a clean 'tour unavailable' no-op, got %+v", r)
	}
}
