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

// ClampToMarketCount bounds a proposed probe target to what `markets` markets can justify at
// the worst-plausible per-market cycle — the sp-iupr issue-3 NOISE CEILING. Per-market cycle
// telemetry is noisy, and a noisy-HIGH reading can size a small-market system far above what
// its market count could ever need (the ZY16 pathology: 3 markets read as needing 6 probes).
// The ceiling is RequiredHulls(markets, worstCycle, sla): the static model at a conservative
// upper-bound cycle. Because RequiredHulls is monotone non-decreasing in markets, so is the
// ceiling — clamping every system to it enforces "a smaller-market system is never sized above
// a larger-market one on cycle noise alone". The clamp only CAPS (never raises), and a
// degenerate ceiling (0, "cannot assess": no markets or a non-positive cycle/sla) never
// clamps. The empirical-age sanity floor at the call site is deliberately applied AFTER this
// and MAY exceed the ceiling — measured staleness is ground truth, this bounds only the model.
func ClampToMarketCount(target, markets int, worstCycle, sla time.Duration) int {
	ceiling := RequiredHulls(markets, worstCycle, sla)
	if ceiling > 0 && target > ceiling {
		return ceiling
	}
	return target
}

// DampenedCycleSeconds shrinks a system's OWN measured per-market cycle toward the fleet-wide
// robust median by dampeningPercent — the sp-iupr issue-3 NOISE DAMPENER. Per-system cycle
// measurements are noisy estimates of a partly-shared underlying travel pace (probes across
// systems pay similar per-hop navigation + scan costs), so systems with equal market counts
// and noisy-but-similar true cycles otherwise diverge into different probe targets purely on
// noise. This is a classic shrinkage estimator: the pooled fleet median is a lower-variance
// prior, and pulling each noisy per-system estimate toward it trades a little bias for a large
// variance reduction, so equal-market systems converge. dampeningPercent is clamped to
// [0,100]; 0 (or no fleet anchor — a single trusted system whose median IS its own cycle)
// returns the own cycle unchanged, the pre-dampening behavior.
func DampenedCycleSeconds(own, fleetMedian float64, dampeningPercent int) float64 {
	if fleetMedian <= 0 || dampeningPercent <= 0 {
		return own
	}
	weight := float64(dampeningPercent) / 100
	if weight > 1 {
		weight = 1
	}
	return own*(1-weight) + fleetMedian*weight
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

// CircuitRequiredHulls sizes a post from its DIRECTLY OBSERVED circuit period (sp-tor9): `hulls`
// probes produced a worst-case market age of `actualAge` — the age of the oldest market, i.e.
// how long since a probe last completed the circuit back to it. The circuit period scales
// inversely with probe count (period ≈ markets × perMarketHop / hulls), so the product
// hulls × actualAge ≈ markets × perMarketHop is CONSERVED as hulls change — a system-invariant
// measure of total circuit work. To bring the worst-case age under `sla`, that work must be
// spread over ceil(hulls × actualAge / sla) probes.
//
// Because the product is conserved, this target is a STABLE FIXPOINT: raising to it drops the
// age proportionally, so the next tick re-derives the same target (no release-flap). It rises
// proportionally with the breach on the way up, sits at the hull count when the age is exactly
// the SLA, and falls BELOW the hull count when comfortably fresh (the caller reads that as
// "release the surplus"). Degenerate inputs (no probes, non-positive age/sla) return 0
// ("cannot assess"), never a spurious probe.
//
// This is the empirical companion to RequiredHulls. RequiredHulls(markets, measuredCycle, sla)
// STRUCTURALLY under-sizes high-market systems: measuredCycle is the pooled inter-scan interval,
// which shrinks as probes are added (more probes ⇒ more frequent scans ⇒ a smaller median gap),
// so the static model collapses toward 1 exactly where a big system needs many probes. Sizing
// from the observed age at the current hull count sidesteps that deflation entirely — the age is
// the circuit period, measured directly.
func CircuitRequiredHulls(hulls int, actualAge, sla time.Duration) int {
	if hulls < 1 || actualAge <= 0 || sla <= 0 {
		return 0
	}
	need := float64(hulls) * float64(actualAge) / float64(sla)
	return int(math.Ceil(need))
}

// WeightedPercentileAgeSeconds returns the age at `percentile` across a system's markets
// (sp-r57g) — the freshness sizer's closed-loop ground truth, superseding the tail-dominated
// max (OldestAgeSeconds). It is the age at which the cumulative market weight first reaches
// `percentile`% of the total (the weighted nearest-rank percentile): walking markets
// freshest-first, the first market whose running weight crosses the threshold sets the age.
//
// When valueWeighted is set each market's weight is its Σ(trade_volume × price) throughput, so
// a HIGH-VALUE stale market carries enough cumulative weight to pull the percentile onto itself
// (it breaches, earning more probes — the arb core stays tight), while an equal-count LOW-value
// straggler contributes little and stays in the tolerated tail (the periphery lags cheaply).
// With valueWeighted off every market weighs 1 — a plain count percentile that simply drops the
// stalest (100−percentile)% of markets (DA78: P90≈62 vs an unachievable max≈167).
//
// Boundary behaviour, all deliberate: percentile 100 returns the exact MAX (the mutation guard —
// reverting the metric to the max re-inflates demand); a single market returns its own age; no
// markets returns 0 ("cannot assess", the caller then falls back to the aggregate max); and a
// value-weighted system whose every weight is non-positive (no throughput signal) DEGRADES to the
// uniform count percentile rather than dividing by zero. percentile is clamped to [0,100].
func WeightedPercentileAgeSeconds(markets []MarketFreshnessSample, valueWeighted bool, percentile int) float64 {
	count := len(markets)
	if count == 0 {
		return 0
	}
	if percentile < 0 {
		percentile = 0
	}
	if percentile > 100 {
		percentile = 100
	}

	sorted := append([]MarketFreshnessSample(nil), markets...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].AgeSeconds < sorted[j].AgeSeconds })

	// Value-weight only when asked AND some market carries a positive weight; otherwise every
	// market weighs 1 (uniform), which also handles the missing-weight-source degenerate.
	useValue := valueWeighted && hasPositiveWeight(sorted)

	total := 0.0
	for i := range sorted {
		total += sampleWeight(sorted[i], useValue)
	}

	threshold := float64(percentile) / 100 * total
	cumulative := 0.0
	for i := range sorted {
		cumulative += sampleWeight(sorted[i], useValue)
		if cumulative >= threshold {
			return sorted[i].AgeSeconds
		}
	}
	return sorted[count-1].AgeSeconds
}

// sampleWeight is a market's contribution to the percentile: its throughput value when value-
// weighting is active (a negative weight is floored to 0 — a zero-value market is pure tail),
// else the uniform 1 that makes a plain count percentile.
func sampleWeight(sample MarketFreshnessSample, useValue bool) float64 {
	if !useValue {
		return 1
	}
	if sample.Weight < 0 {
		return 0
	}
	return sample.Weight
}

// hasPositiveWeight reports whether any market carries a positive value weight — the guard that
// keeps a no-throughput-signal system on the uniform count percentile instead of dividing by zero.
func hasPositiveWeight(markets []MarketFreshnessSample) bool {
	for i := range markets {
		if markets[i].Weight > 0 {
			return true
		}
	}
	return false
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
