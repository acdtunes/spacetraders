package parkedsensing

// ProbeBuyFloor is the treasury a probe purchase must leave behind: the
// immutable working-capital floor, plus whatever capex the operation has
// earmarked, plus kMilli/1000 hours of cargo runway.
//
// The runway term is what makes the floor dynamic. A probe is cheap, but
// buying one out of the money a trading fleet is about to spend on cargo
// stalls the trades that fund everything else — so the busier the fleet, the
// more the floor holds back.
//
// WHY MILLI-HOURS AND NOT A FLOAT (2026-07-28). The operator needs SUB-HOUR
// runway: at a measured 1.8M/hr cargo spend, whole-hour granularity jumps the
// floor by 1.8M per step, so k=1 was already several times the treasury and
// k=0 removed the guard entirely — there was no setting between "blocked" and
// "unguarded". Milli-hours give 0.001h resolution using the same integer
// arithmetic, matching min_scan_rate_milli in this coordinator's own tunable
// registry (which is map[string]int end to end).
//
// A float would have introduced NaN into a money guard: NaN fails every
// comparison, so `k > 0` and `k < max` are BOTH false for it, and every
// clamp below would silently pass it through to multiply the runway into
// garbage. Integer milli cannot express NaN at all, so the failure mode does
// not exist rather than being defended against.
//
// The guard fails closed (RULINGS #4). Every term is clamped non-negative
// before it is added, including each factor of the runway product
// independently, so a negative multiplier and a negative spend cannot
// multiply into a phantom windfall. The product is formed BEFORE the divide
// so the division cannot round a legitimate reserve down to zero, and int64
// holds it comfortably: the tune registry caps kMilli at 10000 and cargo
// spend is fleet-scale, so the product stays ~20 orders below overflow. The
// result is then floored at the immutable reserve, which also catches an
// arithmetic overflow that wrapped the sum negative. A malformed observation
// upstream can therefore only ever make this guard stricter, never weaker.
func ProbeBuyFloor(immutable int64, capexReserve int64, cargoSpendPerHour int64, kMilli int) int64 {
	floor := maxInt64(0, immutable)
	floor += maxInt64(0, capexReserve)
	floor += maxInt64(0, int64(kMilli)) * maxInt64(0, cargoSpendPerHour) / 1000

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
