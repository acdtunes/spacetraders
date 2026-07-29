package trading

import (
	"testing"
	"time"
)

// tour_rate_matching_test.go pins the MATCHING rule behind MedianTourRate: a window may only score a
// trade whose buy and sell both fall inside it.
//
// THE DEFECT, measured on the live fleet. The trailing window admits a leg by planned_at, and a
// tour's legs are planned incrementally as it runs (25 of 27 live tours spread their planned_at by
// more than a minute, one by 3.6 hours). So a window routinely holds PURCHASES with none of the
// revenue they earn, and the tour is scored on the cost alone.
//
// The existing hasSell guard catches only the pure case — a tour with no realized sell at all. It
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

// β NOW USES MATCHED NETS TOO. This replaces TestMedianTourRate_IsDeliberatelyLeftOnUnmatchedNets,
// which pinned the opposite while β's two consumers were still unassessed.
//
// They have now been measured, and the assessment inverted the call. BOTH consumers fail CLOSED on a
// non-positive β — the placement engine falls back to the legacy static-floor reposition, and the
// rate-floor relocation trigger does nothing at all — so an unmatched-net β never accepts a bad
// tour; it silences a mechanism. Correcting it is a noise reduction, not a policy change. And
// leaving β on a netting rule of its own would mean two live definitions of "the realized tour rate"
// free to drift apart.
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

// THE SIDE DOOR IN THE MATCHING RULE, re-pinned on β. matchedTradesOnly must count a leg as half a
// match on REALIZED UNITS, not on the leg merely being present: a SKIPPED sell leg (planned, never
// executed — RealizedUnits 0) is not a sale, and if it were allowed to complete a match the whole
// rule would silently no-op for every tour that skipped a leg. That is the failure mode where the
// fix looks applied and is not.
//
// The fixture is built so the two readings DISAGREE: FUEL is genuinely completed (+40,000 over 1h);
// MACHINERY is bought and its sell leg SKIPPED. Counting the skipped leg would net the MACHINERY
// purchase in and read the tour at -160,000/hr.
//
// (Previously pinned through ComputeFleetTourRate, which was deleted with the autosizer's
// realized_rate / era_payback guards. matchedTradesOnly itself survives on β's path, so the rule
// keeps its own coverage here.)
func TestMedianTourRate_ASkippedSellLegDoesNotMatchAPurchase(t *testing.T) {
	h := hourly(matchBase)
	rows := []TourLegTelemetry{
		mleg("t1", "A", "FUEL", true, 20, 1000, h(0), h(0)),
		mleg("t1", "A", "FUEL", false, 20, 3000, h(0), h(1)),
		mleg("t1", "A", "MACHINERY", true, 40, 5000, h(0), h(1)),
		// The sell leg exists in the plan but realized NOTHING — a skip, not a sale.
		mleg("t1", "A", "MACHINERY", false, 0, 0, h(0), h(1)),
	}

	got, ok := MedianTourRate(rows)
	if !ok {
		t.Fatalf("expected a computable median (FUEL completed)")
	}
	if got != 40000 {
		t.Fatalf("median = %v, want 40000 — a SKIPPED sell leg (0 realized units) must not complete the "+
			"MACHINERY match; -160000 means presence, not realized units, is closing the trade", got)
	}
}

// AN UNMATCHED LEG STILL WIDENS THE SPAN, re-pinned on β. Excluding a trade from the MONEY must not
// also erase the TIME: the hull was working those hours, and dropping them would divide a real net
// by a short span and report a rate far above what the hull actually earns.
//
// Both fixtures complete the same single FUEL trade for +40,000. The second also carries an
// unmatched MACHINERY purchase two hours later, which contributes no money but extends the tour's
// wall-clock span from 1h to 3h — so the honest rate is a THIRD of the first, not equal to it.
//
// (Previously pinned through ComputeFleetTourRate; re-pinned here for the same reason as above.)
func TestMedianTourRate_UnmatchedLegsStillWidenTheSpan(t *testing.T) {
	h := hourly(matchBase)
	completedOnly := []TourLegTelemetry{
		mleg("t1", "A", "FUEL", true, 20, 1000, h(0), h(0)),
		mleg("t1", "A", "FUEL", false, 20, 3000, h(0), h(1)),
	}
	withUnmatchedTail := append(append([]TourLegTelemetry{}, completedOnly...),
		mleg("t1", "A", "MACHINERY", true, 40, 5000, h(2), h(3)))

	narrow, ok := MedianTourRate(completedOnly)
	if !ok || narrow != 40000 {
		t.Fatalf("baseline median = %v (ok=%v), want 40000 over the 1h span", narrow, ok)
	}
	wide, ok := MedianTourRate(withUnmatchedTail)
	if !ok {
		t.Fatalf("expected a computable median with the unmatched tail present")
	}
	if wide != 40000.0/3 {
		t.Fatalf("median = %v, want %v — the unmatched MACHINERY leg contributes no money but the hull "+
			"was still working to h(3), so the span must widen 1h → 3h; %v would mean the unmatched leg "+
			"was erased from the clock as well as the ledger", wide, 40000.0/3, narrow)
	}
}
