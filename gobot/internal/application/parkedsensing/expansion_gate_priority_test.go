package parkedsensing

import (
	"context"
	"errors"
	"testing"
)

// expansion_gate_priority_test.go pins the one ordering rule that decides whether the LEDGER GROWS.
//
// Charting a system whose gate adjacency we have ALREADY read cannot add a system to the map. It
// fills in markets we can already see — worth doing, and worth income — but the ledger stays the
// size it was. Only a system whose gate we have NEVER read can name neighbours we do not hold, and
// those neighbours are what markFrontier records as PENDING and the seed machinery then chases.
//
// MEASURED LIVE, of 21 unseeded uncharted targets: 2 have an unmapped gate, 19 do not. And under the
// ordering as it stood, those 2 ranked TWENTIETH AND TWENTY-FIRST — dead last:
//
//	rank  system     hops  uncharted  unmapped gate
//	 1    X1-QC9       1      14          no
//	 2    X1-NV35      2      68          no
//	 …
//	20    X1-KP42      9      62          YES
//	21    X1-UV56      9      11          YES
//
// The mechanism is DISTANCE, not depth: orderByDistance makes hop count the primary key, so the two
// systems that can actually grow the map sort behind every nearer one and never get a seed. X1-KP42
// carries the second-deepest uncharted count in the whole set and it still came last.
//
// THE PROPERTY IS "WE HOLD NO GATE ROWS FOR THIS SYSTEM", not "its jump-gate waypoint is uncharted"
// and not "it has no traversable neighbours". A system whose every edge is under construction HAS
// been read — we know where it connects, we simply cannot pass — so charting it reveals no new
// adjacency. Conflating the two would rank a known dead end alongside genuine unknown territory.

// gateMap is a gate fake that answers BOTH questions the engine asks of the store: who a system
// connects to, and whether we hold any adjacency for it at all.
//
// The two are deliberately separable here, because in production they are: the real port filters
// under-construction and stale edges out of Neighbours while the ROWS still exist. A fake that
// derived "mapped" from "Neighbours is non-empty" could not express that difference, and would let a
// mapped-but-impassable system pass as unexplored.
type gateMap struct {
	adjacency map[string][]string
	// unmapped names the systems we hold NO gate rows for. Everything else is mapped.
	unmapped   map[string]bool
	mappedErr  error
	mappedCall int
}

func (g *gateMap) Neighbours(_ context.Context, system string) ([]string, error) {
	return g.adjacency[system], nil
}

func (g *gateMap) Mapped(_ context.Context, system string) (bool, error) {
	g.mappedCall++
	if g.mappedErr != nil {
		// Adversarial: "mapped" alongside the error. An engine that leaks it demotes a genuine
		// frontier system to the back of the queue, which is the silent direction.
		return true, g.mappedErr
	}
	return !g.unmapped[system], nil
}

// PassableGraph mirrors this fake's own adjacency as a single snapshot. Mapped enumerates exactly
// the systems this fake HOLDS: a bulk snapshot cannot express the per-system Mapped's "true for
// anything you ask", and enumerating what is actually stored is the truthful reading — a system
// with no entry here has not been read, which is what the reachability filter treats as unknown
// rather than as a dead end.
func (g *gateMap) PassableGraph(_ context.Context) (GateGraph, error) {
	graph := GateGraph{Passable: map[string][]string{}, Mapped: map[string]bool{}}
	for system, neighbours := range g.adjacency {
		graph.Mapped[system] = true
		graph.Passable[system] = append([]string(nil), neighbours...)
	}
	return graph, nil
}

// frontierRace is the live shape, built so the OLD ordering and the NEW one disagree on every key.
//
// X1-NEAR wins under both existing weights — it is one hop out (the current primary key) and carries
// far more uncharted work (the secondary). X1-FAR loses on both, and is the only one that can grow
// the ledger. A fixture whose unmapped-gate system were also the nearest or the deepest would make
// the two orderings agree and prove nothing.
//
//	X1-HOME ── X1-NEAR                 (mapped gate, 1 hop, 60 uncharted)
//	    └───── X1-MID ── X1-FAR        (UNMAPPED gate, 2 hops, 5 uncharted)
func frontierRace() (*expandHarness, *gateMap) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-HOME", Verdict: VerdictInScope},
		{System: "X1-MID", Verdict: VerdictInScope},
		{System: "X1-NEAR", Verdict: VerdictPending, UnchartedCount: 60},
		{System: "X1-FAR", Verdict: VerdictPending, UnchartedCount: 5},
	}
	gates := &gateMap{
		adjacency: map[string][]string{
			"X1-HOME": {"X1-MID", "X1-NEAR"},
			"X1-MID":  {"X1-FAR"},
			"X1-NEAR": {"X1-HOME"},
			// X1-FAR names nobody: we hold no rows for it. It is reachable only because
			// X1-MID's rows point AT it — exactly how X1-KP42 sits nine hops out with no
			// adjacency of its own.
			"X1-FAR": {},
		},
		unmapped: map[string]bool{"X1-FAR": true},
	}
	h.gates = nil // replaced at run time by gateMap
	// ONE parked spare, so the two targets genuinely compete: SetSeed names the TARGET, which is
	// the only discriminator that can tell which one the queue reached first (a staged want records
	// the yard, not the target, and could not distinguish the orderings at all).
	h.ledger.slots = []QueuedSlot{{
		Waypoint: "X1-HOME-A", System: "X1-HOME", Kind: SlotKindSpare,
		State: SlotStateParked, AssignedShip: "PROBE-SPARE",
	}}
	return h, gates
}

// runWithGates drives one tick against a gateMap.
func runWithGates(t *testing.T, h *expandHarness, gates *gateMap) (ExpandReport, error) {
	t.Helper()
	for i := range h.ledger.systems {
		h.ledger.systems[i].CatalogKnown = !h.unswept[h.ledger.systems[i].System]
	}
	p := h.ports()
	p.Gates = gates
	return AdvanceExpansion(context.Background(), p, 1, ExpandKnobs{
		SpendEnabled: true, MinBudgetRate: 0.05, Whitelist: h.whitelist,
	}, 1.0)
}

// THE TEST THAT MATTERS. The only target that can add a system to the ledger is the furthest and the
// shallowest, and it is served first anyway.
func TestAdvanceExpansion_AnUnmappedGateTargetOutranksNearerDeeperMappedOnes(t *testing.T) {
	h, gates := frontierRace()

	rep, err := runWithGates(t, h, gates)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsClaimed != 1 {
		t.Fatalf("SeedsClaimed = %d, want 1", rep.SeedsClaimed)
	}
	if len(h.ledger.setSeeds) != 1 || h.ledger.setSeeds[0].system != "X1-FAR" {
		t.Fatalf("the spare was sent to %v, want X1-FAR — it is the only target whose gate we have "+
			"never read, so it is the only one whose charting can add a system to the ledger; X1-NEAR "+
			"is nearer AND deeper and would win on every existing weight", h.ledger.setSeeds)
	}
}

// WEIGHT, DO NOT FILTER. A mapped-gate target is still worth charting — its markets are the income
// side — so the rule reorders the queue and never truncates it.
//
// With the unmapped-gate target already seeded, the mapped one must be served exactly as it is today.
func TestAdvanceExpansion_AMappedGateTargetIsStillServedOnceTheUnmappedOneIsCovered(t *testing.T) {
	h, gates := frontierRace()
	// X1-FAR already has a seed out, so it is no longer a candidate.
	h.ledger.systems[3].SeedShip, h.ledger.systems[3].SeedState = "PROBE-OUT", SeedStateDispatched

	rep, err := runWithGates(t, h, gates)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsClaimed != 1 {
		t.Fatalf("SeedsClaimed = %d, want 1 — a mapped-gate target is still charting work worth doing; "+
			"this rule reorders the queue, it must never truncate it", rep.SeedsClaimed)
	}
	if len(h.ledger.setSeeds) == 0 || h.ledger.setSeeds[0].system != "X1-NEAR" {
		t.Fatalf("the spare was sent to %v, want X1-NEAR", h.ledger.setSeeds)
	}
}

// WITH NO UNMAPPED-GATE TARGET IN PLAY, the order is exactly what it was: nearest first, deepest
// within a ring. The new key is an outer tier, not a replacement.
func TestAdvanceExpansion_WithEveryGateMappedTheOrderIsUnchanged(t *testing.T) {
	h, gates := frontierRace()
	gates.unmapped = map[string]bool{} // every system's adjacency has been read

	rep, err := runWithGates(t, h, gates)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsClaimed != 1 {
		t.Fatalf("SeedsClaimed = %d, want 1", rep.SeedsClaimed)
	}
	if len(h.ledger.setSeeds) == 0 || h.ledger.setSeeds[0].system != "X1-NEAR" {
		t.Fatalf("the spare was sent to %v, want X1-NEAR — with no unmapped gate anywhere the existing "+
			"nearest-first ordering must be untouched", h.ledger.setSeeds)
	}
}

// DEPTH STILL BREAKS TIES WITHIN THE TIER: a deep unmapped-gate target beats a shallow one, so the
// existing weighting keeps working where the new tier does not separate.
func TestAdvanceExpansion_DepthStillOrdersWithinTheUnmappedGateTier(t *testing.T) {
	h, gates := frontierRace()
	// A SECOND unmapped-gate target, nearer than X1-FAR but far shallower.
	h.ledger.systems = append(h.ledger.systems,
		ExpandSystem{System: "X1-THIN", Verdict: VerdictPending, UnchartedCount: 1})
	gates.adjacency["X1-HOME"] = []string{"X1-MID", "X1-NEAR", "X1-THIN"}
	gates.adjacency["X1-THIN"] = []string{}
	gates.unmapped["X1-THIN"] = true

	rep, err := runWithGates(t, h, gates)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsClaimed != 1 {
		t.Fatalf("SeedsClaimed = %d, want 1", rep.SeedsClaimed)
	}
	// Both are unmapped-gate, so the existing keys decide: X1-THIN is ONE hop out against X1-FAR's
	// two, and distance is the first of them.
	if len(h.ledger.setSeeds) == 0 || h.ledger.setSeeds[0].system != "X1-THIN" {
		t.Fatalf("the spare was sent to %v, want X1-THIN — inside the unmapped-gate tier the existing "+
			"nearest-first ordering still decides", h.ledger.setSeeds)
	}
}

// AN UNREADABLE MAPPING READ FAILS THE TICK rather than silently demoting a frontier system.
//
// The fake answers "mapped" alongside its error, so an engine that swallows it ranks a genuine
// unknown-territory target as ordinary and the fleet quietly stops growing — the failure mode this
// whole rule exists to prevent, arriving through a database hiccup instead of an ordering bug.
func TestAdvanceExpansion_AnUnreadableGateMappingFailsTheTick(t *testing.T) {
	h, gates := frontierRace()
	gates.mappedErr = errors.New("gate store unhappy")

	_, err := runWithGates(t, h, gates)
	if err == nil {
		t.Fatalf("the tick succeeded on an unreadable gate-mapping read — an unread store must never " +
			"present as 'this system's gate is already known'")
	}
	if len(h.ledger.setSeeds) != 0 {
		t.Fatalf("stamped %v off an unreadable mapping read", h.ledger.setSeeds)
	}
}

// The mapping is resolved with ONE read per target, up front — never from inside the sort's
// comparator.
//
// It is a pure store read (zero API calls), so the risk is not the call itself but WHERE it is made:
// sort invokes its less function O(n log n) times, so asking the store there turns a linear pass into
// a superlinear one that grows with the frontier exactly as the frontier succeeds. With 4 targets a
// comparator-resident read would exceed 4 comparisons immediately.
func TestAdvanceExpansion_TheGateMappingIsResolvedOncePerTargetNotPerComparison(t *testing.T) {
	h, gates := frontierRace()
	// Enough targets that a comparator-resident read is unmistakably more than one-per-target.
	for _, sys := range []string{"X1-T1", "X1-T2", "X1-T3", "X1-T4"} {
		h.ledger.systems = append(h.ledger.systems,
			ExpandSystem{System: sys, Verdict: VerdictPending, UnchartedCount: 9})
		gates.adjacency["X1-HOME"] = append(gates.adjacency["X1-HOME"], sys)
		gates.adjacency[sys] = []string{"X1-HOME"}
	}

	if _, err := runWithGates(t, h, gates); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	targets := 0
	for _, s := range h.ledger.systems {
		if s.UnchartedCount > 0 {
			targets++
		}
	}
	if gates.mappedCall > targets {
		t.Fatalf("gate mapping read %d times for %d targets — it is being resolved inside the sort "+
			"comparator instead of once up front", gates.mappedCall, targets)
	}
}
