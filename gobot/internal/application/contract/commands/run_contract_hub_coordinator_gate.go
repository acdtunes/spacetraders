package commands

// This file is the Phase-2 REGRESSION SEAM (sp-q2zq acceptance): the cost-gate that Phase 2 will
// use to decide whether to RE-HOME an existing idle hauler is landed now as a PURE function so it
// is testable in isolation and Phase-1 design provably accommodates it. Phase 1 does not call it
// (it never re-homes); it exists so a one-tick price flip → no re-home is a locked-in guarantee.

// RehomeDecision is the pure input to the Phase-2 re-home cost-gate. All fields are Analyst-owned
// quantities (RULINGS #5): the caller computes the marginal buy-leg saving of the alternative hub
// over the current one, the horizon to amortize it over, the one-time repositioning trip cost, and
// the hysteresis margin.
type RehomeDecision struct {
	// SavingsPerContract is the marginal payment-weighted buy-leg saving of H_alt over H_cur, per
	// contract (the smoothed, durable saving — NOT a single tick's raw delta).
	SavingsPerContract float64
	// ExpectedRemainingContracts is how many more contracts this era the saving is amortized over.
	ExpectedRemainingContracts float64
	// RepositionCost is the one-time cost (distance) of moving the hauler H_cur → H_alt.
	RepositionCost float64
	// HysteresisMargin is the band above break-even the move must clear, to prevent oscillation
	// around the break-even point (with the EWMA upstream, this guarantees a one-tick flip cannot
	// trigger a move).
	HysteresisMargin float64
}

// ShouldRehome reports whether Phase 2 should re-home a hauler: the durable saving, amortized over
// the expected remaining contracts, must STRICTLY exceed the repositioning trip cost plus the
// hysteresis margin. A tiny (one-tick) saving, or no remaining contracts, never clears the bar —
// so re-homing is rejected in exactly the cases that would churn.
func ShouldRehome(d RehomeDecision) bool {
	totalSaving := d.SavingsPerContract * d.ExpectedRemainingContracts
	return totalSaving > d.RepositionCost+d.HysteresisMargin
}
