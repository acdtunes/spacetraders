package parkedsensing

// ProbeBuyFloor is the treasury a probe purchase must leave behind: the
// immutable working-capital floor, plus whatever capex the operation has
// earmarked, plus k hours of cargo runway.
//
// The runway term is what makes the floor dynamic. A probe is cheap, but
// buying one out of the money a trading fleet is about to spend on cargo
// stalls the trades that fund everything else — so the busier the fleet, the
// more the floor holds back.
//
// The guard fails closed (RULINGS #4). Every term is clamped non-negative
// before it is added, including each factor of the runway product
// independently, so a negative multiplier and a negative spend cannot
// multiply into a phantom windfall. The result is then floored at the
// immutable reserve, which also catches an arithmetic overflow that wrapped
// the sum negative. A malformed observation upstream can therefore only ever
// make this guard stricter, never weaker.
func ProbeBuyFloor(immutable int64, capexReserve int64, cargoSpendPerHour int64, k int) int64 {
	floor := maxInt64(0, immutable)
	floor += maxInt64(0, capexReserve)
	floor += maxInt64(0, int64(k)) * maxInt64(0, cargoSpendPerHour)

	return maxInt64(floor, immutable)
}

// CargoSpendPerHour names the recent cargo outflow for call-site readability:
// the caller sums the absolute value of the last hour's cargo transactions and
// passes the result straight through. Clamping a malformed sum is
// ProbeBuyFloor's job, so nothing is done to the value here.
func CargoSpendPerHour(sumAbsAmountLastHour int64) int64 {
	return sumAbsAmountLastHour
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
