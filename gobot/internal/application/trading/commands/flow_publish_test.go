// gobot/internal/application/trading/commands/flow_publish_test.go
package commands

import (
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/flowfeed"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

func TestBuildArbFlow_MapsLaneIntent(t *testing.T) {
	cmd := &RunArbCoordinatorCommand{
		ContainerID:   "arb-run-SHIP-1-abc",
		ShipSymbol:    "SHIP-1",
		Good:          "IRON_ORE",
		BuyAt:         "X1-AA-A1",
		SellAt:        "X1-AA-B2",
		MaxUnits:      120,
		QuotedDestBid: 55,
	}
	cargo := []flowfeed.CargoItem{{Good: "IRON_ORE", Units: 40}}
	now := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)

	f := buildArbFlow(cmd, cargo, now)

	if f.Program != flowfeed.ProgramArb {
		t.Errorf("program = %q, want arb", f.Program)
	}
	if f.ContainerID != "arb-run-SHIP-1-abc" || f.Ship != "SHIP-1" {
		t.Errorf("id/ship mismatch: %+v", f)
	}
	if f.TourID != nil {
		t.Errorf("arb tourId must be null, got %v", *f.TourID)
	}
	if f.CurrentLeg != nil {
		t.Errorf("arb currentLeg must be null (nav join owns position), got %+v", f.CurrentLeg)
	}
	if f.Projected != nil {
		t.Errorf("arb projected must be null at adoption, got %+v", f.Projected)
	}
	if len(f.RemainingHops) != 1 || f.RemainingHops[0].Waypoint != "X1-AA-B2" {
		t.Fatalf("want one sell hop at X1-AA-B2, got %+v", f.RemainingHops)
	}
	tr := f.RemainingHops[0].Tranches[0]
	if tr.Good != "IRON_ORE" || tr.IsBuy || tr.Units != 120 || tr.ExpectedUnitPrice != 55 {
		t.Errorf("sell tranche mismatch: %+v", tr)
	}
	if len(f.Cargo) != 1 || f.Cargo[0].Good != "IRON_ORE" || f.Cargo[0].Units != 40 {
		t.Errorf("cargo mismatch: %+v", f.Cargo)
	}
	if !f.PlannedAt.Equal(now) {
		t.Errorf("plannedAt = %v, want %v", f.PlannedAt, now)
	}
}

func tourPlanFixture() *routing.TourPlan {
	return &routing.TourPlan{
		Legs: []routing.TourLeg{
			{Waypoint: "X1-AA-A1", Trades: []routing.TourTrade{{Good: "IRON", Units: 50, ExpectedUnitPrice: 30, IsBuy: true}}},
			{Waypoint: "X1-AA-B2", Trades: []routing.TourTrade{{Good: "IRON", Units: 50, ExpectedUnitPrice: 42, IsBuy: false}}},
			{Waypoint: "X1-AA-C3", Trades: []routing.TourTrade{{Good: "GOLD", Units: 10, ExpectedUnitPrice: 900, IsBuy: true}}},
		},
		ProjectedProfit:         600,
		ProjectedCreditsPerHour: 5400,
	}
}

func TestBuildTourFlow_AdoptionHasNoCurrentLegAndAllHops(t *testing.T) {
	cmd := &RunTourCoordinatorCommand{ContainerID: "tour-run-SHIP-9-xyz", ShipSymbol: "SHIP-9"}
	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)

	f := buildTourFlow(cmd, tourPlanFixture(), -1, time.Time{}, nil, now)

	if f.Program != flowfeed.ProgramTour {
		t.Errorf("program = %q, want tour", f.Program)
	}
	if f.TourID == nil || *f.TourID != "tour-run-SHIP-9-xyz" {
		t.Errorf("tourId must equal the container id, got %v", f.TourID)
	}
	if f.CurrentLeg != nil {
		t.Errorf("adoption currentLeg must be null, got %+v", f.CurrentLeg)
	}
	if len(f.RemainingHops) != 3 {
		t.Fatalf("adoption remainingHops = %d, want 3 (all legs)", len(f.RemainingHops))
	}
	if f.Projected == nil || f.Projected.Profit != 600 || f.Projected.RatePerHour != 5400 {
		t.Errorf("projected mismatch: %+v", f.Projected)
	}
}

func TestBuildTourFlow_LegBoundarySetsCurrentLegAndTrimsHops(t *testing.T) {
	cmd := &RunTourCoordinatorCommand{ContainerID: "tour-run-SHIP-9-xyz", ShipSymbol: "SHIP-9"}
	arrives := time.Date(2026, 7, 11, 10, 8, 0, 0, time.UTC)
	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)

	// Flying leg index 1 (the second leg): from Legs[0].Waypoint to Legs[1].Waypoint.
	f := buildTourFlow(cmd, tourPlanFixture(), 1, arrives, nil, now)

	if f.CurrentLeg == nil {
		t.Fatal("leg-boundary currentLeg must be set")
	}
	if f.CurrentLeg.From != "X1-AA-A1" || f.CurrentLeg.To != "X1-AA-B2" {
		t.Errorf("currentLeg from/to = %s/%s, want X1-AA-A1/X1-AA-B2", f.CurrentLeg.From, f.CurrentLeg.To)
	}
	if !f.CurrentLeg.DepartedAt.Equal(now) || !f.CurrentLeg.ArrivesAt.Equal(arrives) {
		t.Errorf("currentLeg timestamps mismatch: %+v", f.CurrentLeg)
	}
	if len(f.RemainingHops) != 1 || f.RemainingHops[0].Waypoint != "X1-AA-C3" {
		t.Fatalf("remainingHops after leg 1 = %+v, want [X1-AA-C3]", f.RemainingHops)
	}
	tr := f.RemainingHops[0].Tranches[0]
	if tr.Good != "GOLD" || !tr.IsBuy || tr.Units != 10 || tr.ExpectedUnitPrice != 900 {
		t.Errorf("hop tranche mismatch: %+v", tr)
	}
}

func TestBuildTourFlow_FirstLegHasEmptyFrom(t *testing.T) {
	cmd := &RunTourCoordinatorCommand{ContainerID: "c", ShipSymbol: "S"}
	f := buildTourFlow(cmd, tourPlanFixture(), 0, time.Time{}, nil, time.Now())
	if f.CurrentLeg == nil || f.CurrentLeg.From != "" || f.CurrentLeg.To != "X1-AA-A1" {
		t.Errorf("first leg: want From empty (nav owns origin), To=X1-AA-A1, got %+v", f.CurrentLeg)
	}
}

func TestBuildTradeRouteFlow_MapsCommittedLane(t *testing.T) {
	cmd := &RunTradeRouteCoordinatorCommand{ShipSymbol: "SHIP-7"}
	lane := trading.ArbitrageLane{
		Good:           "FUEL",
		SourceWaypoint: "X1-AA-SRC",
		DestWaypoint:   "X1-AA-DST",
		DestBid:        88,
		VolumeCap:      60,
		CappedSpread:   1800,
	}
	cargo := []flowfeed.CargoItem{{Good: "FUEL", Units: 60}}
	now := time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC)

	f := buildTradeRouteFlow(cmd, lane, 12000, cargo, time.Time{}, now)

	if f.Program != flowfeed.ProgramTradeRoute {
		t.Errorf("program = %q, want trade-route", f.Program)
	}
	if f.Ship != "SHIP-7" {
		t.Errorf("ship = %q, want SHIP-7", f.Ship)
	}
	if f.TourID != nil {
		t.Errorf("trade-route tourId must be null, got %v", *f.TourID)
	}
	if f.CurrentLeg == nil || f.CurrentLeg.From != "X1-AA-SRC" || f.CurrentLeg.To != "X1-AA-DST" {
		t.Fatalf("currentLeg from/to mismatch: %+v", f.CurrentLeg)
	}
	if len(f.RemainingHops) != 1 || f.RemainingHops[0].Waypoint != "X1-AA-DST" {
		t.Fatalf("want sell hop at X1-AA-DST, got %+v", f.RemainingHops)
	}
	tr := f.RemainingHops[0].Tranches[0]
	if tr.Good != "FUEL" || tr.IsBuy || tr.Units != 60 || tr.ExpectedUnitPrice != 88 {
		t.Errorf("sell tranche mismatch: %+v", tr)
	}
	if f.Projected == nil || f.Projected.Profit != 1800 || f.Projected.RatePerHour != 12000 {
		t.Errorf("projected mismatch: %+v", f.Projected)
	}
}
