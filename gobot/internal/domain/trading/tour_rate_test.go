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

// β is the per-TOUR MEDIAN realized $/hr, never the mean — so one blowout tour
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

// β fails CLOSED — empty rows, buys with no realized sell, and a zero-span tour
// each yield ok=false, never a misleading readable 0. A placement caller that cannot see β falls
// back to the legacy engine; a fabricated 0 would silently arm the park floor at φ*0 = 0.
// sp-461l (epic sp-g9td): MedianTourRate is the realized-rate SOURCE the reposition rate-floor
// (run_tour_coordinator_rate_floor.senseRateFloor) and placement β (run_tour_coordinator_placement.
// senseBeta) steer on. Those consumers STAY on telemetry — they need PER-TOUR / PER-HULL rates the
// transactions ledger (no ship column) cannot give, and β must be dimensionally commensurable with
// the per-candidate PROJECTED E_x — but sp-rd21's write-path fix (dropped buy legs now recorded) is
// what makes the telemetry honest. This pins the fix's effect at the source: with the buy legs
// PRESENT the median is the NETTED (true) rate; the sells-only pathology sp-rd21 diagnosed read ~2x
// higher, so a hull with dropped buys looked like a star and the under-earner floor relocated the
// WRONG hulls. The consumers now relocate/hold on the true rate.
func TestMedianTourRate_NetsBuyLegs_NotSellsOnlyInflated(t *testing.T) {
	base := time.Date(2026, 7, 18, 6, 0, 0, 0, time.UTC)
	end := base.Add(time.Hour)
	// Two 1h tours, each: buy 100@1000 (−100k) then sell 100@2000 (+200k) ⇒ TRUE net 100k/hr.
	netted := []TourLegTelemetry{
		tleg("t1", "S1", true, 100, 1000, base, base),
		tleg("t1", "S1", false, 100, 2000, base, end),
		tleg("t2", "S2", true, 100, 1000, base, base),
		tleg("t2", "S2", false, 100, 2000, base, end),
	}
	trueMedian, ok := MedianTourRate(netted)
	if !ok || trueMedian != 100000 {
		t.Fatalf("netted median = %.0f (ok=%v), want 100000 (sell 200k − buy 100k over 1h)", trueMedian, ok)
	}

	// The dropped-buy pathology: the SAME tours with the BUY legs MISSING (what tour_leg_telemetry
	// looked like before sp-rd21). Netting alone read this as 200k/hr — 2x the true rate.
	sellsOnly := []TourLegTelemetry{
		tleg("t1", "S1", false, 100, 2000, base, end),
		tleg("t2", "S2", false, 100, 2000, base, end),
	}

	// IT IS NOW REFUSED OUTRIGHT, not merely netted down, and that is strictly stronger than what
	// this test used to assert. Under matched nets a sell with no purchase inside the window is an
	// unpriceable half-trade — the window cannot say what it cost — so no tour is computable and β is
	// UNREADABLE. Both β consumers fail closed on that (the placement engine falls back to the legacy
	// reposition, the rate-floor trigger stays silent), so the inflated 200k/hr can no longer reach a
	// decision at all rather than reaching it at half strength.
	inflatedMedian, inflatedOK := MedianTourRate(sellsOnly)
	if inflatedOK {
		t.Fatalf("sells-only median = %.0f (ok=true), want UNREADABLE — a sale whose purchase is outside "+
			"the window has no cost basis, so it must not produce a rate at all", inflatedMedian)
	}
	if inflatedMedian != 0 {
		t.Fatalf("unreadable sells-only median returned %.0f, want the zero value", inflatedMedian)
	}
	// The true, fully-matched shape is still readable — the refusal above is about the missing half,
	// not about being strict with everything.
	if trueMedian != 100000 {
		t.Fatalf("netted median = %.0f, want 100000", trueMedian)
	}
}

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

// --- the minimum-span floor (a near-zero span makes the rate meaningless) ---

// A tour whose whole span is under a minute has no computable rate. Dividing by a
// near-zero wall clock does not measure productivity, it amplifies whatever the
// timestamps happened to be: LIVE, a 0.56-second single-leg tour on TORWIND-6
// netting 29,772 reported 189,883,886/hr, and the fleet's worst such reading was
// 836,660,303/hr.
//
// The median lens survives one outlier, but the PER-HULL rate does not when the
// hull has a single tour in the window — there the outlier IS the hull's rate, and
// it is what the under-earner relocation trigger reads. Four of nine hulls on the
// live fleet currently have exactly one computable tour.
func TestMedianTourRate_ADegenerateSpanIsUnreadableNotAstronomical(t *testing.T) {
	base := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	rows := []TourLegTelemetry{
		// A MATCHED trade — both halves realized, so matchedTradesOnly counts it and
		// the SPAN is the only thing left that can exclude it. A lone sell would be
		// dropped by the matching rule instead and this test would pass with no floor
		// at all.
		tleg("degenerate", "A", true, 100, 1000, base, base),
		tleg("degenerate", "A", false, 100, 1297, base, base.Add(560*time.Millisecond)),
	}

	if rate, ok := MedianTourRate(rows); ok {
		t.Fatalf("MedianTourRate = %v, ok=true — a sub-floor span must be UNREADABLE, never a rate", rate)
	}
}

// The floor must never fabricate a readable ZERO, which is the failure mode both
// consumers are built to avoid: the relocation trigger and the placement engine
// each fail closed on unreadable, and a zero would instead read as "this hull earns
// nothing" and flag it permanently.
func TestMedianTourRate_ASubFloorTourYieldsNoRateRatherThanZero(t *testing.T) {
	base := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	rows := []TourLegTelemetry{
		tleg("t1", "A", true, 100, 1000, base, base),
		tleg("t1", "A", false, 100, 2000, base, base.Add(time.Second)),
	}

	rate, ok := MedianTourRate(rows)
	if ok {
		t.Fatalf("MedianTourRate = %v, ok=true — one second of wall clock proves nothing", rate)
	}
	if rate != 0 {
		t.Fatalf("rate = %v, want the zero VALUE alongside ok=false (never a readable zero)", rate)
	}
}

// The boundary, asserted from both sides so the floor is a real threshold rather
// than an accident of the fixture. A tour spanning exactly the floor is computable;
// one a moment short of it is not.
func TestMedianTourRate_TheFloorIsInclusiveAtItsBoundary(t *testing.T) {
	base := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	tour := func(id string, span time.Duration) []TourLegTelemetry {
		return []TourLegTelemetry{
			tleg(id, "A", true, 100, 1000, base, base),
			tleg(id, "A", false, 100, 2000, base, base.Add(span)),
		}
	}

	if _, ok := MedianTourRate(tour("at", MinTourSpan)); !ok {
		t.Fatalf("a tour spanning exactly MinTourSpan (%s) must be computable", MinTourSpan)
	}
	if _, ok := MedianTourRate(tour("under", MinTourSpan-time.Millisecond)); ok {
		t.Fatalf("a tour one millisecond short of MinTourSpan (%s) must not be", MinTourSpan)
	}
}

// A legitimate tour is untouched. Live, the shortest real tour spans 74.8s and the
// median 25.5 minutes, so the floor discards exactly the degenerate reading and
// nothing a hull actually earned.
func TestMedianTourRate_ARealTourIsUnaffectedByTheFloor(t *testing.T) {
	base := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	rows := []TourLegTelemetry{
		tleg("real", "A", true, 100, 1000, base, base),
		tleg("real", "A", false, 100, 2000, base, base.Add(time.Hour)),
	}

	rate, ok := MedianTourRate(rows)
	if !ok {
		t.Fatalf("an hour-long tour must still be computable")
	}
	if rate != 100000 {
		t.Fatalf("rate = %v, want 100000 — the floor must not change the arithmetic", rate)
	}
}

// One degenerate tour must not be able to drag a MIXED set either: it is dropped
// from the sample rather than included at an absurd value, so the median is taken
// over the tours that actually mean something.
func TestMedianTourRate_TheDegenerateTourIsDroppedFromTheSample(t *testing.T) {
	base := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	rows := []TourLegTelemetry{
		// Two real tours at 100k/hr and 60k/hr.
		tleg("r1", "A", true, 100, 1000, base, base),
		tleg("r1", "A", false, 100, 2000, base, base.Add(time.Hour)),
		tleg("r2", "A", true, 100, 1000, base.Add(2*time.Hour), base.Add(2*time.Hour)),
		tleg("r2", "A", false, 100, 1600, base.Add(2*time.Hour), base.Add(3*time.Hour)),
		// ...and the 190M/hr artefact — MATCHED, so only its span can exclude it.
		tleg("bad", "A", true, 100, 1000, base.Add(4*time.Hour), base.Add(4*time.Hour)),
		tleg("bad", "A", false, 100, 1297, base.Add(4*time.Hour), base.Add(4*time.Hour).Add(560*time.Millisecond)),
	}

	rate, ok := MedianTourRate(rows)
	if !ok {
		t.Fatalf("two real tours remain, so the median is still readable")
	}
	// Median of exactly {100000, 60000} — three computable tours would have made
	// this 100000 and hidden the drop.
	if rate != 80000 {
		t.Fatalf("median = %v, want mean(100000,60000)=80000 over the TWO real tours only", rate)
	}
}
