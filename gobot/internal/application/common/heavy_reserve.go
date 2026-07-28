package common

// HeavyReserveInputs are the three derived facts the predicate needs. All come
// from durable tables; nothing here is stored or cached.
//
// CapabilityOpen and CheapestKnownPrice both derive from the era-scoped
// shipyard_inventory read (a known yard selling a heavy hull type, at a positive
// ask); HeaviesOwned is the BROAD owned-heavy census — every owned heavy hull,
// regardless of which fleet it is tagged to. The breadth is deliberate: the cap
// bounds capital exposure, and under-counting is the direction that would
// authorise buying a hull we already own.
type HeavyReserveInputs struct {
	CapabilityOpen     bool  // any known yard sells the heavy hull type
	HeaviesOwned       int   // every owned heavy, regardless of fleet tag
	HeavyCap           int   // operator dial
	CheapestKnownPrice int64 // min positive purchase_price across known heavy yards
}

// HeavyReserve is the ONE definition of how much treasury is held back for the
// next heavy purchase. Both the fleet autosizer and the sensing buy-floor call
// THIS function; a second copy is how a reservation silently drifts.
//
// LOCKSTEP REQUIREMENT (the design's single most important invariant): this
// function has exactly one definition, and every spender that must respect the
// heavy reservation calls it rather than re-deriving the arithmetic. The two
// callers are:
//
//   - the fleet autosizer's heavy purchase path (it must not spend the
//     reservation it is itself accumulating), and
//   - the sensing/probe buy-floor, which adds this to its existing capex term so
//     probe buying pauses while a heavy accumulates.
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
// stored, so there is nothing to re-derive).
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
// Pure: no clock, no I/O, no logging — inputs to an int64.
func HeavyReserve(in HeavyReserveInputs) int64 {
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
	// A listed-but-unpriced yard (purchase_price 0) proves availability but can
	// never feed a money guard, so it reserves nothing — holding treasury against
	// a price we cannot see would stall expansion indefinitely.
	if in.CheapestKnownPrice <= 0 {
		return 0
	}
	return in.CheapestKnownPrice
}
