package commands

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// A live ask that drifted up inside the tourPriceTolerancePct band still buys: the
// per-tranche ceiling is the tolerated ask and nothing narrower.
func TestTourTrades_BuyProceedsWhenTheAskDriftsInsideTheToleranceBand(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B"}},
		bid:     map[string]map[string]int{"X1-S1-B": {"G": 300}},
		ask:     map[string]map[string]int{"X1-S1-A": {"G": 108}, "X1-S1-B": {"G": 400}},
		tv:      map[string]map[string]int{"X1-S1-A": {"G": 1000}, "X1-S1-B": {"G": 1000}},
	}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{{
		Feasible: true, Legs: []routing.TourLeg{
			leg("X1-S1-A", "X1-S1", buy("G", 40, 100)),
			leg("X1-S1-B", "X1-S1", sell("G", 40, 300)),
		},
	}}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	if _, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-BAND-1", PlayerID: 1, ContainerID: "ctr-band-1",
		ModelArtifactPath: writeTourArtifact(t),
	}); err != nil {
		t.Fatalf("tour returned error: %v", err)
	}
	if fx.buys != 1 {
		t.Fatalf("an ask of 108 against a planned 100 is inside the band and must buy, got %d buys", fx.buys)
	}
}
