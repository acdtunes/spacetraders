package mvt

import "time"

// Departure reasons; each is written verbatim into the transition telemetry line.
const (
	ReasonColdStart     = "cold_start"
	ReasonNoAlternative = "no_alternative"
	ReasonStay          = "stay"
	ReasonYieldBelow    = "yield_below_alternative"
)

const DefaultRateSpanFloor = 0 * time.Minute

// YieldTracker is the hull's own view of the ground it stands on: an exponentially
// weighted moving average of realised margin per unit over its sells in the current
// system, plus the credits-per-second rate the travel cost is priced against.
// It is never persisted; a restart resets it and the cold-start guard applies.
type YieldTracker struct {
	alpha     float64
	minSells  int
	spanFloor time.Duration
	sells     int
	ewma      float64
	credits   float64
	firstAt   time.Time
	lastAt    time.Time
}

// NewYieldTracker builds a tracker with alpha = 2/(windowSells+1). windowSells < 1 is
// treated as 1 (alpha = 1: the latest sell is the estimate).
func NewYieldTracker(windowSells, minSells int) *YieldTracker {
	if windowSells < 1 {
		windowSells = 1
	}
	if minSells < 1 {
		minSells = 1
	}
	return &YieldTracker{alpha: 2 / float64(windowSells+1), minSells: minSells, spanFloor: DefaultRateSpanFloor}
}

func (t *YieldTracker) SetRateSpanFloor(d time.Duration) { t.spanFloor = d }

// Observe records one sell leg's realised margin per unit.
func (t *YieldTracker) Observe(marginPerUnit float64, units int, at time.Time) {
	if t.sells == 0 {
		t.ewma = marginPerUnit
		t.firstAt = at
	} else {
		t.ewma = t.alpha*marginPerUnit + (1-t.alpha)*t.ewma
	}
	t.sells++
	t.credits += marginPerUnit * float64(units)
	t.lastAt = at
}

// Estimate is the EWMA; ok is false below minSells (the cold-start guard).
func (t *YieldTracker) Estimate() (float64, bool) {
	if t.sells < t.minSells {
		return 0, false
	}
	return t.ewma, true
}

// Sells is the number of observations since the last Reset.
func (t *YieldTracker) Sells() int { return t.sells }

// CreditsPerSec is realised margin over the span between the first observation and now.
// It needs at least two observations and a positive span; otherwise 0 (caller falls back
// to the fleet rate). Below spanFloor it is 0 too: a short span inflates the rate, and Rank
// multiplies that by the jump toll into a travel cost that prices out every neighbour.
func (t *YieldTracker) CreditsPerSec(now time.Time) float64 {
	if t.sells < 2 {
		return 0
	}
	span := now.Sub(t.firstAt)
	if span <= 0 || span < t.spanFloor {
		return 0
	}
	return t.credits / span.Seconds()
}

// Reset clears the tracker (arrival in a new system).
func (t *YieldTracker) Reset() {
	*t = YieldTracker{alpha: t.alpha, minSells: t.minSells, spanFloor: t.spanFloor}
}

// Decision is the departure verdict with the numbers that produced it.
type Decision struct {
	Leave           bool
	Reason          string
	YieldHere       float64
	BestAlternative float64
}

// Decide applies the rule: leave iff yield_here < best alternative score (already net of
// travel). A tracker below minSells cannot leave on yield.
func Decide(t *YieldTracker, bestAltScore float64, hasAlt bool) Decision {
	here, ok := t.Estimate()
	d := Decision{YieldHere: here, BestAlternative: bestAltScore}
	switch {
	case !ok:
		d.Reason = ReasonColdStart
	case !hasAlt:
		d.Reason = ReasonNoAlternative
	case here < bestAltScore:
		d.Leave, d.Reason = true, ReasonYieldBelow
	default:
		d.Reason = ReasonStay
	}
	return d
}
