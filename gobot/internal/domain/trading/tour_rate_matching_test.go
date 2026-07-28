package trading

import (
	"testing"
	"time"
)

// tour_rate_matching_test.go pins the MATCHING rule: a window may only score a trade whose buy and
// sell both fall inside it.
//
// THE DEFECT, measured on the live fleet. The trailing window admits a leg by planned_at, and a
// tour's legs are planned incrementally as it runs (25 of 27 live tours spread their planned_at by
// more than a minute, one by 3.6 hours). So a window routinely holds a ship's PURCHASES with none of
// the revenue they earn, and the ship is scored on the cost alone. Measured end-to-end on the live
// 12h window, three hulls read -240,510/hr, -165,541/hr and -37,023/hr on a profitable fleet where
// nothing was losing money; corrected, the same three read +35,314, +13,260 and +104,618.
//
// The existing hasSell guard catches only the pure case — a ship with no realized sell at all. It
// does not catch the PARTIAL case, which is the one that actually fires: live tour
// tour-run-TORWIND-40-9d349727 earned +48,820 on ADVANCED_CIRCUITRY and +44,320 on LAB_INSTRUMENTS,
// both bought and sold inside the window, while MACHINERY and MICROPROCESSORS sat bought-but-unsold
// in the hold for -185,360. Netted together that tour reads as a deep loss. It is in fact making money.
//
// THE UNIT IS (tour, good), and the fixtures below are built so the old rule and the new one
// DISAGREE. A fixture whose every good is bought and sold makes this fix invisible — both rules
// return the same number and nothing is proven.

// mleg builds one telemetry row WITH a good, which is what makes a trade matchable. The existing
// tleg helper leaves Good empty, so every leg of a tour shares one trade there; these tests need
// several goods per tour to express a partial match at all.
func mleg(tour, ship, good string, isBuy bool, units, price int, planned, realized time.Time) TourLegTelemetry {
	return TourLegTelemetry{
		TourID:            tour,
		ShipSymbol:        ship,
		Good:              good,
		IsBuy:             isBuy,
		RealizedUnits:     units,
		RealizedUnitPrice: price,
		PlannedAt:         planned,
		RealizedAt:        realized,
		PlayerID:          1,
	}
}

func hourly(base time.Time) func(int) time.Time {
	return func(n int) time.Time { return base.Add(time.Duration(n) * time.Hour) }
}

var matchBase = time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)

// THE HEADLINE CASE. A ship whose window holds a completed trade AND a purchase whose sale falls
// outside is scored on the completed trade, not on the orphan cost.
//
// Shaped on live tour tour-run-TORWIND-40-9d349727: one good bought and sold at a profit, another
// bought and still in the hold. Under the old rule the orphan cost swamps the profit and the hull
// reads as a large negative; nothing about that number describes the hull.
func TestComputeFleetTourRate_InWindowPurchaseWithSaleOutsideIsNotAFakeNegative(t *testing.T) {
	h := hourly(matchBase)
	rows := []TourLegTelemetry{
		// MATCHED: buy 20@1000, sell 20@3000 -> +40,000.
		mleg("t1", "A", "ADVANCED_CIRCUITRY", true, 20, 1000, h(0), h(0)),
		mleg("t1", "A", "ADVANCED_CIRCUITRY", false, 20, 3000, h(0), h(1)),
		// UNMATCHED: bought 40@5000 = 200,000 still in the hold; its sale is outside the window.
		mleg("t1", "A", "MICROPROCESSORS", true, 40, 5000, h(0), h(1)),
		// A second ship so the sample is not the vacuous n=1 case.
		mleg("t2", "B", "FUEL", true, 10, 1000, h(0), h(0)),
		mleg("t2", "B", "FUEL", false, 10, 3000, h(0), h(1)),
	}

	r := ComputeFleetTourRate(rows)
	if !r.Readable {
		t.Fatalf("a ship with a completed trade must be readable")
	}
	// Span h(0)->h(1) = 1h, so ship A's honest rate is its matched net: +40,000/hr.
	if r.Marginal < 0 {
		t.Fatalf("marginal = %v, want a non-negative rate — the -200,000 orphan purchase has no revenue "+
			"inside the window and must not be scored as a loss", r.Marginal)
	}
	if r.Marginal != 20000 {
		// mean(A=40000, B=20000) -> marginal is B at 20000; A must NOT be -160000.
		t.Fatalf("marginal = %v, want 20000 (ship B) — ship A's orphan purchase dragged it below B", r.Marginal)
	}
	if r.FleetAvg != 30000 {
		t.Fatalf("fleet-avg = %v, want mean(40000, 20000)=30000", r.FleetAvg)
	}
}

// A ship whose window holds ONLY unmatched activity has no rate at all. It is EXCLUDED — never
// scored as a readable zero, which the contract reserves for genuinely-earning-zero, and never
// scored on its purchases.
func TestComputeFleetTourRate_ShipWithNoMatchedTradeIsExcludedNotScoredZero(t *testing.T) {
	h := hourly(matchBase)
	rows := []TourLegTelemetry{
		// Two measurable ships, so the exclusion below cannot be blamed on the arity guard.
		mleg("t1", "A", "FUEL", true, 10, 1000, h(0), h(0)),
		mleg("t1", "A", "FUEL", false, 10, 3000, h(0), h(1)),
		mleg("t2", "B", "FUEL", true, 10, 1000, h(0), h(0)),
		mleg("t2", "B", "FUEL", false, 10, 2000, h(0), h(1)),
		// Ship C: bought only. Its sale is outside the window.
		mleg("t3", "C", "MACHINERY", true, 40, 5000, h(0), h(1)),
	}

	r := ComputeFleetTourRate(rows)
	if !r.Readable {
		t.Fatalf("two measurable ships must be readable")
	}
	if r.FleetAvg != 15000 {
		t.Fatalf("fleet-avg = %v, want mean(20000, 10000)=15000 — ship C must be absent from the sample "+
			"entirely, neither as a negative nor as a zero", r.FleetAvg)
	}
	if r.Marginal != 10000 {
		t.Fatalf("marginal = %v, want 10000 (ship B) — ship C has no rate to be the minimum of", r.Marginal)
	}
}

// A SKIPPED LEG IS NOT HALF A MATCH. A sell leg that was planned and then skipped carries zero
// realized units — no cargo moved — so it cannot be the sold side of a trade.
//
// This is the route by which the whole artifact leaks back in through a side door: the tour planner
// emits a sell leg for every good it buys, so if leg PRESENCE counted rather than realized units,
// EVERY purchase would look matched and the fix would do nothing at all. The orphan cost would be
// scored again, exactly as before, while the code read as though it were matching.
func TestComputeFleetTourRate_ASkippedSellLegDoesNotMatchAPurchase(t *testing.T) {
	h := hourly(matchBase)
	rows := []TourLegTelemetry{
		// Ship A: one genuinely completed trade worth +40,000 over 1h.
		mleg("t1", "A", "FUEL", true, 20, 1000, h(0), h(0)),
		mleg("t1", "A", "FUEL", false, 20, 3000, h(0), h(1)),
		// …and MACHINERY bought for 200,000 whose sell leg was PLANNED but skipped: the leg exists,
		// the units are zero. The cargo is still in the hold.
		mleg("t1", "A", "MACHINERY", true, 40, 5000, h(0), h(1)),
		mleg("t1", "A", "MACHINERY", false, 0, 6000, h(0), h(1)),
		mleg("t2", "B", "FUEL", true, 10, 1000, h(0), h(0)),
		mleg("t2", "B", "FUEL", false, 10, 3000, h(0), h(1)),
	}

	r := ComputeFleetTourRate(rows)
	if !r.Readable {
		t.Fatalf("readable expected")
	}
	if r.FleetAvg != 30000 {
		t.Fatalf("fleet-avg = %v, want mean(A=40000, B=20000)=30000 — the skipped sell moved no cargo, so "+
			"MACHINERY is still an unmatched purchase and its -200,000 must stay out of the net", r.FleetAvg)
	}
	if r.Marginal != 20000 {
		t.Fatalf("marginal = %v, want 20000 (ship B) — ship A read below it, so the skipped leg was "+
			"accepted as the sold half of a trade that never happened", r.Marginal)
	}
}

// THE MIRROR ARTIFACT, and it moves the number the other way. A good SOLD inside the window whose
// purchase happened before it starts is revenue with no cost, which reads as a fake HIGH rate.
//
// Excluding it is the stricter direction and the same rule: the trade's other half is outside the
// measurement, so the window cannot price it. This case is reachable for the same reason the
// negative one is — a tour's legs are planned incrementally, so a tour straddles the boundary.
func TestComputeFleetTourRate_SellWhoseBuyPrecedesTheWindowIsNotFreeRevenue(t *testing.T) {
	h := hourly(matchBase)
	rows := []TourLegTelemetry{
		mleg("t1", "A", "FUEL", true, 10, 1000, h(0), h(0)),
		mleg("t1", "A", "FUEL", false, 10, 3000, h(0), h(1)),
		// Ship B: one matched trade, plus a windfall sale of cargo bought before the window.
		mleg("t2", "B", "FUEL", true, 10, 1000, h(0), h(0)),
		mleg("t2", "B", "FUEL", false, 10, 2000, h(0), h(1)),
		mleg("t2", "B", "GOLD", false, 100, 9000, h(0), h(1)), // +900,000 with no cost recorded
	}

	r := ComputeFleetTourRate(rows)
	if !r.Readable {
		t.Fatalf("readable expected")
	}
	if r.FleetAvg != 15000 {
		t.Fatalf("fleet-avg = %v, want mean(20000, 10000)=15000 — the 900,000 sale has no matching "+
			"purchase inside the window and must not be counted as earnings", r.FleetAvg)
	}
}

// AN UNMATCHED LEG STILL WIDENS THE SPAN. The hull was working during those hours — flying out to
// buy the cargo it is still carrying — so the time counts even though the money does not.
//
// This is deliberately the CONSERVATIVE choice and it is load-bearing: dropping the orphan leg from
// the span too would divide the same net by fewer hours and report a HIGHER rate, which is the
// direction RULINGS #4 forbids a measurement to drift on its own.
func TestComputeFleetTourRate_UnmatchedLegsStillWidenTheSpan(t *testing.T) {
	h := hourly(matchBase)
	rows := []TourLegTelemetry{
		// Ship A: matched trade over h0->h1 worth +40,000, then an orphan purchase realized at h3.
		mleg("t1", "A", "FUEL", true, 20, 1000, h(0), h(0)),
		mleg("t1", "A", "FUEL", false, 20, 3000, h(0), h(1)),
		mleg("t1", "A", "MACHINERY", true, 40, 5000, h(2), h(3)),
		mleg("t2", "B", "FUEL", true, 10, 1000, h(0), h(0)),
		mleg("t2", "B", "FUEL", false, 10, 3000, h(0), h(1)),
	}

	r := ComputeFleetTourRate(rows)
	// Ship A: net 40,000 over the FULL span h0->h3 = 3h -> 13,333.33/hr, not 40,000/hr over 1h.
	want := 40000.0 / 3.0
	if r.Marginal < want-0.01 || r.Marginal > want+0.01 {
		t.Fatalf("marginal = %v, want %v — the orphan purchase's hours must stay in the denominator, "+
			"or excluding it would inflate the rate", r.Marginal, want)
	}
}

// THE SAFETY PROPERTY. Excluding unmeasurable ships must never shrink the sample into one the guard
// reads as unanimous.
//
// The autosizer compares MIN per-ship against 0.7 x MEAN. At n=1 those are the same number, so the
// comparison is a tautology and the guard stops being a guard — a fleet that fails the test at n=6
// would PASS it at n=1. If this rule's own exclusions are what took the sample there, the honest
// answer is that the window cannot be read, not that everything is fine.
func TestComputeFleetTourRate_ExclusionMayNotShrinkTheSampleIntoAVacuousOne(t *testing.T) {
	h := hourly(matchBase)
	rows := []TourLegTelemetry{
		// One genuinely measurable ship.
		mleg("t1", "A", "FUEL", true, 10, 1000, h(0), h(0)),
		mleg("t1", "A", "FUEL", false, 10, 3000, h(0), h(1)),
	}
	// Five ships the OLD sell-only rule would have kept — each has a realized sell — but whose
	// purchases are outside the window, so the new rule finds no matched trade for any of them.
	for _, ship := range []string{"B", "C", "D", "E", "F"} {
		rows = append(rows, mleg("t-"+ship, ship, "GOLD", false, 100, 9000, h(0), h(1)))
	}

	r := ComputeFleetTourRate(rows)
	if r.Readable {
		t.Fatalf("readable=true with FleetAvg=%v marginal=%v — five of six ships were excluded and the "+
			"survivor's min equals its own mean, so the 0.7x floor is satisfied by arithmetic rather "+
			"than by economics; that is a spending loosening arriving through the back door",
			r.FleetAvg, r.Marginal)
	}
}

// A ONE-SHIP SAMPLE IS UNREADABLE, however it came to be one.
//
// This replaces TestComputeFleetTourRate_AGenuineSingleShipFleetIsUnchanged, which pinned the
// opposite while the arity floor was gated on "this rule's own exclusions caused the shrink". That
// gate existed so a measurement fix would not quietly make a threshold decision; the decision has
// since been taken deliberately.
//
// WHY IT HAD TO GO. The autosizer's realized-rate guard tests MIN against 0.7 x MEAN. At n=1 they are
// the same number, so the test is `x >= 0.7x` — true for every positive rate, whatever the economics.
// The guard was not lenient there, it was ABSENT, in front of roughly 2M of hulls. Measured on live
// history with MinTourSpan applied, five of the eight 12h windows holding any measurable ship held
// exactly one.
//
// IT IS A TIGHTENING AND ONLY A TIGHTENING: at n>=2 nothing changes, and at n<=1 a readable
// vacuous pass becomes an unreadable fail-closed. No input is admitted that was previously refused.
func TestComputeFleetTourRate_ASingleShipSampleIsUnreadableHoweverItArose(t *testing.T) {
	h := hourly(matchBase)
	// A genuinely single-hull fleet with a real, healthy completed trade — nothing excluded, nothing
	// artificial. The old rule read this as readable BECAUSE nothing was excluded; that is exactly
	// the case the guard could not evaluate.
	rows := []TourLegTelemetry{
		mleg("t1", "A", "FUEL", true, 10, 1000, h(0), h(0)),
		mleg("t1", "A", "FUEL", false, 10, 3000, h(0), h(1)),
	}

	r := ComputeFleetTourRate(rows)
	if r.Readable {
		t.Fatalf("readable=true with avg=%v marginal=%v — min and mean are the same number here, so "+
			"min >= 0.7*mean is satisfied by arithmetic rather than by economics and the guard is not "+
			"evaluating anything at all", r.FleetAvg, r.Marginal)
	}
	if r.FleetAvg != 0 || r.Marginal != 0 {
		t.Fatalf("unreadable result carried avg=%v marginal=%v, want the zero values", r.FleetAvg, r.Marginal)
	}
}

// TWO measurable ships stay readable, so the floor closes a hole rather than disabling the guard.
func TestComputeFleetTourRate_TwoShipsAreStillReadable(t *testing.T) {
	h := hourly(matchBase)
	rows := []TourLegTelemetry{
		mleg("t1", "A", "FUEL", true, 10, 1000, h(0), h(0)),
		mleg("t1", "A", "FUEL", false, 10, 3000, h(0), h(1)),
		mleg("t2", "B", "FUEL", true, 10, 1000, h(0), h(0)),
		mleg("t2", "B", "FUEL", false, 10, 2000, h(0), h(1)),
	}

	r := ComputeFleetTourRate(rows)
	if !r.Readable {
		t.Fatalf("two measurable ships must stay readable — the floor is the arity at which min-vs-mean " +
			"starts to mean something, not a way to switch the guard off")
	}
	if r.FleetAvg != 15000 || r.Marginal != 10000 {
		t.Fatalf("avg=%v marginal=%v, want 15000 / 10000", r.FleetAvg, r.Marginal)
	}
}

// The decline trend reads the same matched nets. Per-tour rates are computed from the same fold, so
// a tour carrying unsold cargo must not be scored as a collapsing one.
//
// The fixture is adverse: unmatched cargo sits on the OLDER tour, so under the old rule the older
// half reads as a deep loss and the trend looks like it is IMPROVING. Under matched nets both tours
// are honest and the newer one is genuinely worse.
func TestComputeFleetTourRate_DecliningIsComputedFromMatchedNets(t *testing.T) {
	h := hourly(matchBase)
	rows := []TourLegTelemetry{
		// Older tour: +40,000 matched, plus 200,000 of cargo still in the hold.
		mleg("t1", "A", "FUEL", true, 20, 1000, h(0), h(0)),
		mleg("t1", "A", "FUEL", false, 20, 3000, h(0), h(1)),
		mleg("t1", "A", "MACHINERY", true, 40, 5000, h(0), h(1)),
		// Newer tour: +10,000 matched over the same one-hour span — genuinely worse.
		mleg("t2", "B", "FUEL", true, 10, 1000, h(2), h(2)),
		mleg("t2", "B", "FUEL", false, 10, 2000, h(2), h(3)),
	}

	r := ComputeFleetTourRate(rows)
	if !r.Declining {
		t.Fatalf("declining=false — on matched nets the older tour earned 40,000/hr and the newer 10,000/hr, " +
			"so the trend is down; the old rule scored the older tour at -160,000/hr and called that an " +
			"improvement")
	}
}

// β NOW USES MATCHED NETS TOO. This replaces TestMedianTourRate_IsDeliberatelyLeftOnUnmatchedNets,
// which pinned the opposite while β's two consumers were still unassessed.
//
// They have now been measured, and the assessment inverted the call. BOTH consumers fail CLOSED on a
// non-positive β — the placement engine falls back to the legacy static-floor reposition, and the
// rate-floor relocation trigger does nothing at all — so an unmatched-net β never accepts a bad
// tour; it silences a mechanism. Correcting it is a noise reduction, not a policy change. And
// leaving β on a different netting rule from ComputeFleetTourRate would mean two live definitions of
// "the realized tour rate" free to drift apart.
func TestMedianTourRate_UsesMatchedNets(t *testing.T) {
	h := hourly(matchBase)
	rows := []TourLegTelemetry{
		mleg("t1", "A", "FUEL", true, 20, 1000, h(0), h(0)),
		mleg("t1", "A", "FUEL", false, 20, 3000, h(0), h(1)),
		// Orphan cost: bought, not sold inside the window. Under the old rule this dragged the tour
		// to -160,000/hr; the tour is in fact earning +40,000/hr on the trade it completed.
		mleg("t1", "A", "MACHINERY", true, 40, 5000, h(0), h(1)),
	}

	got, ok := MedianTourRate(rows)
	if !ok {
		t.Fatalf("expected a computable median")
	}
	if got != 40000 {
		t.Fatalf("median = %v, want 40000 — the MACHINERY purchase has no sale inside the window, so its "+
			"-200,000 is not this tour's result; -160000 is the old unmatched reading", got)
	}
}

// β's FAIL-CLOSED CONTRACT SURVIVES, and it is the one property both consumers depend on: ok=false
// when nothing is computable, never a fabricated readable zero. The placement caller falls back to
// the legacy engine on it and the rate-floor trigger stays silent, so a fabricated zero would have
// both of them deciding off an invented rate.
//
// Matching makes this reachable in a NEW way — a window holding only unmatched activity used to
// yield a number and is now uncomputable — so it is pinned on exactly that shape.
func TestMedianTourRate_FailsClosedWhenOnlyUnmatchedActivityIsInTheWindow(t *testing.T) {
	h := hourly(matchBase)
	rows := []TourLegTelemetry{
		// Bought, never sold in-window.
		mleg("t1", "A", "MACHINERY", true, 40, 5000, h(0), h(1)),
		// Sold, never bought in-window — the mirror: a windfall with no cost. Measured live, this
		// direction DOMINATES on β's 60-minute window, which is why correcting it moves β DOWN.
		mleg("t2", "B", "GOLD", false, 100, 9000, h(0), h(1)),
	}

	got, ok := MedianTourRate(rows)
	if ok {
		t.Fatalf("ok=true (median %v) — a window with no completed trade must be UNREADABLE so the "+
			"placement engine falls back and the rate-floor trigger stays silent; a readable value here "+
			"is a rate nobody measured", got)
	}
	if got != 0 {
		t.Fatalf("unreadable median returned %v, want the zero value", got)
	}
}
