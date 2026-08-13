package parkedsensing

// coverageshare.go holds a share of the FILL tier's own attempts for the best
// placement in a system the fleet has never entered, so a deep backlog of
// already-held systems cannot indefinitely starve every other one. It answers
// to BuyKnobs.CoverageReserve and mirrors seedshare.go one tier down: a split
// of an existing budget, never an extension of it.

// coverageReserveActive reports whether any candidate is an ordinary market
// fill in a system the fleet has never entered — the case the reserve exists
// to reach. No target, no reserve, same as fillAttemptBudget's seed check.
func coverageReserveActive(candidates []QueuedSlot, covered map[string]int, yards yardOrder) bool {
	for _, slot := range candidates {
		if slot.Kind == SlotKindSpare || yards.wants(slot.Waypoint) {
			continue
		}
		if covered[slot.System] == 0 {
			return true
		}
	}
	return false
}

// coverageFillBudget is how many of the fill budget's attempts an
// already-held, ordinary placement may spend before standing aside for the
// reserve.
func coverageFillBudget(fillBudget, reserve int, active bool) int {
	if !active || reserve <= 0 {
		return fillBudget
	}
	if budget := fillBudget - reserve; budget > 0 {
		return budget
	}
	return 0
}

// yieldsToCoverageReserve reports whether an already-held, ordinary fill must
// stand aside for the reserve behind it. A dark yard is exempt — that tier
// outranks everything, reserve or not — and a seed is not a fill at all.
func yieldsToCoverageReserve(slot QueuedSlot, covered map[string]int, yards yardOrder, attempts, coverageBudget int) bool {
	if slot.Kind == SlotKindSpare || yards.wants(slot.Waypoint) {
		return false
	}
	return covered[slot.System] > 0 && attempts >= coverageBudget
}
