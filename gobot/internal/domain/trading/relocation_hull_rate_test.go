package trading

import (
	"math"
	"testing"
	"time"
)

// relocation_hull_rate_test.go — the transaction-based per-hull realized rate behind the opportunity
// relocator (sp-zvywu Part 2), and the shared-cycle attribution hazard it exists to avoid.

// completedTour emits the two matched legs of a one-good tour: buy `units` at `buyPrice`, sell the
// same units at `sellPrice`, over a wall clock of `span`. Both halves realize, so the netting rule
// scores the trade; the span is the tour's own clock, so the rate is net/hours.
func completedTour(tourID, ship string, units, buyPrice, sellPrice int, start time.Time, span time.Duration) []TourLegTelemetry {
	return []TourLegTelemetry{
		{TourID: tourID, ShipSymbol: ship, Good: "FUEL", IsBuy: true, RealizedUnits: units, RealizedUnitPrice: buyPrice, PlannedAt: start, RealizedAt: start},
		{TourID: tourID, ShipSymbol: ship, Good: "FUEL", IsBuy: false, RealizedUnits: units, RealizedUnitPrice: sellPrice, PlannedAt: start, RealizedAt: start.Add(span)},
	}
}

// A one-tour hull's rate is that tour's realized net over its own wall clock — the transaction
// arithmetic, computed from the trade's own units and prices.
func TestEwmaHullTourRateShould_ReportOneTourAtItsRealizedNetOverItsOwnWallClock(t *testing.T) {
	start := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	// 100 units bought at 50, sold at 150 → net 10,000 credits over 2 hours → 5,000/hr.
	rows := completedTour("tour-1", "HAULER-A", 100, 50, 150, start, 2*time.Hour)

	got, ok := EwmaHullTourRate(rows, "HAULER-A", DefaultHullRateSmoothing)

	if !ok {
		t.Fatal("a hull with one matched, sufficiently-long tour has no readable rate")
	}
	if math.Abs(got-5000) > 1e-6 {
		t.Fatalf("realized rate %.2f/hr, want 5000/hr (100 units x (150-50) = 10,000 credits over 2 h)", got)
	}
}

// THE EWMA PROPERTY. The newest tour must dominate: a hull whose ground has just collapsed must read
// close to the collapse, not to the pre-collapse level a median would keep quoting.
func TestEwmaHullTourRateShould_WeightTheNewestTourHeaviestWhenTheGroundCollapses(t *testing.T) {
	start := time.Date(2026, 7, 30, 6, 0, 0, 0, time.UTC)
	var rows []TourLegTelemetry
	// Three strong tours at 10,000/hr, then a collapse to 1,000/hr on the newest.
	for i := 0; i < 3; i++ {
		at := start.Add(time.Duration(i) * 3 * time.Hour)
		rows = append(rows, completedTour("tour-strong-"+string(rune('a'+i)), "HAULER-A", 100, 0, 100, at, time.Hour)...)
	}
	collapsed := start.Add(9 * time.Hour)
	rows = append(rows, completedTour("tour-collapsed", "HAULER-A", 100, 90, 100, collapsed, time.Hour)...)

	got, ok := EwmaHullTourRate(rows, "HAULER-A", DefaultHullRateSmoothing)
	if !ok {
		t.Fatal("no readable rate for a hull with four computable tours")
	}

	// The median of {10000, 10000, 10000, 1000} is 10,000 — a median would not have noticed the
	// collapse at all. At smoothing 0.5 the newest tour carries half the reading, so the EWMA must
	// have fallen well below the strong level and must sit above the newest tour alone.
	if got >= 10000 {
		t.Fatalf("EWMA %.2f/hr did not fall below the pre-collapse 10,000/hr — the newest tour is not weighted heaviest (a median would report 10,000 here)", got)
	}
	if got <= 1000 {
		t.Fatalf("EWMA %.2f/hr collapsed to at-or-below the newest tour alone (1,000/hr) — history is being discarded, not damped", got)
	}
	// s = 0.5*1000 + 0.5*(0.5*10000 + 0.5*(0.5*10000 + 0.5*10000)) = 5500.
	if math.Abs(got-5500) > 1e-6 {
		t.Fatalf("EWMA %.2f/hr, want 5500/hr for rates [10000, 10000, 10000, 1000] at smoothing 0.5", got)
	}
}

// ORDER BY THE CLOCK, NOT BY THE INPUT. Telemetry arrives in repository order; the EWMA's whole
// meaning depends on folding tours in the order they finished.
func TestEwmaHullTourRateShould_FoldToursInFinishOrder_GivenRowsInReverseOrder(t *testing.T) {
	start := time.Date(2026, 7, 30, 6, 0, 0, 0, time.UTC)
	old := completedTour("tour-old", "HAULER-A", 100, 0, 100, start, time.Hour)                         // 10,000/hr
	recent := completedTour("tour-recent", "HAULER-A", 100, 90, 100, start.Add(5*time.Hour), time.Hour) // 1,000/hr

	forward, okForward := EwmaHullTourRate(append(append([]TourLegTelemetry{}, old...), recent...), "HAULER-A", DefaultHullRateSmoothing)
	reversed, okReversed := EwmaHullTourRate(append(append([]TourLegTelemetry{}, recent...), old...), "HAULER-A", DefaultHullRateSmoothing)

	if !okForward || !okReversed {
		t.Fatal("a two-tour hull has no readable rate")
	}
	if math.Abs(forward-reversed) > 1e-6 {
		t.Fatalf("row order changed the rate (%.2f vs %.2f); tours must be folded by finish time, not input order", forward, reversed)
	}
	// 0.5*1000 + 0.5*10000 = 5500 — the recent collapse must be the half-weighted term.
	if math.Abs(forward-5500) > 1e-6 {
		t.Fatalf("rate %.2f/hr, want 5500/hr; the OLDER tour appears to be weighted as the newest", forward)
	}
}

// THE SHARED-CYCLE HAZARD, DIRECTLY. Two hulls working the same ground at the same time must get
// their OWN rates. A metric that bracketed a shared cycle would hand both the same number.
func TestEwmaHullTourRateShould_AttributeEachHullOnlyItsOwnTransactions(t *testing.T) {
	start := time.Date(2026, 7, 30, 6, 0, 0, 0, time.UTC)
	var rows []TourLegTelemetry
	// Concurrent tours, same ground, same clock — one hull earning, one barely earning.
	rows = append(rows, completedTour("tour-star", "HAULER-STAR", 100, 0, 100, start, time.Hour)...)  // 10,000/hr
	rows = append(rows, completedTour("tour-idle", "HAULER-IDLE", 100, 99, 100, start, time.Hour)...) // 100/hr

	star, okStar := EwmaHullTourRate(rows, "HAULER-STAR", DefaultHullRateSmoothing)
	idle, okIdle := EwmaHullTourRate(rows, "HAULER-IDLE", DefaultHullRateSmoothing)

	if !okStar || !okIdle {
		t.Fatal("one of two concurrently-touring hulls has no readable rate")
	}
	if math.Abs(star-10000) > 1e-6 {
		t.Fatalf("the earning hull reads %.2f/hr, want 10000/hr — its neighbour's transactions are leaking in", star)
	}
	if math.Abs(idle-100) > 1e-6 {
		t.Fatalf("the barely-earning hull reads %.2f/hr, want 100/hr — it has inherited its neighbour's rate, the shared-cycle attribution hazard", idle)
	}
}

// FAIL CLOSED. A hull whose earnings cannot be computed must read UNREADABLE, never a readable zero:
// the relocator refuses to move a hull whose current rate it cannot prove.
func TestEwmaHullTourRateShould_FailClosed_GivenNoComputableTour(t *testing.T) {
	start := time.Date(2026, 7, 30, 6, 0, 0, 0, time.UTC)
	buysOnly := []TourLegTelemetry{
		{TourID: "tour-1", ShipSymbol: "HAULER-A", Good: "FUEL", IsBuy: true, RealizedUnits: 100, RealizedUnitPrice: 50, PlannedAt: start, RealizedAt: start.Add(time.Hour)},
	}
	otherHull := completedTour("tour-1", "HAULER-B", 100, 0, 100, start, time.Hour)
	tooShort := completedTour("tour-1", "HAULER-A", 100, 0, 100, start, MinTourSpan-time.Second)

	for name, rows := range map[string][]TourLegTelemetry{
		"no rows at all":                  nil,
		"purchases with no matching sell": buysOnly,
		"only another hull's tours":       otherHull,
		"a span shorter than MinTourSpan": tooShort,
	} {
		if rate, ok := EwmaHullTourRate(rows, "HAULER-A", DefaultHullRateSmoothing); ok {
			t.Fatalf("%s produced a readable rate %.2f/hr; an uncomputable hull rate must fail closed so the relocator cannot move a hull off a fabricated number", name, rate)
		}
	}
}

// A nonsensical smoothing factor must not degenerate the average — 0 would freeze the reading on the
// oldest tour forever, which is the opposite of what the metric is for.
func TestEwmaHullTourRateShould_FallBackToTheFittedSmoothing_GivenAnOutOfRangeFactor(t *testing.T) {
	start := time.Date(2026, 7, 30, 6, 0, 0, 0, time.UTC)
	rows := append(
		completedTour("tour-old", "HAULER-A", 100, 0, 100, start, time.Hour),
		completedTour("tour-recent", "HAULER-A", 100, 90, 100, start.Add(5*time.Hour), time.Hour)...,
	)

	want, _ := EwmaHullTourRate(rows, "HAULER-A", DefaultHullRateSmoothing)
	for _, smoothing := range []float64{0, -1, 1.5} {
		got, ok := EwmaHullTourRate(rows, "HAULER-A", smoothing)
		if !ok {
			t.Fatalf("smoothing %v made a readable hull unreadable", smoothing)
		}
		if math.Abs(got-want) > 1e-6 {
			t.Fatalf("smoothing %v produced %.2f/hr, want the fitted-default %.2f/hr — an out-of-range factor must not degenerate the average", smoothing, got, want)
		}
	}
}
