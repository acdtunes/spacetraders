package parkedsensing

// An errand whose target the router cannot reach from where the hull stands used to be
// held and retried FOREVER: the hull held a probe-cap slot, spent its seed step every
// tick, charted nothing, and was invisible to claimSpares because newSlotBook's
// onErrand filter keeps a hull with an errand out of parkedSpares (sp-c0oyu).
//
// NOT DRIFT BETWEEN THE TWO SEARCHES — they read the same store, filter and unbounded
// declaration; what differs is WHERE each is asked from. The stored graph is one-way in
// places, so a hull can arrive somewhere we know how to enter and not how to leave.

import (
	"errors"
	"fmt"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// unroutable is the resolver's refusal, wrapped as the adapter wraps it, so a sentinel
// that stopped being published fails these tests rather than restoring the hold.
func unroutable(from, to string) error {
	return fmt.Errorf("failed to name the next system bound for %s: %w: %w",
		to, ErrSeedWalkUnroutable, fmt.Errorf("no stored gate route from %s to %s", from, to))
}

// strandedHarness is one probe on an errand it cannot walk.
func strandedHarness() *expandHarness {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-A", Verdict: VerdictInScope},
		{System: "X1-B", Verdict: VerdictPending, UnchartedCount: 3,
			SeedShip: "PROBE-7", SeedState: SeedStateDispatched},
	}
	// No stored exits: the hull came over an edge charted from the other end.
	h.gates.adjacency = map[string][]string{}
	h.ships.positions = map[string]ShipPos{
		"PROBE-7": {Waypoint: "X1-A-YARD", NavStatus: navigation.NavStatusDocked, Found: true},
	}
	h.seed.jumpErr = unroutable("X1-A", "X1-B")
	return h
}

func TestAdvanceExpansion_UnroutableErrandIsEndedInsteadOfHeldForever(t *testing.T) {
	h := strandedHarness()

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rep.SeedsStranded != 1 {
		t.Fatalf("SeedsStranded = %d, want 1 — an errand nothing can walk must be counted, not silently re-asked", rep.SeedsStranded)
	}
	if len(h.ledger.setSeeds) != 1 || h.ledger.setSeeds[0] != (setSeedCall{"X1-B", "", ""}) {
		t.Fatalf("seed writes = %v, want the errand on X1-B cleared", h.ledger.setSeeds)
	}
	if got, ok := h.ledger.slotAt("X1-A-YARD", SlotKindSpare); !ok ||
		got.State != SlotStateParked || got.AssignedShip != "PROBE-7" {
		t.Fatalf("spare row = %+v (found=%v), want PROBE-7 parked where it stands — a hull named by "+
			"no row at all drops out of the probe-cap count and authorises buying its replacement", got, ok)
	}
}

// THE WRITE ORDER IS THE MONEY GUARD (RULINGS #4). The placement is written FIRST, so a
// failure between the two writes names the hull twice — an over-count, which buys FEWER
// probes. The other order names it neither time and the cap funds a replacement.
func TestAdvanceExpansion_StrandedSeedIsParkedBeforeItsErrandIsCleared(t *testing.T) {
	h := strandedHarness()
	h.ledger.upsertSlotErr = errors.New("ledger refusing writes")

	if _, err := h.run(t, nil); err == nil {
		t.Fatal("a failed park must surface loudly, not be swallowed")
	}
	if len(h.ledger.setSeeds) != 0 {
		t.Fatalf("the errand was cleared before the hull had a placement row: %v", h.ledger.setSeeds)
	}
}

// The hull is claimable again the moment it is parked, which is what an errand held
// open denied it.
func TestAdvanceExpansion_StrandedHullIsVisibleToClaimSparesAgain(t *testing.T) {
	h := strandedHarness()
	if _, err := h.run(t, nil); err != nil {
		t.Fatalf("tick 1: %v", err)
	}

	// A second frontier system, reachable from where the hull is parked.
	h.ledger.systems = append(h.ledger.systems,
		ExpandSystem{System: "X1-C", Verdict: VerdictPending, UnchartedCount: 5})
	h.gates.adjacency = map[string][]string{"X1-A": {"X1-C"}}
	h.seed.jumpErr = nil

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("tick 2: %v", err)
	}

	if rep.SeedsClaimed != 1 {
		t.Fatalf("SeedsClaimed = %d, want the freed hull claimed for X1-C without buying anything", rep.SeedsClaimed)
	}
	if got := h.ledger.setSeeds; len(got) != 2 || got[1] != (setSeedCall{"X1-C", "PROBE-7", SeedStateDispatched}) {
		t.Fatalf("seed writes = %v, want PROBE-7 sent to X1-C on the tick after it was freed", got)
	}
}

// THE TARGET IS MARKED UNREACHABLE-FOR-NOW AND NOTHING IS WRITTEN DOWN TO SAY SO:
// claimSpares asks the same walker the stand-down asked, from the system the hull is
// now parked in, so a graph that could not carry the errand cannot re-stamp it.
func TestAdvanceExpansion_StrandedTargetIsNotReSelectedForTheSameHull(t *testing.T) {
	h := strandedHarness()
	if _, err := h.run(t, nil); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	writes := len(h.ledger.setSeeds)

	rep, err := h.run(t, nil) // the graph is unchanged: X1-A still has no stored exits
	if err != nil {
		t.Fatalf("tick 2: %v", err)
	}

	if rep.SeedsClaimed != 0 {
		t.Fatalf("SeedsClaimed = %d, want 0 — re-stamping the errand the last tick could not walk "+
			"is the unbounded hold with extra ledger churn", rep.SeedsClaimed)
	}
	if len(h.ledger.setSeeds) != writes {
		t.Fatalf("seed writes = %v, want none past tick 1", h.ledger.setSeeds[writes:])
	}
	if h.seed.countOf("jump") != 1 {
		t.Fatalf("jump commands = %d, want the single one from tick 1 — a parked spare has no errand to step", h.seed.countOf("jump"))
	}
	if _, ok := h.ledger.slotAt("X1-A-YARD", SlotKindSpare); !ok {
		t.Fatal("the spare row must survive the tick that declined to claim it")
	}
}

// THE MARK DECAYS BECAUSE IT IS RE-DERIVED: a gate charted later from the other end
// stores the missing adjacency and the spare is claimed for the target it lost.
func TestAdvanceExpansion_StrandedTargetIsRetriedOnceTheMissingGateIsStored(t *testing.T) {
	h := strandedHarness()
	if _, err := h.run(t, nil); err != nil {
		t.Fatalf("tick 1: %v", err)
	}

	// The hull's own system is charted from here: its exits are stored at last.
	h.gates.adjacency = map[string][]string{"X1-A": {"X1-B"}}
	h.seed.jumpErr = nil

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("tick 2: %v", err)
	}

	if rep.SeedsClaimed != 1 {
		t.Fatalf("SeedsClaimed = %d, want the stranded target retried the moment the graph can carry it", rep.SeedsClaimed)
	}
	if got := h.ledger.setSeeds; len(got) != 2 || got[1] != (setSeedCall{"X1-B", "PROBE-7", SeedStateDispatched}) {
		t.Fatalf("seed writes = %v, want PROBE-7 re-sent to X1-B", got)
	}
	if len(h.ledger.upsertedSlots) != 1 {
		t.Fatalf("upserted slots = %v, want only the stand-down's own row — a retry must never order a second probe",
			h.ledger.upsertedSlots)
	}
}

// The two disagree only about WHEN they read the store: the walker from the neighbour
// map read at the top of the tick, the resolver live. The errand holds one tick.
func TestAdvanceExpansion_UnroutableStepHoldsWhileTheReachWalkerStillSeesARoute(t *testing.T) {
	h := strandedHarness()
	h.gates.adjacency = map[string][]string{"X1-A": {"X1-B"}}

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rep.SeedsStranded != 0 {
		t.Fatalf("SeedsStranded = %d, want 0 — a stand-down selection would undo on the next tick is churn, not a fix", rep.SeedsStranded)
	}
	if rep.Actions != 0 {
		t.Fatalf("Actions = %d, want 0 — the resolver stopped before any API call, so nothing was spent", rep.Actions)
	}
	if len(h.ledger.setSeeds) != 0 || len(h.ledger.upsertedSlots) != 0 {
		t.Fatalf("a held step must write nothing: seeds=%v slots=%v", h.ledger.setSeeds, h.ledger.upsertedSlots)
	}
}

// A freed hull is a probe like any other: back on the books under the same placement
// row every spare carries, counted by the cap, and no purchase raised (RULINGS #4).
func TestAdvanceExpansion_StrandingAHullOrdersNothing(t *testing.T) {
	h := strandedHarness()

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rep.SeedsRequested != 0 {
		t.Fatalf("SeedsRequested = %d, want 0 — freeing a hull must not order another", rep.SeedsRequested)
	}
	for _, slot := range h.ledger.upsertedSlots {
		if slot.State != SlotStateParked {
			t.Fatalf("upserted %+v, want only the PARKED spare naming the freed hull", slot)
		}
	}
}
