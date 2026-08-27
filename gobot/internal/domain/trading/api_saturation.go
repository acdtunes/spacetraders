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
// A HINGE, NOT A RATIO. Contention alone over-states the pressure: a fleet nobody is queued
// behind is unconstrained, not partly constrained — one more request costs nothing. The
// hinge is that complementary slackness: flat zero while no request queues, then the
// fraction of the limiter's reservoir already spoken for, so weight shifts continuously.
//
// THE EVIDENCE IS QUEUEING TIME, NOT THROUGHPUT. Requests SERVED past a limiter is a
// censored measurement of demand — the limiter caps its own output, so a window reads the
// same whether the fleet asked for one request past the ceiling or a hundred — and it is an
// average, which hides the bursts that do all the queueing: a fleet can serve half the
// ceiling across five minutes while every request it made waited ten seconds, because
// demand arrives in tight bursts that reserve tokens deep into the future and then falls
// silent. The wait per request is uncensored: it is non-zero only when a request queued
// behind other requests, and it grows with how many.

// APISaturationPermilleMax is a fully bound budget: the top of the permille scale readers
// clamp into, and the denominator anything scaled BY saturation divides through.
const APISaturationPermilleMax = 1000

// apiSaturationMinRequests is the observed-request count below which a window is too thin
// to price a fleet-wide resource off. 300 against the tracker's five-minute rolling window,
// which holds around six hundred requests at the ceiling: it admits a half-populated window
// and rejects the minutes after a boot, when the window is still filling out of an empty
// ring.
const apiSaturationMinRequests = 300

// APISaturationParams are the estimator's shape: properties derived from the limiter the
// fleet actually queues on, injected rather than read from a knob. A zero value estimates
// nothing, so an unwired caller withholds the reading instead of inventing one.
type APISaturationParams struct {
	// MinRequests is the request count below which a window is too thin to read.
	MinRequests int
	// QueueFloorSeconds is the mean wait at or below which the limiter imposed no queue.
	QueueFloorSeconds float64
	// QueueCeilingSeconds is the mean wait at or above which the budget is fully committed.
	QueueCeilingSeconds float64
}

// APISaturationParamsForLimiter derives the hinge from the two numbers that define the
// token bucket the fleet queues on, so the estimator cannot drift from the limiter it
// describes. A limiter that cannot be divided by yields the zero value, which reads nothing.
//
// The FLOOR is one token service period, 1/ceiling: a request that waited less than the
// time the bucket takes to mint a single token queued behind no whole request, so it
// displaced nothing. The CEILING is a full burst drain, burst/ceiling — how long the bucket
// takes to hand out every token it can hold, so nothing can bind harder than that.
func APISaturationParamsForLimiter(ceilingReqPerSec float64, burst int) APISaturationParams {
	if math.IsNaN(ceilingReqPerSec) || math.IsInf(ceilingReqPerSec, 0) {
		return APISaturationParams{}
	}
	if ceilingReqPerSec <= 0 || burst <= 0 {
		return APISaturationParams{}
	}
	return APISaturationParams{
		MinRequests:         apiSaturationMinRequests,
		QueueFloorSeconds:   1 / ceilingReqPerSec,
		QueueCeilingSeconds: float64(burst) / ceilingReqPerSec,
	}
}

// SaturationPermille converts a window's mean rate-limiter wait per request into the
// solver's saturation reading, in permille of a fully committed budget.
//
// ok=false is the FAIL-OPEN surface: a thin window, a nonsensical wait, a zero-value params,
// or a limiter nobody queued on all mean the caller sends nothing and selection ranks on
// credits per hour. No queue returns false rather than 0 because a zero reading and no
// reading are the same instruction, and collapsing them keeps one degrade path instead of
// two. The result clamps into [0, 1000], the same bound the solver applies to its own.
func SaturationPermille(meanWaitSeconds float64, observedRequests int, p APISaturationParams) (int, bool) {
	if p.MinRequests <= 0 || p.QueueFloorSeconds < 0 || p.QueueCeilingSeconds <= p.QueueFloorSeconds {
		return 0, false
	}
	if observedRequests < p.MinRequests {
		return 0, false
	}
	if math.IsNaN(meanWaitSeconds) || math.IsInf(meanWaitSeconds, 0) {
		return 0, false
	}
	if meanWaitSeconds <= p.QueueFloorSeconds {
		return 0, false
	}
	committed := (meanWaitSeconds - p.QueueFloorSeconds) / (p.QueueCeilingSeconds - p.QueueFloorSeconds)
	permille := int(math.Round(committed * APISaturationPermilleMax))
	if permille > APISaturationPermilleMax {
		return APISaturationPermilleMax, true
	}
	if permille <= 0 {
		return 0, false
	}
	return permille, true
}
