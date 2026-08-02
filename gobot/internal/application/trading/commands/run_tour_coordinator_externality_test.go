package commands

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// planExternalityWeight runs one plan-only tour and returns the recovery-externality
// weight the coordinator put on the planner request. The plan is infeasible on purpose:
// nothing executes, so this reads the PLANNING input and nothing else.
func planExternalityWeight(t *testing.T, cmd *RunTourCoordinatorCommand) float64 {
	t.Helper()
	fx := backstopFixture("WEAK", time.Minute)
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{{Feasible: false, InfeasibleReason: "planning-only"}}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	cmd.ShipSymbol, cmd.PlayerID, cmd.ContainerID = "TOUR-1", 1, "ctr-1"
	cmd.ModelArtifactPath = writeTourArtifact(t)
	if _, err := h.Handle(context.Background(), cmd); err != nil {
		t.Fatalf("tour returned error: %v", err)
	}
	if len(planner.externalityWeights) != 1 {
		t.Fatalf("expected exactly one plan call, got %d", len(planner.externalityWeights))
	}
	return planner.externalityWeights[0]
}

// The knob is only worth anything if it REACHES the solver: the recovery-externality
// charge lives in the Python pairing loop and reads the weight off the request. A weight
// stranded on the command prices nothing (the candidate-widening failure mode).
func TestTour_ExternalityWeightReachesTheSolverRequest(t *testing.T) {
	got := planExternalityWeight(t, &RunTourCoordinatorCommand{ExternalityWeight: 0.35})

	if got != 0.35 {
		t.Fatalf("cons.ExternalityWeight = %v, want 0.35 — the charge never reaches the solver", got)
	}
}

// Default safety: an unconfigured weight must cross the wire as 0, which the solver
// short-circuits to today's exact ordering. This is the revert path too (set 0, restart).
func TestTour_UnconfiguredExternalityWeightPlansAtTodaysOrdering(t *testing.T) {
	got := planExternalityWeight(t, &RunTourCoordinatorCommand{})

	if got != 0 {
		t.Fatalf("cons.ExternalityWeight = %v, want 0 (unarmed default must be byte-identical)", got)
	}
}
