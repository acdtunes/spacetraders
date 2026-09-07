package parkedsensing

// expansion_reserve_pool_test.go covers the charting crew's side of the RESERVE
// pool: hull-keyed probes we own that hold no placement. Ten hulls standing at one
// yard — the ordinary shape of a bulk buy — cannot all be placement rows, and the
// whole point of recording them is that a crew can draw on them.

import (
	"errors"
	"testing"
)

// A RESERVE IS CLAIMABLE, which is the reason it is recorded at all.
func TestAdvanceExpansion_ReserveHullIsClaimedAsASeedWithoutBuying(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-A", Verdict: VerdictInScope},
		{System: "X1-B", Verdict: VerdictPending, UnchartedCount: 3},
	}
	h.gates.adjacency = map[string][]string{"X1-A": {"X1-B"}}
	h.yards.bySystem = map[string][]string{"X1-A": {"X1-A-YARD"}}
	h.ledger.spareHulls = []SpareHull{{Ship: "PROBE-7", Waypoint: "X1-A-YARD", System: "X1-A"}}

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rep.SeedsClaimed != 1 {
		t.Fatalf("SeedsClaimed = %d, want 1 — a reserve hull is exactly the supply a crew draws on", rep.SeedsClaimed)
	}
	if len(h.ledger.setSeeds) != 1 || h.ledger.setSeeds[0] != (setSeedCall{"X1-B", "PROBE-7", SeedStateDispatched}) {
		t.Fatalf("seed writes = %v, want one DISPATCHED mission for PROBE-7 on X1-B", h.ledger.setSeeds)
	}
	if len(h.ledger.upsertedSlots) != 0 {
		t.Fatalf("claiming a reserve must never order another probe, got %v", h.ledger.upsertedSlots)
	}
}

// THE RELEASE IS BY HULL, AND THAT IS THE MONEY GUARD (RULINGS #4).
//
// MUTANT THIS KILLS: releasing a claimed reserve through DeleteSlot, addressed by
// (waypoint, kind). Two reserves stand at one yard here, so a waypoint-wide release
// takes the sibling with it and that hull is left named by no row: invisible to
// CountOwnedProbes, and re-bought.
func TestAdvanceExpansion_ClaimingOneReserveLeavesItsCoLocatedSiblingOnTheBooks(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-A", Verdict: VerdictInScope},
		{System: "X1-B", Verdict: VerdictPending, UnchartedCount: 3},
	}
	h.gates.adjacency = map[string][]string{"X1-A": {"X1-B"}}
	h.ledger.spareHulls = []SpareHull{
		{Ship: "PROBE-7", Waypoint: "X1-A-YARD", System: "X1-A"},
		{Ship: "PROBE-8", Waypoint: "X1-A-YARD", System: "X1-A"}, // same waypoint
	}

	if _, err := h.run(t, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(h.ledger.deletedSpares) != 1 || h.ledger.deletedSpares[0] != "PROBE-7" {
		t.Fatalf("reserve releases = %v, want exactly the claimed hull, addressed by hull", h.ledger.deletedSpares)
	}
	if len(h.ledger.deleted) != 0 {
		t.Fatalf("a reserve must never be released through the PLACEMENT delete, got %v", h.ledger.deleted)
	}
	if len(h.ledger.spareHulls) != 1 || h.ledger.spareHulls[0].Ship != "PROBE-8" {
		t.Fatalf("remaining reserves = %v, want PROBE-8 still on the books", h.ledger.spareHulls)
	}
}

// A RESERVE DOES NOT SHADOW THE YARD IT STANDS ON.
//
// MUTANT THIS KILLS: writing reserves into the slot book's occupancy map. Seed
// staging refuses a yard already carrying a SPARE, so adopted hulls would lock
// their yards out of staging and strangle the expansion this exists to feed.
func TestSlotBook_AReserveDoesNotOccupyTheWaypointItStandsOn(t *testing.T) {
	book := newSlotBook(
		[]QueuedSlot{{
			Waypoint: "X1-A-YARD", System: "X1-A", Kind: SlotKindMarket,
			State: SlotStateParked, AssignedShip: "PROBE-WATCHER",
		}},
		[]SpareHull{
			{Ship: "PROBE-7", Waypoint: "X1-A-YARD", System: "X1-A"},
			{Ship: "PROBE-8", Waypoint: "X1-A-YARD", System: "X1-A"},
		},
		nil,
	)

	if book.occupied("X1-A-YARD", SlotKindSpare) {
		t.Fatal("a reserve is a hull standing somewhere, not a claim on the waypoint — " +
			"holding the yard here is what would lock every adopted yard out of seed staging")
	}
	if !book.occupied("X1-A-YARD", SlotKindMarket) {
		t.Fatal("and the MARKET placement standing at the same symbol is untouched")
	}
	if len(book.parkedSpares) != 2 {
		t.Fatalf("parkedSpares = %v, want both reserves claimable", book.parkedSpares)
	}
	for _, spare := range book.parkedSpares {
		if !spare.Reserve {
			t.Fatalf("%s must be marked Reserve, or its release is routed to the placement delete", spare.AssignedShip)
		}
	}
}

// A RESERVE NAMING A HULL ALREADY OUT CHARTING IS NOT CLAIMABLE: claiming it
// stamps a SECOND mission on a probe that can only fly one (RULINGS #3).
func TestSlotBook_AReserveWhoseHullIsAlreadyCharting_IsNotClaimable(t *testing.T) {
	book := newSlotBook(nil,
		[]SpareHull{{Ship: "PROBE-7", Waypoint: "X1-A-YARD", System: "X1-A"}},
		map[string]bool{"PROBE-7": true},
	)

	if len(book.parkedSpares) != 0 {
		t.Fatalf("parkedSpares = %v, want none — PROBE-7 is already on an errand", book.parkedSpares)
	}
	if len(book.errandSpares) != 1 {
		t.Fatalf("errandSpares = %v, want the ghost reserve routed to the releaser", book.errandSpares)
	}
}

// AN UNREADABLE POOL IS NOT AN EMPTY ONE (RULINGS #4): read permissively, the tick
// requests a purchase for a target a hull we already own could have covered.
func TestAdvanceExpansion_AnUnreadableReservePoolFailsTheTick(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-A", Verdict: VerdictInScope},
		{System: "X1-B", Verdict: VerdictPending, UnchartedCount: 3},
	}
	h.gates.adjacency = map[string][]string{"X1-A": {"X1-B"}}
	h.yards.bySystem = map[string][]string{"X1-A": {"X1-A-YARD"}}
	h.ledger.spareHullsErr = errors.New("ledger unavailable")

	if _, err := h.run(t, nil); err == nil {
		t.Fatal("an unreadable reserve pool must fail the tick, not read as a fleet with no spares")
	}
	if len(h.ledger.upsertedSlots) != 0 {
		t.Fatalf("nothing may be ordered on a tick that cannot see what we already own, got %v", h.ledger.upsertedSlots)
	}
}
