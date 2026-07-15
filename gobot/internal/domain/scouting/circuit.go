package scouting

import (
	"math"
	"sort"
	"time"
)

// circuit.go holds the deterministic scout-post circuit math (sp-k7q5): the
// Admiral's freshness model, freshness-per-market ≈ (markets / hulls) × avgHop,
// where avgHop is the average per-market cost of a tour hop (navigation + scan
// dwell). It is pure math with no I/O so BOTH the undersized-post warning in the
// scout-post coordinator (layer 1, daemon process) and the auto-post proposal
// detector in the watchkeeper (layer 3, a separate process) share one definition
// of "how many probes does this many markets need" rather than each rederiving it.
//
// avgHop and the freshness target are CONFIG at the call sites (RULINGS #5), never
// constants here — this file only turns those inputs into a hull count.

// RequiredHulls is the minimum probe count needed to keep `markets` markets scanned
// within `freshness`, at an average per-market hop cost of avgHop. From the circuit
// model (markets / hulls) × avgHop ≤ freshness, so hulls ≥ markets × avgHop /
// freshness; the result is the ceiling of that ratio, never below 1 when there is at
// least one market. Degenerate inputs (no markets, or a non-positive freshness/avgHop)
// return 0 — a signal the caller reads as "cannot assess", NOT "undersized".
func RequiredHulls(markets int, avgHop, freshness time.Duration) int {
	if markets <= 0 || avgHop <= 0 || freshness <= 0 {
		return 0
	}
	need := float64(markets) * float64(avgHop) / float64(freshness)
	r := int(math.Ceil(need))
	if r < 1 {
		r = 1
	}
	return r
}

// MedianScanIntervalSeconds MEASURES the empirical per-market cycle — the seconds a
// probe spends moving to and scanning the next market — as the MEDIAN of the intervals
// between consecutive market-scan events (sp-orgp). It is the freshness sizer's
// avgHop input, derived from live scan telemetry (market last-scan timestamps) rather
// than a hardcoded constant, because travel time varies by system layout. A median
// (not a mean) is used so a single stalled leg — a probe that parked, refuelled, or
// crossed a gate mid-circuit — does not inflate the estimate and over-size the post.
//
// It returns the median interval in seconds and the SAMPLE COUNT backing it (the number
// of consecutive-interval gaps, i.e. len-1). Fewer than two events yields (0, 0): there
// is no interval to measure, a signal the caller reads as "seed the default until enough
// telemetry exists", never as a zero cycle. Input need not be pre-sorted — the timestamps
// are sorted here so a telemetry query with no ORDER BY still measures correctly.
func MedianScanIntervalSeconds(scanTimes []time.Time) (float64, int) {
	if len(scanTimes) < 2 {
		return 0, 0
	}
	times := make([]time.Time, len(scanTimes))
	copy(times, scanTimes)
	sort.Slice(times, func(i, j int) bool { return times[i].Before(times[j]) })

	intervals := make([]float64, 0, len(times)-1)
	for i := 1; i < len(times); i++ {
		intervals = append(intervals, times[i].Sub(times[i-1]).Seconds())
	}
	return median(intervals), len(intervals)
}

// FreshnessRequiredHulls is the CLOSED-LOOP sizing: the static circuit model
// (RequiredHulls) corrected by the measured worst-case market age (sp-orgp). The static
// model estimates how many probes SHOULD hold `markets` fresh within `sla` at the
// measured `cycle`; but the empirical age is the ground truth. When the oldest market is
// AT OR UNDER the SLA the model stands. When it is BREACHING (actualAge > sla) the model
// under-estimated — travel was slower than measured, or a lane stalled — so demand is
// raised in proportion to the breach: doubling the age needs roughly double the probes.
// This makes the sizer self-correcting rather than open-loop.
//
// A degenerate cycle/sla/markets (static == 0, "cannot assess") is never raised — the
// coordinator seeds a cycle so this guards only the no-telemetry edge.
func FreshnessRequiredHulls(markets int, cycle, sla, actualAge time.Duration) int {
	static := RequiredHulls(markets, cycle, sla)
	if static == 0 {
		return 0
	}
	if actualAge <= sla || sla <= 0 {
		return static
	}
	raised := int(math.Ceil(float64(static) * float64(actualAge) / float64(sla)))
	if raised < static {
		return static
	}
	return raised
}

// median returns the middle value of xs (the mean of the two middle values for an even
// count). xs must be non-empty; the sole caller guarantees it. It sorts a copy so the
// caller's slice order is preserved.
func median(xs []float64) float64 {
	sorted := make([]float64, len(xs))
	copy(sorted, xs)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

// CircuitDuration is the modeled time for ONE hull to complete its share of a post's
// tour: (markets / hulls) × avgHop. It is the observability figure the undersized
// warning reports (the modeled age the post's freshest lane will reach), not a
// decision input. hulls < 1 is treated as 1 — a post always has at least its primary
// slot. Zero markets or a non-positive avgHop yields 0.
func CircuitDuration(markets, hulls int, avgHop time.Duration) time.Duration {
	if markets <= 0 || avgHop <= 0 {
		return 0
	}
	if hulls < 1 {
		hulls = 1
	}
	return time.Duration(float64(markets) / float64(hulls) * float64(avgHop))
}

// IsUndersized reports whether a post touring `markets` markets with `hulls` probes
// cannot keep them within `freshness` under the avgHop circuit model — its modeled
// circuit exceeds the freshness target, so every lane ages past the contract and (past
// the tour planner's cap) goes silently invisible. Equivalent to hulls <
// RequiredHulls(...). A post with no markets or an unusable freshness/avgHop is never
// undersized (there is nothing to assess).
func IsUndersized(markets, hulls int, avgHop, freshness time.Duration) bool {
	req := RequiredHulls(markets, avgHop, freshness)
	if req == 0 {
		return false
	}
	if hulls < 1 {
		hulls = 1
	}
	return hulls < req
}
