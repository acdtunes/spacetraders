package common

import "testing"

func TestHeavyReserve(t *testing.T) {
	base := HeavyReserveInputs{CapabilityOpen: true, HeaviesOwned: 0, HeavyCap: 5, TargetYardPrice: 1_200_000}
	cases := []struct {
		name string
		in   HeavyReserveInputs
		want int64
	}{
		{"open with room reserves exactly one heavy", base, 1_200_000},
		{"closed capability reserves nothing", func() HeavyReserveInputs { c := base; c.CapabilityOpen = false; return c }(), 0},
		{"at cap reserves nothing", func() HeavyReserveInputs { c := base; c.HeaviesOwned = 5; return c }(), 0},
		{"over cap reserves nothing", func() HeavyReserveInputs { c := base; c.HeaviesOwned = 9; return c }(), 0},
		{"zero cap is a legitimate hold", func() HeavyReserveInputs { c := base; c.HeavyCap = 0; return c }(), 0},
		{"unpriced yard reserves nothing", func() HeavyReserveInputs { c := base; c.TargetYardPrice = 0; return c }(), 0},
		{"negative price reserves nothing", func() HeavyReserveInputs { c := base; c.TargetYardPrice = -5; return c }(), 0},
		{"room for four still reserves only one", func() HeavyReserveInputs { c := base; c.HeaviesOwned = 1; return c }(), 1_200_000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HeavyReserve(tc.in); got != tc.want {
				t.Fatalf("HeavyReserve = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestHeavyReserveLockstep is the LOCKSTEP PIN on the reservation arithmetic —
// the design's single most important invariant. HeavyReserve must have exactly
// ONE definition, and every spender that respects the heavy reservation must call
// it rather than re-deriving the arithmetic. Go cannot forbid a second copy of a
// function, so this pins the properties a divergent copy would violate: any
// second implementation wired in behind this contract fails the suite rather than
// quietly drifting the economics apart (one caller holding a stale price while
// the other spends against the fresh one, so the heavy never accumulates).
func TestHeavyReserveLockstep(t *testing.T) {
	// SINGLE SLOT: the reserve is one heavy's price, never cap−owned multiples,
	// at every level of remaining headroom. This is what creates the interleaving
	// that lets expansion spend between heavy purchases.
	t.Run("always exactly one heavy regardless of headroom", func(t *testing.T) {
		const price = int64(1_200_000)
		for owned := 0; owned < 5; owned++ {
			in := HeavyReserveInputs{CapabilityOpen: true, HeaviesOwned: owned, HeavyCap: 5, TargetYardPrice: price}
			if got := HeavyReserve(in); got != price {
				t.Fatalf("owned=%d (headroom %d): reserve = %d, want exactly one heavy at %d", owned, 5-owned, got, price)
			}
		}
	})

	// The reserve TRACKS the TARGET yard's ask exactly, rather than any stored or
	// remembered figure: the target is recomputed every tick, so a newly-discovered
	// nearer yard immediately re-prices the reservation and hands any difference
	// back to expansion. It is deliberately the target's ask and not the cheapest
	// on the map — the purchase path buys NEAREST, so reserving the cheapest would
	// under-reserve and the heavy would never clear its guard.
	t.Run("tracks the target yard ask exactly", func(t *testing.T) {
		for _, price := range []int64{1, 23_500, 900_000, 1_200_000, 4_000_000} {
			in := HeavyReserveInputs{CapabilityOpen: true, HeaviesOwned: 0, HeavyCap: 5, TargetYardPrice: price}
			if got := HeavyReserve(in); got != price {
				t.Fatalf("price %d: reserve = %d, want the price itself", price, got)
			}
		}
	})

	// Every "cannot" answer RELEASES treasury (reserves zero) rather than holding
	// it. A reserve that held treasury on a blind input would starve expansion
	// indefinitely; the heavy buy itself stays gated by the autosizer's own
	// fail-closed guard stack, so releasing here weakens no money guard.
	t.Run("every cannot-buy condition releases the treasury", func(t *testing.T) {
		open := HeavyReserveInputs{CapabilityOpen: true, HeaviesOwned: 0, HeavyCap: 5, TargetYardPrice: 1_200_000}
		blocked := map[string]HeavyReserveInputs{
			"capability closed": func() HeavyReserveInputs { c := open; c.CapabilityOpen = false; return c }(),
			"at cap":            func() HeavyReserveInputs { c := open; c.HeaviesOwned = 5; return c }(),
			"over cap":          func() HeavyReserveInputs { c := open; c.HeaviesOwned = 500; return c }(),
			"zero cap":          func() HeavyReserveInputs { c := open; c.HeavyCap = 0; return c }(),
			"negative cap":      func() HeavyReserveInputs { c := open; c.HeavyCap = -3; return c }(),
			"unpriced yard":     func() HeavyReserveInputs { c := open; c.TargetYardPrice = 0; return c }(),
			"negative price":    func() HeavyReserveInputs { c := open; c.TargetYardPrice = -1; return c }(),
		}
		for name, in := range blocked {
			if got := HeavyReserve(in); got != 0 {
				t.Fatalf("%s: reserve = %d, want 0 (treasury released)", name, got)
			}
		}
	})

	// PURE: same inputs, same answer, no clock and no hidden state. A predicate
	// two containers evaluate independently must agree between them.
	t.Run("pure and repeatable", func(t *testing.T) {
		in := HeavyReserveInputs{CapabilityOpen: true, HeaviesOwned: 2, HeavyCap: 5, TargetYardPrice: 987_654}
		first := HeavyReserve(in)
		for i := 0; i < 100; i++ {
			if got := HeavyReserve(in); got != first {
				t.Fatalf("call %d returned %d, first call returned %d — not pure", i, got, first)
			}
		}
	})
}
