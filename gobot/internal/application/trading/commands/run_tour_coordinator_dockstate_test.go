package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// A warehouse deposit orbits the hull to match the warehouse anchor, and deposits sort
// ahead of buys, so a market trade sharing the stop must re-dock before it trades.
func TestTour_MarketTradeAfterDepositAtSameStop_RedocksAndTrades(t *testing.T) {
	depositLeg := func(followUp routing.TourTrade) routing.TourLeg {
		return leg("X1-S1-W", "X1-S1", deposit("ELECTRONICS", 40, 3000), followUp)
	}
	tests := []struct {
		name         string
		legs         []routing.TourLeg
		startCargo   map[string]int
		wantTimeline string
		wantSpent    int64
		wantRevenue  int64
	}{
		{
			name: "buy after deposit",
			legs: []routing.TourLeg{
				depositLeg(buy("MACHINERY", 10, 500)),
				leg("X1-S1-B", "X1-S1", sell("MACHINERY", 10, 900)),
			},
			startCargo:   map[string]int{"ELECTRONICS": 40},
			wantTimeline: "BUY:MACHINERY",
			wantSpent:    10 * 500,
			wantRevenue:  10 * 900,
		},
		{
			name:         "sell after deposit",
			legs:         []routing.TourLeg{depositLeg(sell("MACHINERY", 10, 480))},
			startCargo:   map[string]int{"ELECTRONICS": 40, "MACHINERY": 10},
			wantTimeline: "SELL:MACHINERY",
			wantRevenue:  10 * 480,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fx := &tourFixture{
				cargo: tc.startCargo, location: "X1-S1-W", cargoCap: 80,
				markets: map[string][]string{"X1-S1": {"X1-S1-W", "X1-S1-B"}},
				ask: map[string]map[string]int{
					"X1-S1-W": {"MACHINERY": 500, "ELECTRONICS": 744},
					"X1-S1-B": {"MACHINERY": 950}},
				bid: map[string]map[string]int{
					"X1-S1-W": {"MACHINERY": 480, "ELECTRONICS": 700},
					"X1-S1-B": {"MACHINERY": 900}},
				tv: map[string]map[string]int{
					"X1-S1-W": {"MACHINERY": 10, "ELECTRONICS": 40},
					"X1-S1-B": {"MACHINERY": 10}},
			}
			planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{{
				Feasible: true, ProjectedProfit: 90240, DepositValue: 120000,
				Legs: tc.legs,
			}}}
			h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
			coord := wireWarehouse(t, h, "wh-op", "X1-S1-W", 1000, []string{"ELECTRONICS"})

			resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
				ShipSymbol: "TOUR-1", PlayerID: 1, ContainerID: "ctr-1", ModelArtifactPath: writeTourArtifact(t),
			})
			if err != nil {
				t.Fatalf("tour returned error: %v", err)
			}
			r := tourResponse(t, resp)

			if got := coord.GetTotalCargoAvailable("wh-op", "ELECTRONICS"); got != 40 {
				t.Fatalf("warehouse should hold the 40 deposited ELECTRONICS, got %d", got)
			}
			fx.mu.Lock()
			timeline := strings.Join(fx.timeline, ",")
			fx.mu.Unlock()
			if !strings.Contains(timeline, tc.wantTimeline) {
				t.Fatalf("the trade sharing the deposit stop must execute; timeline=%q want %q", timeline, tc.wantTimeline)
			}
			if r.TotalSpent != tc.wantSpent {
				t.Fatalf("spent %d, want %d", r.TotalSpent, tc.wantSpent)
			}
			if r.TotalRevenue != tc.wantRevenue {
				t.Fatalf("revenue %d, want %d", r.TotalRevenue, tc.wantRevenue)
			}
			if !r.Completed {
				t.Fatalf("tour should complete cleanly, got %+v", r)
			}
		})
	}
}
