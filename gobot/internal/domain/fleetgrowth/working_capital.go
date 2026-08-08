// Package fleetgrowth holds the pure money math the fleet-growth coordinator judges a heavy
// purchase against. No clock, no I/O, no stored state — observations to a credit count.
package fleetgrowth

// The arm names WorkingCapitalTerms.Binding reports. They are label values on the growth gauges and
// literals in the decision line, so they are declared once here rather than spelled at each writer.
const (
	// BindingRunway — the reserve came from the unrecovered-position runway.
	BindingRunway = "runway"
	// BindingHoldFill — the reserve came from the per-hull hold-fill bound.
	BindingHoldFill = "hold_fill"
	// BindingNone — neither arm reserved anything; the buy is held back by the immutable floor alone.
	BindingNone = "none"
)

// CargoOutflow is ONE trailing window of the trading fleet's cargo ledger, read in one pass so the
// terms derived from it cannot disagree about which rows they saw.
//
// SPENT AND RECOVERED ARE THE TWO SIDES OF THE SAME CARGO, and the difference between them is the
// only part of the spend that is still tied up. Reading spend alone counts a credit again every time
// it recycles through buy→sell→buy, so a fleet turning its float over four times inside the window
// reports four times the capital it actually has committed.
//
// COMPLETE IS THE READER'S OWN VERDICT ON ITS WINDOW, and it is false by default on purpose. A read
// that hit a row bound saw an unknown slice of the buys against a fuller slice of the sells, and
// netting those two understates the position by exactly what it could not see. Recovery is
// subtracted only when the whole window was seen, so a forgotten field falls back to the gross
// measure — the strict direction (RULINGS #4).
type CargoOutflow struct {
	// Spent is the absolute cargo-purchase outflow over the window.
	Spent int64
	// Recovered is the cargo-sale inflow over the SAME window.
	Recovered int64
	// Largest is the single biggest cargo purchase in the window — the per-hull scale.
	Largest int64
	// Complete is true only when neither side of the read was truncated.
	Complete bool
}

// WorkingCapitalTerms are the two arms the reserve is the maximum of, kept apart rather than
// collapsed at the source.
//
// THE ARM THAT BINDS IS HALF THE DIAGNOSIS. A single credit figure in the decision line says a heavy
// was refused but not which measure refused it, and the two arms fail for opposite reasons and are
// fixed with different knobs — the runway by retuning growth_runway_milli_hours or by trading down
// the position, the hold-fill by the pool's size and the fills it takes. Collapsing them at the
// source made the field diagnosis a source read.
type WorkingCapitalTerms struct {
	// Runway is runwayMilliHours applied to the UNRECOVERED position.
	Runway int64
	// HoldFill is the trade pool's count times the largest observed fill.
	HoldFill int64
}

// Credits is the reserve itself: the larger of the two arms.
func (t WorkingCapitalTerms) Credits() int64 { return maxInt64(t.Runway, t.HoldFill) }

// Binding names the arm the reserve came from, and BindingNone when neither reserved anything.
//
// A TIE RESOLVES TO THE RUNWAY ARM, ALWAYS. The credit figure is identical either way, so the choice
// is about the LABEL: a name that flips between two arms on identical inputs is a series no operator
// can alert on.
func (t WorkingCapitalTerms) Binding() string {
	switch {
	case t.Credits() <= 0:
		return BindingNone
	case t.Runway >= t.HoldFill:
		return BindingRunway
	default:
		return BindingHoldFill
	}
}

// WorkingCapital is the credits a heavy purchase must leave behind ON TOP OF the immutable
// reserve floor. It is a term ABOVE that floor and never a replacement for it, so no money
// guard is relaxed by adding it (RULINGS #4).
//
//	unrecovered     = max(0, spent − recovered)          ← recovered only when the window is COMPLETE
//	working_capital = max(
//	    runwayMilliHours × unrecovered / 1000,
//	    tradeHulls × largestSingleFill,
//	)
//
// TWO TERMS, BECAUSE ONE OF THEM IS BLIND EXACTLY WHEN IT MATTERS. The runway term asks what
// the fleet is already committed to spending: the busier it is, the more of the treasury is
// spoken for, and buying a hull out of money the trading fleet is about to spend on cargo
// stalls the trades that fund everything else. But a hull bought a minute ago has no spend
// history, so the observed runway does not contain it — and that is precisely the moment
// capacity was added. The hold-fill term is per-hull-scale by construction and therefore does
// not dilute as the pool grows, which is what makes it bind on fresh capacity.
//
// THE RUNWAY MEASURES A POSITION, NOT A TURNOVER. Gross cargo spend over a fixed window is not the
// capital the fleet has committed: the same credits are counted again on every recycle, so the
// measure GROWS AS TRADING IMPROVES and a fleet cycling its float four times in the window reserves
// four times its float. Netting the sales back out leaves what the fleet has committed and not yet
// recovered — which is what working capital means, and what a hull purchase actually competes with.
// Both regimes keep the arm they need: a fleet that has bought and not sold nets nothing away and
// reserves its full spend exactly as before, while a fleet recovering everything it spends stands
// the arm down and falls through to the hold-bounded float beneath it.
//
// MILLI-HOURS AND INTEGERS THROUGHOUT. Sub-hour runway is the operating range, and a float
// would put NaN inside a money guard, where it fails every comparison and passes every clamp.
// Integer milli cannot express NaN at all, so the failure mode does not exist rather than
// being defended against.
//
// FAILS STRICTER. Every factor is clamped non-negative BEFORE its product is formed, so a
// negative multiplier and a negative observation cannot multiply into a phantom windfall; the
// product is formed before the divide so a legitimate sub-hour reserve cannot round to zero.
// A malformed observation can therefore only ever raise this number — BOUNDED, in both
// directions: recovery is clamped non-negative before it is subtracted, because a negative
// recovery left alone would reserve MORE than the fleet ever spent, which starves the buy path
// on a bad row just as surely as a negative reserve would weaken the floor.
func WorkingCapital(runwayMilliHours int, obs CargoOutflow, tradeHulls int) WorkingCapitalTerms {
	recovered := int64(0)
	if obs.Complete {
		recovered = maxInt64(0, obs.Recovered)
	}
	unrecovered := maxInt64(0, maxInt64(0, obs.Spent)-recovered)

	return WorkingCapitalTerms{
		Runway:   maxInt64(0, int64(runwayMilliHours)) * unrecovered / 1000,
		HoldFill: maxInt64(0, int64(tradeHulls)) * maxInt64(0, obs.Largest),
	}
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
