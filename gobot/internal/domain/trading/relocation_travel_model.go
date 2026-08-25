package trading

import "time"

// relocation_travel_model.go — the SWAPPABLE travel-time input the opportunity relocator's NPV
// prices a relocation with (sp-zvywu Part 2).
//
// WHY A SEAM AND NOT A FUNCTION. The relocator's NPV charges `current_rate x travel_h` for the
// hours a relocating hull earns nothing, so travel_h is load-bearing on every relocation decision.
// The coefficients behind it were fitted (sp-smbgd, routing-service/utils/tour_solver.py) on a 24h
// window that is ~85% cap-4-era crossings, and `max_tour_systems` was reverted 4 -> 2 on
// 2026-07-30. Per-hop cost is jump-cooldown + leg physics, so the MECHANISM is
// cap-independent — but the measured medians may still move, and sp-zvywu's spec v2 requires a
// refit before the first live relocation if they shift more than 15%.
//
// Hence: NPV depends on the TravelHopModel INTERFACE, never on the coefficients. A refit is
// (a) FitAffineHopModel over the new medians, (b) inject the returned model — or, for a permanent
// re-level, edit the two named consts below. No NPV code changes either way, and a model of an
// entirely different SHAPE (a measured per-depth table, a per-pair matrix) satisfies the same
// interface. MedianDriftExceeds is the standing check that says whether a refit is due.

const (
	// DefaultCrossingBaseSeconds is the FIXED per-crossing cost — a crossing's two endpoint legs
	// (to-gate and from-gate), which are paid exactly ONCE however deep the crossing goes. Mirrors
	// tour_solver.INTER_SYSTEM_TRAVEL_BASE_SECONDS so the relocator prices a crossing the same way
	// the solver that will plan the tour there does.
	//
	// PROVENANCE (sp-smbgd, refit 2026-07-30 over tour_leg_telemetry: consecutive realized legs
	// whose system changes, dt in [120s, 3h], hop depth by BFS over the open-era gate graph).
	// Median crossing seconds by gate-hop depth over the last 24h, n=567:
	//
	//	hops   1      2      3      4      5      6
	//	med  1376   2105   2732   3571   3786   4161
	//
	// Weighted OLS on those medians gives total(h) = 749 + 661*h, stable across 12h/24h/48h/72h
	// windows (base 642-833, per_hop 632-723), so 750 + 650 is the fitted level rounded — within
	// 6% of every measured median at depth 1-5.
	//
	// THE CAP CAVEAT (sp-zvywu spec v2): that window is ~85% cap-4-era crossings. Re-verify against
	// accumulated cap-2 crossings with MedianDriftExceeds before trusting a relocation, and refit
	// with FitAffineHopModel if it reports drift.
	DefaultCrossingBaseSeconds = 750.0

	// DefaultCrossingPerHopSeconds is the MARGINAL cost of each gate hop (~ the jump plus its
	// cooldown) — the only part of a crossing that scales with depth. Mirrors
	// tour_solver.INTER_SYSTEM_TRAVEL_PER_HOP_SECONDS. Same provenance and same cap caveat as
	// DefaultCrossingBaseSeconds.
	DefaultCrossingPerHopSeconds = 650.0

	// CrossingTermMinSeconds and CrossingTermMaxSeconds clamp EITHER fitted term, mirroring
	// tour_solver.INTER_SYSTEM_TRAVEL_TERM_MIN/MAX. The floor is POSITIVE on purpose: a crossing's
	// endpoint legs and its jump are both real costs that physically exist, and a zero base would
	// let a refit silently restore the flat per-hop model the affine fit exists to remove. The
	// bounds keep the 1-hop TOTAL inside [600, 3600] seconds.
	CrossingTermMinSeconds = 300.0
	CrossingTermMaxSeconds = 1800.0
)

// TravelHopModel prices an inter-system crossing of a given gate-hop depth as wall-clock travel
// time. It is the ONE seam the relocator's NPV reads travel_h through, so refitting the model —
// or replacing its shape entirely — never touches NPV code.
type TravelHopModel interface {
	// CrossingHours is the wall-clock travel time for a crossing gateHops gate hops deep. A
	// non-positive depth is a same-system "crossing" and costs nothing.
	CrossingHours(gateHops int) float64
}

// AffineHopModel prices a crossing as base + perHop*hops — the fitted sp-smbgd shape, where the
// endpoint legs are fixed and amortize over depth while only the jump is marginal.
type AffineHopModel struct {
	BaseSeconds   float64
	PerHopSeconds float64
}

// DefaultAffineHopModel returns the ARMED fitted model (RULINGS #5: the coefficients are documented
// constants with one definition, not per-run knobs — an operator retunes by refitting, which is a
// measurement, not a preference).
func DefaultAffineHopModel() AffineHopModel {
	return AffineHopModel{
		BaseSeconds:   DefaultCrossingBaseSeconds,
		PerHopSeconds: DefaultCrossingPerHopSeconds,
	}
}

// ArrivalP90HeadroomFactor lifts the fitted MEDIAN crossing time to a conservative ARRIVAL
// BOUND — the p90 of the measured travel distribution. It is the factor a GUARD multiplies the
// objective's coefficients by, and that distinction is the whole point:
//
//   - The tour solver's $/hr objective and the relocator's NPV are EXPECTED-VALUE calculations.
//     They should price a crossing at its MEDIAN, because over many crossings the halves cancel.
//   - A freshness guard gets no such averaging. It asks "will this quote still be inside its
//     activity's cap when the hull ARRIVES", and a median arrival is wrong half the time — which
//     silently halves the confidence of a cap that was itself fitted at p90 (RankerAgeCaps are
//     fitted so p90 sell-price drift over the window stays ≤ ~5%). Ageing at the p90 arrival
//     holds BOTH terms of that guard at one confidence level instead of pairing a coin-flip
//     arrival with a p90 drift bound.
//
// MEASURED 2026-07-30 over tour_leg_telemetry, same methodology as the median fit above
// (consecutive realized legs whose system changes, dt in [120s, 3h], leg_index >= 0, depth by
// BFS over the open-era gate graph). Depths 1-5 — the entire range a strict flight bound can
// produce, since gategraph.MaxJumpPath is 5 — n = 868:
//
//	hops    1      2      3      4      5
//	n     509    188     95     42     34
//	p50   987   1813   2235   2821   3573
//	p90  1630   2720   3441   3899   4415
//
// Weighted OLS on the p90 row gives 925 + 781*h. ScaledBy(1.21) over the armed median
// coefficients gives 1694 / 2480 / 3267 / 4054 / 4840 — within 0.8% of that direct fit at every
// depth. So the RATIO is the honest primitive here, and ScaledBy is the right way to spend it:
// the bound then tracks any refit of the median coefficients instead of going stale beside them
// (the cap-2 refit the caveat above calls for would otherwise move one and not the other).
//
// WHY 1.21 AND NOT THE ~1.4-1.7 RAW p90/p50 OF THAT TABLE: the armed coefficients were fitted on
// a ~85% cap-4 window and already sit ABOVE current realized medians — a p50 refit over the full
// history reads 381 + 640*h. The armed line is therefore already conservative, and 1.21x of it
// is where the measured p90 lands.
const ArrivalP90HeadroomFactor = 1.21

// ArrivalBoundHopModel is the travel model a FRESHNESS GUARD must predict arrival with: the
// armed fitted shape lifted to its measured p90. Use it wherever being LATE is the failure mode;
// never for pricing, where DefaultAffineHopModel's median is the correct expectation.
//
// It is a MODEL, not a second set of constants: the coefficients still have exactly one
// definition (DefaultCrossingBaseSeconds / DefaultCrossingPerHopSeconds), and this is that model
// scaled by a measured ratio.
func ArrivalBoundHopModel() AffineHopModel {
	return DefaultAffineHopModel().ScaledBy(ArrivalP90HeadroomFactor)
}

// CrossingHours prices the crossing. Each term is clamped to [CrossingTermMinSeconds,
// CrossingTermMaxSeconds] so no refit — however badly conditioned its input — can price a crossing
// at zero (which would make every distant region look free) or at an absurd level. A zero-value
// AffineHopModel therefore still prices at the fitted floor rather than free, mirroring the
// RankerAgeCaps zero-value defense.
func (m AffineHopModel) CrossingHours(gateHops int) float64 {
	if gateHops <= 0 {
		return 0 // same system: no crossing to pay for
	}
	base := clampCrossingTerm(m.BaseSeconds, DefaultCrossingBaseSeconds)
	perHop := clampCrossingTerm(m.PerHopSeconds, DefaultCrossingPerHopSeconds)
	seconds := base + perHop*float64(gateHops)
	return seconds / float64(time.Hour/time.Second)
}

// clampCrossingTerm holds a fitted term inside the physical bounds, falling back to its fitted
// default when the term is unset (zero or negative) so an unwired model is never free.
func clampCrossingTerm(term, fitted float64) float64 {
	if term <= 0 {
		term = fitted
	}
	if term < CrossingTermMinSeconds {
		return CrossingTermMinSeconds
	}
	if term > CrossingTermMaxSeconds {
		return CrossingTermMaxSeconds
	}
	return term
}

// MEASURED CAP-2 DRIFT (2026-07-30, economy analyst, direct DB read — the re-verification spec v2
// asks for). Per-tour wall time from tour_leg_telemetry, split by the tour container's actual
// max_tour_systems launch value:
//
//	cap    tours   avg_systems   median_tour_s   median_s_per_crossing
//	4        298      2.04            1604              1579
//	2         49      2.02            1025              1025
//
// A −35% shift per crossing, far past the 15% bar: cap-4 sat ABOVE sp-smbgd's 850-1360 s/hop band
// at 1579, cap-2 lands inside it at 1025. TRAVEL IS CHEAPER THAN THE ARMED COEFFICIENTS PRICE IT.
//
// WHY THE ARMED CONSTANTS ARE NOT SIMPLY SET TO 1025. The measurement's unit is NOT this model's
// unit. Their "crossing" is (distinct systems − 1), and one such crossing may span several GATE HOPS,
// which is what BaseSeconds and PerHopSeconds are denominated in. 1025 is therefore an UPPER BOUND on
// per-hop cost, not a substitute for the per-hop term, and a per-crossing figure cannot re-split the
// base from the marginal term at all. The reliable result is the RELATIVE −35%; use ScaledBy for it.
//
// TWO METHODOLOGY TRAPS the analyst hit before getting this right, recorded so the next refit does
// not repeat them: tour_id is the CONTAINER id, not a tour id (one container runs many sequential
// tours, so a naive GROUP BY tour_id aggregates ~8 tours and washed the shift out to −9%; split at
// leg_index resets instead), and leg_index < 0 rows are look-back/opportunistic buys rather than
// planned tour legs (1,059 of 3,745 rows) which inflate both system counts and elapsed time.
//
// THE UN-REFITTED MODEL IS THE FAIL-SAFE DIRECTION, which is why the estimator-silent case rests
// on it. Over-pricing travel overstates `current_rate x travel_h` and shrinks the productive
// window, so the NPV is biased AGAINST relocating: the armed coefficients yield FEWER relocations
// than the truth warrants, never more. Re-levelling only lets legitimately-good moves through —
// and it moves no guard, because the uplift bar, the risk margin, the anti-herd and concurrency
// caps, the cooldown and the freshness caps are all independent of travel time (RULINGS #4
// intact). The LEVEL tracks the fleet live via AdoptMeasuredPerHopLevel — the ScaledBy path this
// block prescribes for a relative reading; only a per-depth medians re-measurement through
// FitAffineHopModel can move the SHAPE, under the traps recorded here.
//
// NOT AN INVARIANT: avg_systems is ~2.0 under BOTH caps, so the cap changed WHICH neighbour a tour
// chose rather than how many it visited, and cap-2 tours were observed touching 3 systems. Nothing
// here assumes a bound on a tour's reach; RegionHopRadius bounds candidate DISCOVERY, not tour reach.

// ScaledBy returns the model with both fitted terms multiplied by factor, preserving the fitted
// base:per-hop RATIO. It is the refit path for a RELATIVE measurement — a per-crossing shift like the
// −35% above, which establishes that a crossing costs less without being able to say how that saving
// splits between the endpoint legs and the jumps. Holding the ratio is the honest reading of such
// evidence: it changes the LEVEL the measurement speaks to and leaves the SHAPE it does not.
//
// Prefer FitAffineHopModel whenever medians BY GATE-HOP DEPTH are available, since that re-fits the
// shape too. Use this when only a relative shift is trustworthy.
//
// A non-positive factor is refused (the model is returned unchanged): scaling to zero would price
// every crossing at the clamp floor and make distant regions look nearly free.
func (m AffineHopModel) ScaledBy(factor float64) AffineHopModel {
	if factor <= 0 {
		return m
	}
	base := clampCrossingTerm(m.BaseSeconds, DefaultCrossingBaseSeconds)
	perHop := clampCrossingTerm(m.PerHopSeconds, DefaultCrossingPerHopSeconds)
	return AffineHopModel{BaseSeconds: base * factor, PerHopSeconds: perHop * factor}
}

// FitAffineHopModel is THE REFIT PATH. Given measured median crossing seconds keyed by gate-hop
// depth — exactly the table sp-smbgd published and exactly what a cap-2 re-measurement produces —
// it returns the least-squares affine model through them.
//
// Ordinary (unweighted) least squares over the depth/median pairs: with n depths, Sx = sum(h),
// Sy = sum(med), Sxx = sum(h*h), Sxy = sum(h*med), the slope is
// (n*Sxy - Sx*Sy) / (n*Sxx - Sx*Sx) and the intercept (Sy - slope*Sx) / n.
//
// UNWEIGHTED, AND THE DIFFERENCE IS MATERIAL — SO A FIT IS A CANDIDATE, NOT AN ADOPTION. The
// published sp-smbgd fit was WEIGHTED by sample count per depth and reads 749 + 661*h; unweighted
// over the SAME medians this returns 974 + 566*h, because the shallow depths that dominate the
// sample no longer dominate the line. Unweighted is deliberate — a re-measurement reliably carries
// median seconds by depth, and requiring per-depth n would make the refit path unusable whenever it
// does not — but it means the returned coefficients must be VALIDATED, never adopted blind. The
// intended sequence is: fit the new medians, then MedianDriftExceeds the candidate against those
// same medians (and against the incumbent) and adopt only if the candidate tracks them better. What
// the two fits agree on is the SHAPE and the LEVEL to within the clamp; they disagree on how the
// intercept and slope split a fixed total, which MedianDriftExceeds measures directly.
//
// The ARMED constants above remain the published WEIGHTED fit. This function exists so a cap-2
// refit is a measurement plus one call, not a code change.
//
// ok=false — fail closed — when the input cannot determine a line: fewer than two distinct depths,
// a degenerate denominator, or any non-positive median (a median of zero seconds is not a
// measurement, it is a hole in the data). A caller that cannot fit KEEPS the model it has; it never
// relocates off coefficients it had to invent.
func FitAffineHopModel(medianSecondsByHops map[int]float64) (AffineHopModel, bool) {
	var n, sumX, sumY, sumXX, sumXY float64
	for hops, median := range medianSecondsByHops {
		if hops <= 0 || median <= 0 {
			return AffineHopModel{}, false
		}
		x := float64(hops)
		n++
		sumX += x
		sumY += median
		sumXX += x * x
		sumXY += x * median
	}
	// n*Sxx - Sx^2 is zero exactly when the input cannot determine a line — no depths, one depth, or
	// every entry at the same depth. ONE check covers all three: an explicit `n < 2` guard alongside
	// it killed no mutation probe, because this denominator already catches every case it did.
	denominator := n*sumXX - sumX*sumX
	if denominator == 0 {
		return AffineHopModel{}, false
	}
	perHop := (n*sumXY - sumX*sumY) / denominator
	base := (sumY - perHop*sumX) / n
	return AffineHopModel{BaseSeconds: base, PerHopSeconds: perHop}, true
}

// CrossingLevelDriftTolerance is sp-zvywu spec v2's re-verification bar as a fraction: a
// measured shift inside 15% is noise the fitted model already tolerates; past it a refit is due.
const CrossingLevelDriftTolerance = 0.15

// crossingLevelCheckMaxHops is the depth range the level-drift check is evaluated over: 1
// through the deepest crossing a strict flight bound can produce (gategraph.MaxJumpPath).
const crossingLevelCheckMaxHops = 5

// AdoptMeasuredPerHopLevel re-levels the model to a MEASURED marginal per-hop toll (the
// EstimatePerHopTollSeconds reading) and reports whether it was adopted — the standing live
// form of this file's refit sequence for a PARTIAL measurement: the reading isolates only the
// MARGINAL term, so it moves the LEVEL via ScaledBy and leaves the SHAPE alone, gated by
// MedianDriftExceeds at CrossingLevelDriftTolerance over the crossing medians the measured
// level implies. Inside the bar the incumbent stands — re-levelling there would churn every
// consumer on estimator wobble the bar exists to absorb. The scaled candidate needs no second
// check: by construction it IS the implied level. A non-positive toll is the estimator's
// fail-open silence, not a measurement — the incumbent returns unchanged — and the shift is
// derived from the model's EFFECTIVE terms (the clamp's unset fallbacks), so an unset model
// re-levels from the fitted defaults rather than dividing by an unset zero term.
func (m AffineHopModel) AdoptMeasuredPerHopLevel(measuredPerHopSeconds float64) (AffineHopModel, bool) {
	if measuredPerHopSeconds <= 0 {
		return m, false
	}
	base := clampCrossingTerm(m.BaseSeconds, DefaultCrossingBaseSeconds)
	perHop := clampCrossingTerm(m.PerHopSeconds, DefaultCrossingPerHopSeconds)
	factor := measuredPerHopSeconds / perHop
	impliedMedians := make(map[int]float64, crossingLevelCheckMaxHops)
	for hops := 1; hops <= crossingLevelCheckMaxHops; hops++ {
		impliedMedians[hops] = (base + perHop*float64(hops)) * factor
	}
	if !m.MedianDriftExceeds(impliedMedians, CrossingLevelDriftTolerance) {
		return m, false
	}
	return m.ScaledBy(factor), true
}

// MedianDriftExceeds reports whether the model has drifted off the measured medians by more than
// tolerance (a fraction: CrossingLevelDriftTolerance is sp-zvywu spec v2's 15% re-verification
// bar) at ANY measured depth.
// It is the standing "is a refit due?" check the cap caveat calls for: run it over accumulated
// cap-2 crossings, and if it says yes, FitAffineHopModel the new medians and inject the result.
//
// ANY depth, not the average, because the failure mode that matters is a SHAPE change — the cap
// revert plausibly moves the marginal term without moving the base, which shows up at depth 4-6
// while depth 1 still fits. An averaged check would hide exactly that.
//
// An empty table reports NO drift: no measurement is not evidence of drift (and the caller keeps
// the model it has, the conservative direction).
func (m AffineHopModel) MedianDriftExceeds(medianSecondsByHops map[int]float64, tolerance float64) bool {
	for hops, median := range medianSecondsByHops {
		if hops <= 0 || median <= 0 {
			continue // not a measurement
		}
		predicted := m.CrossingHours(hops) * float64(time.Hour/time.Second)
		drift := (predicted - median) / median
		if drift < 0 {
			drift = -drift
		}
		if drift > tolerance {
			return true
		}
	}
	return false
}
