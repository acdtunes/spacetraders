package parkedsensing

// seedshare.go holds a share of each tick's attempt budget for SEEDS.
//
// THE ORDERING IT CORRECTS. drainCandidates returns fills first — placements in
// systems already judged IN_SCOPE, deepest first — and seeds last, and that is
// right on its own terms: a fill watches a market we have already justified
// while a seed is speculative, so a tick that can afford one probe should spend
// it on the known-good placement. What the ordering did not anticipate is having
// MORE fundable fills than maxDrainAttempts. Then the budget is exhausted before
// the loop ever reaches a seed, on every tick, forever — and no treasury fixes
// it, because the fills are genuinely worth buying.
//
// It does not self-clear either. Placements with no local buyer cost no attempts
// at all (they resolve no candidates and never touch the API — see
// drainskip_test.go), so the queue does not "work through" a backlog and reach
// the seeds later. Only fundable fills spend the budget, and they keep arriving.
//
// WHY THAT IS WORTH PROTECTING AGAINST. Every seed alive on the fleet today was
// obtained by re-tasking an already-parked spare — takeReachableSpare — not by
// buying one, and that pool is nearly empty. Once it is gone, expanding at all
// requires a seed the queue BUYS, and under the plain ordering that purchase can
// never happen. A market placement in an unopened system is also worth nothing
// until a seed has opened it, so a tick that only ever deepens systems we
// already hold eventually runs out of things worth buying.
//
// IT SPLITS THE BUDGET, IT NEVER EXTENDS IT. maxDrainAttempts is an API burst
// limit, not an economic lever, and this file does not touch it: the reserve is
// carved out of the existing six, so a tick's worst-case API cost is exactly
// what it was.

// seedAttemptReserve is how many of the tick's attempts are held for SEEDS when
// any seed is outstanding.
//
// TWO, of six. One would not be a guarantee: a single attempt that meets an
// unpriceable counter leaves the frontier with nothing that tick, and fillSlot
// exists precisely because a refusal is usually local to the counter rather than
// fatal to the placement — the second attempt is what lets a seed try the yard
// next door. More than two would invert the queue's documented priority and let
// speculation outrank known-good coverage; at four of six the fills would be the
// minority, which is not what the ordering says.
//
// Two also matches maxFootholdRetasks, the other frontier-conversion pacing
// constant in this package, and for the same underlying reason: converting more
// than a couple of systems per tick buys no extra speed, because the placement
// machine still flies one step per hull per tick.
//
// A plain constant, deliberately not a knob, in the manner of maxDrainAttempts
// and maxFootholdRetasks: it paces how one fixed budget is divided, and nothing
// downstream benefits from tuning it.
const seedAttemptReserve = 2

// fillAttemptBudget is how many of the tick's attempts the FILLS may spend
// before standing aside for the seeds queued behind them.
//
// NOT A STANDING TAX. The reserve is withheld only when a seed is actually
// outstanding; with none, the fills keep the entire budget. That matters because
// the steady state is most ticks: a reserve that idled two attempts whenever the
// frontier was quiet would cost two purchases a tick, forever, to protect nothing.
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
// once they have spent their share the loop skips the remaining fills — free,
// because the caller checks this before any read — and arrives at the seeds with
// the reserve intact.
//
// A SPARE never yields. It is the frontier the reserve exists for, and a seed
// standing aside for a seed would simply strand the budget.
func yieldsToSeeds(slot QueuedSlot, attempts, fillBudget int) bool {
	return slot.Kind != SlotKindSpare && attempts >= fillBudget
}
