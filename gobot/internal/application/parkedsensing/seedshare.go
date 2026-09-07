package parkedsensing

// seedshare.go holds a share of each tick's attempt budget for SEEDS.
//
// drainCandidates returns fills first and seeds last, which is right on its own
// terms — a fill watches a market already justified, a seed is speculative. But with
// more fundable fills than the tick's budget the budget is spent before the loop
// reaches a seed, on every tick, and it does not self-clear: placements with no local
// buyer cost no attempts, so the queue never works through the backlog to the seeds.
//
// IT SPLITS THE BUDGET, IT NEVER EXTENDS IT, so the worst-case API cost is unchanged.

// seedAttemptReserve is how many attempts are held for SEEDS when one is outstanding,
// at the ceiling-era budget of MaxDrainAttempts. More than one, because a lone attempt
// meeting an unpriceable counter leaves the frontier with nothing that tick; well
// under half, or speculation would outrank known-good coverage.
const seedAttemptReserve = 2

// seedAttemptShare is that reserve at THIS tick's budget. Both terms above are RATIOS
// of MaxDrainAttempts, so a flat 2 against a budget six times wider would fund the
// frontier as thinly as today while every extra attempt went to the fills. Floored at
// the constant: byte-identical at the ceiling-era budget, upward only.
func seedAttemptShare(maxAttempts int) int {
	share := maxAttempts * seedAttemptReserve / MaxDrainAttempts
	if share < seedAttemptReserve {
		return seedAttemptReserve
	}
	return share
}

// fillAttemptBudget is what the FILLS may spend before standing aside for the seeds
// behind them, FROM maxAttempts AND NEVER FROM THE CONSTANT: it divides the budget the
// loop spends, so splitting the constant while the loop ran scaled would hand the whole
// difference to the fills. Not a standing tax — withheld only when a seed is queued.
func fillAttemptBudget(candidates []QueuedSlot, maxAttempts int) int {
	if maxAttempts <= 0 {
		maxAttempts = MaxDrainAttempts
	}
	for _, slot := range candidates {
		if slot.Kind == SlotKindSpare {
			return maxAttempts - seedAttemptShare(maxAttempts)
		}
	}
	return maxAttempts
}

// yieldsToSeeds reports whether this candidate stands aside so the seeds behind it keep
// their share. Seeds sort LAST, which is what makes a cap on the fills the mechanism:
// the loop skips the remaining fills — free, checked before any read — and reaches the
// seeds with the reserve intact. A SPARE never yields; it is what the reserve is for.
func yieldsToSeeds(slot QueuedSlot, attempts, fillBudget int) bool {
	return slot.Kind != SlotKindSpare && attempts >= fillBudget
}
