package shipyard

import "testing"

// The census predicate: frame list PRIMARY, cargo-capacity SAFETY NET.
//
// The fleet owns no heavy hull today (verified 2026-07-27: FRAME_LIGHT_FREIGHTER ×8 at 80 cargo,
// FRAME_FRIGATE ×1 at 40, FRAME_PROBE ×3 at 0), so the heavy frame symbols are INFERRED from
// SpaceTraders' SHIP_/FRAME_ naming symmetry and cannot be corroborated against an owned hull. A
// wrong frame symbol would UNDER-count, which is the money-unsafe direction: an invisible heavy
// leaves the reservation open and authorises re-buying a hull we already own.
//
// The capacity net closes that: anything at or above the threshold counts as heavy whatever its
// frame. That OVER-counts in the ambiguous case, which buys FEWER heavies — the safe direction.
func TestIsHeavyHull(t *testing.T) {
	cases := []struct {
		name             string
		frame            string
		cargo            int
		wantHeavy        bool
		wantUnrecognised bool
	}{
		// Frame list is primary and authoritative — capacity is not consulted to say "yes".
		{"known heavy frame counts", "FRAME_HEAVY_FREIGHTER", 225, true, false},
		{"the other known heavy frame counts", "FRAME_BULK_FREIGHTER", 500, true, false},
		{"known heavy frame counts even below the threshold", "FRAME_HEAVY_FREIGHTER", 10, true, false},

		// The safety net: big hold, frame we do not recognise ⇒ count it AND flag it.
		{"unknown frame at the threshold counts and flags", "FRAME_MYSTERY", HeavyCargoCapacityThreshold, true, true},
		{"unknown frame above the threshold counts and flags", "FRAME_MYSTERY", 400, true, true},

		// Below the threshold with an unrecognised frame is an ordinary hull: no count, no noise.
		{"unknown frame below the threshold does not count", "FRAME_MYSTERY", HeavyCargoCapacityThreshold - 1, false, false},

		// The hulls the fleet actually owns today must never count.
		{"the 80-cargo light freighter we own never counts", "FRAME_LIGHT_FREIGHTER", 80, false, false},
		{"the 40-cargo frigate we own never counts", "FRAME_FRIGATE", 40, false, false},
		{"a 0-cargo probe never counts", "FRAME_PROBE", 0, false, false},
		{"an empty frame with no cargo never counts", "", 0, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			heavy, unrecognised := IsHeavyHull(tc.frame, tc.cargo)
			if heavy != tc.wantHeavy {
				t.Fatalf("IsHeavyHull(%q, %d) heavy = %v, want %v", tc.frame, tc.cargo, heavy, tc.wantHeavy)
			}
			if unrecognised != tc.wantUnrecognised {
				t.Fatalf("IsHeavyHull(%q, %d) unrecognisedFrame = %v, want %v", tc.frame, tc.cargo, unrecognised, tc.wantUnrecognised)
			}
		})
	}
}

// The threshold must sit in the empirically-verified gap between the largest hull the fleet owns
// (80) and a heavy freighter (~225). Too low and ordinary fleet growth would classify working
// haulers as heavy, close the capability and stall heavy buying forever; too high and a real heavy
// slips under the net, which is the under-count direction the net exists to prevent.
func TestHeavyCargoCapacityThresholdSitsInTheVerifiedGap(t *testing.T) {
	const largestOwnedToday = 80
	const heavyFreighterHold = 225
	if HeavyCargoCapacityThreshold <= largestOwnedToday {
		t.Fatalf("threshold %d must be ABOVE the largest hull we own (%d) or working haulers count as heavy", HeavyCargoCapacityThreshold, largestOwnedToday)
	}
	if HeavyCargoCapacityThreshold >= heavyFreighterHold {
		t.Fatalf("threshold %d must be BELOW a heavy freighter's hold (%d) or a real heavy slips the net", HeavyCargoCapacityThreshold, heavyFreighterHold)
	}
}
