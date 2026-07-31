package parkedsensing

// ferrycost.go prices DELIVERY of a bought probe, so the buy floor binds on what
// a hull actually costs to put on post rather than on its sticker price.
//
// WHY IT EXISTS (sp-e46yc). An Admiral-authorised ~10M probe expansion bought 258
// probes for 10,152,775 — on budget — and then spent a further 6,444,427 in gate
// fees flying them to post. Landed cost 16.6M, 63% over the authorisation. The
// buy floor never saw it: fillSlot checked `credits - quote < floor`, and `quote`
// is what the counter charges, not what the hull costs to deliver. A probe
// evaluated at ~83k sticker actually cost ~135k landed, so every sizing decision
// the engine has ever taken was taken on the wrong number.
//
// It is a value-in/value-out function like everything else in this package: the
// caller supplies the hop count and the fee, and gets credits back.

// DefaultGateFeeCredits is the expected cost of ONE gate crossing, in credits.
//
// MEASURED 2026-07-31 over 4,235 recorded jumps: mean 5,930, median 5,504, p75
// 6,232, p90 7,475, range 3,816-19,409.
//
// THE MEAN IS THE RIGHT STATISTIC HERE, not the median, and the distinction is
// load-bearing rather than pedantic. This constant is multiplied by a hop COUNT
// to price a SUM of that many crossings, and the expectation of a sum is the sum
// of expectations — so E[cost of n jumps] = n x mean. The distribution is
// right-skewed (mean above median), so pricing off the median would understate
// every multi-hop ferry, and understating is the one direction a money guard may
// not fail. 5,900 is the measured mean rounded to two significant figures, and it
// agrees with the ~5,900 figure the jump-fee accounting lane arrived at
// independently.
//
// IT IS A FLEET-WIDE SCALAR, AND THAT IS A KNOWN APPROXIMATION. sp-9idvn
// establishes that the fee is a per-ORIGIN-GATE constant — 286 scalars, each
// learnable from a single observed jump, with corr(fee, distance) = 0.124 and the
// same edge costing 27% more in one direction than the other. A per-gate table is
// therefore both correct and available in the ledger, and is the natural
// refinement: this constant is the fleet mean of exactly that table. Refining it
// changes only which number FerryCost multiplies, not any caller.
const DefaultGateFeeCredits int64 = 5_900

// FerryHops normalises a purchase candidate's hop count to what may be CHARGED
// for, and it is the fail-closed heart of this file.
//
// A counter in the placement's own system is free to reach: no gate is crossed,
// so there is nothing to price and the answer is zero.
//
// A CROSS-SYSTEM COUNTER IS AT LEAST ONE GATE AWAY BY DEFINITION — that is what
// "different system" means on this map — so an unknown or absent hop count prices
// as ONE, never as free. That case is not hypothetical: purchaseCandidate.ferried
// is derived from the two symbols and is therefore honest on every path, but the
// hop count is only resolved on the path that walked the gate graph. The
// recorded-yard preference re-offers a remote yard an earlier tick chose without
// re-walking it, and a zero there would silently reproduce the exact defect this
// file exists to close: a ferried hull priced at sticker.
//
// So the floor is derived from the ferried flag, not guessed. It understates a
// nine-hop ferry, which is a real limitation and the reason the walked path
// supplies the true count — but it can never price a delivery at nothing.
func FerryHops(ferried bool, hops int) int {
	if !ferried {
		return 0
	}
	if hops < 1 {
		return 1
	}
	return hops
}

// FerryCost prices flying a bought hull `hops` gate crossings to its post.
//
// Both inputs are clamped non-negative INDEPENDENTLY before they are multiplied,
// for the reason ProbeBuyFloor clamps each factor of its runway term: a negative
// hop count and a negative fee would otherwise multiply into a positive number,
// and a phantom credit inside a money guard is the failure that matters. Clamping
// the product alone would not catch it.
func FerryCost(hops int, perGateFee int64) int64 {
	if hops < 0 {
		hops = 0
	}
	if perGateFee < 0 {
		perGateFee = 0
	}
	return int64(hops) * perGateFee
}

// LandedProbeCost is what a probe must be afforded against: the counter's quote
// plus the cost of delivering it.
//
// Each term is clamped non-negative before the sum, so a malformed quote cannot
// discount the ferry and a malformed ferry cannot discount the quote. The result
// is then floored at the quote itself, which is what makes this function unable
// to be WEAKER than the sticker-price check it replaces: whatever the ferry term
// does, including an arithmetic overflow that wrapped the sum negative, the guard
// still demands at least what it demanded before this file existed.
func LandedProbeCost(quote int64, ferry int64) int64 {
	if quote < 0 {
		quote = 0
	}
	if ferry < 0 {
		ferry = 0
	}
	landed := quote + ferry
	if landed < quote {
		return quote
	}
	return landed
}
