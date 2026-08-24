package commands

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// The same-market rebuy guard. A hull whose local margins collapse while it is holding
// cargo sells into the best bid it can reach, and the next plan reads that same market's
// ask as the cheapest source for the good it just dumped — so the hull buys its own dump
// back and pays the spread again. The guard removes the BUY side of any (market, good) the
// SAME hull sold at inside the window, leaving the sink intact so a partly-discharged hold
// can still finish selling there.

// churnFixture is the cycle's shape: one system, two markets, a good with a live ask AND a
// live bid at the same market (EXCHANGE, so the snapshot builder keeps the bid) plus a
// control good and a control market to prove the guard is per-hull, per-market, per-good.
func churnFixture() *tourFixture {
	a, b := "X1-CH1-A", "X1-CH1-B"
	return &tourFixture{
		cargo: map[string]int{}, location: a, cargoCap: 40,
		markets: map[string][]string{"X1-CH1": {a, b}},
		ask: map[string]map[string]int{
			a: {"MACHINERY": 460, "FABRICS": 120},
			b: {"MACHINERY": 300, "FABRICS": 100},
		},
		bid: map[string]map[string]int{
			a: {"MACHINERY": 400, "FABRICS": 110},
			b: {"MACHINERY": 250, "FABRICS": 90},
		},
		tv: map[string]map[string]int{
			a: {"MACHINERY": 20, "FABRICS": 20},
			b: {"MACHINERY": 20, "FABRICS": 20},
		},
		tradeType: map[string]map[string]string{
			a: {"MACHINERY": "EXCHANGE", "FABRICS": "EXCHANGE"},
			b: {"MACHINERY": "EXCHANGE", "FABRICS": "EXCHANGE"},
		},
	}
}

// planChurnOnce drives the shared plan-assembly seam once for the churn hull and returns
// the good universe the coordinator handed the planner.
func planChurnOnce(t *testing.T, h *RunTourCoordinatorHandler, fake *tourFakeRoutingClient, ship string) []routing.TourGoodSnapshot {
	t.Helper()
	cmd := &RunTourCoordinatorCommand{ShipSymbol: ship, PlayerID: 1}
	state := routing.TourShipState{
		ShipSymbol: ship, CurrentWaypoint: "X1-CH1-A", CurrentSystem: "X1-CH1", HoldCapacity: 40,
	}
	before := len(fake.snapshots)
	if _, _, _, err := h.planForState(
		context.Background(), state, []string{"X1-CH1"}, cmd, tourPlanBudget{maxHops: 6, maxSpend: 1_000_000},
	); err != nil {
		t.Fatalf("planForState: %v", err)
	}
	if len(fake.snapshots) != before+1 {
		t.Fatalf("expected exactly one planner call, got %d", len(fake.snapshots)-before)
	}
	return fake.snapshots[len(fake.snapshots)-1]
}

// snapshotAsk reports the buy-side quote the planner was offered for one (market, good),
// and whether the row reached it at all.
func snapshotAsk(rows []routing.TourGoodSnapshot, waypoint, good string) (int, bool) {
	for _, r := range rows {
		if r.Waypoint == waypoint && r.Good == good {
			return r.Ask, true
		}
	}
	return 0, false
}

func snapshotBid(rows []routing.TourGoodSnapshot, waypoint, good string) int {
	for _, r := range rows {
		if r.Waypoint == waypoint && r.Good == good {
			return r.Bid
		}
	}
	return 0
}

// The churn kill. The hull sold the good at this market moments ago, so the planner must
// not be offered it as a buy source there — while the sink at the same market survives,
// because refusing to sell the rest of a hold is not what this guard is for.
func TestTourRebuyGuard_DropsTheBuySideOfAMarketThisHullJustSold(t *testing.T) {
	fx := churnFixture()
	fake := &tourFakeRoutingClient{plans: []*routing.TourPlan{{Feasible: true}}}
	h := newTourHandler(t, fx, fake, nil)
	h.noteRecentSell("CHURN-1", "X1-CH1-A", "MACHINERY")

	rows := planChurnOnce(t, h, fake, "CHURN-1")

	if ask, ok := snapshotAsk(rows, "X1-CH1-A", "MACHINERY"); !ok || ask != 0 {
		t.Fatalf("the just-sold market must not be offered as a MACHINERY buy source, ask=%d present=%v", ask, ok)
	}
	if bid := snapshotBid(rows, "X1-CH1-A", "MACHINERY"); bid <= 0 {
		t.Fatalf("the sink at the same market must survive so a partial hold can finish selling, bid=%d", bid)
	}
}

// The guard is per-market and per-good: the same hull may still source the good elsewhere
// and may still source other goods at the market it sold into.
func TestTourRebuyGuard_LeavesOtherMarketsAndOtherGoodsAlone(t *testing.T) {
	fx := churnFixture()
	fake := &tourFakeRoutingClient{plans: []*routing.TourPlan{{Feasible: true}}}
	h := newTourHandler(t, fx, fake, nil)
	h.noteRecentSell("CHURN-1", "X1-CH1-A", "MACHINERY")

	rows := planChurnOnce(t, h, fake, "CHURN-1")

	if ask, _ := snapshotAsk(rows, "X1-CH1-B", "MACHINERY"); ask <= 0 {
		t.Errorf("the same good at ANOTHER market must stay a buy source, ask=%d", ask)
	}
	if ask, _ := snapshotAsk(rows, "X1-CH1-A", "FABRICS"); ask <= 0 {
		t.Errorf("another good at the SAME market must stay a buy source, ask=%d", ask)
	}
}

// And per-hull: one hull's sale never bars another hull from sourcing there.
func TestTourRebuyGuard_IsPerHull(t *testing.T) {
	fx := churnFixture()
	fake := &tourFakeRoutingClient{plans: []*routing.TourPlan{{Feasible: true}, {Feasible: true}}}
	h := newTourHandler(t, fx, fake, nil)
	h.noteRecentSell("CHURN-1", "X1-CH1-A", "MACHINERY")

	rows := planChurnOnce(t, h, fake, "CHURN-2")

	if ask, _ := snapshotAsk(rows, "X1-CH1-A", "MACHINERY"); ask <= 0 {
		t.Fatalf("another hull must still be able to source MACHINERY at X1-CH1-A, ask=%d", ask)
	}
}

// No record for the hull is the fail-OPEN direction: the good universe is the same slice
// the snapshot builder produced, so an unrecorded history plans exactly as before.
func TestTourRebuyGuard_NoRecordIsAByteIdenticalNoOp(t *testing.T) {
	fx := churnFixture()
	fake := &tourFakeRoutingClient{plans: []*routing.TourPlan{{Feasible: true}}}
	h := newTourHandler(t, fx, fake, nil)

	rows := planChurnOnce(t, h, fake, "CHURN-1")

	if ask, _ := snapshotAsk(rows, "X1-CH1-A", "MACHINERY"); ask <= 0 {
		t.Fatalf("with no recorded sale every buy source must survive, ask=%d", ask)
	}
}

// The window expires: once the market has had time to recover, the hull may source there
// again. Driven through the pure core so the boundary is exact rather than wall-clock.
func TestRecentRebuySources_ExpiresWithTheWindow(t *testing.T) {
	h := &RunTourCoordinatorHandler{}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h.recordSellAt("CHURN-1", "X1-CH1-A", "MACHINERY", now)

	window := 30 * time.Minute
	key := marketGood{waypoint: "X1-CH1-A", good: "MACHINERY"}

	if blocked := h.recentRebuySources("CHURN-1", now.Add(window-time.Minute), window); !blocked[key] {
		t.Errorf("inside the window the buy source must be blocked, got %v", blocked)
	}
	if blocked := h.recentRebuySources("CHURN-1", now.Add(window+time.Minute), window); blocked[key] {
		t.Errorf("past the window the buy source must be allowed again, got %v", blocked)
	}
}

// The pure filter: only the named buy sources lose their ask, every other row is carried
// through untouched, and an empty block set returns the SAME slice (a true no-op).
func TestDropRecentRebuySources(t *testing.T) {
	in := []routing.TourGoodSnapshot{
		{Waypoint: "W", Good: "MACHINERY", Ask: 460, Bid: 400},
		{Waypoint: "W", Good: "FABRICS", Ask: 120, Bid: 110},
		{Waypoint: "V", Good: "MACHINERY", Ask: 300, Bid: 250},
	}

	got, dropped := dropRecentRebuySources(in, map[marketGood]bool{{waypoint: "W", good: "MACHINERY"}: true})

	if dropped != 1 {
		t.Errorf("expected exactly one dropped buy source, got %d", dropped)
	}
	if ask, _ := snapshotAsk(got, "W", "MACHINERY"); ask != 0 {
		t.Errorf("the blocked buy source must lose its ask, got %d", ask)
	}
	if snapshotBid(got, "W", "MACHINERY") != 400 {
		t.Errorf("the blocked source must keep its sink bid")
	}
	if ask, _ := snapshotAsk(got, "V", "MACHINERY"); ask != 300 {
		t.Errorf("an unblocked market must be untouched, got %d", ask)
	}
	if in[0].Ask != 460 {
		t.Errorf("the filter must not mutate the caller's rows")
	}

	for _, empty := range []map[marketGood]bool{nil, {}} {
		same, n := dropRecentRebuySources(in, empty)
		if n != 0 || reflect.ValueOf(same).Pointer() != reflect.ValueOf(in).Pointer() {
			t.Fatalf("an empty block set must return the same slice untouched")
		}
	}
}

// End to end: a hull that SELLS a good on a flown leg cannot be handed that market as a
// buy source on the plan that follows. This is the wiring the ledger evidence needs — the
// record is written by the executor, not by the test.
func TestTourRebuyGuard_ASoldLegBarsTheRebuyOnTheNextPlan(t *testing.T) {
	fx := churnFixture()
	fx.cargo = map[string]int{"MACHINERY": 20}
	dump := &routing.TourPlan{Feasible: true, ProjectedProfit: 1, Legs: []routing.TourLeg{
		leg("X1-CH1-A", "X1-CH1", sell("MACHINERY", 20, 400)),
	}}
	fake := &tourFakeRoutingClient{plans: []*routing.TourPlan{dump, {Feasible: false, InfeasibleReason: "no_profitable_tour"}}}
	h := newTourHandler(t, fx, fake, &tourFakeTelemetry{})

	if _, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "CHURN-1", PlayerID: 1, ContainerID: "ctr-churn", Iterations: 2,
		ModelArtifactPath: writeTourArtifact(t),
	}); err != nil {
		t.Fatalf("tour returned error: %v", err)
	}
	if len(fake.snapshots) < 2 {
		t.Fatalf("expected a second plan after the sale, got %d planner calls", len(fake.snapshots))
	}

	after := fake.snapshots[len(fake.snapshots)-1]
	if ask, _ := snapshotAsk(after, "X1-CH1-A", "MACHINERY"); ask != 0 {
		t.Fatalf("the market the hull just sold MACHINERY into must not be offered back as a source, ask=%d", ask)
	}
}

// The window is an operator lever, not a constant: a tuned value on the live trade-fleet
// surface governs, and an untuned surface resolves the documented default.
func TestRebuyWindow_ResolvesFromTheLiveTuneSurface(t *testing.T) {
	h := &RunTourCoordinatorHandler{}
	if got := h.rebuyWindow(context.Background(), 1); got != defaultSameMarketRebuyWindowMinutes*time.Minute {
		t.Errorf("unwired resolver must give the documented default, got %s", got)
	}

	h.SetMarketFreshness(NewMarketFreshness(nil, &fakeFloorSource{
		config: liveconfig.Snapshot{TuneKeySameMarketRebuyWindowMinutes: 90},
	}, nil))
	if got := h.rebuyWindow(context.Background(), 1); got != 90*time.Minute {
		t.Errorf("a tuned window must govern, got %s", got)
	}
}
