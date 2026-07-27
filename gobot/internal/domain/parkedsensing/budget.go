// Package parkedsensing holds the pure arithmetic behind parked-probe market
// sensing: how much of the shared API rate-limiter ceiling sensing may spend,
// how attention is weighted across waypoints by observed price spread, when
// each slot next falls due for a scan, and the credit floor a probe purchase
// must clear.
//
// Everything here is a value-in/value-out function. There is no clock, no
// repository, no mediator and no API client: callers pass the observations in
// and get numbers back. That keeps the policy — which is where the judgement
// calls live — testable without a running fleet, and keeps the adapters free
// to change how the observations are gathered.
//
// The organising idea is that sensing is a *residual* consumer. Trading,
// contracts, navigation and bootstrap all have bounded, fleet-size-driven
// appetites and get right-of-way; sensing has unbounded appetite and takes
// whatever headroom is left under the utilization target. See
// internal/domain/apibudget for the source taxonomy that residual is measured
// against.
package parkedsensing

import "time"

// BudgetInputs is one observation of the fleet's API spend, from which the
// sensing and pacer rates are derived.
type BudgetInputs struct {
	// CeilingReqPerSec is the hard sustained rate-limiter ceiling
	// (api.RateLimitPerSecond). Passed in rather than imported so this package
	// stays free of adapter dependencies.
	CeilingReqPerSec float64
	// TargetUtilPct is the share of the ceiling the fleet aims to occupy,
	// leaving the remainder as burst headroom. Knob; default 92.
	TargetUtilPct int
	// MinScanRateMilli is the floor sensing is clamped up to, in thousandths
	// of a request per second. Knob; default 100 (= 0.1 req/s).
	MinScanRateMilli int
	// NonSensingRate is the measured req/s currently spent by every other
	// source (trading, contracts, navigation, bootstrap, untagged).
	NonSensingRate float64
	// ChartingRate is the measured req/s recently spent on charting. Charting
	// is bounded and higher-priority, so it comes out of the pacer's share.
	ChartingRate float64
	// BrakeFactor is the emergency throttle in (0,1] produced by ApplyBrake.
	// The zero value means "not yet initialised" and is read as fully
	// released, so a zero BudgetInputs does not silently stall sensing.
	BrakeFactor float64
}

// brakeFloor is the lowest the emergency throttle may go. Sensing is degraded
// hard under API pressure but never switched off entirely: a zero brake would
// be unrecoverable, since the recovery multiplier is multiplicative.
const brakeFloor = 0.1

// brakeReleased is the fully-open throttle.
const brakeReleased = 1.0

// brakeClampMultiplier is applied when the observed limiter wait is above the
// high-water mark, and brakeRecoveryMultiplier when it is below the low-water
// mark. Braking is deliberately faster than recovery (halve down, creep up):
// overshooting the ceiling costs 429s and retry storms, while under-using it
// only costs a little market freshness.
const (
	brakeClampMultiplier    = 0.5
	brakeRecoveryMultiplier = 1.2
)

// minScanRate converts the milli-unit knob to req/s.
func (in BudgetInputs) minScanRate() float64 {
	return float64(in.MinScanRateMilli) / 1000.0
}

// normalizeBrake reads an out-of-contract brake factor as fully released.
// Both bounds matter: a zero (uninitialised caller) must not stall sensing,
// and a value above 1 must not let the brake inflate sensing past the
// rate-limiter ceiling.
func normalizeBrake(brake float64) float64 {
	if brake <= 0 || brake > brakeReleased {
		return brakeReleased
	}
	return brake
}

// SensingRate is the residual req/s sensing may spend: the utilization target
// less what every other source is already consuming, clamped into
// [minScanRate, ceiling], then scaled by the emergency brake.
//
// The brake multiplies AFTER the clamp, so a braked fleet can legitimately
// end up below the minimum scan rate. That sub-floor value is meaningful and
// is meant to be read: expansion gating compares SensingRate against its own
// minimum-budget threshold and pauses expansion first, which is where the
// brake's pressure is supposed to land in the yield order.
//
// It is NOT the rate the scan pacer runs at. PacerRate re-imposes the floor,
// so however hard the brake bites, the pacer never drops below min_scan_rate
// and planner data never goes fully dark. The brake's effective domain at the
// pacer is therefore the band between the ceiling and the floor; below the
// floor it speaks only to the other consumers.
func SensingRate(in BudgetInputs) float64 {
	target := float64(in.TargetUtilPct) / 100.0 * in.CeilingReqPerSec
	residual := target - in.NonSensingRate
	clamped := clampFloat(residual, in.minScanRate(), in.CeilingReqPerSec)
	return clamped * normalizeBrake(in.BrakeFactor)
}

// PacerRate is the req/s the parked-probe pacer actually runs at: the sensing
// residual less charting's cut, floored at the minimum scan rate.
//
// This function is where min_scan_rate is enforced, and the floor is
// deliberately the last word — it is what guarantees planner data never goes
// fully dark. It therefore outranks both things that can push the number
// down: a charting burst (bounded, and worth more per request since an
// uncharted waypoint yields no prices at all, so it is subtracted rather than
// competed with), and a sub-floor SensingRate produced by the emergency
// brake. See SensingRate for what that sub-floor value is still good for.
func PacerRate(in BudgetInputs) float64 {
	return maxFloat(SensingRate(in)-in.ChartingRate, in.minScanRate())
}

// ApplyBrake advances the emergency throttle by one tick from the observed
// rate-limiter wait.
//
// Above waitHigh the brake halves; below waitLow it recovers by 1.2x, capped
// at fully released; between the marks it is a dead band and the brake is
// unchanged, which keeps a wait hovering near a threshold from oscillating the
// scan rate. Both comparisons are strict, so sitting exactly on a mark is
// inside the dead band. The result is clamped into [0.1, 1.0]; a prev of zero
// or below is read as fully released, so an uninitialised caller starts
// unbraked rather than stalled.
func ApplyBrake(prev float64, waitEWMA, waitLow, waitHigh time.Duration) float64 {
	brake := normalizeBrake(prev)

	switch {
	case waitEWMA > waitHigh:
		brake *= brakeClampMultiplier
	case waitEWMA < waitLow:
		brake *= brakeRecoveryMultiplier
	}

	return clampFloat(brake, brakeFloor, brakeReleased)
}

// clampFloat confines v to [lo, hi]. A caller that passes lo > hi gets hi,
// keeping the hard ceiling authoritative over any floor knob.
func clampFloat(v, lo, hi float64) float64 {
	if v > hi {
		return hi
	}
	if v < lo {
		return minFloat(lo, hi)
	}
	return v
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
