package routing

import (
	"testing"
	"time"

	domainRouting "github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/routing"
)

// buildTourRequest maps every domain field onto the proto request 1:1 — the
// planner reads the snapshot, waypoint coordinates, ship state and money-guard
// constraints entirely from this payload, so a dropped field is a silently
// degraded plan.
func TestBuildTourRequest_MapsAllFields(t *testing.T) {
	observed := time.Unix(1_720_000_000, 0)
	req := buildTourRequest(
		[]domainRouting.TourGoodSnapshot{{
			Waypoint: "X1-NK36-D39", System: "X1-NK36", Good: "MEDICINE",
			Supply: "LIMITED", Activity: "WEAK", Ask: 1900, Bid: 1844,
			TradeVolume: 20, ObservedAt: observed,
		}},
		[]domainRouting.TourWaypoint{{Symbol: "X1-NK36-D39", System: "X1-NK36", X: 7, Y: -3}},
		domainRouting.TourShipState{
			ShipSymbol: "TORWIND-19", CurrentWaypoint: "X1-NK36-D39", CurrentSystem: "X1-NK36",
			HoldCapacity: 360, FuelCurrent: 800, FuelCapacity: 1200, EngineSpeed: 30,
			Cargo: map[string]int{"FABRICS": 40, "MEDICINE": 10},
		},
		domainRouting.TourConstraints{
			MaxHops: 6, MinMarginPerUnit: 5, MaxSnapshotAgeMinutes: 75,
			MaxSpend: 250000, WorkingCapitalReserve: 50000,
			AllowedSystems: []string{"X1-NK36", "X1-GQ92"}, ExpectedModelVersion: "1@torwind-2026-07-05",
		},
		[]domainRouting.TourDepositCandidate{
			{Good: "ELECTRONICS", UnitsWanted: 120, SyntheticBid: 3400, StorageWaypoint: "X1-NK36-H1", StorageSystem: "X1-NK36"},
		},
		[]domainRouting.TourMarketAbsorption{
			// Emitted deterministically by (waypoint, good, side): this SELL row sorts
			// after the BUY row below despite being passed first.
			{Waypoint: "X1-NK36-D39", Good: "MEDICINE", Side: "sell", PlannedUnits: 20, RecoveringUnits: 12.5},
			{Waypoint: "X1-NK36-D39", Good: "FABRICS", Side: "buy", PlannedUnits: 40, RecoveringUnits: 0},
		},
		[]domainRouting.TourStockSource{
			{Good: "CLOTHING", UnitsAvailable: 80, UnitAsk: 3200, StorageWaypoint: "X1-NK36-H1", StorageSystem: "X1-NK36"},
		},
	)

	if len(req.Snapshot) != 1 {
		t.Fatalf("expected 1 snapshot row, got %d", len(req.Snapshot))
	}
	s := req.Snapshot[0]
	if s.WaypointSymbol != "X1-NK36-D39" || s.SystemSymbol != "X1-NK36" || s.GoodSymbol != "MEDICINE" {
		t.Fatalf("snapshot identity wrong: %+v", s)
	}
	if s.Ask != 1900 || s.Bid != 1844 || s.TradeVolume != 20 || s.Supply != "LIMITED" || s.Activity != "WEAK" {
		t.Fatalf("snapshot prices/tier wrong: %+v", s)
	}
	if s.ObservedAtUnix != observed.Unix() {
		t.Fatalf("ObservedAtUnix = %d, want %d", s.ObservedAtUnix, observed.Unix())
	}

	if len(req.Waypoints) != 1 || req.Waypoints[0].Symbol != "X1-NK36-D39" ||
		req.Waypoints[0].SystemSymbol != "X1-NK36" || req.Waypoints[0].X != 7 || req.Waypoints[0].Y != -3 {
		t.Fatalf("waypoint coords wrong: %+v", req.Waypoints)
	}

	if req.Ship.ShipSymbol != "TORWIND-19" || req.Ship.HoldCapacity != 360 ||
		req.Ship.FuelCurrent != 800 || req.Ship.FuelCapacity != 1200 || req.Ship.EngineSpeed != 30 {
		t.Fatalf("ship state wrong: %+v", req.Ship)
	}
	// Cargo is emitted in deterministic good-symbol order.
	if len(req.Ship.Cargo) != 2 || req.Ship.Cargo[0].GoodSymbol != "FABRICS" ||
		req.Ship.Cargo[0].Units != 40 || req.Ship.Cargo[1].GoodSymbol != "MEDICINE" || req.Ship.Cargo[1].Units != 10 {
		t.Fatalf("cargo mapping/order wrong: %+v", req.Ship.Cargo)
	}

	c := req.Constraints
	if c.MaxHops != 6 || c.MinMarginPerUnit != 5 || c.MaxSnapshotAgeMinutes != 75 ||
		c.MaxSpend != 250000 || c.WorkingCapitalReserve != 50000 || c.ExpectedModelVersion != "1@torwind-2026-07-05" {
		t.Fatalf("constraints wrong: %+v", c)
	}
	if len(c.AllowedSystems) != 2 || c.AllowedSystems[0] != "X1-NK36" || c.AllowedSystems[1] != "X1-GQ92" {
		t.Fatalf("allowed systems wrong: %+v", c.AllowedSystems)
	}

	// sp-dchv Lane C: deposit candidates map onto the request 1:1 so the planner
	// can offer haul-to-storage sinks.
	if len(req.DepositCandidates) != 1 {
		t.Fatalf("expected 1 deposit candidate, got %d", len(req.DepositCandidates))
	}
	d := req.DepositCandidates[0]
	if d.GoodSymbol != "ELECTRONICS" || d.UnitsWanted != 120 || d.SyntheticBid != 3400 ||
		d.StorageWaypoint != "X1-NK36-H1" || d.StorageSystem != "X1-NK36" {
		t.Fatalf("deposit candidate mapping wrong: %+v", d)
	}

	// sp-78ai L3: absorption rows map onto the request 1:1 and are emitted in
	// deterministic (waypoint, good, side) order — FABRICS/buy before MEDICINE/sell.
	if len(req.Absorption) != 2 {
		t.Fatalf("expected 2 absorption rows, got %d", len(req.Absorption))
	}
	if a := req.Absorption[0]; a.GoodSymbol != "FABRICS" || a.Side != "buy" || a.UnitsPlanned != 40 || a.UnitsRecovering != 0 {
		t.Fatalf("absorption[0] mapping/order wrong: %+v", a)
	}
	if a := req.Absorption[1]; a.WaypointSymbol != "X1-NK36-D39" || a.GoodSymbol != "MEDICINE" ||
		a.Side != "sell" || a.UnitsPlanned != 20 || a.UnitsRecovering != 12.5 {
		t.Fatalf("absorption[1] mapping wrong: %+v", a)
	}

	// Stock sources (C1, sp-64je): warehouse stock offered at basis marshals through.
	if len(req.StockSources) != 1 {
		t.Fatalf("expected 1 stock source, got %d", len(req.StockSources))
	}
	if s := req.StockSources[0]; s.GoodSymbol != "CLOTHING" || s.UnitsAvailable != 80 || s.UnitAsk != 3200 ||
		s.StorageWaypoint != "X1-NK36-H1" || s.StorageSystem != "X1-NK36" {
		t.Fatalf("stock source mapping wrong: %+v", s)
	}
}

// tourPlanFromPb parses the ordered legs (trades in planner-emitted execution
// order, sells before buys), tour totals and top-3 rejects back into the domain.
func TestTourPlanFromPb_ParsesLegsAndRejects(t *testing.T) {
	resp := &pb.OptimizeTradeTourResponse{
		Feasible:                true,
		ProjectedProfit:         123456,
		ProjectedCreditsPerHour: 78910.5,
		HeldLiquidation:         44444,
		DepositValue:            33333,
		ModelVersion:            "1@torwind-2026-07-05",
		Legs: []*pb.TradeTourLeg{{
			WaypointSymbol:        "X1-GQ92-A1",
			SystemSymbol:          "X1-GQ92",
			ProjectedLegProfit:    60000,
			TravelSecondsFromPrev: 420,
			Trades: []*pb.TourTrade{
				{GoodSymbol: "MEDICINE", Units: 40, ExpectedUnitPrice: 1800, IsBuy: false},
				{GoodSymbol: "ELECTRONICS", Units: 25, ExpectedUnitPrice: 3400, IsBuy: false, IsDeposit: true},
				{GoodSymbol: "FABRICS", Units: 30, ExpectedUnitPrice: 120, IsBuy: true},
			},
		}},
		TopRejected: []*pb.RejectedTour{
			{Summary: "GQ92→NK36 medicine dump", Reason: "ladders bid below floor"},
			{Summary: "single-lane fallback", Reason: ""},
		},
	}

	plan := tourPlanFromPb(resp)

	if !plan.Feasible || plan.ProjectedProfit != 123456 || plan.ProjectedCreditsPerHour != 78910.5 ||
		plan.HeldLiquidation != 44444 || plan.DepositValue != 33333 || plan.ModelVersion != "1@torwind-2026-07-05" {
		t.Fatalf("plan totals wrong: %+v", plan)
	}
	if len(plan.Legs) != 1 {
		t.Fatalf("expected 1 leg, got %d", len(plan.Legs))
	}
	leg := plan.Legs[0]
	if leg.Waypoint != "X1-GQ92-A1" || leg.System != "X1-GQ92" ||
		leg.ProjectedLegProfit != 60000 || leg.TravelSecondsFromPrev != 420 {
		t.Fatalf("leg fields wrong: %+v", leg)
	}
	if len(leg.Trades) != 3 || leg.Trades[0].IsBuy || leg.Trades[0].Good != "MEDICINE" ||
		leg.Trades[0].Units != 40 || leg.Trades[0].ExpectedUnitPrice != 1800 || leg.Trades[0].IsDeposit {
		t.Fatalf("sell trade wrong (sells must come first): %+v", leg.Trades)
	}
	// sp-dchv: the deposit tranche round-trips its IsDeposit flag and synthetic price.
	if leg.Trades[1].IsBuy || !leg.Trades[1].IsDeposit || leg.Trades[1].Good != "ELECTRONICS" ||
		leg.Trades[1].Units != 25 || leg.Trades[1].ExpectedUnitPrice != 3400 {
		t.Fatalf("deposit trade wrong: %+v", leg.Trades)
	}
	if !leg.Trades[2].IsBuy || leg.Trades[2].Good != "FABRICS" || leg.Trades[2].Units != 30 || leg.Trades[2].IsDeposit {
		t.Fatalf("buy trade wrong: %+v", leg.Trades)
	}
	if len(plan.TopRejected) != 2 || plan.TopRejected[0] != "GQ92→NK36 medicine dump — ladders bid below floor" ||
		plan.TopRejected[1] != "single-lane fallback" {
		t.Fatalf("rejects flattened wrong: %+v", plan.TopRejected)
	}
}

// A default-constructed mock never fabricates a tour — it returns a benign
// infeasible plan so a caller that forgot to configure it fails open, not into a
// phantom trade.
func TestMockRoutingClient_OptimizeTradeTour_Configurable(t *testing.T) {
	mock := NewMockRoutingClient()
	plan, err := mock.OptimizeTradeTour(t.Context(), nil, nil, domainRouting.TourShipState{}, domainRouting.TourConstraints{}, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Feasible {
		t.Fatalf("default mock must be infeasible, got %+v", plan)
	}

	mock.CannedTourPlan = &domainRouting.TourPlan{Feasible: true, ProjectedProfit: 999}
	plan, err = mock.OptimizeTradeTour(t.Context(), nil, nil, domainRouting.TourShipState{}, domainRouting.TourConstraints{}, nil, nil, nil)
	if err != nil || !plan.Feasible || plan.ProjectedProfit != 999 {
		t.Fatalf("canned plan not returned: plan=%+v err=%v", plan, err)
	}
}
