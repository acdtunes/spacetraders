package commands

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// distressFixture is the sp-2v69u TERTIARY world: a heavy stuck LADEN (80/100 ORE) on home
// X1-D1, a genuinely dead ground with no profitable fresh tour anywhere. Its only jump neighbor
// X1-D2 has a market but NEVER buys ORE, so the held-cargo offload finds no reachable sink and
// declines — exactly the corner the PRIMARY offload cannot rescue. But the hull's OWN system has
// a below-floor ORE buyer at a DIFFERENT waypoint (X1-D1-B imports ORE at a near-worthless bid
// of 100) — a ground the offload never even evaluates (its candidate set is the current system's
// jump NEIGHBORS, never the current system itself). The hull is parked at X1-D1-A, which has a
// market that does not buy ORE. Only a last-resort distress liquidation can free this hull.
func distressFixture() *tourFixture {
	return &tourFixture{
		cargo: map[string]int{"ORE": 80}, location: "X1-D1-A", cargoCap: 100,
		markets: map[string][]string{
			"X1-D1": {"X1-D1-A", "X1-D1-B"},
			"X1-D2": {"X1-D2-A"},
		},
		ask: map[string]map[string]int{
			"X1-D1-A": {"WIDGETS": 50}, // a market exists where the hull sits, but it never buys ORE
			"X1-D1-B": {"ORE": 999},    // the local ORE buyer (a DIFFERENT waypoint, same system)
			"X1-D2-A": {"WIDGETS": 50}, // the only neighbor: no ORE sink -> offload declines
		},
		bid: map[string]map[string]int{
			"X1-D1-B": {"ORE": 100}, // near-worthless, BELOW any profit floor — a distress-only price
		},
		tradeType: map[string]map[string]string{
			"X1-D1-B": {"ORE": "IMPORT"}, // a real sink (non-EXPORT), so distress ranking accepts its bid
		},
		tv: map[string]map[string]int{
			"X1-D1-A": {"WIDGETS": 1000},
			"X1-D1-B": {"ORE": 1000},
			"X1-D2-A": {"WIDGETS": 1000},
		},
		neighbors: map[string][]string{"X1-D1": {"X1-D2"}},
	}
}

func navDestsContain(dests []string, want string) bool {
	for _, d := range dests {
		if d == want {
			return true
		}
	}
	return false
}

// THE sp-2v69u TERTIARY unlock. A laden heavy (80/100 ORE) whose margins die with NO profitable
// fresh tour anywhere AND no reachable OTHER-system sink for its load (the fresh-arb reposition
// and the held-cargo offload both decline) must not churn relaunch-forever full. As a last
// resort it distress-liquidates its held cargo at the best AVAILABLE local bid — X1-D1-B's
// near-worthless ORE import (100/u, well below the profit floor) in its OWN system — so it
// re-enters planning EMPTY. Unguarded this hull exits Completed=true still holding 80 ORE (its
// pre-held cargo never entered netBought, so the honest-completion stranded veto never fired),
// the coordinator relaunched a new tour container on the STILL-FULL hull, and it churned forever.
func TestTour_MarginsDeath_LadenHullNoReachableSink_DistressLiquidatesLocally(t *testing.T) {
	fx := distressFixture()
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		return infeasibleTour() // no fresh-arb tour anywhere - only a distress liquidation can free the hull
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-DISTRESS", PlayerID: 1, ContainerID: "ctr-distress", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("distress liquidation run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.DistressLiquidations != 1 {
		t.Fatalf("expected exactly one distress liquidation, got %d (%+v)", r.DistressLiquidations, r)
	}
	if fx.sells != 1 {
		t.Fatalf("expected exactly one distress sale, got %d sells", fx.sells)
	}
	if fx.cargo["ORE"] != 0 {
		t.Fatalf("the held ORE must be fully liquidated (hull freed), still holding %d", fx.cargo["ORE"])
	}
	if !navDestsContain(fx.navDests, "X1-D1-B") {
		t.Fatalf("the hull must navigate to the in-system sink X1-D1-B to dump its load, navDests=%v", fx.navDests)
	}
	if len(fx.jumps) != 0 {
		t.Fatalf("a distress liquidation is an INTRA-system last resort — it must never jump, got jumps %v", fx.jumps)
	}
	if r.ExitReason != tourExitStarvation {
		t.Fatalf("exit reason = %q, want %q (the ground is still fresh-arb-dead, so the run exits honestly after freeing the hull)", r.ExitReason, tourExitStarvation)
	}
}

// The PRIMARY held-cargo offload must WIN over the TERTIARY distress liquidation whenever it
// finds a reachable sink: distress is the LAST resort, tried only after offload itself returns
// repositioned=false. In the offload world X1-O2 IMPORTS the held PARTS at a rich bid, so the
// offload jumps there (moving toward a real buyer beats a below-floor local dump) and the
// distress pass never engages. This proves TERTIARY does not pre-empt the offload.
func TestTour_MarginsDeath_ReachableSink_OffloadsNotDistress(t *testing.T) {
	fx := offloadFixture()
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		return infeasibleTour() // no fresh-arb tour - the rescue is offload's to make, not distress's
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-OFFLOAD-WINS", PlayerID: 1, ContainerID: "ctr-offload-wins", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("offload-wins run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.Repositions != 1 || len(fx.jumps) != 1 || fx.jumps[0] != "X1-O2" {
		t.Fatalf("the offload must jump to the reachable sink X1-O2, got %d repositions / jumps %v", r.Repositions, fx.jumps)
	}
	if r.DistressLiquidations != 0 {
		t.Fatalf("distress must NOT fire when the offload can rescue the hull, got %d distress liquidations", r.DistressLiquidations)
	}
	// The one sale here is the sp-8zhit EXIT sweep, not a distress dump: the offload jumped the
	// hull to X1-O2 precisely because that market buys the PARTS it is stuck holding, and the run
	// then ends there. Flying a hull to its buyer and releasing it still loaded was the whole
	// sp-8zhit defect — the load must leave the hold before the hull does. DistressLiquidations
	// above is the assertion that distress did not pre-empt the offload; this one pins WHICH
	// mechanism sold.
	if r.ExitHoldLiquidations != 1 || fx.sells != 1 {
		t.Fatalf("the offloaded PARTS must be sold at the sink the hull was flown to, got ExitHoldLiquidations=%d / %d sells", r.ExitHoldLiquidations, fx.sells)
	}
	if fx.cargo["PARTS"] != 0 {
		t.Fatalf("the hull must not be released still holding the load the offload flew it to sell, got %+v", fx.cargo)
	}
}

// The distress liquidation shares the SAME kill-switch (RepositionDisabled) as the fresh-arb
// reposition and the offload: an operator who disabled the stuck-hull rescue machinery opts out
// of ALL of it. With the switch thrown, the laden hull exits honestly STILL FULL — the churn is
// the operator's known trade-off — and no cargo is dumped.
func TestTour_DistressLiquidation_RespectsRepositionKillSwitch(t *testing.T) {
	fx := distressFixture()
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-DISTRESS-OFF", PlayerID: 1, ContainerID: "ctr-distress-off", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t), RepositionDisabled: true,
	})
	if err != nil {
		t.Fatalf("kill-switch run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.DistressLiquidations != 0 {
		t.Fatalf("RepositionDisabled must suppress the distress liquidation, got %d", r.DistressLiquidations)
	}
	if fx.sells != 0 {
		t.Fatalf("no distress sale may occur under the kill-switch, got %d sells", fx.sells)
	}
	if fx.cargo["ORE"] != 80 {
		t.Fatalf("with the rescue disabled the hull stays full, expected 80 ORE aboard, got %d", fx.cargo["ORE"])
	}
}

// bestLocalDistressSinks is the pure ranking behind the distress pass: for each held good it
// picks the current-system market with the highest non-EXPORT bid (an EXPORT bid is a low
// sellback, never a real buyer — mirrors heldCargoSinkValue), carrying the waypoint to dock at
// and the market's traded volume (the per-sale absorption cap). A held good with no positive
// non-EXPORT local bid is absent — nothing local will buy it.
func TestBestLocalDistressSinks_PicksHighestNonExportBidPerHeldGood(t *testing.T) {
	tests := []struct {
		name     string
		listings []trading.GoodListing
		held     map[string]int
		want     map[string]distressSink
	}{
		{
			name:     "a single import sink is chosen with its waypoint, bid and volume",
			listings: []trading.GoodListing{gl("ORE", "X1-B", "IMPORT", 100, 999, 50)},
			held:     map[string]int{"ORE": 80},
			want:     map[string]distressSink{"ORE": {waypoint: "X1-B", bid: 100, volume: 50}},
		},
		{
			name: "the highest bid across waypoints wins",
			listings: []trading.GoodListing{
				gl("ORE", "X1-A", "IMPORT", 100, 999, 50),
				gl("ORE", "X1-B", "EXCHANGE", 150, 999, 30),
			},
			held: map[string]int{"ORE": 80},
			want: map[string]distressSink{"ORE": {waypoint: "X1-B", bid: 150, volume: 30}},
		},
		{
			name:     "an EXPORT bid is a sellback, never a sink - excluded",
			listings: []trading.GoodListing{gl("ORE", "X1-A", "EXPORT", 500, 999, 50)},
			held:     map[string]int{"ORE": 80},
			want:     map[string]distressSink{},
		},
		{
			name:     "a non-positive bid is no buyer - excluded",
			listings: []trading.GoodListing{gl("ORE", "X1-A", "IMPORT", 0, 999, 50)},
			held:     map[string]int{"ORE": 80},
			want:     map[string]distressSink{},
		},
		{
			name:     "a good the hull is not holding is ignored",
			listings: []trading.GoodListing{gl("WIDGETS", "X1-A", "IMPORT", 100, 999, 50)},
			held:     map[string]int{"ORE": 80},
			want:     map[string]distressSink{},
		},
		{
			name: "each held good gets its own best sink",
			listings: []trading.GoodListing{
				gl("ORE", "X1-A", "IMPORT", 100, 999, 50),
				gl("GEAR", "X1-B", "EXCHANGE", 200, 999, 40),
			},
			held: map[string]int{"ORE": 80, "GEAR": 30},
			want: map[string]distressSink{
				"ORE":  {waypoint: "X1-A", bid: 100, volume: 50},
				"GEAR": {waypoint: "X1-B", bid: 200, volume: 40},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bestLocalDistressSinks(tt.listings, tt.held)
			if len(got) != len(tt.want) {
				t.Fatalf("bestLocalDistressSinks() = %+v, want %+v", got, tt.want)
			}
			for good, wantSink := range tt.want {
				if got[good] != wantSink {
					t.Fatalf("sink for %s = %+v, want %+v", good, got[good], wantSink)
				}
			}
		})
	}
}
