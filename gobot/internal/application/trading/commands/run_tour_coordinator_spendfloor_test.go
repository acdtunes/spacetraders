package commands

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// sp-agzj (P1 money-guard): a tour tranche buy re-reads the LIVE balance immediately
// before purchasing and SHRINKS the tranche to the units the working-capital reserve
// can still afford, rather than the old all-or-nothing skip. This suite drives
// executeBuy through the real fixture with a live (fake) apiClient and asserts the four
// outcomes: shrink-to-floor, skip-when-even-one-unit-pierces, fail-closed-on-unreadable,
// and the concurrency shape (a plan sized against high plan-time treasury still binds the
// floor at execution against the live — dropped — balance).

// tourSeqAPIClient returns a scripted SEQUENCE of live balances so a test can distinguish
// the plan-time treasury read (defaultMaxSpend, call #1 under a dynamic cap) from the
// later buy-time floor read (reserveHeadroom, call #2+). The last balance repeats.
type tourSeqAPIClient struct {
	domainPorts.APIClient
	mu       sync.Mutex
	balances []int
	calls    int
}

func (c *tourSeqAPIClient) GetAgent(_ context.Context, _ string) (*player.AgentData, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	i := c.calls
	if i >= len(c.balances) {
		i = len(c.balances) - 1
	}
	c.calls++
	return &player.AgentData{Credits: c.balances[i]}, nil
}

// tourErrAPIClient always fails the live treasury read — the buy-time floor must fail
// CLOSED (no spend) when it cannot read the balance (RULINGS #4).
type tourErrAPIClient struct{ domainPorts.APIClient }

func (c *tourErrAPIClient) GetAgent(_ context.Context, _ string) (*player.AgentData, error) {
	return nil, errors.New("agent API unavailable (simulated buy-time floor read failure)")
}

// floorRoundTripFixture: buy G at A, sell G at B. Ask 1000 at A, bid 1200 at B, ample
// hold and trade volume so ONLY the working-capital floor can bind the tranche.
func floorRoundTripFixture() *tourFixture {
	return &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 200,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B"}},
		bid:     map[string]map[string]int{"X1-S1-B": {"G": 1200}},
		ask:     map[string]map[string]int{"X1-S1-A": {"G": 1000}, "X1-S1-B": {"G": 1200}},
		tv:      map[string]map[string]int{"X1-S1-A": {"G": 1000}, "X1-S1-B": {"G": 1000}},
	}
}

func floorRoundTripPlan(buyUnits int) *routing.TourPlan {
	return &routing.TourPlan{Feasible: true, Legs: []routing.TourLeg{
		leg("X1-S1-A", "X1-S1", buy("G", buyUnits, 1000)),
		leg("X1-S1-B", "X1-S1", sell("G", buyUnits, 1200)),
	}}
}

// Shrink: live balance 1,090,000, reserve 1,000,000 → 90,000 headroom / 1,000 ask = 90
// affordable units. A planned 100-unit buy is SHRUNK to 90 (not skipped, not bought
// whole). The old skip-only guard bought 0 here, so this is the behavior delta.
func TestTour_BuyFloor_ShrinksTrancheToFloorRespectingUnits(t *testing.T) {
	fx := floorRoundTripFixture()
	api := &tourSeqAPIClient{balances: []int{1_090_000}}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{floorRoundTripPlan(100)}}
	h := newTourHandlerWithAPI(t, fx, planner, &tourFakeTelemetry{}, api)

	ctx := auth.WithPlayerToken(context.Background(), "TOUR-SHRINK")
	resp, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-SHRINK", PlayerID: 1, ContainerID: "ctr-shrink",
		MaxSpend: 10_000_000, WorkingCapitalReserve: 1_000_000, // fixed cap → only the floor can bind
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("a floor-shrunk buy must not error: %v", err)
	}
	r := tourResponse(t, resp)

	if fx.buys != 1 {
		t.Fatalf("expected exactly ONE buy dispatch (shrunk, not skipped), got %d", fx.buys)
	}
	if r.TotalSpent != 90*1000 {
		t.Fatalf("expected the tranche shrunk to 90 units (spend 90,000), got spend %d — a whole 100-unit buy (100,000) breaches the 1M floor, a skip spends 0", r.TotalSpent)
	}
	if r.CargoStranded {
		t.Fatalf("the shrunk 90 units were sold at B — must not strand: %s", r.CargoStrandedReason)
	}
	if !r.Completed {
		t.Fatalf("a shrunk round trip completes cleanly, got %+v", r)
	}
}

// Skip: live balance 1,000,500, reserve 1,000,000 → 500 headroom / 1,000 ask = 0
// affordable units. Even one unit pierces the floor, so the buy is SKIPPED entirely
// (zero spend), the leg degrades, the tour re-plans.
func TestTour_BuyFloor_SkipsWhenEvenOneUnitPierces(t *testing.T) {
	fx := floorRoundTripFixture()
	api := &tourSeqAPIClient{balances: []int{1_000_500}}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{
		floorRoundTripPlan(100),
		{Feasible: false, InfeasibleReason: "no_profitable_tour"},
	}}
	h := newTourHandlerWithAPI(t, fx, planner, &tourFakeTelemetry{}, api)

	ctx := auth.WithPlayerToken(context.Background(), "TOUR-SKIP")
	resp, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-SKIP", PlayerID: 1, ContainerID: "ctr-skip",
		MaxSpend: 10_000_000, WorkingCapitalReserve: 1_000_000,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("a floor-skipped buy must not error: %v", err)
	}
	r := tourResponse(t, resp)

	if fx.buys != 0 {
		t.Fatalf("expected ZERO buys (even one unit pierces the floor), got %d", fx.buys)
	}
	if r.TotalSpent != 0 {
		t.Fatalf("a fully-skipped buy spends nothing, got %d", r.TotalSpent)
	}
}

// Unreadable balance at buy time fails CLOSED: no spend, no error, the loop continues
// (the container is never killed by a transient treasury blip — RULINGS #4).
func TestTour_BuyFloor_UnreadableBalanceFailsClosedNoSpendNoDeath(t *testing.T) {
	fx := floorRoundTripFixture()
	api := &tourErrAPIClient{}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{
		floorRoundTripPlan(100),
		{Feasible: false, InfeasibleReason: "no_profitable_tour"},
	}}
	h := newTourHandlerWithAPI(t, fx, planner, &tourFakeTelemetry{}, api)

	ctx := auth.WithPlayerToken(context.Background(), "TOUR-BLIND")
	resp, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-BLIND", PlayerID: 1, ContainerID: "ctr-blind",
		MaxSpend: 10_000_000, WorkingCapitalReserve: 1_000_000, // fixed cap → the only GetAgent is the buy-time floor read
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("an unreadable buy-time balance must fail closed gracefully, not error the run: %v", err)
	}
	r := tourResponse(t, resp)

	if fx.buys != 0 {
		t.Fatalf("a blind buy-time floor must dispatch ZERO buys (fail-closed), got %d", fx.buys)
	}
	if r.TotalSpent != 0 {
		t.Fatalf("a fail-closed buy spends nothing, got %d", r.TotalSpent)
	}
}

// Concurrency shape (the sp-agzj incident class): the tour is PLANNED against a high
// plan-time treasury (8,000,000 → a 2,000,000 dynamic cap that comfortably admits the
// 100-unit buy), but by execution the live balance has DROPPED to 1,090,000 (a sibling
// hull drained the shared treasury). The floor binds at EXECUTION against the live
// balance — the tranche shrinks to the 90 units that 90,000 of headroom allows — proving
// the guard reads the balance at buy time, not at plan time.
func TestTour_BuyFloor_HoldsAtExecutionWhenTreasuryDropsAfterPlanning(t *testing.T) {
	fx := floorRoundTripFixture()
	// call #1 = plan-time defaultMaxSpend read (high), call #2 = buy-time floor read (low).
	api := &tourSeqAPIClient{balances: []int{8_000_000, 1_090_000}}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{floorRoundTripPlan(100)}}
	h := newTourHandlerWithAPI(t, fx, planner, &tourFakeTelemetry{}, api)

	ctx := auth.WithPlayerToken(context.Background(), "TOUR-RACE")
	resp, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-RACE", PlayerID: 1, ContainerID: "ctr-race",
		MaxSpend: 0, WorkingCapitalReserve: 1_000_000, // dynamic cap: 25% of the 8M plan-time treasury = 2M
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("tour returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if len(planner.maxSpends) == 0 || planner.maxSpends[0] != 2_000_000 {
		t.Fatalf("the plan must be sized against the HIGH plan-time treasury (cap 2,000,000), got %v", planner.maxSpends)
	}
	if fx.buys != 1 {
		t.Fatalf("expected one shrunk buy, got %d", fx.buys)
	}
	if r.TotalSpent != 90*1000 {
		t.Fatalf("the floor must bind at EXECUTION against the dropped live balance (spend 90,000 = 90 units), got %d — 100,000 would mean the floor used the stale plan-time balance", r.TotalSpent)
	}
}
