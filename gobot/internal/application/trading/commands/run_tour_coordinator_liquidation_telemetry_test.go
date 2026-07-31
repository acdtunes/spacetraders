package commands

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// sellLegs returns the recorded SELL telemetry rows, which is the side sp-xfrfw was losing.
func sellLegs(rows []trading.TourLegTelemetry) []trading.TourLegTelemetry {
	var sells []trading.TourLegTelemetry
	for _, r := range rows {
		if !r.IsBuy {
			sells = append(sells, r)
		}
	}
	return sells
}

// assertLiquidationLeg checks the one invariant every liquidation row must satisfy: it describes
// the sale that actually happened, and it claims NO plan basis it never had.
func assertLiquidationLeg(t *testing.T, leg trading.TourLegTelemetry, tourID, ship, waypoint, good string, units, unitPrice int) {
	t.Helper()
	if leg.TourID != tourID || leg.ShipSymbol != ship {
		t.Fatalf("liquidation leg must be attributed to the selling tour/hull, got tour=%q ship=%q want tour=%q ship=%q", leg.TourID, leg.ShipSymbol, tourID, ship)
	}
	// The waypoint is where the cargo was SOLD (the sink the sweep chose), not where the hull
	// started — the lane view draws the hop from it, so a wrong waypoint draws a wrong lane.
	if leg.Waypoint != waypoint || leg.Good != good {
		t.Fatalf("liquidation leg must name the sink it sold at and the good it sold, got waypoint=%q good=%q want waypoint=%q good=%q", leg.Waypoint, leg.Good, waypoint, good)
	}
	if leg.RealizedUnits != units || leg.RealizedUnitPrice != unitPrice {
		t.Fatalf("liquidation leg must carry the realized sale, got %d units @ %d want %d @ %d", leg.RealizedUnits, leg.RealizedUnitPrice, units, unitPrice)
	}
	if leg.RealizedAt.IsZero() {
		t.Fatalf("a realized sale must stamp RealizedAt — /flows/lanes filters on realized_at and drops a zero row")
	}
	// A liquidation has NO solver plan behind it. Recording a fabricated planned basis would
	// poison the planned-vs-realized drift readers; zero is the established "no basis" encoding
	// (ObserveLegPriceDrift and the SQL NULLIF(planned,0) both skip a non-positive basis).
	if leg.PlannedUnitPrice != 0 || leg.PlannedUnits != 0 {
		t.Fatalf("a liquidation has no plan basis and must not invent one, got PlannedUnits=%d PlannedUnitPrice=%d", leg.PlannedUnits, leg.PlannedUnitPrice)
	}
	// The lane aggregator sorts a tour's legs by leg_index and draws a directed lane between
	// CONSECUTIVE legs. A liquidation always happens after the plan is exhausted, so its index
	// must sort after every 0..N plan position or the hop would be drawn backwards.
	if leg.LegIndex < liquidationLegIndexBase {
		t.Fatalf("a liquidation leg must sort after every plan leg, got LegIndex=%d want >= %d", leg.LegIndex, liquidationLegIndexBase)
	}
	// sp-fzt09: and it must SAY it is a liquidation. Carrying no plan basis is what makes this
	// row correct; being indistinguishable from a solver leg whose plan failed to persist is
	// what made a quarter of all realized sells unattributable in SQL. An analysis that read
	// these zeros as solver legs concluded the planner used 36.7% of market depth and had to be
	// withdrawn. The zero basis above is the DATA; this is the ATTRIBUTION, and a reader must
	// not have to infer the second from the first.
	if leg.Engine != trading.LegEngineLiquidation {
		t.Fatalf("a liquidation leg must declare its engine, got Engine=%q want %q — an unattributed "+
			"zero-basis sell is indistinguishable from a solver leg that lost its plan", leg.Engine, trading.LegEngineLiquidation)
	}
}

// sp-xfrfw. THE DEFECT: a liquidation sale is a REAL realized sell — real units leave the hull at
// a real waypoint for real revenue, and it writes a SELL_CARGO ledger row like any other sale —
// but it was the one sell path that never recorded leg telemetry. distressSellGood books the
// revenue, discharges the purchase obligation and logs, then returns without calling recordLeg,
// which both executeBuy and executeSell do unconditionally.
//
// Measured against the ledger (transactions, operation_type='tour') as ground truth: over 24h the
// ledger held 222 tour sells and tour_leg_telemetry held 166 — 55 missing, and a join against the
// container logs attributed 55 of 55 to liquidation sales. Zero telemetry sells lacked a ledger
// row, so the loss was one-directional and total on this path. BUY legs matched the ledger exactly
// in every window, which is the whole signal: the buy path has no such bypass.
//
// The consumer this breaks is the galaxy view. visualizer/server/utils/laneAggregation.ts folds
// tour_leg_telemetry rows into directed lanes, so a sell with no row is a lane that never appears
// — the view silently under-drew a quarter of realized trade activity.
//
// This is the exit sweep (liquidateStrandBeforeExit), the dominant producer of those rows.
func TestTour_ExitHoldLiquidation_RecordsSellTelemetry(t *testing.T) {
	fx := torwindAFixture()
	planner := &tourFakeRoutingClient{planFn: func(routing.TourShipState) *routing.TourPlan {
		return infeasibleTour() // no plan; the exit sweep is the only thing that trades
	}}
	tel := &tourFakeTelemetry{}
	h := newTourHandler(t, fx, planner, tel)

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TORWIND-A", PlayerID: 1, ContainerID: "ctr-torwind-a",
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("the no-plan exit must be a clean terminal exit, got error: %v", err)
	}
	r := tourResponse(t, resp)
	// Guard the premise: if the sweep never ran there is no sale to record and this test would
	// pass vacuously on a broken build.
	if r.ExitHoldLiquidations != 1 || fx.sells != 1 {
		t.Fatalf("premise broken — the exit sweep must have sold exactly once, got ExitHoldLiquidations=%d sells=%d", r.ExitHoldLiquidations, fx.sells)
	}

	sells := sellLegs(tel.rows)
	// The invariant, stated the way the ledger states it: one telemetry sell per sale executed.
	if len(sells) != fx.sells {
		t.Fatalf("every realized sale must record one sell leg (the ledger writes one SELL_CARGO per sale), got %d telemetry sells for %d sales; all rows: %+v", len(sells), fx.sells, tel.rows)
	}
	assertLiquidationLeg(t, sells[0], "ctr-torwind-a", "TORWIND-A", "X1-KP23-D41", "MICROPROCESSORS", 20, 4141)
}

// sp-xfrfw, the second entry point into the same bypass: the margins-death distress liquidation
// (maybeDistressLiquidate). It reaches distressSellGood by a different route and lost its
// telemetry for the identical reason, so fixing only the exit sweep would leave this path dark.
func TestTour_DistressLiquidation_RecordsSellTelemetry(t *testing.T) {
	fx := distressFixture()
	planner := &tourFakeRoutingClient{planFn: func(routing.TourShipState) *routing.TourPlan {
		return infeasibleTour() // no fresh tour anywhere — only a distress liquidation frees the hull
	}}
	tel := &tourFakeTelemetry{}
	h := newTourHandler(t, fx, planner, tel)

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-DISTRESS", PlayerID: 1, ContainerID: "ctr-distress", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("distress liquidation run returned error: %v", err)
	}
	r := tourResponse(t, resp)
	if r.DistressLiquidations != 1 || fx.sells != 1 {
		t.Fatalf("premise broken — the distress path must have sold exactly once, got DistressLiquidations=%d sells=%d", r.DistressLiquidations, fx.sells)
	}

	sells := sellLegs(tel.rows)
	if len(sells) != fx.sells {
		t.Fatalf("every realized sale must record one sell leg, got %d telemetry sells for %d sales; all rows: %+v", len(sells), fx.sells, tel.rows)
	}
	// It sold at X1-D1-B, the in-system sink it flew to — NOT X1-D1-A where it was parked.
	assertLiquidationLeg(t, sells[0], "ctr-distress", "TOUR-DISTRESS", "X1-D1-B", "ORE", 80, 100)
}
