package parkedsensing

import "testing"

// expansion_staging_reach_test.go pins the decoupling of WHERE WE CAN TRANSACT from WHAT IS NEAR THE
// TARGET.
//
// THE FLAW. stagingYardFor required a yard that was BOTH staffed AND in a system within a couple of
// gate hops OF THE TARGET. Those are two independent concerns welded together: the first is a money
// constraint (the buy queue can only buy where a hull of ours stands), the second is about the
// flight. Welded, they produce a structural dead zone — a target whose in-reach systems all happen to
// lack a shipyard can NEVER be seeded, however many staffed yards the fleet owns elsewhere. And since
// a system with no shipyard can never itself be staffed, the dead zone propagates outward.
//
// MEASURED LIVE, of 23 unseeded uncharted targets, by hop distance from the nearest STAFFED system
// over traversable (not under-construction) gates:
//
//	1 hop  3      served today
//	2 hops 2      served today
//	3 hops 1  ┐
//	4 hops 1  │
//	5 hops 2  ├   blocked ONLY by the old bound — every one of them gate-routable
//	6 hops 3  │
//	7 hops 4  │
//	8 hops 5  │
//	9 hops 2  ┘
//
// NONE is gate-disconnected. The worked example — X1-KP42 and X1-UV56, the only two systems in the
// fleet with an UNMAPPED jump gate and therefore the only two that can add new systems to the ledger
// — sit at NINE hops, and every one of their neighbours (X1-AM61, X1-PA58, X1-SU95, X1-UT77) has
// zero shipyards. That is the dead zone exactly.

// deadZone is the fixture the flaw needs, and it is built so the old rule and the new one DISAGREE.
//
// The only shipyard in the fleet is FOUR hops from the target, and every system nearer the target has
// none — so under the old "within a couple of hops of the TARGET" rule there is no eligible yard at
// any price, while under routability there is exactly one obvious answer. A fixture whose staffed
// yard sits near the target cannot tell the two rules apart.
//
//	X1-YARD ── X1-A ── X1-B ── X1-C ── X1-TARGET
//	  ^staffed   (no shipyard anywhere along the chain)
func deadZone() *expandHarness {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-YARD", Verdict: VerdictInScope},
		{System: "X1-A", Verdict: VerdictInScope},
		{System: "X1-B", Verdict: VerdictInScope},
		{System: "X1-C", Verdict: VerdictInScope},
		{System: "X1-TARGET", Verdict: VerdictPending, UnchartedCount: 7},
	}
	h.gates.adjacency = map[string][]string{
		"X1-YARD":   {"X1-A"},
		"X1-A":      {"X1-B"},
		"X1-B":      {"X1-C"},
		"X1-C":      {"X1-TARGET"},
		"X1-TARGET": {},
	}
	// One shipyard in the whole fleet, and it is the far end of the chain.
	h.yards.bySystem = map[string][]string{"X1-YARD": {"X1-YARD-A"}}
	h.ledger.slots = []QueuedSlot{staffedYardRow("X1-YARD", "X1-YARD-A")}
	return h
}

// THE TEST THAT MATTERS. Every system near the target lacks a shipyard; the one staffed yard we own
// is four hops away and the target IS routable from it. The seed stages there.
func TestAdvanceExpansion_StagesAtADistantStaffedYardWhenNoNearOneExists(t *testing.T) {
	h := deadZone()

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rep.SeedsRequested != 1 {
		t.Fatalf("SeedsRequested = %d, want 1 — X1-TARGET is routable from X1-YARD in 4 hops and that is "+
			"the only staffed yard in the fleet; requiring the yard to sit NEAR the target means this "+
			"target can never be seeded at all", rep.SeedsRequested)
	}
	if len(h.ledger.upsertedSlots) != 1 {
		t.Fatalf("wrote %d slots, want exactly 1: %v", len(h.ledger.upsertedSlots), h.ledger.upsertedSlots)
	}
	got := h.ledger.upsertedSlots[0]
	if got.Waypoint != "X1-YARD-A" || got.System != "X1-YARD" {
		t.Fatalf("seed staged at %s in %s, want X1-YARD-A in X1-YARD", got.Waypoint, got.System)
	}
	if got.Kind != SlotKindSpare || got.State != SlotStateWanted {
		t.Fatalf("seed request recorded as %s/%s, want SPARE/WANTED", got.Kind, got.State)
	}
}

// THE MONEY CONSTRAINT IS UNCHANGED. Routability decides WHICH staffed yard, never whether the yard
// must be staffed at all: the buy queue can only transact where a hull of ours stands, so a want
// written at an unstaffed yard is refused every tick forever and — through takeSupplyFor — goes on
// suppressing the correct request for the very target it was meant to serve.
//
// The fixture is adverse: the unstaffed yard is NEARER the target than the staffed one, so an
// implementation that widened reach and dropped the staffed test picks the wrong one and this fails.
func TestAdvanceExpansion_ANearerUnstaffedYardIsStillRefusedForADistantStaffedOne(t *testing.T) {
	h := deadZone()
	// X1-B has a yard but no hull of ours — two hops from the target against X1-YARD's four.
	h.yards.bySystem["X1-B"] = []string{"X1-B-YARD"}

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsRequested != 1 {
		t.Fatalf("SeedsRequested = %d, want 1", rep.SeedsRequested)
	}
	if got := h.ledger.upsertedSlots[0].Waypoint; got != "X1-YARD-A" {
		t.Fatalf("seed staged at %s, want X1-YARD-A — X1-B-YARD is nearer the target but no hull of ours "+
			"stands there, so the buy queue could never fund it", got)
	}
}

// FEWEST HOPS WINS among staffed yards that can all reach the target: a nearer yard is a shorter
// seed flight and fewer ticks holding probe-cap headroom.
//
// Adverse to the alphabet as well as to the old rule: X1-AAA sorts first and is the FURTHER of the
// two, so an implementation that widened the bound without ordering by distance picks it.
func TestAdvanceExpansion_TheNearestRoutableStaffedYardWins(t *testing.T) {
	h := deadZone()
	// X1-AAA is five hops from the target (AAA -> YARD -> A -> B -> C -> TARGET); X1-YARD is four.
	h.ledger.systems = append(h.ledger.systems, ExpandSystem{System: "X1-AAA", Verdict: VerdictInScope})
	h.gates.adjacency["X1-AAA"] = []string{"X1-YARD"}
	h.yards.bySystem["X1-AAA"] = []string{"X1-AAA-YARD"}
	h.ledger.slots = append(h.ledger.slots, staffedYardRow("X1-AAA", "X1-AAA-YARD"))

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsRequested != 1 {
		t.Fatalf("SeedsRequested = %d, want 1", rep.SeedsRequested)
	}
	if got := h.ledger.upsertedSlots[0].Waypoint; got != "X1-YARD-A" {
		t.Fatalf("seed staged at %s, want X1-YARD-A — X1-YARD is 4 hops from the target and X1-AAA is 5, "+
			"and the shorter flight wins over the alphabet", got)
	}
}

// A TARGET NO STAFFED YARD CAN REACH stages nothing, and the tick carries on. Gate-disconnected
// targets are not this lane's business — they need warp, and that path is disarmed.
func TestAdvanceExpansion_AnUnroutableTargetStagesNothingAndDoesNotFailTheTick(t *testing.T) {
	h := deadZone()
	// Sever the chain: nothing leaves X1-B, so X1-TARGET is unreachable from the only staffed yard.
	h.gates.adjacency["X1-B"] = []string{}

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("an unroutable target must be skipped quietly, not fail the tick: %v", err)
	}
	if rep.SeedsRequested != 0 {
		t.Fatalf("SeedsRequested = %d, want 0 — no traversable gate route reaches X1-TARGET, so a seed "+
			"staged for it could never arrive", rep.SeedsRequested)
	}
	for _, slot := range h.ledger.upsertedSlots {
		if slot.Kind == SlotKindSpare {
			t.Fatalf("wrote a seed want %v for a target nothing can route to", slot)
		}
	}
}

// THE BOUND IS THE SEED'S FLIGHT, AND IT REACHES THE WORKED EXAMPLE. Nine hops is not an arbitrary
// ring count: it is where the live traversable graph SATURATES (a bound of 10 serves no additional
// target), and it is the distance to X1-KP42 and X1-UV56 — the only two systems that can add new
// systems to the ledger. A bound of 6 would have doubled coverage and still left the lane's own
// headline example unreachable.
func TestAdvanceExpansion_StagingReachesNineHopsTheDistanceToTheUnmappedGateSystems(t *testing.T) {
	h := newExpandHarness()
	// A nine-hop chain from the only staffed yard to the target, mirroring the live measurement.
	systems := []string{"X1-YARD", "X1-H1", "X1-H2", "X1-H3", "X1-H4", "X1-H5", "X1-H6", "X1-H7", "X1-H8", "X1-TARGET"}
	h.gates.adjacency = map[string][]string{}
	for i, sys := range systems {
		row := ExpandSystem{System: sys, Verdict: VerdictInScope}
		if sys == "X1-TARGET" {
			row = ExpandSystem{System: sys, Verdict: VerdictPending, UnchartedCount: 7}
		}
		h.ledger.systems = append(h.ledger.systems, row)
		if i+1 < len(systems) {
			h.gates.adjacency[sys] = []string{systems[i+1]}
		} else {
			h.gates.adjacency[sys] = []string{}
		}
	}
	h.yards.bySystem = map[string][]string{"X1-YARD": {"X1-YARD-A"}}
	h.ledger.slots = []QueuedSlot{staffedYardRow("X1-YARD", "X1-YARD-A")}

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsRequested != 1 {
		t.Fatalf("SeedsRequested = %d, want 1 — the target is exactly %d hops out, which is the measured "+
			"distance to the only two systems that can grow the ledger", rep.SeedsRequested, MaxSeedFlightHops)
	}
	if got := h.ledger.upsertedSlots[0].Waypoint; got != "X1-YARD-A" {
		t.Fatalf("seed staged at %s, want X1-YARD-A", got)
	}
}

// …and the bound still BOUNDS. A target beyond it is not staged: the seed's own walk resolves its
// next hop over the same bound, so a want written past it would stamp an errand the walk can never
// route, holding probe-cap headroom while charting nothing.
func TestAdvanceExpansion_ATargetBeyondTheFlightBoundIsNotStaged(t *testing.T) {
	h := newExpandHarness()
	// One hop further than the bound.
	var systems []string
	for i := 0; i <= MaxSeedFlightHops+1; i++ {
		systems = append(systems, "X1-H"+string(rune('A'+i)))
	}
	h.gates.adjacency = map[string][]string{}
	for i, sys := range systems {
		row := ExpandSystem{System: sys, Verdict: VerdictInScope}
		if i == len(systems)-1 {
			row = ExpandSystem{System: sys, Verdict: VerdictPending, UnchartedCount: 7}
		}
		h.ledger.systems = append(h.ledger.systems, row)
		if i+1 < len(systems) {
			h.gates.adjacency[sys] = []string{systems[i+1]}
		} else {
			h.gates.adjacency[sys] = []string{}
		}
	}
	h.yards.bySystem = map[string][]string{systems[0]: {systems[0] + "-YARD"}}
	h.ledger.slots = []QueuedSlot{staffedYardRow(systems[0], systems[0]+"-YARD")}

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsRequested != 0 {
		t.Fatalf("SeedsRequested = %d, want 0 — the target is %d hops out and the seed's walk resolves "+
			"only %d, so the errand could never route", rep.SeedsRequested, MaxSeedFlightHops+1, MaxSeedFlightHops)
	}
}
