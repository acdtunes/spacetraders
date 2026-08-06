// Package fleetgrowth holds the pure money math the fleet-growth coordinator judges a heavy
// purchase against. No clock, no I/O, no stored state — observations to a credit count.
package fleetgrowth

// WorkingCapital is the credits a heavy purchase must leave behind ON TOP OF the immutable
// reserve floor. It is a term ABOVE that floor and never a replacement for it, so no money
// guard is relaxed by adding it (RULINGS #4).
//
//	working_capital = max(
//	    runwayMilliHours × cargoOutflow / 1000,
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
// MILLI-HOURS AND INTEGERS THROUGHOUT. Sub-hour runway is the operating range, and a float
// would put NaN inside a money guard, where it fails every comparison and passes every clamp.
// Integer milli cannot express NaN at all, so the failure mode does not exist rather than
// being defended against.
//
// FAILS STRICTER. Every factor is clamped non-negative BEFORE its product is formed, so a
// negative multiplier and a negative observation cannot multiply into a phantom windfall; the
// product is formed before the divide so a legitimate sub-hour reserve cannot round to zero.
// A malformed observation can therefore only ever raise this number.
func WorkingCapital(runwayMilliHours int, cargoOutflow int64, tradeHulls int, largestSingleFill int64) int64 {
	runway := maxInt64(0, int64(runwayMilliHours)) * maxInt64(0, cargoOutflow) / 1000
	holdFill := maxInt64(0, int64(tradeHulls)) * maxInt64(0, largestSingleFill)
	return maxInt64(runway, holdFill)
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
