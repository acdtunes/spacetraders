package contract

import "testing"

// THE LOAD-BEARING PROOF (sp-mtgje): N delivery hulls own N DISTINCT fixed slots — one per
// waypoint, never piled. hull[i] (symbol order) owns slot[i] (symbol order), a pure zip: no
// demand, no occupancy, no live position. This is the fixed-placement replacement for the
// runtime distributor whose concurrent-homing timing piled idle hulls on the top-demand hub.
func TestAssignedSlot_NDeliveryHullsOwnNDistinctSlots(t *testing.T) {
	fleet := []string{"H-3", "H-1", "H-2"} // supplied out of symbol order
	slots := []string{"X1-K83", "X1-E43", "X1-H52"}

	// symbol-sorted fleet [H-1,H-2,H-3] zips to symbol-sorted slots [X1-E43,X1-H52,X1-K83].
	cases := map[string]string{"H-1": "X1-E43", "H-2": "X1-H52", "H-3": "X1-K83"}
	seen := map[string]string{}
	for hull, wantSlot := range cases {
		gotSlot, ok := AssignedSlot(hull, fleet, slots)
		if !ok {
			t.Fatalf("AssignedSlot(%s) ok=false, want a slot", hull)
		}
		if gotSlot != wantSlot {
			t.Fatalf("AssignedSlot(%s) = %q, want %q (symbol-zip)", hull, gotSlot, wantSlot)
		}
		if prev, dup := seen[gotSlot]; dup {
			t.Fatalf("slot %q assigned to BOTH %s and %s — hulls piled, not one-per-waypoint", gotSlot, prev, hull)
		}
		seen[gotSlot] = hull
	}
}

// Deterministic / restart-idempotent (#2): identical inputs yield an identical slot every
// call, regardless of input order — so the assignment is byte-identical across restarts and a
// second pass moves no hull.
func TestAssignedSlot_DeterministicAcrossCallsAndInputOrder(t *testing.T) {
	slotsA := []string{"X1-A", "X1-B", "X1-C"}
	slotsB := []string{"X1-C", "X1-A", "X1-B"} // same set, different order
	fleet := []string{"H-2", "H-1", "H-3"}

	first, ok := AssignedSlot("H-2", fleet, slotsA)
	if !ok {
		t.Fatalf("AssignedSlot(H-2) ok=false, want a slot")
	}
	for i := 0; i < 20; i++ {
		got, ok := AssignedSlot("H-2", []string{"H-3", "H-1", "H-2"}, slotsB)
		if !ok || got != first {
			t.Fatalf("non-deterministic assignment on run %d: got %q (ok=%v), want %q", i, got, ok, first)
		}
	}
}

// Surplus over the delivery knee owns NO slot: with more hulls than slots, the highest-symbol
// hulls (beyond the slot count) get ok=false so the scaler re-roles them to a warehouse rather
// than the homing piling them onto an occupied slot.
func TestAssignedSlot_SurplusHullsOwnNoSlot(t *testing.T) {
	fleet := []string{"H-1", "H-2", "H-3", "H-4"} // 4 hulls
	slots := []string{"X1-A", "X1-B"}             // only 2 slots

	if slot, ok := AssignedSlot("H-1", fleet, slots); !ok || slot != "X1-A" {
		t.Fatalf("AssignedSlot(H-1) = %q,%v, want X1-A,true", slot, ok)
	}
	if slot, ok := AssignedSlot("H-2", fleet, slots); !ok || slot != "X1-B" {
		t.Fatalf("AssignedSlot(H-2) = %q,%v, want X1-B,true", slot, ok)
	}
	for _, surplus := range []string{"H-3", "H-4"} {
		if slot, ok := AssignedSlot(surplus, fleet, slots); ok {
			t.Fatalf("AssignedSlot(%s) = %q,true, want no slot (surplus beyond the knee)", surplus, slot)
		}
	}
}

// A hull absent from the passed fleet roster is still placed (it is added to the roster), and
// an empty slot set yields no slot (homing disabled / no placement resolved).
func TestAssignedSlot_ShipAddedToRosterAndEmptySlotsYieldNone(t *testing.T) {
	if slot, ok := AssignedSlot("H-1", nil, []string{"X1-A", "X1-B"}); !ok || slot != "X1-A" {
		t.Fatalf("AssignedSlot(H-1, nil fleet) = %q,%v, want X1-A,true (ship added to roster)", slot, ok)
	}
	if slot, ok := AssignedSlot("H-1", []string{"H-2"}, nil); ok {
		t.Fatalf("AssignedSlot with no slots = %q,true, want no slot", slot)
	}
}
