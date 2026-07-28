package parkedsensing

import "testing"

// expansion_reach_test.go pins the seed's REACH: how far from a system we hold a
// charting target may be and still be served.
//
// The frontier ran out of ring. Measured on the live fleet, 33 unseeded systems
// carried uncharted waypoints and exactly ONE of them was a direct gate
// neighbour of a system we occupied, so seed staging — which required direct
// adjacency at both gates, stagingYardFor and takeReachableSpare — could serve
// one target and then nothing, forever. Seven are reachable within MaxWalkRings.
//
// EVERY FIXTURE HERE IS BUILT SO THE TWO RULES DISAGREE. A two-hop target that
// is also a neighbour, or an origin set where every candidate is equidistant,
// makes the old rule and the new one indistinguishable and proves nothing. So
// each test below names the system the OLD rule would have picked and asserts we
// picked the other one.

// twoHopWorld is the shape every test in this file starts from: a system we hold
// with a staffed probe yard, an intermediate we merely know about, and a target
// two gate hops out that X1-HOME does NOT border.
//
// The gate graph is written FORWARD ONLY in the direction the walk traverses it,
// because the stored graph really is asymmetric — measured live, 617 of 5463
// edges have no reverse row, since a gate we have charted from one end names a
// system whose own gate we have not charted yet. A reach test that quietly
// relied on symmetry would stage seeds onto routes nextHopToward cannot resolve,
// and each one would strand a probe.
func twoHopWorld() *expandHarness {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-HOME", Verdict: VerdictInScope},
		{System: "X1-MID", Verdict: VerdictPending},
		{System: "X1-FAR", Verdict: VerdictPending, UnchartedCount: 3},
	}
	h.gates.adjacency = map[string][]string{
		"X1-HOME": {"X1-MID"},
		"X1-MID":  {"X1-FAR"},
		"X1-FAR":  {},
	}
	h.yards.bySystem = map[string][]string{"X1-HOME": {"X1-HOME-YARD"}}
	h.ledger.slots = []QueuedSlot{staffedYardRow("X1-HOME", "X1-HOME-YARD")}
	return h
}

// A target two gate hops from the nearest system we hold is staged, at the
// staffed yard in that system.
//
// This is the headline case and the whole point of the change: under the old
// direct-adjacency rule X1-HOME does not border X1-FAR, so no yard was ever
// found and the target waited forever behind a purchase nobody would make.
func TestAdvanceExpansion_TargetTwoGateHopsAwayIsStaged(t *testing.T) {
	h := twoHopWorld()

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rep.SeedsRequested != 1 {
		t.Fatalf("SeedsRequested = %d, want 1 — X1-FAR is %d hops from X1-HOME, within MaxWalkRings=%d, "+
			"and the frontier has no closer target left", rep.SeedsRequested, 2, MaxWalkRings)
	}
	if len(h.ledger.upsertedSlots) != 1 {
		t.Fatalf("wrote %d slots, want exactly 1: %v", len(h.ledger.upsertedSlots), h.ledger.upsertedSlots)
	}
	got := h.ledger.upsertedSlots[0]
	if got.Waypoint != "X1-HOME-YARD" || got.System != "X1-HOME" {
		t.Fatalf("seed staged at %s in %s, want X1-HOME-YARD in X1-HOME", got.Waypoint, got.System)
	}
	if got.Kind != SlotKindSpare || got.State != SlotStateWanted {
		t.Fatalf("seed request recorded as %s/%s, want SPARE/WANTED", got.Kind, got.State)
	}
}

// A target THREE hops out is not staged at all: no want, no error, and the tick
// carries on.
//
// The bound is what keeps a dispatched seed from stranding. nextHopToward names
// no next system beyond MaxWalkRings, so a seed stamped for a target out there
// would hold probe-cap headroom, re-issue a failing step every tick, and chart
// nothing — strictly worse than never dispatching it.
func TestAdvanceExpansion_TargetBeyondTheWalkIsNeverStaged(t *testing.T) {
	h := twoHopWorld()
	h.ledger.systems = append(h.ledger.systems,
		ExpandSystem{System: "X1-OUT", Verdict: VerdictPending, UnchartedCount: 9})
	// X1-OUT is three hops: HOME -> MID -> FAR -> OUT. It carries the DEEPEST
	// dark of any target, so an engine that had merely stopped bounding the walk
	// would reach for it FIRST and this assertion would catch it.
	h.gates.adjacency["X1-FAR"] = []string{"X1-OUT"}
	// Only X1-OUT is left needing charting, so nothing else can absorb the tick.
	h.ledger.systems[2].UnchartedCount = 0
	h.ledger.systems[2].CatalogKnown = true

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("a target beyond the walk must be skipped quietly, not fail the tick: %v", err)
	}

	if rep.SeedsRequested != 0 {
		t.Fatalf("SeedsRequested = %d, want 0 — X1-OUT is 3 hops out and MaxWalkRings=%d, so a seed "+
			"stamped for it could never route there", rep.SeedsRequested, MaxWalkRings)
	}
	for _, slot := range h.ledger.upsertedSlots {
		if slot.Kind == SlotKindSpare {
			t.Fatalf("wrote a seed want %v for a target no walk can reach", slot)
		}
	}
	if len(h.ledger.setSeeds) != 0 {
		t.Fatalf("stamped an errand %v for a target beyond the walk", h.ledger.setSeeds)
	}
}

// Widening REACH must not widen WHERE WE BUY.
//
// X1-MID is the CLOSER origin — one hop from the target against X1-HOME's two —
// and it has a probe yard, so an implementation that ordered origins by distance
// and forgot the staffed test would stage there. Nothing of ours stands in
// X1-MID, so the buy queue would refuse that want on every tick forever, and
// through takeSupplyFor the dead row would go on suppressing the correct request
// for the very target it was meant to serve.
func TestAdvanceExpansion_ANearerYardWeDoNotStandAtIsStillRefused(t *testing.T) {
	h := twoHopWorld()
	h.yards.bySystem["X1-MID"] = []string{"X1-MID-YARD"}

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rep.SeedsRequested != 1 {
		t.Fatalf("SeedsRequested = %d, want 1", rep.SeedsRequested)
	}
	got := h.ledger.upsertedSlots[0]
	if got.Waypoint != "X1-HOME-YARD" {
		t.Fatalf("seed staged at %s, want X1-HOME-YARD — X1-MID-YARD is nearer the target but no hull "+
			"of ours stands there, so the buy queue could never fund it", got.Waypoint)
	}
}

// Among origins that can all reach the target, the NEAREST one is staged at: a
// one-hop origin costs the seed one flight, a two-hop origin two.
//
// The fixture is adverse to the ordering that was there before. Origins used to
// be walked in sorted-symbol order, and X1-AAA sorts FIRST while being the
// FURTHER of the two — so an implementation that widened reach without ordering
// by distance picks X1-AAA and this fails.
func TestAdvanceExpansion_TheNearestStaffedOriginIsPreferred(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-AAA", Verdict: VerdictInScope},
		{System: "X1-ZZZ", Verdict: VerdictInScope},
		{System: "X1-MID", Verdict: VerdictPending},
		{System: "X1-FAR", Verdict: VerdictPending, UnchartedCount: 3},
	}
	h.gates.adjacency = map[string][]string{
		"X1-AAA": {"X1-MID"}, // two hops to the target
		"X1-MID": {"X1-FAR"},
		"X1-ZZZ": {"X1-FAR"}, // one hop to the target
		"X1-FAR": {},
	}
	h.yards.bySystem = map[string][]string{
		"X1-AAA": {"X1-AAA-YARD"},
		"X1-ZZZ": {"X1-ZZZ-YARD"},
	}
	h.ledger.slots = []QueuedSlot{
		staffedYardRow("X1-AAA", "X1-AAA-YARD"),
		staffedYardRow("X1-ZZZ", "X1-ZZZ-YARD"),
	}

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsRequested != 1 {
		t.Fatalf("SeedsRequested = %d, want 1", rep.SeedsRequested)
	}
	if got := h.ledger.upsertedSlots[0].Waypoint; got != "X1-ZZZ-YARD" {
		t.Fatalf("seed staged at %s, want X1-ZZZ-YARD — X1-ZZZ is one hop from X1-FAR and X1-AAA is two, "+
			"and the cheaper flight wins over the alphabet", got)
	}
}

// A parked spare two hops from the target is claimed as its seed — no purchase,
// no wait for one.
//
// takeReachableSpare enforced direct adjacency too, so a probe idling two hops
// out was invisible to the target it could serve and the tick ordered a fresh
// one instead. Measured live, 57 probes sat parked while expansion staged
// nothing.
func TestAdvanceExpansion_SpareTwoHopsFromTheTargetIsClaimed(t *testing.T) {
	h := twoHopWorld()
	h.ledger.slots = append(h.ledger.slots, QueuedSlot{
		Waypoint: "X1-HOME-B", System: "X1-HOME", Kind: SlotKindSpare,
		State: SlotStateParked, AssignedShip: "PROBE-SPARE",
	})

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rep.SeedsClaimed != 1 {
		t.Fatalf("SeedsClaimed = %d, want 1 — PROBE-SPARE is parked %d hops from X1-FAR, within "+
			"MaxWalkRings=%d, and claiming it costs nothing", rep.SeedsClaimed, 2, MaxWalkRings)
	}
	if len(h.ledger.setSeeds) != 1 ||
		h.ledger.setSeeds[0].system != "X1-FAR" ||
		h.ledger.setSeeds[0].ship != "PROBE-SPARE" ||
		h.ledger.setSeeds[0].state != SeedStateDispatched {
		t.Fatalf("errand stamped %v, want X1-FAR/PROBE-SPARE/DISPATCHED", h.ledger.setSeeds)
	}
	// Claiming a hull we already own must never also buy one.
	if rep.SeedsRequested != 0 {
		t.Fatalf("SeedsRequested = %d, want 0 — the claimed spare already covers X1-FAR", rep.SeedsRequested)
	}
}

// A spare beyond the walk is still no seed, and still must not suppress the
// demand it cannot serve.
func TestAdvanceExpansion_SpareBeyondTheWalkIsNeitherClaimedNorCountedAsSupply(t *testing.T) {
	h := twoHopWorld()
	// X1-EDGE is three hops from X1-FAR (EDGE -> HOME -> MID -> FAR).
	h.ledger.systems = append(h.ledger.systems,
		ExpandSystem{System: "X1-EDGE", Verdict: VerdictInScope})
	h.gates.adjacency["X1-EDGE"] = []string{"X1-HOME"}
	h.ledger.slots = append(h.ledger.slots, QueuedSlot{
		Waypoint: "X1-EDGE-A", System: "X1-EDGE", Kind: SlotKindSpare,
		State: SlotStateParked, AssignedShip: "PROBE-EDGE",
	})

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsClaimed != 0 {
		t.Fatalf("SeedsClaimed = %d, want 0 — X1-EDGE is 3 hops from X1-FAR", rep.SeedsClaimed)
	}
	if rep.SeedsRequested != 1 {
		t.Fatalf("SeedsRequested = %d, want 1 — a spare that cannot reach X1-FAR is not a seed for it, "+
			"and counting it as one would stall the target forever", rep.SeedsRequested)
	}
}

// SUPPLY AND STAGING MUST WIDEN IN LOCKSTEP.
//
// This is the duplicate-order bug that widening only one of them creates. Tick 1
// stages a want at X1-HOME-YARD for X1-FAR. Tick 2 rebuilds the book: if
// takeSupplyFor still asks for DIRECT adjacency it does not recognise that want
// as supply for a two-hop target, so it falls through to stagingYardFor — which
// now DOES reach — finds X1-HOME-YARD occupied by a SPARE, walks on to the next
// free yard, and orders a SECOND probe for a target already served.
//
// Two yards in X1-HOME is what makes the bug visible: with one, the second
// request has nowhere to land and the fixture hides the defect.
func TestAdvanceExpansion_AWantWithinTheWalkSuppressesADuplicateRequest(t *testing.T) {
	h := twoHopWorld()
	h.yards.bySystem["X1-HOME"] = []string{"X1-HOME-YARD", "X1-HOME-YARD2"}
	h.ledger.slots = []QueuedSlot{
		staffedYardRow("X1-HOME", "X1-HOME-YARD"),
		staffedYardRow("X1-HOME", "X1-HOME-YARD2"),
		// The want tick 1 already wrote for X1-FAR.
		{Waypoint: "X1-HOME-YARD", System: "X1-HOME", Kind: SlotKindSpare, State: SlotStateWanted},
	}

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsRequested != 0 {
		t.Fatalf("SeedsRequested = %d, want 0 — a want already outstanding at X1-HOME-YARD is a seed "+
			"on order for X1-FAR, and ordering a second at X1-HOME-YARD2 buys a probe twice", rep.SeedsRequested)
	}
	for _, slot := range h.ledger.upsertedSlots {
		if slot.Waypoint == "X1-HOME-YARD2" {
			t.Fatalf("staged a duplicate seed at %s for a target already supplied", slot.Waypoint)
		}
	}
}

// THE CHEAP FRONTIER MOVES FIRST. With one spare and two reachable targets, the
// nearer target takes it: a one-hop errand is one flight, a two-hop errand two,
// and the probe is held for the whole of it.
//
// The fixture is adverse to the ordering that was there before. seedlessTargets
// ranks DEEPEST DARK first, so X1-FAR's nine uncharted waypoints put it ahead of
// X1-NEAR's one — an engine that widened reach without ordering by hop count
// hands the spare to the target twice as far away, and this fails.
//
// The spare claim is the discriminator on purpose: SetSeed names the TARGET,
// while a staged want records only the yard, so a staging fixture could not tell
// the two orderings apart at all.
func TestAdvanceExpansion_TheNearerTargetTakesTheOnlySpare(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-HOME", Verdict: VerdictInScope},
		{System: "X1-NEAR", Verdict: VerdictPending, UnchartedCount: 1},
		{System: "X1-MID", Verdict: VerdictPending},
		{System: "X1-FAR", Verdict: VerdictPending, UnchartedCount: 9},
	}
	h.gates.adjacency = map[string][]string{
		"X1-HOME": {"X1-MID", "X1-NEAR"},
		"X1-MID":  {"X1-FAR"},
		"X1-NEAR": {},
		"X1-FAR":  {},
	}
	// No yards anywhere, so the ONE parked spare is the only seed available and
	// the two targets genuinely compete for it.
	h.ledger.slots = []QueuedSlot{{
		Waypoint: "X1-HOME-A", System: "X1-HOME", Kind: SlotKindSpare,
		State: SlotStateParked, AssignedShip: "PROBE-SPARE",
	}}

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsClaimed != 1 {
		t.Fatalf("SeedsClaimed = %d, want 1", rep.SeedsClaimed)
	}
	if len(h.ledger.setSeeds) != 1 || h.ledger.setSeeds[0].system != "X1-NEAR" {
		t.Fatalf("the spare was sent to %v, want X1-NEAR — it is one hop out against X1-FAR's two, "+
			"and the cheap frontier moves first even though X1-FAR is darker", h.ledger.setSeeds)
	}
}

// Among spares that can all reach the target, the NEAREST is claimed.
//
// The fixture is adverse to taking the first match: PROBE-TWO is listed FIRST in
// the ledger and is the FURTHER of the two, so an implementation that stopped at
// the first reachable spare sends the probe that has to cross two gates instead
// of the one already next door — twice the flying, twice the window in which the
// walk can be interrupted, for the same charting.
func TestAdvanceExpansion_TheNearestReachableSpareIsClaimed(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-TWO", Verdict: VerdictInScope},
		{System: "X1-ONE", Verdict: VerdictInScope},
		{System: "X1-MID", Verdict: VerdictPending},
		{System: "X1-FAR", Verdict: VerdictPending, UnchartedCount: 3},
	}
	h.gates.adjacency = map[string][]string{
		"X1-TWO": {"X1-MID"}, // two hops to the target
		"X1-MID": {"X1-FAR"},
		"X1-ONE": {"X1-FAR"}, // one hop to the target
		"X1-FAR": {},
	}
	h.ledger.slots = []QueuedSlot{
		{Waypoint: "X1-TWO-A", System: "X1-TWO", Kind: SlotKindSpare,
			State: SlotStateParked, AssignedShip: "PROBE-TWO"},
		{Waypoint: "X1-ONE-A", System: "X1-ONE", Kind: SlotKindSpare,
			State: SlotStateParked, AssignedShip: "PROBE-ONE"},
	}

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsClaimed != 1 {
		t.Fatalf("SeedsClaimed = %d, want 1", rep.SeedsClaimed)
	}
	if len(h.ledger.setSeeds) != 1 || h.ledger.setSeeds[0].ship != "PROBE-ONE" {
		t.Fatalf("claimed %v, want PROBE-ONE — it is one hop from X1-FAR against PROBE-TWO's two, "+
			"and it is listed second precisely so taking the first match picks the wrong one",
			h.ledger.setSeeds)
	}
}

// Reach is decided from the STORE alone, and the tick does not re-read a system
// it has already read.
//
// An expansion tick must not spend an API call working out how far a target is:
// gate adjacency is a pure store read by contract, so the whole cost of the
// widening is extra reads of a local table. This pins that those reads do not
// multiply — a two-hop search passes THROUGH intermediates, and every origin in
// a neighbourhood usually shares them.
//
// THE INTERMEDIATE IS DELIBERATELY NOT A LEDGER SYSTEM. readNeighbours only maps
// systems the ledger holds, so a system reached in the second ring is exactly
// the one the search has to go to the store for — and it is the ONLY read the
// memo can save. A fixture whose intermediate is already in the ledger reads
// entirely from the map, and the count never moves however the memo is mutated.
func TestAdvanceExpansion_AnIntermediateOutsideTheLedgerIsReadOnceForTheTick(t *testing.T) {
	h := newExpandHarness()
	// Four origins we hold, all routing to the target through ONE intermediate
	// that the ledger has never heard of. Each origin gets its own search, so an
	// unmemoised fallback reads X1-MID once per origin.
	h.ledger.systems = []ExpandSystem{
		{System: "X1-FAR", Verdict: VerdictPending, UnchartedCount: 3},
	}
	h.gates.adjacency = map[string][]string{"X1-MID": {"X1-FAR"}, "X1-FAR": {}}
	h.yards.bySystem = map[string][]string{}
	for _, origin := range []string{"X1-H1", "X1-H2", "X1-H3", "X1-H4"} {
		h.ledger.systems = append(h.ledger.systems,
			ExpandSystem{System: origin, Verdict: VerdictInScope})
		h.gates.adjacency[origin] = []string{"X1-MID"}
		h.yards.bySystem[origin] = []string{origin + "-YARD"}
		h.ledger.slots = append(h.ledger.slots, staffedYardRow(origin, origin+"-YARD"))
	}

	if _, err := h.run(t, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// readNeighbours reads each ledger system once; the search may go to the
	// store for the systems that map does not cover, but only once each.
	budget := len(h.ledger.systems) + 1 // + X1-MID
	if h.gates.calls > budget {
		t.Fatalf("gate store read %d times, want at most %d — an intermediate outside the ledger is "+
			"being re-read for every origin that routes through it, and that cost grows with the "+
			"frontier exactly as the frontier succeeds", h.gates.calls, budget)
	}
}
