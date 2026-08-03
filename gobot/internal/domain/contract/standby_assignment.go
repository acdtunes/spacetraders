package contract

import "sort"

// AssignedSlot returns the FIXED home slot a delivery hull owns under the design's
// one-hull-per-waypoint placement: the delivery fleet and the
// placement slots are each put in a stable symbol order, and hull[i] permanently owns
// slot[i]. It is a PURE, deterministic function of the roster + the slot set — NO demand
// ranking, NO occupancy, NO live position — so it is byte-identical across restarts, and a
// second homing pass moves no hull (a hull already at its slot stays put). Distributing by
// live demand/occupancy instead loses that: concurrent homing races on the same reading and
// piles idle hulls on the top-demand hub.
//
// A hull BEYOND the number of slots (surplus over the delivery knee, e.g. an 8th hull when
// the plan caps delivery at 6) owns NO slot (ok=false); the scaler re-roles that surplus to
// a warehouse rather than the homing piling it onto an occupied slot. deliveryFleet need not
// include shipSymbol (it is added), and duplicate symbols in either list are collapsed.
//
// When the fleet is SMALLER than the slot set, the caller's slot ORDER decides which slots go
// unused: the set arrives in PLACEMENT PRIORITY (contractscaler.TopDeliverySlots) and the tail
// is dropped before the zip — see truncateToFleet.
func AssignedSlot(shipSymbol string, deliveryFleet []string, slots []string) (string, bool) {
	orderedFleet := distinctSorted(append([]string{shipSymbol}, deliveryFleet...))
	orderedSlots := distinctSorted(truncateToFleet(slots, len(orderedFleet)))
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
// central fill (sp-9suun) — so three hulls drop the 4th slot and two drop the 3rd and 4th.
//
// Without it the symbol-zip below would keep the ALPHABETICALLY-first slots instead, which on a
// real era silently strands the two highest-value anchors (era 5: four hulls over six slots
// would take A1/B7/D40/E42 and never park the H-stack or the far sink at all). The zip itself is
// unchanged, and so is every case where the fleet is at least as large as the slot set.
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

// distinctSorted returns the input symbols deduplicated and sorted ascending — the stable
// total order both the fleet roster and the retained slots are zipped in, so which hull owns
// which of the retained slots never depends on input order.
func distinctSorted(symbols []string) []string {
	out := distinct(symbols)
	sort.Strings(out)
	return out
}
