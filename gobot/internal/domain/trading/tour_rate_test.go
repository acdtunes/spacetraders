package trading

import (
	"testing"
	"time"
)

// tleg builds one TourLegTelemetry row for the fleet-tour-rate tests.
func tleg(tour, ship string, isBuy bool, units, price int, planned, realized time.Time) TourLegTelemetry {
	return TourLegTelemetry{
		TourID:            tour,
		ShipSymbol:        ship,
		IsBuy:             isBuy,
		RealizedUnits:     units,
		RealizedUnitPrice: price,
		PlannedAt:         planned,
		RealizedAt:        realized,
		PlayerID:          1,
	}
}

// The fleet rate is the MEAN of per-ship realized $/hr; the marginal is the MIN (the lowest-earning
// heavy); the trend declines when newer tours earn less than older ones.
func TestComputeFleetTourRate_ComputesFleetAvgMarginalAndDecline(t *testing.T) {
	base := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	h := func(n int) time.Time { return base.Add(time.Duration(n) * time.Hour) }
	rows := []TourLegTelemetry{
		// Ship A, tour a1: buy 100@1000 -> sell 100@2000 over 1h = net 100k / 1h = 100k/hr (completes h1).
		tleg("a1", "A", true, 100, 1000, h(0), h(0)),
		tleg("a1", "A", false, 100, 2000, h(0), h(1)),
		// Ship B, tour b1: buy 100@1000 -> sell 100@1600 over 1h = net 60k / 1h = 60k/hr (completes h3).
		tleg("b1", "B", true, 100, 1000, h(2), h(2)),
		tleg("b1", "B", false, 100, 1600, h(2), h(3)),
	}
	r := ComputeFleetTourRate(rows)
	if !r.Readable {
		t.Fatalf("two ships with realized sells must be readable")
	}
	if r.FleetAvg != 80000 {
		t.Fatalf("fleet-avg = %v, want mean(100k,60k)=80000", r.FleetAvg)
	}
	if r.Marginal != 60000 {
		t.Fatalf("marginal = %v, want min(100k,60k)=60000 (the lowest-earning heavy)", r.Marginal)
	}
	// Tours ordered by completion: [a1:100k @h1, b1:60k @h3] -> newer(60k) < older(100k) -> declining.
	if !r.Declining {
		t.Fatalf("a 100k->60k tour trend must read as declining (absorption saturating)")
	}
}

// Empty telemetry is a GENUINE unreadability -> fail closed (no buy).
func TestComputeFleetTourRate_EmptyRows_Unreadable(t *testing.T) {
	if ComputeFleetTourRate(nil).Readable {
		t.Fatalf("no telemetry must be unreadable (fail closed)")
	}
}

// A ship that has only BOUGHT (no realized sell) has no computable rate -> unreadable, not a
// misleading negative rate.
func TestComputeFleetTourRate_OnlyBuysNoRealization_Unreadable(t *testing.T) {
	base := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	rows := []TourLegTelemetry{
		tleg("a1", "A", true, 100, 1000, base, base.Add(time.Hour)),
	}
	if ComputeFleetTourRate(rows).Readable {
		t.Fatalf("a ship with no realized sell has no computable rate -> must be unreadable")
	}
}

// A single completed tour is readable but cannot establish a TREND -> not declining.
func TestComputeFleetTourRate_SingleTour_NotDeclining(t *testing.T) {
	base := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	rows := []TourLegTelemetry{
		tleg("a1", "A", true, 100, 1000, base, base),
		tleg("a1", "A", false, 100, 2000, base, base.Add(time.Hour)),
	}
	r := ComputeFleetTourRate(rows)
	if !r.Readable {
		t.Fatalf("a completed tour must be readable")
	}
	if r.Declining {
		t.Fatalf("a single tour cannot establish a declining trend")
	}
}

// A flat (non-declining) trend across two tours must NOT read as declining.
func TestComputeFleetTourRate_StableTrend_NotDeclining(t *testing.T) {
	base := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	h := func(n int) time.Time { return base.Add(time.Duration(n) * time.Hour) }
	rows := []TourLegTelemetry{
		tleg("a1", "A", true, 100, 1000, h(0), h(0)),
		tleg("a1", "A", false, 100, 2000, h(0), h(1)), // 100k/hr, completes h1
		tleg("a2", "A", true, 100, 1000, h(2), h(2)),
		tleg("a2", "A", false, 100, 2000, h(2), h(3)), // 100k/hr, completes h3
	}
	if ComputeFleetTourRate(rows).Declining {
		t.Fatalf("a flat 100k->100k trend must not read as declining")
	}
}

// RED#1 (sp-z7ng): β is the per-TOUR MEDIAN realized $/hr, never the mean — so one blowout tour
// cannot drag the fleet's placement reference. Three tours at 100k/200k/900k → 200k (the middle),
// not the mean 400k; an even count averages the two middles.
func TestMedianTourRate_PerTourMedianOddAndEven(t *testing.T) {
	base := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	h := func(n int) time.Time { return base.Add(time.Duration(n) * time.Hour) }
	// One buy@0 + one sell@1h per tour ⇒ net = 100*(price) over 1h = price*100 /hr.
	tour := func(id string, sellPrice int) []TourLegTelemetry {
		return []TourLegTelemetry{
			tleg(id, "A", true, 100, 1000, h(0), h(0)),       // buy 100@1000 = -100k
			tleg(id, "A", false, 100, sellPrice, h(0), h(1)), // sell 100@sellPrice over 1h
		}
	}
	// net = 100*sellPrice - 100*1000; sell 2000→100k/hr, 3000→200k/hr, 10000→900k/hr.
	odd := append(append(tour("t1", 2000), tour("t2", 3000)...), tour("t3", 10000)...)
	rate, ok := MedianTourRate(odd)
	if !ok {
		t.Fatalf("three computable tours must be readable")
	}
	if rate != 200000 {
		t.Fatalf("median(100k,200k,900k) = %v, want 200000 (the MIDDLE, not the mean 400000)", rate)
	}
	// Even count: drop t3 → median averages the two middles of {100k,200k} = 150k.
	even := append(tour("t1", 2000), tour("t2", 3000)...)
	rate2, ok2 := MedianTourRate(even)
	if !ok2 || rate2 != 150000 {
		t.Fatalf("median(100k,200k) = %v (ok=%v), want mean-of-two-middles 150000", rate2, ok2)
	}
}

// RED#2 (sp-z7ng): β fails CLOSED — empty rows, buys with no realized sell, and a zero-span tour
// each yield ok=false, never a misleading readable 0. A placement caller that cannot see β falls
// back to the legacy engine; a fabricated 0 would silently arm the park floor at φ*0 = 0.
func TestMedianTourRate_FailsClosedWhenNoComputableTour(t *testing.T) {
	base := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	if _, ok := MedianTourRate(nil); ok {
		t.Fatalf("empty rows must be unreadable (fail closed)")
	}
	buysOnly := []TourLegTelemetry{tleg("t1", "A", true, 100, 1000, base, base.Add(time.Hour))}
	if _, ok := MedianTourRate(buysOnly); ok {
		t.Fatalf("a tour with no realized sell has no computable rate — must be unreadable")
	}
	zeroSpan := []TourLegTelemetry{
		tleg("t1", "A", true, 100, 1000, base, base),
		tleg("t1", "A", false, 100, 2000, base, base), // sell realized at the same instant → zero span
	}
	if _, ok := MedianTourRate(zeroSpan); ok {
		t.Fatalf("a zero-wall-clock-span tour is not computable — must be unreadable")
	}
}
