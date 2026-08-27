package trading

import "math"

// api_saturation.go — how hard the shared API request budget is binding, as the tour
// solver's selection objective needs to see it.
//
// A tour spends two scarce resources: the hull's wall clock and the account's shared
// request ceiling. The solver prices only the first, so it ranks plans by credits per hour.
// That is right while the limiter has headroom and wrong once it does not: at the ceiling a
// hull idling out a cooldown costs nothing, while a stop-dense tour spends the budget every
// other hull is queued behind. This estimator tells the solver which regime it is in.
//
// A HINGE, NOT A RATIO. Utilization alone over-states the pressure: a fleet at half the
// ceiling is unconstrained, not half-constrained — one more request costs nothing. The hinge
// is that complementary slackness: flat zero while slack, then the fraction of remaining
// headroom already spent, so weight shifts continuously. Utilization rather than queue delay
// because it is a ratio to a stated ceiling, so it reads as a fraction with no new constant.

// APISaturationHeadroomFloorPct is the utilization below which the budget counts as slack.
//
// The limiter is a token bucket, so a fleet whose average sits well under the ceiling never
// queues: the bucket refills faster than any five-minute window drains it. The floor is
// where that stops holding and the average starts implying sustained periods pinned at the
// ceiling inside the window. Three quarters leaves a fleet running with real headroom
// ranking on credits per hour, while a fleet at the ceiling reads as fully bound.
const APISaturationHeadroomFloorPct = 75.0

// APISaturationParams are the estimator's shape: properties chosen against the measured
// distribution, injected rather than read from a knob, exactly as the crossing coefficients
// beside them are. A zero value estimates nothing, so an unwired caller withholds the
// reading instead of inventing one.
type APISaturationParams struct {
	// HeadroomFloorPct is the utilization below which the budget counts as slack.
	HeadroomFloorPct float64
	// MinRequests is the observed-request count below which a window is too thin to price
	// a fleet-wide resource off and no reading is emitted.
	MinRequests int
}

// DefaultAPISaturationParams is the armed shape. MinRequests 300 against the tracker's
// five-minute rolling window, which holds around six hundred requests at the ceiling: it
// admits a half-populated window and rejects the minutes after a boot, when the average is
// still climbing out of an empty ring and would read as headroom on a saturated fleet.
func DefaultAPISaturationParams() APISaturationParams {
	return APISaturationParams{
		HeadroomFloorPct: APISaturationHeadroomFloorPct,
		MinRequests:      300,
	}
}

// SaturationPermille converts an observed request-budget utilization into the solver's
// saturation reading, in permille of the ceiling.
//
// ok=false is the FAIL-OPEN surface: a thin window, a nonsensical reading, a zero-value
// params, or genuine slack all mean the caller sends nothing and selection ranks on credits
// per hour. Slack returns false rather than 0 because a zero reading and no reading are the
// same instruction, and collapsing them keeps one degrade path instead of two.
//
// The result clamps into [0, 1000] — the same bound the solver applies to its own reading —
// so a window measured past the ceiling reads as fully bound rather than as requests
// outranking everything else on the board.
func SaturationPermille(utilizationPct float64, observedRequests int, p APISaturationParams) (int, bool) {
	if p.MinRequests <= 0 || p.HeadroomFloorPct < 0 || p.HeadroomFloorPct >= 100 {
		return 0, false
	}
	if observedRequests < p.MinRequests {
		return 0, false
	}
	if math.IsNaN(utilizationPct) || utilizationPct <= p.HeadroomFloorPct {
		return 0, false
	}
	consumed := (utilizationPct - p.HeadroomFloorPct) / (100 - p.HeadroomFloorPct)
	permille := int(math.Round(consumed * 1000))
	if permille > 1000 {
		return 1000, true
	}
	if permille <= 0 {
		return 0, false
	}
	return permille, true
}
