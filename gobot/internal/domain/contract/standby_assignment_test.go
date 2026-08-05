package contract

import (
	"math"
	"testing"
)

// GROWING THE ROSTER RELOCATES NOBODY (sp-lkuh9). The regression the 2-hull/3-hull cases below
// could not see: each asserts ONE fleet size in isolation, so a rule whose retained slot set
// re-orders as the fleet grows passes both and still evicts parked hulls on the transition.
// This sweeps the WHOLE growth curve — 1 hull to one past the slot count — and asserts that at
// every step each incumbent keeps the exact slot it already owned. Ship symbols increment, so
// appending the highest symbol IS what buying a hull does, twice on every cold-start ramp.
//
// STABILITY IS ONLY HALF THE CONTRACT, and on its own it is worth nothing: an AssignedSlot that
// places NOBODY never moves anyone, and one that piles the whole fleet on slot 0 never moves
// anyone either. Both are perfectly "stable" and both are useless, so each fleet size also
// asserts that exactly min(fleetSize, slots) DISTINCT slots are occupied — placement and the
// one-hull-per-waypoint spread, checked at every step alongside the stability. Without that this
// test passes a do-nothing stub, which is the same shape of vacuum as the sp-9suun coverage gap
// this bead exists to close.
func TestAssignedSlot_GrowingTheFleetRelocatesNoParkedHull(t *testing.T) {
	// Real era-5 placement priority: the four era-invariant anchors ((1) H-stack, (2) far sink,
	// (3) far source base, (4) E-stack) then the demand-ranked central fill. Deliberately
	// anti-correlated with symbol order — that anti-correlation is what the bug fed on.
	slots := []string{"X1-KP46-H49", "X1-KP46-J56", "X1-KP46-B7", "X1-KP46-E42", "X1-KP46-A1", "X1-KP46-AX5Z"}
	// Ascending symbols: each step appends the newest (highest) hull, the purchase case.
	growth := []string{"TORWINDSTG-3", "TORWINDSTG-7", "TORWINDSTG-B", "TORWINDSTG-E", "TORWINDSTG-F", "TORWINDSTG-G", "TORWINDSTG-H"}

	settled := map[string]string{}
	for size := 1; size <= len(growth); size++ {
		fleet := growth[:size]
		occupant := map[string]string{} // slot -> hull, at THIS fleet size
		for _, hull := range fleet {
			slot, owns := AssignedSlot(hull, fleet, slots)
			previous, parked := settled[hull]
			switch {
			case parked && !owns:
				t.Errorf("fleet grew to %d hulls: %s was PARKED at %s and now owns NO slot — a settled hull was un-homed by a peer's arrival",
					size, hull, previous)
			case parked && slot != previous:
				t.Errorf("fleet grew to %d hulls: %s was PARKED at %s and got RELOCATED to %s — adding a hull must move no settled hull (sp-lkuh9)",
					size, hull, previous, slot)
			}
			if !owns {
				continue
			}
			if peer, piled := occupant[slot]; piled {
				t.Errorf("at %d hulls, slot %s is assigned to BOTH %s and %s — hulls piled, not one per waypoint", size, slot, peer, hull)
			}
			occupant[slot] = hull
			settled[hull] = slot
		}
		// Placement + spread, without which "nothing moved" is satisfied by nothing happening.
		if want := min(size, len(slots)); len(occupant) != want {
			t.Errorf("at %d hulls, %d distinct slots are occupied, want %d — every hull up to the slot count must actually be PLACED, one per waypoint. "+
				"An assignment that places nobody (or piles everyone on one slot) relocates nobody either, and is not what this test is for",
				size, len(occupant), want)
		}
	}
}

// THE LIVE INCIDENT (sp-lkuh9, staging X1-KP46): one hull purchase permuted the whole assignment
// for a 13.1x-worse placement — the new hull was sent 711u to the far sink while the slot that
// actually opened for it lay 62u away, AND an already-parked hull was evicted 100u off its own
// slot. 811u of travel where 62u was both sufficient and adjacent.
//
// The bead's acceptance: adding a hull costs ONLY the new hull's own leg to the slot that opened
// for it — every settled hull contributes zero. Distances are the bead's measured ones.
func TestAssignedSlot_AddingAHullCostsOnlyTheNewHullsOwnLeg(t *testing.T) {
	const (
		hStack   = "X1-KP46-H51" // (1) H-stack anchor
		farSink  = "X1-KP46-J58" // (2) far sink — the 711u mis-assignment
		farBase  = "X1-KP46-B7"  // (3) far source base
		eStack   = "X1-KP46-E42" // (4) E-stack — the slot that OPENS for the 4th hull
		newIdles = "X1-KP46-A2"  // where the newly-bought hull sits when it joins
	)
	// Collinear positions reproducing the bead's three measured distances exactly:
	// A2->E42 = 62u (the adjacent optimum), A2->J58 = 711u (the leg actually flown),
	// H51->E42 = 100u (the eviction of the hull that had only just finished homing).
	positionX := map[string]float64{newIdles: 0, eStack: 62, hStack: 162, farSink: 711, farBase: 2000}
	slots := []string{hStack, farSink, farBase, eStack} // PLACEMENT PRIORITY

	settled := []string{"TORWINDSTG-3", "TORWINDSTG-7", "TORWINDSTG-B"}
	grown := append(append([]string{}, settled...), "TORWINDSTG-E")

	// Before the purchase every incumbent is standing ON its own assigned slot (homing settled).
	standingAt := map[string]string{}
	for _, hull := range settled {
		slot, owns := AssignedSlot(hull, settled, slots)
		if !owns {
			t.Fatalf("fixture: %s owns no slot at 3 hulls over 4 slots — cannot express a settled fleet", hull)
		}
		standingAt[hull] = slot
	}
	standingAt["TORWINDSTG-E"] = newIdles

	travelled := 0.0
	for _, hull := range grown {
		slot, owns := AssignedSlot(hull, grown, slots)
		if !owns {
			t.Errorf("%s owns no slot at 4 hulls over 4 slots — every hull must be placed", hull)
			continue
		}
		from, known := positionX[standingAt[hull]]
		to, targetKnown := positionX[slot]
		if !known || !targetKnown {
			t.Fatalf("fixture gap: no position for %q -> %q (the geometry cannot price this assignment)", standingAt[hull], slot)
		}
		travelled += math.Abs(to - from)
	}

	if travelled != 62 {
		t.Errorf("adding one hull cost %.1fu of homing travel, want 62.0u — only the new hull's own leg to the slot that opened for it. "+
			"A re-permutation costs 1360u here and cost 811u live (sp-lkuh9)", travelled)
	}
}

// KNOWN AND UNAVOIDABLE LIMIT, recorded so it is never mistaken for the bug above: a hull joining
// with a LOWER symbol than an incumbent shifts every hull at or after it one slot down the
// priority list. No PURE function of the roster SET can avoid this — "which hull is new" is not in
// the input, so the only way to hand the newcomer the newly-opened slot is for it to sort last.
// It always does in practice (ship symbols increment, so a purchase appends), which is exactly the
// case the test above pins. A mid-list insert is a `fleet add` of an older hull, not a buy.
func TestAssignedSlot_LowerSymbolJoinerShiftsTheZip_KnownLimit(t *testing.T) {
	slots := []string{"X1-P1", "X1-P2", "X1-P3"}
	before := []string{"H-5", "H-9"}
	after := []string{"H-1", "H-5", "H-9"} // H-1 joins BELOW both incumbents

	firstBefore, _ := AssignedSlot("H-5", before, slots)
	firstAfter, _ := AssignedSlot("H-5", after, slots)
	if firstBefore != "X1-P1" || firstAfter != "X1-P2" {
		t.Errorf("H-5 went %s -> %s across a lower-symbol join, want X1-P1 -> X1-P2 (a one-slot shift down the priority list)",
			firstBefore, firstAfter)
	}
	if joiner, owns := AssignedSlot("H-1", after, slots); !owns || joiner != "X1-P1" {
		t.Errorf("the lower-symbol joiner took %q (owns=%v), want the TOP priority slot X1-P1 — the zip is by symbol rank", joiner, owns)
	}
}

// THE LOAD-BEARING PROOF: N delivery hulls own N DISTINCT fixed slots — one per
// waypoint, never piled. hull[i] (symbol order) owns slot[i] (PLACEMENT PRIORITY order), a pure
// zip: no demand, no occupancy, no live position. This is the fixed-placement replacement for the
// runtime distributor whose concurrent-homing timing piled idle hulls on the top-demand hub.
func TestAssignedSlot_NDeliveryHullsOwnNDistinctSlots(t *testing.T) {
	fleet := []string{"H-3", "H-1", "H-2"} // supplied out of symbol order
	slots := []string{"X1-K83", "X1-E43", "X1-H52"}

	// symbol-sorted fleet [H-1,H-2,H-3] zips onto the slots AS GIVEN — priority order, never
	// re-sorted (sp-lkuh9), so the retained set stays a prefix and growth relocates nobody.
	cases := map[string]string{"H-1": "X1-K83", "H-2": "X1-E43", "H-3": "X1-H52"}
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

// Deterministic / restart-idempotent (#2): identical inputs yield an identical slot every call,
// and the ROSTER's read order never matters (it is symbol-sorted) — so the assignment is
// byte-identical across restarts and a second pass moves no hull.
//
// The SLOT list's order, by contrast, IS significant and must be: it is the placement priority,
// and honouring it as given is what keeps the retained set a growing prefix (sp-lkuh9). The
// second half pins that directly — it is the assertion a re-sorting implementation cannot pass,
// so the eviction bug cannot be reintroduced silently. The caller owes a DETERMINISTIC priority
// order in exchange (contractscaler.TopDeliverySlots is one: fixed anchor tuple, then a
// demand-ranked fill with a symbol tiebreak).
func TestAssignedSlot_DeterministicAcrossCallsAndRosterOrder(t *testing.T) {
	slots := []string{"X1-C", "X1-A", "X1-B"} // priority order, NOT symbol order
	fleet := []string{"H-2", "H-1", "H-3"}

	first, ok := AssignedSlot("H-2", fleet, slots)
	if !ok {
		t.Fatalf("AssignedSlot(H-2) ok=false, want a slot")
	}
	for i := 0; i < 20; i++ {
		got, ok := AssignedSlot("H-2", []string{"H-3", "H-1", "H-2"}, slots)
		if !ok || got != first {
			t.Errorf("roster read order changed the assignment on run %d: got %q (ok=%v), want %q", i, got, ok, first)
		}
	}
	if first != "X1-A" {
		t.Errorf("H-2 (2nd of 3 by symbol) = %q, want X1-A — the 2nd slot in PRIORITY order, not the 2nd alphabetically (X1-B)", first)
	}

	// Same slot SET, different priority: the assignment MUST follow the new priority.
	repriorit, ok := AssignedSlot("H-2", fleet, []string{"X1-A", "X1-B", "X1-C"})
	if !ok || repriorit != "X1-B" {
		t.Errorf("re-prioritised slots gave H-2 %q (ok=%v), want X1-B — slot ORDER is the placement priority and must be honoured, never re-sorted", repriorit, ok)
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

// TRUNCATION DROPS FROM THE END: with fewer hulls than slots the caller's PLACEMENT PRIORITY
// order decides which slots go unused — the tail. The slot set arrives era-invariant-anchors
// first ((1) H-stack, (2) far sink, (3) far source base, (4) E-stack, then the demand-ranked
// central fill), so two hulls park on the H-stack and the far sink. The alphabetical symbol-zip
// alone would have taken the two lowest SYMBOLS (here the central fill) and stranded both
// high-value anchors — the placement gain the whole set exists for.
func TestAssignedSlot_FewerHullsThanSlotsDropsTheLowestPrioritySlots(t *testing.T) {
	// Priority order deliberately anti-correlated with symbol order.
	slots := []string{"X1-Z-HSTACK", "X1-Y-FARSINK", "X1-X-FARBASE", "X1-A-CENTRAL"}

	two := []string{"H-1", "H-2"}
	got := map[string]bool{}
	for _, hull := range two {
		slot, ok := AssignedSlot(hull, two, slots)
		if !ok {
			t.Fatalf("AssignedSlot(%s) ok=false, want one of the two priority slots", hull)
		}
		got[slot] = true
	}
	if !got["X1-Z-HSTACK"] || !got["X1-Y-FARSINK"] || len(got) != 2 {
		t.Fatalf("2 hulls over 4 slots occupied %v, want the two HIGHEST-PRIORITY slots (H-stack, far sink)", got)
	}

	three := []string{"H-1", "H-2", "H-3"}
	got = map[string]bool{}
	for _, hull := range three {
		slot, ok := AssignedSlot(hull, three, slots)
		if !ok {
			t.Fatalf("AssignedSlot(%s) ok=false, want one of the three priority slots", hull)
		}
		got[slot] = true
	}
	if got["X1-A-CENTRAL"] || len(got) != 3 {
		t.Fatalf("3 hulls over 4 slots occupied %v, want the first three priority slots (the 4th dropped)", got)
	}
}

// The truncation is a NO-OP whenever the fleet is at least as large as the slot set — the whole
// list is retained, still in PRIORITY order. That is the deliberate reconciliation of the two
// zip regimes (sp-lkuh9): the slots are never re-sorted on EITHER side of the fleet==slots
// boundary. Sorting only where nothing is truncated would just move the eviction to that boundary
// — growing from slots-1 to slots hulls would re-sort the set and permute everyone.
//
// Duplicate slot symbols collapse before the count, so a repeated entry never eats a priority slot.
func TestAssignedSlot_TruncationIsANoOpWhenTheFleetCoversTheSlots(t *testing.T) {
	slots := []string{"X1-Z-HSTACK", "X1-Y-FARSINK", "X1-A-CENTRAL"}
	fleet := []string{"H-1", "H-2", "H-3"}

	for hull, want := range map[string]string{"H-1": "X1-Z-HSTACK", "H-2": "X1-Y-FARSINK", "H-3": "X1-A-CENTRAL"} {
		if got, ok := AssignedSlot(hull, fleet, slots); !ok || got != want {
			t.Errorf("AssignedSlot(%s) = %q,%v, want %q (priority zip, no truncation)", hull, got, ok, want)
		}
	}

	// Two hulls, and a slot set whose 3 entries are only 2 DISTINCT symbols → nothing truncated.
	dupSlots := []string{"X1-Z-HSTACK", "X1-Y-FARSINK", "X1-Z-HSTACK"}
	pair := []string{"H-1", "H-2"}
	if got, ok := AssignedSlot("H-1", pair, dupSlots); !ok || got != "X1-Z-HSTACK" {
		t.Errorf("AssignedSlot(H-1) = %q,%v, want X1-Z-HSTACK (duplicates collapse before the count)", got, ok)
	}
	if got, ok := AssignedSlot("H-2", pair, dupSlots); !ok || got != "X1-Y-FARSINK" {
		t.Errorf("AssignedSlot(H-2) = %q,%v, want X1-Y-FARSINK (duplicates collapse before the count)", got, ok)
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
