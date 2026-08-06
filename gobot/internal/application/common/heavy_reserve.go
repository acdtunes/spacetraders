package common

// HeavyReserveInputs are the three derived facts the predicate needs. All come
// from durable tables; nothing here is stored or cached.
//
// CapabilityOpen and TargetYardPrice both derive from the era-scoped
// shipyard_inventory read, but they answer DIFFERENT questions and the gap
// between them is load-bearing. CapabilityOpen is pure AVAILABILITY — does any
// known yard sell a heavy hull type at all — and it counts availability-only
// rows (purchase_price 0), the catalogue a presence-less shipyard read leaves
// behind. TargetYardPrice is what the yard we would actually buy at is asking.
// A yard can be known for days with the capability open and no price to reserve
// against; that state is real, it is what the heavy-yard pricing errand exists
// to resolve, and collapsing the two would hide it.
//
// HeaviesOwned is the BROAD owned-heavy census — every owned heavy hull,
// regardless of which fleet it is tagged to. The breadth is deliberate: the cap
// bounds capital exposure, and under-counting is the direction that would
// authorise buying a hull we already own.
type HeavyReserveInputs struct {
	CapabilityOpen bool // any known yard sells the heavy hull type, PRICED OR NOT
	HeaviesOwned   int  // every owned heavy, regardless of fleet tag
	HeavyCap       int  // operator dial
	// TargetYardPrice is the ask at the yard the purchase path would TARGET —
	// the nearest reachable priced yard for the preferred heavy class — not the
	// cheapest ask on the map.
	//
	// WHY THE TARGET AND NOT THE CHEAPEST (sp-fwk8z). The purchase path buys
	// NEAREST, not cheapest. Reserving the cheapest therefore UNDER-reserves
	// whenever the two differ: treasury tops out at floor + cheapest, the
	// nearer yard asks more, the guard never clears, and the heavy is never
	// bought — the exact stall this reservation exists to prevent. Reserving
	// the target's ask holds back at least what we will be asked, which is the
	// stricter direction and the only one that can complete a purchase
	// (RULINGS #4: a money guard may only ever get stricter). It is resolved by
	// ONE shared implementation both callers consume, so the two can never
	// disagree about which yard they are saving toward.
	TargetYardPrice int64
}

// HeavyReserveTarget is the FULL ask the fleet is saving toward: what the target yard would
// charge for the next heavy. It is a DISTINCT TYPE from the credits actually withheld, and the
// distinction is the whole of the sp-zg71k fix.
//
// IT IS NOT A SPEND-FLOOR INPUT. A target is an aspiration measured in the SHIPYARD's units; a
// hold is a decision measured against the treasury we actually have. sp-zg71k was exactly that
// confusion: the target went verbatim into the sensing capex term, ProbeBuyFloor ADDED it, and
// the probe-buy floor became 50,000 + the entire asking price of a heavy freighter (~1,510,645 on
// era-5 evidence) against a ~372,000 treasury. Probe buying stood down completely and the
// autosizer's light/explorer/warehouse buying went with it — every non-heavy class judges
// treasury − floor − reserve, which had gone a million credits negative. Expansion froze the
// instant the reservation succeeded at the thing it was trying to do: pricing a heavy yard.
//
// The named type is what makes that a COMPILE ERROR rather than a code review. A
// HeavyReserveTarget cannot be added to an int64 floor term; the only way to get credits out of
// it is HoldAt, which is where the treasury bound lives. int64(target) conversions are for
// REPORTING ONLY (a gauge, a heartbeat field) — a conversion feeding a floor, a guard or a
// spendable balance is the defect coming back.
type HeavyReserveTarget int64

// HeavyReserveEntrySharePct is the share of the target ask a fleet must ALREADY hold — in surplus
// above ImmutableReserveFloor — before ONE credit is withheld toward it. Half: save toward a heavy
// once you are half way there, never before.
//
// IMMUTABLE, const-only, no config/tune/launch seam — the same treatment every other number in
// this file gets (RULINGS #5, and see the #5 amendment's hard-floor exception). It is not an
// operator dial and must not become one: it is the shape of the savings curve, and a savings curve
// an operator can set to 0 is the sp-zg71k freeze again.
//
// WHY HALF, and why the entry threshold scales with the ASK rather than being a flat number. The
// question the bound has to answer is "is this target plausibly reachable", and the only
// era-independent way to ask it is RELATIVELY: a fleet holding half a hull's price is credibly
// saving for it; a fleet holding a fifth of it is not saving, it is starving. A flat threshold
// would answer differently in era 3 and era 9 for the same fleet. Half also splits the climb
// evenly — the first half of the ask is expansion's, the second half is the heavy's — so neither
// spender can lock the other out of the whole journey.
const HeavyReserveEntrySharePct = 50

// HeavyReserve is the ONE definition of WHAT the fleet is saving toward for the next heavy
// purchase — the target, not the hold. Both the fleet autosizer and the sensing buy-floor call
// THIS function and then HoldAt; a second copy of either half is how a reservation silently
// drifts.
//
// TWO STEPS, ONE DEFINITION EACH (sp-zg71k). HeavyReserve answers "which ask are we saving
// toward" from durable, treasury-independent facts. HoldAt answers "how much of it may we
// actually withhold right now" from the live balance. They are split because the two callers
// learn the treasury at different moments: the sensing reserve port is a DB-only read placed
// ahead of every network gate precisely so a tick with nothing to buy costs no API call, and the
// live-credits read that would collapse the two into one call sits behind that gate. Splitting
// the steps keeps the cheap read cheap; the named return type keeps them from being used apart.
//
// LOCKSTEP REQUIREMENT (the design's single most important invariant): each half has exactly one
// definition, and every spender that must respect the heavy reservation calls both rather than
// re-deriving the arithmetic. The two callers are:
//
//   - the fleet autosizer's heavy purchase path (it must not spend the
//     reservation it is itself accumulating), and
//   - the sensing/probe buy-floor, which adds the HOLD to its existing capex term so
//     probe buying stands down while a heavy accumulates.
//
// Two near-copies of this predicate is precisely how a reservation silently
// drifts: one caller would hold back a stale price while the other spends against
// the fresh one, and the heavy would never accumulate. HeavyReserveLockstep pins
// the arithmetic so a divergent second copy fails the suite rather than the
// economics.
//
// The reservation is DERIVED, never stored: it is recomputed from durable tables
// on every read, so there is no write protocol, no staleness question, and no
// re-fire hazard on restart (RULINGS #2 is satisfied trivially — nothing was
// stored, so there is nothing to re-derive). The treasury bound in HoldAt keeps that
// property: it is a function of the live balance on the tick that asks, never of a
// remembered high-water mark or an accumulated savings counter.
//
// Exactly ONE heavy is ever reserved, never cap−owned multiples. The
// single-slot reservation is what creates the interleaving: expansion gets a
// spending window between each heavy purchase instead of being held off until
// the whole heavy fleet is bought.
//
// Every "cannot" answer reserves ZERO, which RELEASES treasury to the other
// spender. That is the correct fail direction here and does not weaken any money
// guard (RULINGS #4): this predicate authorises no spend of its own — it only
// withholds — and the heavy buy itself remains gated by the autosizer's full
// fail-closed guard stack. A reserve that failed "closed" by holding treasury on
// an unreadable input would starve expansion on a blind signal.
//
// Pure: no clock, no I/O, no logging — inputs to a target.
func HeavyReserve(in HeavyReserveInputs) HeavyReserveTarget {
	// No known yard sells a heavy: there is nothing to save toward.
	if !in.CapabilityOpen {
		return 0
	}
	// A non-positive cap is a legitimate operator hold ("own no heavies"), not an
	// unset knob to be defaulted here — the caller resolves its own default.
	if in.HeavyCap <= 0 {
		return 0
	}
	// At or over the cap: the reservation drops to zero and the treasury is
	// released to expansion. Written >= so an over-cap fleet (a hull acquired
	// outside this path) also reserves nothing.
	if in.HeaviesOwned >= in.HeavyCap {
		return 0
	}
	// The capability is open on availability alone, so this rung is REACHED in
	// normal operation rather than being a defensive check: a known yard whose
	// ask nobody has ever read (purchase_price 0) proves availability and can
	// never feed a money guard. It reserves nothing — holding treasury against a
	// price we cannot see would stall expansion indefinitely — and the heavy-yard
	// pricing errand is what resolves that state by sending a hull to read the ask.
	if in.TargetYardPrice <= 0 {
		return 0
	}
	return HeavyReserveTarget(in.TargetYardPrice)
}

// HoldAt is the ONE definition of how many credits are ACTUALLY withheld toward the target at a
// given live treasury. It is the second half of the reservation and the sp-zg71k fix.
//
//	surplus := treasury − ImmutableReserveFloor        // the only money that was ever ours to allocate
//	entry   := ask × HeavyReserveEntrySharePct / 100   // half the ask: the credibility threshold
//	hold    := clamp(0, ask, surplus − entry)
//
// THE DEFECT IT FIXES. Withholding the whole ask the moment a yard is priced is a savings plan
// only when the ask is reachable. When it is not — era-5's ~1,510,645 heavy against a ~372,000
// treasury — it is a deadlock wearing a guard's clothing: the floor exceeds the balance, every
// other spender stops, and the fleet that would have earned the difference stops earning it. A
// savings target larger than anything that can plausibly be accumulated protects nothing.
//
// WHY THIS IS A POLICY CORRECTION AND NOT A WEAKENED MONEY GUARD (RULINGS #4). The money guard is
// ImmutableReserveFloor, and it is untouched: this function only ever subtracts it, never lowers
// it, and every consumer still floors at it independently (ProbeBuyFloor's closing
// maxInt64(floor, immutable); the autosizer's own const floor term). The heavy reservation is an
// ACCUMULATION POLICY layered above that guard — it authorises no spend of its own, it only
// withholds, and this file's own standing rationale already establishes that releasing it is the
// correct direction: "Every 'cannot' answer reserves ZERO, which RELEASES treasury to the other
// spender ... A reserve that failed 'closed' by holding treasury on an unreadable input would
// starve expansion on a blind signal." Bounding an unreachable hold is the same judgement applied
// to an unreachable target rather than an unreadable one. No solvency guard moves: the 25%
// big-ticket rule, the per-buy margin, the probe cap and the immutable floor all still bind
// exactly as before.
//
// WHY IT CANNOT THRASH — the property, not the intention. hold is CONTINUOUS and
// NON-DECREASING in treasury, with slope at most 1, so:
//
//   - spendable(T) = T − ImmutableReserveFloor − hold(T) is non-decreasing in T. Earning never
//     costs a spender headroom and spending never buys one headroom back. There is no arm/disarm
//     edge to sit on and no feedback loop to ring.
//   - a one-credit change in treasury moves the hold by at most one credit. The tempting
//     one-liner — "stand the reserve down when it would push the floor above treasury" —
//     fails exactly here: at the threshold a single credit flips the hold between 0 and the whole
//     ask, so the fleet crosses, arms, buys one probe, falls back, disarms, and the entire
//     accumulation is released for probe spending. TestHeavyReserveHoldCannotThrash runs that
//     naive rule beside this one and asserts it violates both properties.
//
// WHY sp-fwk8z'S INTENT SURVIVES. That bead made the reservation the STRICTER of two options —
// the target yard's ask, not the cheapest on the map — because under-reserving means the guard
// never clears and the heavy is never bought. The target is untouched here, and once the fleet is
// genuinely in range (surplus ≥ ask + entry) the hold is the FULL ask, byte-identical to the
// behaviour sp-fwk8z shipped, and treasury-independent from there up. The autosizer's heavy buy
// waives its own reserve, so it clears at surplus ≥ price + margin — and under the 25% big-ticket
// rule (RULINGS #6) it cannot fire until treasury ≈ 4× the ask, by which point the hold has long
// since saturated at the full ask. The purchase this reservation exists to complete still
// completes.
//
// AN UNREADABLE TREASURY HOLDS NOTHING, by the same rule every other "cannot" answer follows: a
// zero or unknown balance yields a non-positive surplus and therefore a zero hold. Both callers
// independently refuse to spend on an unreadable treasury (the autosizer's affordability guard
// fails closed on it; the drain aborts the tick), so releasing here can never authorise a buy.
//
// Pure: no clock, no I/O, no stored state — a target and a balance to a credit count.
func (t HeavyReserveTarget) HoldAt(liveTreasury int64) int64 {
	ask := int64(t)
	if ask <= 0 {
		return 0
	}
	// Only the surplus above the immutable floor was ever available to allocate. Subtracting the
	// same base every consumer floors at is what makes "hold ≤ surplus" mean "the reservation can
	// never push a floor above the balance".
	surplus := liveTreasury - ImmutableReserveFloor
	if surplus <= 0 {
		return 0
	}
	// entry = ask × pct / 100, computed in two terms so the product cannot overflow int64 for any
	// price the API can express. Exactly equal to the single-expression form, remainder included.
	entry := ask/100*HeavyReserveEntrySharePct + (ask%100)*HeavyReserveEntrySharePct/100
	hold := surplus - entry
	if hold <= 0 {
		// Not yet half way: this era cannot plausibly reach the ask, so nothing is saved toward it
		// and expansion runs at full speed. THE DEADLOCK CASE, and it now costs the fleet nothing.
		return 0
	}
	if hold > ask {
		// Saturated: never more than one heavy's ask, whatever the treasury. This is the rung that
		// keeps sp-fwk8z's behaviour intact for a reachable target.
		return ask
	}
	return hold
}
