package mvt

import (
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

func leg(ship, wp, good string, isBuy bool, units, price int, at time.Time) trading.TourLegTelemetry {
	return trading.TourLegTelemetry{ShipSymbol: ship, Waypoint: wp, Good: good, IsBuy: isBuy,
		RealizedUnits: units, RealizedUnitPrice: price, PlannedUnits: units, PlannedUnitPrice: price,
		PlannedAt: at, RealizedAt: at, TourID: "t", PlayerID: 1}
}

func TestComputeFleetStats_IntraAndLaneTranches(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)
	legs := []trading.TourLegTelemetry{
		// H1: intra-system visit in X1-A: buy 10 @100, sell 10 @150 → margin 500, one intra tranche
		leg("H1", "X1-A-1", "IRON", true, 10, 100, t0),
		leg("H1", "X1-A-2", "IRON", false, 10, 150, t0.Add(10*time.Minute)),
		// H1: cross-system: buy 20 @200 in X1-A, sell 20 @400 in X1-B after 600 s → margin 4000 on lane A→B
		leg("H1", "X1-A-1", "GOLD", true, 20, 200, t0.Add(20*time.Minute)),
		leg("H1", "X1-B-1", "GOLD", false, 20, 400, t0.Add(30*time.Minute)),
		// H2: sell with no lot → skipped
		leg("H2", "X1-C-1", "IRON", false, 5, 999, t0),
	}
	s := ComputeFleetStats(legs, time.Hour)
	if s.Hulls != 2 || s.MarginTotal != 4500 {
		t.Fatalf("hulls=%d total=%v", s.Hulls, s.MarginTotal)
	}
	// visits: H1@A (margin 500), H1@B (4000), H2@C (0) → mean 1500
	if s.MeanMarginPerSystemVisit != 1500 {
		t.Fatalf("mean per visit = %v, want 1500", s.MeanMarginPerSystemVisit)
	}
	if s.IntraMarginPerTranche != 500 {
		t.Fatalf("intra per tranche = %v, want 500", s.IntraMarginPerTranche)
	}
	if s.CreditsPerHullSec != 4500/(2*3600.0) {
		t.Fatalf("credits/hull/sec = %v", s.CreditsPerHullSec)
	}
	if s.PerHullMargin["H1"] != 4500 || s.PerHullMargin["H2"] != 0 {
		t.Fatalf("per hull = %v", s.PerHullMargin)
	}
	if len(s.Lanes) != 1 || s.Lanes[0] != (LaneStat{Source: "X1-A", Sink: "X1-B", Good: "GOLD", Tranches: 1, MarginPerTranche: 4000, MeanTransitSeconds: 600}) {
		t.Fatalf("lanes = %+v", s.Lanes)
	}
}

func TestComputeFleetStats_EmptyAndZeroWindow(t *testing.T) {
	s := ComputeFleetStats(nil, time.Hour)
	if s.Hulls != 0 || s.CreditsPerHullSec != 0 || s.MeanMarginPerSystemVisit != 0 || len(s.Lanes) != 0 {
		t.Fatalf("empty stats = %+v", s)
	}
	s = ComputeFleetStats([]trading.TourLegTelemetry{leg("H1", "X1-A-1", "IRON", true, 1, 1, time.Unix(0, 0))}, 0)
	if s.CreditsPerHullSec != 0 {
		t.Fatal("zero window must not divide by zero")
	}
}

func TestComputeFleetStats_FIFOAcrossPartialLots(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)
	legs := []trading.TourLegTelemetry{
		leg("H1", "X1-A-1", "IRON", true, 10, 100, t0),
		leg("H1", "X1-A-1", "IRON", true, 10, 200, t0.Add(time.Minute)),
		leg("H1", "X1-A-2", "IRON", false, 15, 300, t0.Add(2*time.Minute)), // 10@100 + 5@200 → 2000+500
	}
	s := ComputeFleetStats(legs, time.Hour)
	if s.MarginTotal != 2500 {
		t.Fatalf("margin = %v, want 2500", s.MarginTotal)
	}
}
