package common

import "testing"

// atCap is the live frame's reservation input: a known heavy yard, the operator's cap of 4, and the
// 4 hulls that meet it. THE PRICE IS ZERO ON PURPOSE — at the cap the heavy pricing errand stops
// reading yards ("no ask needs reading"), so a predicate that demanded a priced target would be
// unsatisfiable in exactly the state it exists to name.
func atCap() HeavyReserveInputs {
	return HeavyReserveInputs{CapabilityOpen: true, HeaviesOwned: 4, HeavyCap: 4, TargetYardPrice: 0}
}

func TestHeavyCapBinding_MetCapOnAKnownYardBinds(t *testing.T) {
	if !HeavyCapBinding(atCap()) {
		t.Fatal("4 owned against a cap of 4 on a known heavy yard: the cap IS what bars the next heavy")
	}
}

// OVER the cap binds too, for the same reason HeavyReserve is written >=: a hull acquired outside
// the buy path must not read as headroom.
func TestHeavyCapBinding_OverTheCapBinds(t *testing.T) {
	in := atCap()
	in.HeaviesOwned = 5
	if !HeavyCapBinding(in) {
		t.Fatal("5 owned against a cap of 4 must bind, not read as headroom")
	}
}

// THE CAP MUST BE THE BAR, and this is the list of things that are not it. Each of these states
// also stops a heavy purchase, and naming any of them "the cap" would publish a reason that lies —
// the exact defect sp-suzfh filed one layer up.
func TestHeavyCapBinding_OnlyTheCapBinds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*HeavyReserveInputs)
	}{
		{"no known yard sells a heavy — availability bars it, not the cap", func(in *HeavyReserveInputs) { in.CapabilityOpen = false }},
		{"under the cap: there is headroom", func(in *HeavyReserveInputs) { in.HeaviesOwned = 3 }},
		{"cap of zero is an operator HOLD, a different decision with its own name", func(in *HeavyReserveInputs) { in.HeavyCap = 0 }},
		{"a negative cap is an unset knob, never a bound met", func(in *HeavyReserveInputs) { in.HeavyCap = -1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := atCap()
			tc.mutate(&in)
			if HeavyCapBinding(in) {
				t.Fatalf("%s: must not read as the cap binding", tc.name)
			}
		})
	}
}

// THE TWO READ ONE SET OF FACTS, and this is what stops them drifting: wherever the cap binds, the
// reservation this file's other predicate resolves is ZERO — the cap rung of HeavyReserve and this
// predicate are two readings of the same rung. A change that lets one see a cap the other does not
// fails here rather than in production, where the symptom is a reason label that names the wrong
// cause on a fleet holding millions.
func TestHeavyCapBinding_BindingImpliesTheReservationStoodDown(t *testing.T) {
	for _, open := range []bool{false, true} {
		for _, owned := range []int{0, 3, 4, 5} {
			for _, capacity := range []int{-1, 0, 4, 9} {
				for _, price := range []int64{0, 1_764_591} {
					in := HeavyReserveInputs{
						CapabilityOpen: open, HeaviesOwned: owned, HeavyCap: capacity, TargetYardPrice: price,
					}
					if HeavyCapBinding(in) && HeavyReserve(in) != 0 {
						t.Fatalf("cap binds but the reservation still targets %d: %+v", HeavyReserve(in), in)
					}
				}
			}
		}
	}
}
