package contract

import "sort"

// AssignedSlot returns the FIXED home slot a delivery hull owns under the design's
// one-hull-per-waypoint placement: the delivery fleet is put in a stable symbol order, the
// placement slots are kept in the caller's PLACEMENT PRIORITY order, and hull[i] permanently
// owns slot[i]. It is a PURE, deterministic function of the roster + the ordered slot list —
// NO demand ranking, NO occupancy, NO live position — so it is byte-identical across restarts,
// and a second homing pass moves no hull (a hull already at its slot stays put). Distributing by
// live demand/occupancy instead loses that: concurrent homing races on the same reading and
// piles idle hulls on the top-demand hub.
//
// SLOT ORDER IS LOAD-BEARING, AND THE SLOTS ARE NEVER RE-SORTED (sp-lkuh9). The retained set is a
// PREFIX of the priority list, so growing the roster only ever APPENDS a slot: every incumbent
// keeps its index, keeps its slot, and stays put, while the newcomer takes the slot that opened.
// Re-sorting the retained slots instead made the retained SET depend on fleet size, so a 4th hull
// re-sorted a different set and permuted everyone — live, one purchase evicted a hull that had
// just finished homing and sent the newcomer 711u to a slot 649u past the one that opened for it.
// This is why the caller must supply a DETERMINISTIC priority order (TopDeliverySlots is: fixed
// anchor tuple, then demand-ranked central fill with a symbol tiebreak) — an unstable order here
// now permutes assignments where the old re-sort would have masked it.
//
// The stability guarantee is under APPEND of a higher symbol, which is what buying a hull does
// (ship symbols increment). A hull joining with a LOWER symbol shifts the hulls at and after it
// one slot down; no pure function of the roster SET can avoid that, since "which hull is new" is
// not in the input. See TestAssignedSlot_LowerSymbolJoinerShiftsTheZip_KnownLimit.
//
// A hull BEYOND the number of slots (surplus over the delivery knee, e.g. an 8th hull when
// the plan caps delivery at 6) owns NO slot (ok=false); the scaler re-roles that surplus to
// a warehouse rather than the homing piling it onto an occupied slot. deliveryFleet need not
// include shipSymbol (it is added), and duplicate symbols in either list are collapsed.
//
// When the fleet is SMALLER than the slot set, the slot order also decides which slots go unused:
// the low-priority tail is dropped before the zip — see truncateToFleet.
func AssignedSlot(shipSymbol string, deliveryFleet []string, slots []string) (string, bool) {
	orderedFleet := distinctSorted(append([]string{shipSymbol}, deliveryFleet...))
	// PRIORITY ORDER, NOT SORTED: sorting here is the sp-lkuh9 eviction bug.
	orderedSlots := truncateToFleet(slots, len(orderedFleet))
	if len(orderedSlots) == 0 {
		return "", false
	}
	for index, symbol := range orderedFleet {
		if symbol != shipSymbol {
			continue
		}
		if index < len(orderedSlots) {
			return orderedSlots[index], true
		}
		return "", false // surplus beyond the delivery knee — owns no slot
	}
	return "", false // unreachable: shipSymbol is always present in orderedFleet
}

// truncateToFleet drops the LOW-PRIORITY TAIL of the slot set when there are fewer hulls than
// slots. The caller supplies the slots in PLACEMENT PRIORITY order — the era-invariant anchors
// first, (1) H-stack, (2) far sink, (3) far source base, (4) E-stack, then the demand-ranked
// central fill — so three hulls drop the 4th slot and two drop the 3rd and 4th.
//
// Without it a symbol-sorted slot set would keep the ALPHABETICALLY-first slots instead, which on
// a real era silently strands the two highest-value anchors (era 5: four hulls over six slots
// would take A1/B7/D40/E42 and never park the H-stack or the far sink at all).
//
// It returns a PREFIX of the priority list and the caller does not re-sort it, so the retained set
// only ever grows at the tail as hulls are added — the property that keeps parked hulls parked
// (sp-lkuh9). A no-op whenever the fleet already covers the slots: the whole list is retained,
// still in priority order.
//
// HONEST NOTE ON ITS PRESENT WEIGHT: once the re-sort is gone, this slicing no longer changes any
// RESULT. The zip is positional and a hull's index is always below the fleet size, so indexing the
// full priority list and indexing its fleet-length prefix are the same answer in every case,
// including the surplus branch. A mutation probe replacing this call with plain distinct(slots)
// killed ZERO tests — by design, not for want of coverage. It is kept because it states which
// slots go unused where the reader looks for it, and it keeps that answer right if the indexing is
// ever changed. What it must NOT be read as is optional in the presence of a sort: sp-9suun added
// it precisely because a SORTED slot set keeps the alphabetically-first slots, and truncating by
// priority BEFORE that sort was the only thing saving the anchors. Sort and truncation go together
// or neither goes.
func truncateToFleet(slots []string, fleetSize int) []string {
	prioritised := distinct(slots)
	if len(prioritised) <= fleetSize {
		return prioritised
	}
	return prioritised[:fleetSize]
}

// distinct returns the input symbols deduplicated, first occurrence wins, order preserved — the
// caller's priority order survives so truncateToFleet can cut from the true tail.
func distinct(symbols []string) []string {
	seen := make(map[string]struct{}, len(symbols))
	out := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		if _, dup := seen[symbol]; dup {
			continue
		}
		seen[symbol] = struct{}{}
		out = append(out, symbol)
	}
	return out
}

// distinctSorted returns the input symbols deduplicated and sorted ascending — the stable total
// order the FLEET ROSTER is zipped in (the slots keep their priority order), so which hull owns
// which of the retained slots never depends on the order the roster was read in.
func distinctSorted(symbols []string) []string {
	out := distinct(symbols)
	sort.Strings(out)
	return out
}
