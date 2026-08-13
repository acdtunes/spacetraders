package commands

import "testing"

// A retiring hull is being withdrawn, so it belongs to nobody's next job — least of all a
// relocation, which exists to put a hull on new ground to trade from. Flying a hull that is
// about to be scrapped spends the API budget a trading hull would have used, and it lands
// the drained hull somewhere the operator was not expecting to find it.
//
// The exclusion goes in hullProtected, the ONE ownership definition read at BOTH the
// scoring gate and the actuation re-check, so a retirement honoured at scoring cannot be
// licensed and then abandoned at commit.
func TestOpportunityRelocatorShould_NeverRelocateARetiringHull(t *testing.T) {
	h := newRelocHarness(t)
	h.fleet.hulls = []RelocatorHull{
		{ShipSymbol: "HAULER-RETIRED", CurrentSystem: "X1-HOME", Retiring: true},
		{ShipSymbol: "HAULER-B", CurrentSystem: "X1-HOME"},
	}

	result := h.reconcile(t)

	for _, moved := range h.actuator.movedShips() {
		if moved == "HAULER-RETIRED" {
			t.Fatalf("a retiring hull was relocated; it is being withdrawn, not repositioned to trade")
		}
	}
	if result.Skipped["retiring_hull_protected"] != 1 {
		t.Fatalf("the retirement must be reported as an ownership exclusion, not silently dropped; skips %v", result.Skipped)
	}
}
