package common

// THE PROBE LEG'S SATURATION TEST — the corner the wave's own depth term does not cover.
//
// TradeSaturated flips the wave to PROBE when depth <= hold: the fleet can lift everything
// reachable, so another hull carries nothing. This is its OPPOSITE corner. Depth > hold, so the
// wave WANTS a heavy, and the operator's cap forbids one — at which point the cap does not stop the
// spending, it REDIRECTS it: the wave wants heavy, the cap forces PROBE, PROBE buys probes, probes
// raise depth, and the threshold is exceeded by more. Nothing in that cycle observes that its own
// exit condition is unreachable while the cap holds. The re-check is what this file is; it is not a
// second cap and does not touch the first.
//
// WHY UNAFFORDABILITY IS DELIBERATELY NOT IN HERE, though it bars a heavy just as firmly: a cap is
// a bound nothing inside the loop can lift, while unaffordability lifts by itself as a trading
// fleet earns, so that loop does exit. The wave's unreachable clause keeps probe buying running
// there on purpose — "pausing probe buying to save for a purchase nothing can make is a deadlock no
// spender is able to clear" — so folding affordability in would stop BOTH spenders at once.

// ProbeSpendHold names what, if anything, stops probe PURCHASES inside a PROBE wave.
//
// A SEPARATE VOCABULARY FROM WaveProbeReason, and it must stay one: the wave answers "heavy or
// probe", this answers "and is that spending worth anything". WaveProbe's contract already splits
// them — it authorises nothing, and the drain's floor, probe cap and immutable reserve all still
// bind — so this is one more thing the drain's own gate binds. On the wave's enum it would read as
// the two wave readers disagreeing, which is an alarm.
type ProbeSpendHold string

const (
	// ProbeSpendHoldNone is the zero value and the ordinary answer: nothing here stops the buy.
	ProbeSpendHoldNone ProbeSpendHold = ""
	// ProbeSpendHoldHeavyCapped is the loop above, caught: the class that would consume this depth is
	// barred by the operator's cap while the surface already holds more than the fleet can lift.
	ProbeSpendHoldHeavyCapped ProbeSpendHold = "heavy_capped_ample_depth"
)

// ProbeSpendHolds is the closed set of non-empty holds, in publication order. COMPLETENESS IS
// LOAD-BEARING: every reason is written to the gauge each tick (1 for the one holding, 0 for the
// rest), so a hold missing here would publish when it fired and never fall back, reporting a fleet
// that refuses to spend long after it resumed. None is not a reason.
func ProbeSpendHolds() []ProbeSpendHold {
	return []ProbeSpendHold{ProbeSpendHoldHeavyCapped}
}

// ProbeSpendInputs are the facts the hold judges, all read fresh on the tick that asks.
//
// NOT WaveInputs, deliberately: the two are assembled side by side from ONE set of locals in the
// wave port, but an input WaveInputs carried and DeriveWave ignored would be a trap.
type ProbeSpendInputs struct {
	// GrowthEnabled is the heavy buyer's master switch, and OFF RELEASES: a probe-only deployment
	// has no heavy buyer by design, so "no hull will consume this depth" is the intended
	// configuration there and holding would stop its only spender.
	GrowthEnabled bool

	// UnservedLanes and UnservedLanesReadable are the capacity-short signal — the half of the loop
	// that says the wave wants a heavy. Nothing unserved means probing is right on its own terms.
	UnservedLanes         int
	UnservedLanesReadable bool

	// TradeHoldCapacity is the trade pool's summed hold. Required POSITIVE and checked separately
	// from the verdict below, because fleetgrowth.TradeSaturated reports an EMPTY pool as "not
	// saturated" — correct there, a trap here, where !Saturated must mean "deeper than the pool".
	TradeHoldCapacity int

	// TradeSaturated is the DEBOUNCED depth verdict from the ONE shared lane reader, never a second
	// comparison. Saturated RELEASES: raising depth is then what the fleet should do. The reader's
	// dwell costs nothing here — where it differs in the saturated direction the wave is HEAVY,
	// which stopped purchases one gate earlier.
	TradeSaturated          bool
	TradeSaturationReadable bool

	// HeavyCapBinding is POSITIVE EVIDENCE that the cap, not the money and not a missing yard, bars
	// the next heavy — common.HeavyCapBinding, never a re-derivation. IT CANNOT BE THE WAVE'S REASON
	// STRING: in this frame the wave publishes WaveProbeReasonUnreachable, borrowed from the
	// affordability clause (sp-suzfh), so a hold keyed on that label would fire on a genuinely poor
	// fleet — where stopping probe buying is wrong — and keep firing once the label is corrected.
	HeavyCapBinding bool
}

// DeriveProbeSpendHold reports whether probe PURCHASES have a consumer this tick.
//
// DEMAND-REDUCING BY CONSTRUCTION, three ways, none of them a test:
//
//  1. Its range is {None, the one hold}. No value of it means "buy" — the absence of a hold is not
//     permission, it is the caller's existing verdict left untouched.
//  2. Its single consumer ANDs `hold == None` into a conjunction that already had to be true.
//     Adding a conjunct to a boolean AND can only ever turn true into false.
//  3. EVERY RUNG RELEASES. A hold needs positive evidence of all six facts at once, so no blind
//     read, zero or missing wiring can produce one — which is also why the term is byte-identical
//     to today's behaviour outside the frame it names.
//
// Pure: no clock, no I/O, no stored state.
func DeriveProbeSpendHold(in ProbeSpendInputs) ProbeSpendHold {
	// A deployment with no heavy buyer switched on is a coverage deployment. Probing is its point.
	if !in.GrowthEnabled {
		return ProbeSpendHoldNone
	}
	// Something else bars the heavy — affordability, an unknown yard, plain headroom. None of those
	// is a bar with no exit.
	if !in.HeavyCapBinding {
		return ProbeSpendHoldNone
	}
	// PROBE on its own demand reasoning: coverage is then the right spend however deep the surface.
	if !in.UnservedLanesReadable || in.UnservedLanes <= 0 {
		return ProbeSpendHoldNone
	}
	if !in.TradeSaturationReadable {
		return ProbeSpendHoldNone
	}
	if in.TradeHoldCapacity <= 0 {
		return ProbeSpendHoldNone
	}
	if in.TradeSaturated {
		return ProbeSpendHoldNone
	}
	// Both halves evidenced: work the pool cannot lift, and the class that would lift it barred.
	return ProbeSpendHoldHeavyCapped
}
