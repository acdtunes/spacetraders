package parkedsensing

import (
	"errors"
	"testing"
	"time"
)

// expansion_supply_evidence_test.go pins that a want the buy queue can never fund does not count as
// SUPPLY and so cannot suppress its own replacement.
//
// THE LOOP IT CLOSES. takeSupplyFor asks "is a seed for this target already in the pipeline?" and
// answers it, for a want with no hull yet, by asking whether the yard is STAFFED. That was the whole
// definition of fundable when it was written. Staging has since learned a second condition — the
// yard must also not be known to sell no probe — and takeSupplyFor is the third caller of that rule
// and did not get it. So a want at a staffed-but-probe-less yard reads as a seed on order, its
// target is skipped, and the row that blocks the target is the row that can never be filled.
//
// IT IS REACHED THROUGH THE TRAIT PASS, not through a mistake. Staging deliberately still stages at
// NEVER-PRICED yards — that is how the fleet learns where probes are sold. The buy queue then scans
// one, finds no probe, and the memo records it. From that moment the want sits at a staffed,
// probe-less yard: exactly the shape below.
//
// A WANT THAT NAMES A HULL IS STILL SUPPLY, unconditionally, and that is a money guard rather than
// an oversight — the hull is already bought, and re-testing fundability on it would order a second.

// supplyWorld: one target, and one already-staged SPARE want at a STAFFED yard several hops away.
// The want has no hull. Whether it counts as supply is the only thing deciding if the target is
// re-staged, so the fixture isolates exactly that.
func supplyWorld() (*expandHarness, *fakeListingMemo) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-YARD", Verdict: VerdictInScope},
		{System: "X1-GOOD", Verdict: VerdictInScope},
		{System: "X1-TARGET", Verdict: VerdictPending, UnchartedCount: 9},
	}
	h.gates.adjacency = map[string][]string{
		"X1-YARD":   {"X1-TARGET"},
		"X1-GOOD":   {"X1-TARGET"},
		"X1-TARGET": {},
	}
	h.yards.bySystem = map[string][]string{
		"X1-YARD": {"X1-YARD-A"},
		"X1-GOOD": {"X1-GOOD-A"},
	}
	h.ledger.slots = []QueuedSlot{
		staffedYardRow("X1-YARD", "X1-YARD-A"),
		staffedYardRow("X1-GOOD", "X1-GOOD-A"),
		// The stale want: staffed yard, NO hull behind it.
		{Waypoint: "X1-YARD-A", System: "X1-YARD", Kind: SlotKindSpare, State: SlotStateWanted},
	}
	memo := &fakeListingMemo{
		// X1-YARD-A has been scanned and sells NO probe. X1-GOOD-A is evidenced.
		sells:     map[string]bool{"X1-YARD-A": false, "X1-GOOD-A": true},
		scannedAt: map[string]time.Time{"X1-YARD-A": time.Now(), "X1-GOOD-A": time.Now()},
	}
	return h, memo
}

// THE TEST THAT MATTERS. A want at a staffed yard the memo says sells no probe is NOT supply, so the
// target is re-staged — onto the evidenced yard.
func TestAdvanceExpansion_AWantAtAProbelessYardIsNotSupplyAndDoesNotBlockRestaging(t *testing.T) {
	h, memo := supplyWorld()

	rep, err := h.runWithMemo(t, memo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsRequested != 1 {
		t.Fatalf("SeedsRequested = %d, want 1 — the outstanding want sits at a staffed yard that sells "+
			"no probe, so the buy queue can never fund it; counting it as a seed on order makes the row "+
			"block its own replacement", rep.SeedsRequested)
	}
	if got := h.ledger.upsertedSlots[0].Waypoint; got != "X1-GOOD-A" {
		t.Fatalf("re-staged at %s, want X1-GOOD-A (the evidenced yard)", got)
	}
}

// A WANT AT AN EVIDENCED YARD IS STILL SUPPLY. The rule removes unfundable wants from the supply
// count; it must not stop a genuine outstanding request from suppressing a duplicate.
func TestAdvanceExpansion_AWantAtAnEvidencedYardStillCountsAsSupply(t *testing.T) {
	h, memo := supplyWorld()
	// The outstanding want now sits at the EVIDENCED yard instead.
	h.ledger.slots[2] = QueuedSlot{Waypoint: "X1-GOOD-A", System: "X1-GOOD", Kind: SlotKindSpare, State: SlotStateWanted}

	rep, err := h.runWithMemo(t, memo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsRequested != 0 {
		t.Fatalf("SeedsRequested = %d, want 0 — a want at an evidenced, staffed yard is a probe genuinely "+
			"on order, and ordering a second would buy two for one target", rep.SeedsRequested)
	}
}

// A WANT THAT NAMES A HULL IS SUPPLY WHATEVER THE YARD SAYS. The hull is already bought; re-testing
// fundability on it would order a second (RULINGS #4 — the doubt resolves toward NOT spending).
func TestAdvanceExpansion_AWantNamingAHullIsSupplyEvenAtAProbelessYard(t *testing.T) {
	h, memo := supplyWorld()
	h.ledger.slots[2] = QueuedSlot{
		Waypoint: "X1-YARD-A", System: "X1-YARD", Kind: SlotKindSpare,
		State: SlotStateBought, AssignedShip: "PROBE-BOUGHT",
	}

	rep, err := h.runWithMemo(t, memo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsRequested != 0 {
		t.Fatalf("SeedsRequested = %d, want 0 — PROBE-BOUGHT is already paid for and on its way; the "+
			"yard's stock is irrelevant once a hull exists, and asking would buy a second", rep.SeedsRequested)
	}
}

// A NEVER-PRICED yard is still supply: it may yet sell probes, and the buy queue will find out. Only
// a yard we have positively read as probe-less is discounted.
func TestAdvanceExpansion_AWantAtANeverPricedYardIsStillSupply(t *testing.T) {
	h, memo := supplyWorld()
	delete(memo.sells, "X1-YARD-A")
	delete(memo.scannedAt, "X1-YARD-A") // never read

	rep, err := h.runWithMemo(t, memo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsRequested != 0 {
		t.Fatalf("SeedsRequested = %d, want 0 — the yard has never been priced, so the queue will scan "+
			"it and may well buy there; discounting it would order a duplicate on a guess", rep.SeedsRequested)
	}
}

// AN UNREADABLE MEMO FAILS THE TICK HERE TOO, rather than defaulting the want to supply.
//
// Swallowing it is the silent direction and it is self-concealing: the want is treated as a seed on
// order, the target is skipped, staging is never reached — so the error never surfaces anywhere and
// the target simply stops being served. The adversarial fake returns a usable-looking answer
// alongside the error precisely so a swallowed error produces a plausible result rather than an
// obvious one.
func TestAdvanceExpansion_AnUnreadableMemoFailsTheTickRatherThanAssumingSupply(t *testing.T) {
	h, memo := supplyWorld()
	memo.err = errors.New("shipyard inventory unhappy")

	_, err := h.runWithMemo(t, memo)
	if err == nil {
		t.Fatalf("the tick succeeded on an unreadable memo — the outstanding want would be counted as " +
			"a seed on order without ever having been read, and its target would stop being served " +
			"with nothing logged")
	}
}
