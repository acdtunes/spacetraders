package commands

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// run_tour_coordinator_beta_matched_test.go pins that the CORRECTED β reaches both of its consumers,
// and that the fail-closed contract they both depend on survives the correction.
//
// The matching rule itself is tested in internal/domain/trading. What is tested HERE is the seam:
// senseBeta and senseRateFloor each call MedianTourRate over rows they read for themselves, so a
// corrected β only matters if it actually arrives at the decision. Both fail closed on a
// non-positive or unreadable β — the placement engine falls back to the legacy static-floor
// reposition, and the rate-floor relocation trigger does nothing — which is why an unmatched-net β
// never licensed a bad tour: it silenced a mechanism. That makes the fail-closed path the property
// worth guarding hardest, because it is the one both consumers stand on.

// betaLeg builds one telemetry leg with an explicit GOOD, which is what makes a trade matchable.
// The rate-floor file's rfTleg leaves Good empty, so every leg of a tour there shares one trade;
// these tests need a second good to express a purchase whose sale is outside the window at all.
func betaLeg(ship, tour, good string, isBuy bool, units, price int, planned, realized time.Time) trading.TourLegTelemetry {
	return trading.TourLegTelemetry{
		TourID: tour, ShipSymbol: ship, Good: good, IsBuy: isBuy,
		RealizedUnits: units, RealizedUnitPrice: price,
		PlannedAt: planned, RealizedAt: realized, PlayerID: 1,
	}
}

// betaHandler is the smallest handler that can answer a β question: a telemetry repo and a clock.
func betaHandler(rows []trading.TourLegTelemetry) *RunTourCoordinatorHandler {
	return &RunTourCoordinatorHandler{
		telemetry: &seededTelemetry{rows: rows},
		clock:     shared.NewRealClock(),
	}
}

// oneMatchedTourPlusOrphan is one hull's window: a completed trade worth +40,000/hr, and a purchase
// of 200,000 whose sale falls outside the window. Netted whole it reads −160,000/hr.
func oneMatchedTourPlusOrphan(ship, tour string) []trading.TourLegTelemetry {
	base := time.Now().Add(-30 * time.Minute)
	end := base.Add(time.Hour)
	return []trading.TourLegTelemetry{
		betaLeg(ship, tour, "FUEL", true, 20, 1000, base, base),
		betaLeg(ship, tour, "FUEL", false, 20, 3000, base, end),
		betaLeg(ship, tour, "MACHINERY", true, 40, 5000, base, end),
	}
}

// THE PLACEMENT CONSUMER gets the matched β. Under the old netting this window read −160,000/hr,
// which is non-positive, so senseBeta returned unreadable and the caller fell back to the legacy
// static-floor engine — the placement scorer never ran at all on a hull carrying unsold cargo.
func TestSenseBeta_ReadsTheMatchedRateNotTheOrphanDraggedOne(t *testing.T) {
	h := betaHandler(oneMatchedTourPlusOrphan("S1", "t1"))

	beta, ok := h.senseBeta(context.Background(), &RunTourCoordinatorCommand{PlayerID: 1, ShipSymbol: "S1"})
	if !ok {
		t.Fatalf("β unreadable — the window holds a completed trade earning 40,000/hr, so the placement "+
			"engine should score rather than fall back to the legacy reposition (got %v)", beta)
	}
	if beta != 40000 {
		t.Fatalf("β = %v, want 40000 — the MACHINERY purchase has no sale in the window; -160000 is the "+
			"old unmatched reading and is non-positive, which silenced the whole engine", beta)
	}
}

// THE RATE-FLOOR CONSUMER gets it too, on BOTH of its rates: the fleet median AND the per-hull
// median at the second call site. They must share one basis or the under-earner RATIO compares two
// different measurements.
//
// The fixture is adverse: the orphan purchase sits on the HULL under test, so under the old netting
// the hull reads −160,000/hr against a healthy fleet — the shape that manufactures a spurious
// under-earner and relocates a hull that is doing fine.
func TestSenseRateFloor_UsesMatchedNetsForBothTheFleetAndTheHullRate(t *testing.T) {
	rows := oneMatchedTourPlusOrphan("S1", "t1")
	// Two other hulls, cleanly matched, at 20,000/hr each so the fleet median is well defined.
	base := time.Now().Add(-30 * time.Minute)
	end := base.Add(time.Hour)
	for _, ship := range []string{"S2", "S3"} {
		rows = append(rows,
			betaLeg(ship, "t-"+ship, "FUEL", true, 10, 1000, base, base),
			betaLeg(ship, "t-"+ship, "FUEL", false, 10, 3000, base, end),
		)
	}

	h := betaHandler(rows)
	hullRate, fleetMedian, ok := h.senseRateFloor(context.Background(),
		&RunTourCoordinatorCommand{PlayerID: 1, ShipSymbol: "S1"})
	if !ok {
		t.Fatalf("rate floor unreadable — a positive fleet median and a computable hull rate both exist")
	}
	// Tours: S1 = 40,000, S2 = 20,000, S3 = 20,000 → median 20,000.
	if fleetMedian != 20000 {
		t.Fatalf("fleet median = %v, want 20000", fleetMedian)
	}
	if hullRate != 40000 {
		t.Fatalf("hull rate = %v, want 40000 — under the old netting S1 read -160000 and would have been "+
			"flagged as a chronic under-earner and relocated off ground it is earning on", hullRate)
	}
	// The under-earner predicate the caller applies: hullRate < 40% × fleetMedian.
	if floor := 0.40 * fleetMedian; hullRate < floor {
		t.Fatalf("hull %v < floor %v — a hull earning twice the fleet median was flagged as under-earning", hullRate, floor)
	}
}

// FAIL-CLOSED, PRESERVED, and reachable in a NEW way. A window holding only unmatched activity used
// to produce a number; it is now uncomputable. Both consumers must read that as "cannot see the
// economics" rather than as a rate.
//
// This is the property that makes the whole change safe: if it broke, an unreadable β would arrive
// as a fabricated 0, the placement engine would score against it instead of falling back, and the
// rate-floor trigger would compare every hull against a zero floor.
func TestSenseBetaAndRateFloor_FailClosedOnAWindowWithNoCompletedTrade(t *testing.T) {
	base := time.Now().Add(-30 * time.Minute)
	end := base.Add(time.Hour)
	rows := []trading.TourLegTelemetry{
		// Bought, never sold in-window.
		betaLeg("S1", "t1", "MACHINERY", true, 40, 5000, base, end),
		// Sold, never bought in-window — the mirror artifact.
		betaLeg("S2", "t2", "GOLD", false, 100, 9000, base, end),
	}
	h := betaHandler(rows)
	cmd := &RunTourCoordinatorCommand{PlayerID: 1, ShipSymbol: "S1"}

	if beta, ok := h.senseBeta(context.Background(), cmd); ok {
		t.Fatalf("senseBeta returned β=%v ok=true — with no completed trade the placement engine must "+
			"fall back to the legacy reposition, never score against an invented rate", beta)
	}
	hullRate, fleetMedian, ok := h.senseRateFloor(context.Background(), cmd)
	if ok {
		t.Fatalf("senseRateFloor returned hull=%v median=%v ok=true — with no completed trade no "+
			"relocation may be decided at all", hullRate, fleetMedian)
	}
}

// A NEGATIVE β still fails closed after the correction. Matching removes the artifact, not the
// possibility of a genuinely losing fleet, and a real loss must still silence both consumers rather
// than drive a relocation off a negative floor.
func TestSenseBetaAndRateFloor_StillFailClosedOnAGenuinelyLosingFleet(t *testing.T) {
	base := time.Now().Add(-30 * time.Minute)
	end := base.Add(time.Hour)
	var rows []trading.TourLegTelemetry
	// Fully MATCHED tours that genuinely lose money: bought high, sold low.
	for _, ship := range []string{"S1", "S2", "S3"} {
		rows = append(rows,
			betaLeg(ship, "t-"+ship, "FUEL", true, 100, 3000, base, base),
			betaLeg(ship, "t-"+ship, "FUEL", false, 100, 1000, base, end),
		)
	}
	h := betaHandler(rows)
	cmd := &RunTourCoordinatorCommand{PlayerID: 1, ShipSymbol: "S1"}

	if beta, ok := h.senseBeta(context.Background(), cmd); ok {
		t.Fatalf("senseBeta returned β=%v ok=true on a fleet losing 200,000/hr per tour — a non-positive "+
			"median must fall back, matched or not", beta)
	}
	if _, _, ok := h.senseRateFloor(context.Background(), cmd); ok {
		t.Fatalf("senseRateFloor was readable on a genuinely losing fleet — a relocation must never be " +
			"decided off a non-positive median")
	}
}
