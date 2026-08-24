package parkedsensing

import "math"

// procurement.go decides WHICH counter a probe is bought at. LandedYardCost RANKS and
// is advisory; WalkAwayCeiling REFUSES and is a money guard — it declines a spend,
// never compels one (RULINGS #4).

// DefaultWalkAwayMult is how many times the fleet's own cheapest fresh ask a counter
// may charge before the queue refuses it outright. A REFUSAL threshold, not a
// preference — the ranking already prefers the cheap counter — so it binds only when
// the cheap ones are out of reach and the queue would otherwise pay whatever the one
// counter in reach asks. A WHOLE MULTIPLE: this is not a sub-unit dial.
const DefaultWalkAwayMult = 3

// DefaultJumpPenaltyCredits is charged per crossing ON TOP of the gate fee when
// counters are ranked, so a marginally cheaper distant one does not win on noise.
// About a third of the measured mean gate fee (DefaultGateFeeCredits), standing for
// what that fee misses: the in-system approach legs at both ends, and the turns spent
// in transit rather than scanning. Small on purpose — a larger penalty biases back
// toward the local counter, the behaviour this file corrects.
const DefaultJumpPenaltyCredits int64 = 2_000

// LandedYardCost prices a counter as what a probe bought there costs DELIVERED to the
// placement: the ask, plus the fees of flying it home. A RANKING KEY, NEVER A GUARD —
// the buy floor keeps its own stricter arithmetic against the LIVE quote
// (LandedProbeCost). Clamped by the helpers it delegates to and floored at the ask, so
// no delivery term can rank a counter below its own sticker.
func LandedYardCost(ask int64, hops int, perGateFee, perJumpPenalty int64) int64 {
	return LandedProbeCost(ask, FerryCost(hops, perGateFee)+FerryCost(hops, perJumpPenalty))
}

// WalkAwayCeiling is the highest ask the fleet will pay, given the cheapest fresh ask
// it can see anywhere. ZERO MEANS NO CEILING, the one direction this may fail: with no
// fresh reference there is nothing to be a multiple OF, and inventing one would refuse
// every purchase exactly when the fleet knows least. A non-positive multiple reads the
// same way rather than literally — a ceiling of zero is a fleet-wide purchase stop
// wearing a price guard's name — and so does an overflowing product.
func WalkAwayCeiling(cheapestAsk int64, mult int) int64 {
	if cheapestAsk <= 0 || mult <= 0 {
		return 0
	}
	if cheapestAsk > math.MaxInt64/int64(mult) {
		return 0
	}
	return cheapestAsk * int64(mult)
}
