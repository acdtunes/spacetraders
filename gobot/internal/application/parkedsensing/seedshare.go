package parkedsensing

// seedshare.go holds a share of each tick's attempt budget for SEEDS.
//
// THE ORDERING IT CORRECTS. drainCandidates returns fills first — placements in
// systems already judged IN_SCOPE, deepest first — and seeds last, which is right
// on its own terms: a fill watches a market we have already justified while a seed
// is speculative. But with MORE fundable fills than maxDrainAttempts the budget is
// exhausted before the loop ever reaches a seed, on every tick, and no treasury
// fixes it because the fills are genuinely worth buying.
//
// It does not self-clear either: placements with no local buyer cost no attempts
// at all (they resolve no candidates and never touch the API), so the queue does
// not "work through" a backlog and reach the seeds later. Only fundable fills
// spend the budget, and they keep arriving. A market placement in an unopened
// system is worth nothing until a seed has opened it, so a tick that only ever
// deepens systems we already hold eventually runs out of things worth buying.
//
// IT SPLITS THE BUDGET, IT NEVER EXTENDS IT. maxDrainAttempts is an API burst
// limit, not an economic lever, and this file does not touch it: the reserve is
// carved out of it, so a tick's worst-case API cost is unchanged.

// seedAttemptReserve is how many of the tick's attempts are held for SEEDS when
// any seed is outstanding.
//
// More than one, because a single attempt that meets an unpriceable counter leaves
// the frontier with nothing that tick — fillSlot exists precisely because a refusal
// is usually local to the counter rather than fatal to the placement, and the
// second attempt is what lets a seed try the yard next door. Well under half of
// maxDrainAttempts, because a larger share would invert the queue's documented
// priority and let speculation outrank known-good coverage.
const seedAttemptReserve = 2

// fillAttemptBudget is how many of the tick's attempts the FILLS may spend
// before standing aside for the seeds queued behind them.
//
// NOT A STANDING TAX. The reserve is withheld only when a seed is actually
// outstanding; with none, the fills keep the entire budget. A reserve that idled
// attempts whenever the frontier was quiet would cost purchases every tick to
// protect nothing.
func fillAttemptBudget(candidates []QueuedSlot) int {
	for _, slot := range candidates {
		if slot.Kind == SlotKindSpare {
			return maxDrainAttempts - seedAttemptReserve
		}
	}
	return maxDrainAttempts
}

// yieldsToSeeds reports whether this candidate must stand aside so the seeds
// behind it keep their share of the tick.
//
// Seeds sort LAST, which is what makes a cap on the fills the whole mechanism:
// once the fills have spent their share the loop skips the rest of them — free,
// because the caller checks this before any read — and arrives at the seeds with
// the reserve intact. A SPARE never yields: it is the frontier the reserve exists
// for, and a seed standing aside for a seed would simply strand the budget.
func yieldsToSeeds(slot QueuedSlot, attempts, fillBudget int) bool {
	return slot.Kind != SlotKindSpare && attempts >= fillBudget
}
