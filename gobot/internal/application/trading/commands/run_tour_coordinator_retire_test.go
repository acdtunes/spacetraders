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

// The drain promise: a retiring hull that still holds cargo at a boundary is DISPOSED of, not
// toured. Its plan's sink absorbs only half the load, so the boundary is LADEN — and the run
// sells the remainder sell-only until the hold is empty, never planning the hull another tour.
// Planning one is what let a marked hull refill as fast as it drained: an ordinary tour BUYS.
func TestTour_RetiringHullLadenAtBoundary_DisposesUntilDrained(t *testing.T) {
	fx := oneLaneFixture()
	fx.sellCap = map[string]int{"G": 20}                                    // every sale absorbs 20 of the load
	fx.tradeType = map[string]map[string]string{"X1-S1-B": {"G": "IMPORT"}} // a real sink, not an exporter's sellback
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{oneLaneRoundTrip(), deadGround()}}
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

	if planner.calls != 1 {
		t.Fatalf("a hull leaving service is never planned another tour - the disposal ladder drains it; got %d planner calls", planner.calls)
	}
	if fx.buys != 1 {
		t.Fatalf("only the pre-mark buy may ever run on a retiring hull, got %d buys", fx.buys)
	}
	if fx.cargo["G"] != 0 {
		t.Fatalf("the retiring hull must leave service EMPTY, got %d unit(s) aboard", fx.cargo["G"])
	}
	if r.RetirementDisposalSales == 0 {
		t.Fatalf("the remainder must be cleared by the sell-only disposal, not by a fresh tour; got %+v", r)
	}
	if r.ExitReason != tourExitRetired {
		t.Fatalf("exit reason = %q, want %q once the hold finally drained", r.ExitReason, tourExitRetired)
	}
	if ok, reason := r.CompletionOutcome(); !ok {
		t.Fatalf("a hull that sold everything it bought completes honestly, got veto: %s", reason)
	}
}

// THE mid-tour defect, in one test. A hull marked while a plan with a REMAINING BUY LEG is in
// flight must execute no further purchase — not even one already planned and already reserved —
// while the sell leg that disposes of what it is carrying still flies. Before this, the marked
// hull flew the whole plan including its buys, so its hold refilled as fast as it drained and
// nothing bounded how long the drain took.
func TestTour_MarkedMidPlan_SkipsRemainingBuyLegs_StillFliesTheSell(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S2-A", cargoCap: 100,
		markets: map[string][]string{"X1-S2": {"X1-S2-A", "X1-S2-C", "X1-S2-D"}},
		bid:     map[string]map[string]int{"X1-S2-D": {"G1": 300}},
		ask: map[string]map[string]int{
			"X1-S2-A": {"G1": 100}, "X1-S2-C": {"G2": 50}, "X1-S2-D": {"G1": 300},
		},
		tv: map[string]map[string]int{
			"X1-S2-A": {"G1": 1000}, "X1-S2-C": {"G2": 1000}, "X1-S2-D": {"G1": 1000},
		},
	}
	// Buy G1 at A, buy G2 at C, sell G1 at D. The mark lands at C — the hull is laden with G1,
	// so the drained stand-down cannot fire and the ONLY thing that can stop the G2 purchase is
	// the discharge.
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{
		{Feasible: true, Legs: []routing.TourLeg{
			leg("X1-S2-A", "X1-S2", buy("G1", 40, 100)),
			leg("X1-S2-C", "X1-S2", buy("G2", 40, 50)), // the queued buy the mark must suppress
			leg("X1-S2-D", "X1-S2", sell("G1", 40, 300)),
		}},
		deadGround(),
	}}
	repo := &retireMarkRepo{tourFakeShipRepo: &tourFakeShipRepo{fx: fx, t: t}, markWhenLaden: true}
	h := newRetireTourHandler(t, fx, planner, repo)
	logger := &propFloorCapturingLogger{}

	resp, err := h.Handle(common.WithLogger(context.Background(), logger), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-MIDBUY", PlayerID: 1, ContainerID: "ctr-midbuy", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("a mid-plan discharge returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if fx.buys != 1 {
		t.Fatalf("the mark must suppress the plan's REMAINING buys; expected only the pre-mark G1 buy, got %d buys", fx.buys)
	}
	if fx.cargo["G2"] != 0 {
		t.Fatalf("a hull leaving service must never take on a new good, got %d G2 aboard", fx.cargo["G2"])
	}
	if fx.sells != 1 || fx.cargo["G1"] != 0 {
		t.Fatalf("the sell leg that disposes of the hold must still fly; got %d sells, %d G1 aboard", fx.sells, fx.cargo["G1"])
	}
	if planner.calls != 1 {
		t.Fatalf("a discharged plan needs no replacement - the boundary ladder takes the hull; got %d planner calls", planner.calls)
	}
	if !r.RetirementStandDown || r.ExitReason != tourExitRetired {
		t.Fatalf("the drained hull must stand down retired, got stand_down=%v reason=%q", r.RetirementStandDown, r.ExitReason)
	}
	if !logger.infoContains("SELLS ONLY") {
		t.Fatalf("the discharge must log once that the rest of the plan flies sells only")
	}
	if ok, reason := r.CompletionOutcome(); !ok {
		t.Fatalf("a hull that sold everything it bought completes honestly, got veto: %s", reason)
	}
}

// Rung 3. Nothing in the marked hull's own system bids for what it holds, but a reachable one
// does: the ladder jumps ONCE toward that sink and disposes there. It never plans a tour — the
// jump is sell-side cash recovery, not a fresh ground.
func TestTour_RetiringHull_NoLocalBid_ReachesTheSinkAndDisposes(t *testing.T) {
	fx := offloadFixture() // 80 PARTS at X1-O1 (no local bid); X1-O2 IMPORTS them, X1-O3 does not
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{oneLaneRoundTrip(), deadGround()}}
	repo := &retireMarkRepo{tourFakeShipRepo: &tourFakeShipRepo{fx: fx, t: t}, marked: true}
	h := newRetireTourHandler(t, fx, planner, repo)

	resp, err := h.Handle(common.WithLogger(context.Background(), &propFloorCapturingLogger{}), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-REACH", PlayerID: 1, ContainerID: "ctr-reach", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("a retirement reach run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if len(fx.jumps) != 1 || fx.jumps[0] != "X1-O2" {
		t.Fatalf("the ladder must jump exactly once, to the only system that bids for the load; got jumps %v", fx.jumps)
	}
	if fx.cargo["PARTS"] != 0 {
		t.Fatalf("the hull must be drained at the sink it reached, still holding %d PARTS", fx.cargo["PARTS"])
	}
	if fx.buys != 0 || planner.calls != 0 {
		t.Fatalf("a marked hull buys nothing and is never planned a tour; got %d buys, %d planner calls", fx.buys, planner.calls)
	}
	if r.ExitReason != tourExitRetired {
		t.Fatalf("exit reason = %q, want %q once the reached sink drained it", r.ExitReason, tourExitRetired)
	}
}

// TERMINATION. A marked hull holding a good NOTHING within reach bids for cannot be drained, and
// the ladder must end rather than loop on it (the live TORWIND-B shape: 20 firearms nobody buys).
// It stands down naming the residue, so an operator can clear the hold by hand before scrapping
// — the load's reachable liquidation value is zero, so nothing was left on the table.
func TestTour_RetiringHull_UnsellableInReach_StandsDownNamingTheResidue(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{"FIREARMS": 20}, location: "X1-S3-A", cargoCap: 100,
		markets: map[string][]string{"X1-S3": {"X1-S3-A"}},
		ask:     map[string]map[string]int{"X1-S3-A": {"WIDGETS": 50}}, // a market, but no firearms buyer
		tv:      map[string]map[string]int{"X1-S3-A": {"WIDGETS": 1000}},
	}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{oneLaneRoundTrip(), deadGround()}}
	repo := &retireMarkRepo{tourFakeShipRepo: &tourFakeShipRepo{fx: fx, t: t}, marked: true}
	h := newRetireTourHandler(t, fx, planner, repo)
	logger := &propFloorCapturingLogger{}

	resp, err := h.Handle(common.WithLogger(context.Background(), logger), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-STUCK", PlayerID: 1, ContainerID: "ctr-stuck", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("an undrainable retirement must complete honestly, not error; got %v", err)
	}
	r := tourResponse(t, resp)

	if r.ExitReason != tourExitRetiredHolding {
		t.Fatalf("exit reason = %q, want %q", r.ExitReason, tourExitRetiredHolding)
	}
	if r.RetirementResidualUnits != 20 {
		t.Fatalf("the stand-down must report the residue that blocks the scrap, got %d unit(s)", r.RetirementResidualUnits)
	}
	if fx.sells != 0 || fx.buys != 0 || len(fx.jumps) != 0 || planner.calls != 0 {
		t.Fatalf("nothing bids for the load anywhere in reach, so the ladder must do NOTHING and end; got %d sells, %d buys, jumps %v, %d planner calls", fx.sells, fx.buys, fx.jumps, planner.calls)
	}
	if !r.Completed {
		t.Fatalf("standing an undrainable hull down is an HONEST completion so the claim is released, got %+v", r)
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

	if retiring, drained := h.retirementState(context.Background(), cmd); retiring || drained {
		t.Fatalf("an unreadable hull must read as UNMARKED (fail open), got retiring=%v drained=%v", retiring, drained)
	}

	resp, err := h.Handle(common.WithLogger(context.Background(), &propFloorCapturingLogger{}), cmd)
	if err == nil {
		t.Fatalf("an unreadable hull is still the resumable error it always was, got nil")
	}
	if r := tourResponse(t, resp); r.ExitReason == tourExitRetired || r.RetirementStandDown {
		t.Fatalf("an unreadable mark must never stand a hull down, got %+v", r)
	}
}
