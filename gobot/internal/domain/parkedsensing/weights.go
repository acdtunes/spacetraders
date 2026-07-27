package parkedsensing

// baselineScanWeight is the share every slot earns regardless of how quiet it
// is. Weighting reallocates attention; it never evicts a waypoint from the
// rotation, because a market with no recent observation is exactly the one
// whose spread estimate is least trustworthy.
const baselineScanWeight = 1.0

// priorSpreadMultiple is the weight assigned to a slot with no spread history
// yet: a p75-ish stand-in, deliberately above the baseline so an unmeasured
// waypoint is sampled sooner than a measured-and-boring one and its estimate
// converges quickly. It is a multiple of the fleet median by construction,
// so it is expressed in the same units as a computed weight.
const priorSpreadMultiple = 2.0

// spreadEWMAAlpha is the smoothing factor on observed price spread. At 0.3 a
// single outlier observation moves the estimate by under a third, while a
// genuine level change is tracked within a handful of scans.
const spreadEWMAAlpha = 0.3

// ScanWeight is how much scan attention a waypoint earns, expressed as a
// multiple of the baseline share: its smoothed price spread relative to the
// fleet median, clamped into [1, clampR].
//
// The floor keeps every waypoint in the rotation; the ceiling stops a single
// outlier from monopolising the scan budget. With no usable history — no
// spread observed yet, or no fleet median yet on a cold start — the slot gets
// OptimisticPriorWeight instead, so unmeasured waypoints are sampled ahead of
// measured-but-quiet ones rather than being ranked on a zero they never
// earned.
func ScanWeight(spreadEWMA, fleetMedianSpread float64, clampR int) float64 {
	if spreadEWMA <= 0 || fleetMedianSpread <= 0 {
		return OptimisticPriorWeight(clampR)
	}
	return clampFloat(spreadEWMA/fleetMedianSpread, baselineScanWeight, float64(clampR))
}

// OptimisticPriorWeight is the weight a slot carries before it has any spread
// history: the p75-ish stand-in, never above the caller's clamp. A clampR of 1
// therefore flattens the prior along with everything else, which is how
// weighting is switched off.
func OptimisticPriorWeight(clampR int) float64 {
	return minFloat(priorSpreadMultiple, float64(clampR))
}

// UpdateSpreadEWMA folds a fresh spread observation into the running estimate
// at alpha 0.3.
//
// A prev at or below zero means "no history", and the observation is adopted
// outright rather than blended toward a zero that was never measured. Note the
// asymmetry: an *observed* zero is a real reading and is blended normally.
func UpdateSpreadEWMA(prev, observed float64) float64 {
	if prev <= 0 {
		return observed
	}
	return spreadEWMAAlpha*observed + (1-spreadEWMAAlpha)*prev
}
