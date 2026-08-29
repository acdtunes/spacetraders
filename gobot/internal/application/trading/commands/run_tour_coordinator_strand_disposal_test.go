package commands

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// torwind348Fixture is the incident, to the waypoint. TORWIND-348 sourced 105 CLOTHING at
// X1-PM17-X11A — the EXPORT market it bought from, whose only CLOTHING quote is an exporter's
// sellback, never a sink — and its margins died there. X1-HY46, one gate hop away, IMPORTS
// CLOTHING; X1-KY37, also one hop away, does not trade it at all.
//
// Three details make this the shape the previous code released loaded: the tour BOUGHT the load,
// so the honest-completion contract vetoes the container; 105 of 240 units is 43.75%, NOT laden
// by isLadenForOffload's `units*100 > capacity*50`, so neither the offload nor the distress dump
// could rescue it; and nothing in X1-PM17 bids, so the exit sweep had nothing to sell either.
func torwind348Fixture() *tourFixture {
	return &tourFixture{
		cargo: map[string]int{}, location: "X1-PM17-X11A", cargoCap: 240,
		markets: map[string][]string{
			"X1-PM17": {"X1-PM17-X11A"},
			"X1-HY46": {"X1-HY46-A"},
			"X1-KY37": {"X1-KY37-A"},
		},
		ask: map[string]map[string]int{
			"X1-PM17-X11A": {"CLOTHING": 300},
			"X1-HY46-A":    {"CLOTHING": 4600},
			"X1-KY37-A":    {"ORE": 50},
		},
		bid: map[string]map[string]int{
			"X1-PM17-X11A": {"CLOTHING": 290}, // the source exporter's sellback — never a sink
			"X1-HY46-A":    {"CLOTHING": 4500},
			"X1-KY37-A":    {"ORE": 40},
		},
		tradeType: map[string]map[string]string{
			"X1-PM17-X11A": {"CLOTHING": "EXPORT"},
			"X1-HY46-A":    {"CLOTHING": "IMPORT"},
			"X1-KY37-A":    {"ORE": "IMPORT"},
		},
		tv: map[string]map[string]int{
			"X1-PM17-X11A": {"CLOTHING": 1000},
			"X1-HY46-A":    {"CLOTHING": 1000},
			"X1-KY37-A":    {"ORE": 1000},
		},
		neighbors: map[string][]string{"X1-PM17": {"X1-HY46", "X1-KY37"}},
	}
}

// torwind348Planner sources the load at X1-PM17 and then finds nothing anywhere: the margins-death
// shape, with the hull left holding what it bought.
func torwind348Planner() *tourFakeRoutingClient {
	return &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		if ship.CurrentSystem == "X1-PM17" && len(ship.Cargo) == 0 {
			return &routing.TourPlan{Feasible: true, Legs: []routing.TourLeg{
				leg("X1-PM17-X11A", "X1-PM17", buy("CLOTHING", 105, 300)),
			}}
		}
		return infeasibleTour()
	}}
}

// THE INCIDENT (sp-b9alf). Margins died on a hull holding cargo its own system will not bid for,
// while one gate hop away a market imports exactly that good. Every rescue above declined, so the
// run released the hull LOADED, the honest-completion contract refused, and the container FAILED.
//
// The hull must instead descend the disposal ladder before it is released: nothing here bids, so
// reach the system that does and sell there. The veto is not weakened — it has nothing left to
// fire on, which is the only honest way to silence it.
func TestTour_MarginsDeath_NoLocalBid_ReachesTheSinkAndDrainsBeforeRelease(t *testing.T) {
	fx := torwind348Fixture()
	planner := torwind348Planner()
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	logger := &propFloorCapturingLogger{}

	resp, err := h.Handle(common.WithLogger(context.Background(), logger), &RunTourCoordinatorCommand{
		ShipSymbol: "TORWIND-348", PlayerID: 1, ContainerID: "ctr-torwind-348", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("a margins-death exit must be a clean terminal exit, got error: %v", err)
	}
	r := tourResponse(t, resp)

	// The defect, stated as an assertion: the hold must be empty at release.
	if fx.cargo["CLOTHING"] != 0 {
		t.Fatalf("TORWIND-348 must not be released holding a load a reachable market bids for; still holding %d CLOTHING at %s", fx.cargo["CLOTHING"], fx.location)
	}
	// It must have REACHED the sink, not merely emptied somehow: X1-KY37 is the witness — it is
	// equally one hop away and never bid for CLOTHING, so a ladder that ranked on anything but
	// held-cargo absorption could have gone there.
	if len(fx.jumps) != 1 || fx.jumps[0] != "X1-HY46" {
		t.Fatalf("the ladder must jump exactly once, to the only reachable system that bids for the load; got jumps %v", fx.jumps)
	}
	if shared.ExtractSystemSymbol(fx.location) != "X1-HY46" {
		t.Fatalf("the hull must end at the sink system X1-HY46, got %q", fx.location)
	}
	// The ladder must be CONSULTED, not merely coincident with an empty hold: this counter moves
	// only inside it, and no plan after the source buy could have sold anything.
	if r.StrandDisposalSales != 1 {
		t.Fatalf("the drain must have cleared exactly one good, got StrandDisposalSales=%d (a zero here means the ladder never ran)", r.StrandDisposalSales)
	}
	if r.TotalRevenue != 105*4500 {
		t.Fatalf("the whole load must be booked at the sink's 4,500/u bid, got %d", r.TotalRevenue)
	}
	// The point of the fix: the container COMPLETES instead of failing the veto, so the hull is
	// released clean and the fleet coordinator relaunches it on the base cooldown, not the
	// doubled fast-fail one.
	if ok, reason := r.CompletionOutcome(); !ok {
		t.Fatalf("a hull whose hold was drained before release must complete honestly, got veto: %s", reason)
	}
	if r.CargoStranded {
		t.Fatalf("nothing is stranded once the hold is clear: %+v", r)
	}
	if warningContains(logger, "is being released still holding") {
		t.Fatal("a drained hull must not also report a residual strand")
	}
	if r.ExitReason != tourExitStarvation {
		t.Fatalf("exit reason = %q, want %q (the sink system is fresh-arb-dead too, so the run still exits honestly after the drain)", r.ExitReason, tourExitStarvation)
	}
	// The ladder disposes and stops. It never buys — the source buy is the only purchase this
	// run may ever make.
	if fx.buys != 1 {
		t.Fatalf("the disposal ladder must never buy; expected only the source buy, got %d buys", fx.buys)
	}
}

// A drained hull RESUMES rather than standing down: retirement stands down because leaving service
// is the point, but an ordinary hull that just cleared its hold is ready to work, and after a reach
// it stands where the markets are. Here X1-HY46 has a live lane, so the hull that drained there
// must go on to fly it — the run ends on iterations/starvation with a productive tour to its name,
// not on the boundary the drain happened at.
func TestTour_MarginsDeath_DrainedHullKeepsTouringAtTheGroundItReached(t *testing.T) {
	fx := torwind348Fixture()
	// X1-HY46 also EXPORTS ORE that X1-HY46-B imports: a real lane, discoverable only by a hull
	// that is still touring when it gets there.
	fx.markets["X1-HY46"] = []string{"X1-HY46-A", "X1-HY46-B"}
	fx.ask["X1-HY46-A"]["ORE"] = 100
	fx.ask["X1-HY46-B"] = map[string]int{"ORE": 500}
	fx.bid["X1-HY46-B"] = map[string]int{"ORE": 450}
	fx.tradeType["X1-HY46-B"] = map[string]string{"ORE": "IMPORT"}
	fx.tv["X1-HY46-A"]["ORE"] = 1000
	fx.tv["X1-HY46-B"] = map[string]int{"ORE": 1000}

	// The lane exists only for a hull that is REALLY standing at X1-HY46, never for the
	// candidate pre-flight the fresh-arb ranking runs from X1-PM17 (which clears cargo and prices
	// each neighbour in place). Otherwise the rescue above would jump there on fresh arb and the
	// ladder — the thing under test — would never run.
	hy46Plans := 0
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		fx.mu.Lock()
		standingAt := shared.ExtractSystemSymbol(fx.location)
		fx.mu.Unlock()
		if standingAt == "X1-PM17" && len(ship.Cargo) == 0 && ship.CurrentSystem == "X1-PM17" {
			return &routing.TourPlan{Feasible: true, Legs: []routing.TourLeg{
				leg("X1-PM17-X11A", "X1-PM17", buy("CLOTHING", 105, 300)),
			}}
		}
		// The lane at the ground the drain reached — offered ONCE, so the run still ends.
		if standingAt == "X1-HY46" && ship.CurrentSystem == "X1-HY46" && len(ship.Cargo) == 0 {
			hy46Plans++
			if hy46Plans == 1 {
				return &routing.TourPlan{Feasible: true, Legs: []routing.TourLeg{
					leg("X1-HY46-A", "X1-HY46", buy("ORE", 40, 100)),
					leg("X1-HY46-B", "X1-HY46", sell("ORE", 40, 450)),
				}}
			}
		}
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TORWIND-348R", PlayerID: 1, ContainerID: "ctr-torwind-348r", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.ToursCompleted != 2 {
		t.Fatalf("a drained hull must keep touring at the ground it reached: expected the source tour plus the lane it found at X1-HY46 (2), got %d", r.ToursCompleted)
	}
	if fx.cargo["CLOTHING"] != 0 || fx.cargo["ORE"] != 0 {
		t.Fatalf("both loads must have been sold, got %+v", fx.cargo)
	}
	if ok, reason := r.CompletionOutcome(); !ok {
		t.Fatalf("the run completes honestly, got veto: %s", reason)
	}
}

// TERMINATION, the property sp-58zaj's ladder was built around and this caller must preserve
// exactly: a hull holding a good NOTHING within reach bids for cannot be drained, so the ladder
// must end rather than hunt. It sells nothing, spends no jump (no candidate absorbs any of the
// residue), and the run exits releasing the hull loaded and failing the veto honestly — which
// remains the right outcome for a load nobody will buy.
func TestTour_MarginsDeath_UnsellableInReach_ReleasesLoadedAndFailsHonestly(t *testing.T) {
	fx := torwind348Fixture()
	// The one reachable sink stops bidding for CLOTHING; nothing else ever did.
	delete(fx.bid["X1-HY46-A"], "CLOTHING")
	delete(fx.ask["X1-HY46-A"], "CLOTHING")
	delete(fx.tradeType["X1-HY46-A"], "CLOTHING")
	fx.ask["X1-HY46-A"]["ORE"] = 50
	fx.bid["X1-HY46-A"]["ORE"] = 40
	fx.tradeType["X1-HY46-A"]["ORE"] = "IMPORT"
	fx.tv["X1-HY46-A"]["ORE"] = 1000

	planner := torwind348Planner()
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	logger := &propFloorCapturingLogger{}

	resp, err := h.Handle(common.WithLogger(context.Background(), logger), &RunTourCoordinatorCommand{
		ShipSymbol: "TORWIND-B", PlayerID: 1, ContainerID: "ctr-torwind-b", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("an undrainable margins-death must complete the loop honestly, not error; got %v", err)
	}
	r := tourResponse(t, resp)

	if len(fx.jumps) != 0 {
		t.Fatalf("no reachable market bids for the load, so the ladder must spend no jump; got %v", fx.jumps)
	}
	if r.StrandDisposalSales != 0 || fx.sells != 0 {
		t.Fatalf("there is nothing to sell anywhere in reach; got StrandDisposalSales=%d, %d sells", r.StrandDisposalSales, fx.sells)
	}
	if fx.cargo["CLOTHING"] != 105 {
		t.Fatalf("the undrainable load stays aboard, got %+v", fx.cargo)
	}
	// The honest-completion contract is untouched: a run that ends with unsold cargo it bought
	// still refuses to report success.
	if !r.CargoStranded {
		t.Fatalf("a hull released holding cargo THIS RUN bought must still veto the completion, got %+v", r)
	}
	if ok, _ := r.CompletionOutcome(); ok {
		t.Fatal("the honest-completion veto must still fire when the ladder cannot clear the hold")
	}
	if !warningContains(logger, "is being released still holding 105 CLOTHING") {
		t.Fatal("an undrainable residue must still be named at WARNING so the strand is greppable")
	}
}

// The RepositionDisabled kill-switch means the operator opted this hull out of MOVING. The drain
// must honour it exactly as the exit sweep does: no reach hop, and it sells only where the hull
// already stands — which here is nothing, so the run exits releasing it loaded.
func TestTour_MarginsDeathDrain_RespectsRepositionKillSwitch(t *testing.T) {
	fx := torwind348Fixture()
	planner := torwind348Planner()
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TORWIND-348K", PlayerID: 1, ContainerID: "ctr-torwind-348k", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t), RepositionDisabled: true,
	})
	if err != nil {
		t.Fatalf("kill-switch run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if len(fx.jumps) != 0 {
		t.Fatalf("the kill-switch forbids moving the hull, got jumps=%v", fx.jumps)
	}
	if r.StrandDisposalSales != 0 || fx.cargo["CLOTHING"] != 105 {
		t.Fatalf("under the switch the drain may only sell where the hull stands, and X1-PM17 has no bid; got StrandDisposalSales=%d cargo=%+v", r.StrandDisposalSales, fx.cargo)
	}
}
