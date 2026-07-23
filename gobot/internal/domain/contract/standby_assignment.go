package contract

import "sort"

// AssignedSlot returns the FIXED home slot a delivery hull owns under the design's
// one-hull-per-waypoint placement (sp-9le3x / sp-mtgje): the delivery fleet and the
// placement slots are each put in a stable symbol order, and hull[i] permanently owns
// slot[i]. It is a PURE, deterministic function of the roster + the slot set — NO demand
// ranking, NO occupancy, NO live position — so it is byte-identical across restarts, and a
// second homing pass moves no hull (a hull already at its slot stays put). This replaces the
// runtime demand/occupancy distributor whose concurrent-homing timing piled idle hulls on
// the top-demand hub.
//
// A hull BEYOND the number of slots (surplus over the delivery knee, e.g. an 8th hull when
// the plan caps delivery at 6) owns NO slot (ok=false); the scaler re-roles that surplus to
// a warehouse rather than the homing piling it onto an occupied slot. deliveryFleet need not
// include shipSymbol (it is added), and duplicate symbols in either list are collapsed.
func AssignedSlot(shipSymbol string, deliveryFleet []string, slots []string) (string, bool) {
	orderedSlots := distinctSorted(slots)
	if len(orderedSlots) == 0 {
		return "", false
	}
	orderedFleet := distinctSorted(append([]string{shipSymbol}, deliveryFleet...))
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

// distinctSorted returns the input symbols deduplicated and sorted ascending — the stable
// total order both the fleet roster and the slot set are zipped in, so the assignment never
// depends on input order.
func distinctSorted(symbols []string) []string {
	seen := make(map[string]struct{}, len(symbols))
	out := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		if _, dup := seen[symbol]; dup {
			continue
		}
		seen[symbol] = struct{}{}
		out = append(out, symbol)
	}
	sort.Strings(out)
	return out
}
