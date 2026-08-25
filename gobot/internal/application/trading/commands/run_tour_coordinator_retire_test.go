package commands

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Retirement is the operator's per-hull drain, and a CONTINUOUS tour must honour it from
// INSIDE the container it is already flying: such a run re-plans itself forever on rich
// ground, so a mark that only binds when the container exits never binds at all. It binds at
// the first boundary — between legs or between plans — where the hold is EMPTY. A laden hull
// always flies on: that flight is how it sells.

// retireMarkRepo is the operator's hand on a hull a tour is already flying. It stamps the
// retirement mark onto every ship read once set, and markWhenLaden sets it the moment the
// hull is first seen holding cargo — the live shape, an operator marking a hull mid-tour.
// unreadable models the hull the daemon cannot read at all.
type retireMarkRepo struct {
	*tourFakeShipRepo
	mu            sync.Mutex
	marked        bool
	markWhenLaden bool
	unreadable    bool
}

func (r *retireMarkRepo) FindBySymbol(ctx context.Context, symbol string, playerID shared.PlayerID) (*navigation.Ship, error) {
	r.mu.Lock()
	unreadable := r.unreadable
	r.mu.Unlock()
	if unreadable {
		return nil, fmt.Errorf("ship %s unreadable", symbol)
	}
	ship, err := r.tourFakeShipRepo.FindBySymbol(ctx, symbol, playerID)
	if err != nil || ship == nil {
		return ship, err
	}
	r.mu.Lock()
	if r.markWhenLaden && ship.CargoUnits() > 0 {
		r.marked = true
	}
	marked := r.marked
	r.mu.Unlock()
	if marked {
		ship.MarkRetiring(&trFakeClock{})
	}
	return ship, nil
}

func newRetireTourHandler(t *testing.T, fx *tourFixture, planner routing.RoutingClient, repo navigation.ShipRepository) *RunTourCoordinatorHandler {
	t.Helper()
	return NewRunTourCoordinatorHandler(
		&tourFakeMediator{fx: fx},
		repo,
		&tourFakeMarketRepo{fx: fx, t: t},
		&tourFakeWaypointRepo{fx: fx},
		&tourFakeTelemetry{},
		planner,
		nil,
		&trFakeClock{},
		nil,
	)
}

// oneLaneFixture is the single-lane world these tests trade in: buy G at A, sell G at B.
func oneLaneFixture() *tourFixture {
	return &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B"}},
		bid:     map[string]map[string]int{"X1-S1-B": {"G": 200}},
		ask:     map[string]map[string]int{"X1-S1-A": {"G": 100}, "X1-S1-B": {"G": 200}},
		tv:      map[string]map[string]int{"X1-S1-A": {"G": 1000}, "X1-S1-B": {"G": 1000}},
	}
}

func oneLaneRoundTrip() *routing.TourPlan {
	return &routing.TourPlan{Feasible: true, Legs: []routing.TourLeg{
		leg("X1-S1-A", "X1-S1", buy("G", 40, 100)),
		leg("X1-S1-B", "X1-S1", sell("G", 40, 200)),
	}}
}

// deadGround ends a plan sequence so a test can never depend on the loop stopping itself:
// margins die after the canned plans run out.
func deadGround() *routing.TourPlan {
	return &routing.TourPlan{Feasible: false, InfeasibleReason: "no_profitable_tour"}
}

// The live shape: a hull marked retiring mid-tour reaches its next drained boundary and the
// CONTAINER stands down there — it plans nothing more, and completes honestly so the runner
// releases the claim. Ground that is still rich is exactly the case the old exit-only mark
// could not serve.
func TestTour_ContinuousRetiringHull_StandsDownAtNextDrainedBoundary(t *testing.T) {
	fx := oneLaneFixture()
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{
		oneLaneRoundTrip(), oneLaneRoundTrip(), deadGround(),
	}}
	repo := &retireMarkRepo{tourFakeShipRepo: &tourFakeShipRepo{fx: fx, t: t}, markWhenLaden: true}
	h := newRetireTourHandler(t, fx, planner, repo)
	logger := &propFloorCapturingLogger{}

	resp, err := h.Handle(common.WithLogger(context.Background(), logger), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-RETIRE", PlayerID: 1, ContainerID: "ctr-retire", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("a retirement stand-down is a clean completion, not a Go error; got %v", err)
	}
	r := tourResponse(t, resp)

	if planner.calls != 1 {
		t.Fatalf("expected the retiring hull to plan exactly 1 tour then stand down, got %d planner calls", planner.calls)
	}
	if r.ExitReason != tourExitRetired {
		t.Fatalf("exit reason = %q, want %q", r.ExitReason, tourExitRetired)
	}
	if r.ToursCompleted != 1 {
		t.Fatalf("the tour it was flying still counted, expected ToursCompleted=1, got %d", r.ToursCompleted)
	}
	if !r.Completed {
		t.Fatalf("a stand-down completes the container so the claim is released, got %+v", r)
	}
	if ok, reason := r.CompletionOutcome(); !ok {
		t.Fatalf("a drained stand-down must complete honestly, got veto: %s", reason)
	}
	if !logger.infoContains("stands down drained") || !logger.infoContains("TOUR-RETIRE") {
		t.Fatalf("the stand-down must log one INFO naming the hull and the retirement")
	}
}

// The drain promise: a retiring hull that still holds cargo at a boundary keeps flying and
// selling. Its first tour's sink absorbs only part of the load, so the boundary is LADEN —
// the run must plan the next tour, sell the remainder, and only then stand down.
func TestTour_RetiringHullLadenAtBoundary_KeepsFlyingUntilDrained(t *testing.T) {
	fx := oneLaneFixture()
	fx.sellCap = map[string]int{"G": 20} // the sink takes 20 of the 40 bought
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{
		oneLaneRoundTrip(),
		{Feasible: true, Legs: []routing.TourLeg{
			leg("X1-S1-B", "X1-S1", sell("G", 20, 200)), // the carried-forward load
		}},
		deadGround(),
	}}
	repo := &retireMarkRepo{tourFakeShipRepo: &tourFakeShipRepo{fx: fx, t: t}, markWhenLaden: true}
	h := newRetireTourHandler(t, fx, planner, repo)

	resp, err := h.Handle(common.WithLogger(context.Background(), &propFloorCapturingLogger{}), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-DRAIN", PlayerID: 1, ContainerID: "ctr-drain", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("draining a retiring hull returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if planner.calls != 2 {
		t.Fatalf("a LADEN retiring hull must plan the next tour to sell its load; got %d planner calls", planner.calls)
	}
	if fx.cargo["G"] != 0 {
		t.Fatalf("the retiring hull must leave service EMPTY, got %d unit(s) aboard", fx.cargo["G"])
	}
	if r.ExitReason != tourExitRetired {
		t.Fatalf("exit reason = %q, want %q once the hold finally drained", r.ExitReason, tourExitRetired)
	}
	if ok, reason := r.CompletionOutcome(); !ok {
		t.Fatalf("a hull that sold everything it bought completes honestly, got veto: %s", reason)
	}
}

// The mark binds BETWEEN legs, never mid-leg: marked while laden on the first lane, the hull
// finishes selling that lane and stands down at the next leg boundary — before it buys the
// second lane the same plan had queued.
func TestTour_RetiringMarkMidLeg_BindsAtNextLegBoundary(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B", "X1-S1-C", "X1-S1-D"}},
		bid:     map[string]map[string]int{"X1-S1-B": {"G1": 200}, "X1-S1-D": {"G2": 120}},
		ask: map[string]map[string]int{
			"X1-S1-A": {"G1": 100}, "X1-S1-B": {"G1": 200},
			"X1-S1-C": {"G2": 50}, "X1-S1-D": {"G2": 120},
		},
		tv: map[string]map[string]int{
			"X1-S1-A": {"G1": 1000}, "X1-S1-B": {"G1": 1000},
			"X1-S1-C": {"G2": 1000}, "X1-S1-D": {"G2": 1000},
		},
	}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{
		{Feasible: true, Legs: []routing.TourLeg{
			leg("X1-S1-A", "X1-S1", buy("G1", 40, 100)),
			leg("X1-S1-B", "X1-S1", sell("G1", 40, 200)), // hold empties here
			leg("X1-S1-C", "X1-S1", buy("G2", 40, 50)),   // the lane the stand-down must skip
			leg("X1-S1-D", "X1-S1", sell("G2", 40, 120)),
		}},
		deadGround(),
	}}
	repo := &retireMarkRepo{tourFakeShipRepo: &tourFakeShipRepo{fx: fx, t: t}, markWhenLaden: true}
	h := newRetireTourHandler(t, fx, planner, repo)

	resp, err := h.Handle(common.WithLogger(context.Background(), &propFloorCapturingLogger{}), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-LEGS", PlayerID: 1, ContainerID: "ctr-legs", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("a mid-plan stand-down returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if fx.buys != 1 {
		t.Fatalf("the stand-down must precede the next lane's buy, got %d buys", fx.buys)
	}
	if fx.sells != 1 {
		t.Fatalf("the marked lane still sold out, expected 1 sell, got %d", fx.sells)
	}
	if r.LegsExecuted != 2 {
		t.Fatalf("expected the plan to stop after its 2 flown legs, got %d", r.LegsExecuted)
	}
	if planner.calls != 1 {
		t.Fatalf("no ground is re-planned for a hull leaving service, got %d planner calls", planner.calls)
	}
	if r.ExitReason != tourExitRetired {
		t.Fatalf("exit reason = %q, want %q", r.ExitReason, tourExitRetired)
	}
	if ok, reason := r.CompletionOutcome(); !ok {
		t.Fatalf("stopping a plan at a drained leg boundary strands nothing, got veto: %s", reason)
	}
}

// Fail OPEN. Retirement is an operator convenience and may never become a stall source: a
// hull the daemon cannot read is never stood down, and the run behaves exactly as an
// unmarked one does against the same unreadable hull.
func TestTour_RetirementMarkUnreadable_FailsOpen(t *testing.T) {
	fx := oneLaneFixture()
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{oneLaneRoundTrip()}}
	repo := &retireMarkRepo{tourFakeShipRepo: &tourFakeShipRepo{fx: fx, t: t}, marked: true, unreadable: true}
	h := newRetireTourHandler(t, fx, planner, repo)
	cmd := &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-BLIND", PlayerID: 1, ContainerID: "ctr-blind", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	}

	if h.retiringDrained(context.Background(), cmd) {
		t.Fatalf("an unreadable hull must read as NOT standing down (fail open)")
	}

	resp, err := h.Handle(common.WithLogger(context.Background(), &propFloorCapturingLogger{}), cmd)
	if err == nil {
		t.Fatalf("an unreadable hull is still the resumable error it always was, got nil")
	}
	if r := tourResponse(t, resp); r.ExitReason == tourExitRetired || r.RetirementStandDown {
		t.Fatalf("an unreadable mark must never stand a hull down, got %+v", r)
	}
}
