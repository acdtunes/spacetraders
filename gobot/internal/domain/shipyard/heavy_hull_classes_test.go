package shipyard

import "testing"

// TestHeavyHullClassPairing is the LOCKSTEP GUARD on what "heavy" means. The
// heavy hull class is named by two different identifiers in two different tables
// — ship_type in shipyard_inventory (yard queries) and frame_symbol on ships
// (the owned-heavy census) — and there is no ship-type→frame mapping anywhere
// else in the tree. If those two sets ever disagree, the yard query and the
// census silently describe different fleets: the census under-counts, the
// reservation stays open, and the autosizer is authorised to buy a hull we
// already own. This test fails LOUD on a half-filled row so that drift is a
// broken suite rather than broken economics.
func TestHeavyHullClassPairing(t *testing.T) {
	if len(heavyHullClasses) == 0 {
		t.Fatal("heavyHullClasses is empty — heavy detection would never fire")
	}
	seenTypes := map[string]struct{}{}
	seenFrames := map[string]struct{}{}
	for i, c := range heavyHullClasses {
		if c.ShipType == "" {
			t.Fatalf("heavyHullClasses[%d] has no ShipType — the yard query would miss this class", i)
		}
		if c.FrameSymbol == "" {
			t.Fatalf("heavyHullClasses[%d] (%s) has no FrameSymbol — the owned census would UNDER-COUNT this class, authorising a duplicate buy", i, c.ShipType)
		}
		if _, dup := seenTypes[c.ShipType]; dup {
			t.Fatalf("heavyHullClasses[%d]: duplicate ShipType %s", i, c.ShipType)
		}
		if _, dup := seenFrames[c.FrameSymbol]; dup {
			t.Fatalf("heavyHullClasses[%d]: duplicate FrameSymbol %s", i, c.FrameSymbol)
		}
		seenTypes[c.ShipType] = struct{}{}
		seenFrames[c.FrameSymbol] = struct{}{}
	}
}

// TestHeavyProjectionsStayInLockstep pins that the two exported projections are
// derived from the one table and therefore always have equal cardinality and
// matching order. A second, hand-maintained list is exactly how the two sides
// drift apart.
func TestHeavyProjectionsStayInLockstep(t *testing.T) {
	if len(DefaultHeavyShipTypes) != len(heavyHullClasses) {
		t.Fatalf("DefaultHeavyShipTypes has %d entries, table has %d — projection is not derived", len(DefaultHeavyShipTypes), len(heavyHullClasses))
	}
	if len(DefaultHeavyFrameSymbols) != len(heavyHullClasses) {
		t.Fatalf("DefaultHeavyFrameSymbols has %d entries, table has %d — projection is not derived", len(DefaultHeavyFrameSymbols), len(heavyHullClasses))
	}
	for i, c := range heavyHullClasses {
		if DefaultHeavyShipTypes[i] != c.ShipType {
			t.Fatalf("DefaultHeavyShipTypes[%d] = %s, table says %s", i, DefaultHeavyShipTypes[i], c.ShipType)
		}
		if DefaultHeavyFrameSymbols[i] != c.FrameSymbol {
			t.Fatalf("DefaultHeavyFrameSymbols[%d] = %s, table says %s", i, DefaultHeavyFrameSymbols[i], c.FrameSymbol)
		}
	}
}

// TestDefaultHeavyShipTypesUnchanged pins the documented default set the
// [scouting] heavy_ship_types config key defers to (RULINGS #5). Adding a class
// is a deliberate act; this test makes an accidental one visible.
func TestDefaultHeavyShipTypesUnchanged(t *testing.T) {
	want := []string{"SHIP_HEAVY_FREIGHTER", "SHIP_BULK_FREIGHTER"}
	if len(DefaultHeavyShipTypes) != len(want) {
		t.Fatalf("DefaultHeavyShipTypes = %v, want %v", DefaultHeavyShipTypes, want)
	}
	for i := range want {
		if DefaultHeavyShipTypes[i] != want[i] {
			t.Fatalf("DefaultHeavyShipTypes = %v, want %v", DefaultHeavyShipTypes, want)
		}
	}
}
