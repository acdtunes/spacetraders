package parkedsensing

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/system/gategraph"
)

// expansion_gate_read_test.go pins the pass that asks a system where it connects INSTEAD OF WAITING
// FOR A HULL TO GET THERE.
//
// THE PRODUCTION FAILURE, exactly. Agent TORWIND believed it was sealed inside a pocket of 57
// gate-reachable systems with all 53 exits under construction. X1-TD22 was the single in-scope system
// whose jump gate had never been read. Read directly from the API with NO SHIP PRESENT:
//
//	X1-TD22-Z10A  JUMP_GATE  isUnderConstruction=false
//	  -> X1-KP42  (already ours; the edge KP42->TD22 is open)
//	  -> X1-RV70  (a system absent from sensing_systems AND from system_coords)
//	X1-RV70-C24B  JUMP_GATE  isUnderConstruction=false
//	  -> X1-SN89, X1-KF69, X1-TU10, X1-ZA91, X1-TD22
//
// Walking outward from X1-TD22 through the public API cost 58 calls, moved zero ships and revealed 33
// systems we had never heard of; it stopped only because the walk was capped at 4 hops. Seven of the
// systems previously written off as "unreachable, behind the wall" are plain gate-reachable.
//
// WHY THE ENGINE NEVER ASKED. seedlessTargets keeps a system only while
// `(UnchartedCount > 0 || !CatalogKnown) && !hasActiveSeed`, and orderByGateMapping — the only caller
// of Gates.Mapped — runs over its output. So a system leaves the gate question entirely once EITHER a
// seed is dispatched to it OR its catalogue is swept and fully charted. X1-TD22 hit the first: a probe
// stamped hours earlier was still in flight several hops away, and from that moment the engine waited
// for the hull rather than asking the gate. An entire region sat behind one in-flight probe.
//
// SO THE FIXTURES HERE ARE BUILT AROUND THE TWO EXCLUSIONS, and the first test is the live shape: a
// system that is IN SCOPE, HAS AN ACTIVE SEED IN FLIGHT, and whose adjacency is UNREAD. Under the
// seedlessTargets-derived behaviour it is invisible to every gate-related pass in the engine, so a
// fixture that did not carry an active seed would prove nothing about the bug that was shipped.

// gateStore is the tick's gate topology, split into the two halves production actually has: what the
// STORE holds (Neighbours/Mapped, the pure per-tick reads) and what a DELIBERATE FETCH-THROUGH READ
// would learn (ReadGate). A successful read moves a system from the second into the first, which is
// exactly what persisting the edge set does.
//
// The split is the point. A fake that derived "we have read this gate" from "Neighbours returned
// something" could not express the shape that sealed the fleet in — a system whose every exit is
// under construction reports NO traversable neighbours and is nonetheless fully mapped — and would
// let the pass re-read the whole pocket forever while looking correct.
type gateStore struct {
	// stored is the persisted adjacency. KEY PRESENCE is the `ok` the real edge store returns and
	// Mapped reports, so `stored["X1-SEALED"] = nil` is a system we have READ that connects nowhere
	// passable, while an ABSENT key is genuinely unread territory. The two must not be conflated.
	stored map[string][]string
	// live is what the API would answer for a gate read: the topology beyond what we hold.
	live map[string][]string
	// uncharted names the gates that genuinely need a hull standing on them. A read of one fails
	// with gategraph.ErrGateUnreadable, which is the API telling the truth rather than an error.
	uncharted map[string]bool
	// readErr injects a NON-ordinary failure for one system — a store or token fault, the kind that
	// must not be mistaken for "this gate needs a probe".
	readErr map[string]error

	mappedErr error

	// reads records every system ReadGate was asked for, IN ORDER, which is the only way to assert
	// the frontier-first ordering and the per-tick cap.
	reads       []string
	mappedCalls int
}

func newGateStore() *gateStore {
	return &gateStore{
		stored:    map[string][]string{},
		live:      map[string][]string{},
		uncharted: map[string]bool{},
		readErr:   map[string]error{},
	}
}

func (g *gateStore) Neighbours(_ context.Context, system string) ([]string, error) {
	return g.stored[system], nil
}

func (g *gateStore) Mapped(_ context.Context, system string) (bool, error) {
	g.mappedCalls++
	if g.mappedErr != nil {
		// Adversarial: "mapped" alongside the error, so an engine that swallows it silently drops a
		// genuine unknown out of the candidate set — the quiet direction, and the one this whole
		// file exists to prevent.
		return true, g.mappedErr
	}
	_, held := g.stored[system]
	return held, nil
}

func (g *gateStore) ReadGate(_ context.Context, _ int, system string) error {
	g.reads = append(g.reads, system)
	if err := g.readErr[system]; err != nil {
		return err
	}
	if g.uncharted[system] {
		// Wrapped exactly as gategraph wraps it, so a pass that matched on the message instead of
		// errors.Is would still pass here and then fail in production the day the wording changes.
		return fmt.Errorf("%w for %s (%s-GATE): uncharted, skipped doomed live fetch",
			gategraph.ErrGateUnreadable, system, system)
	}
	// PERSISTED. Nothing is handed back: the engine learns what the read found by re-deriving from
	// the store on the next tick, which is what makes the recursion hold no state.
	g.stored[system] = g.live[system]
	return nil
}

func (g *gateStore) readCount(system string) int {
	n := 0
	for _, read := range g.reads {
		if read == system {
			n++
		}
	}
	return n
}

// runGateRead drives one tick with the deliberate gate reader wired.
func runGateRead(t *testing.T, h *expandHarness, gates *gateStore) (ExpandReport, error) {
	t.Helper()
	for i := range h.ledger.systems {
		h.ledger.systems[i].CatalogKnown = !h.unswept[h.ledger.systems[i].System]
	}
	p := h.ports()
	p.Gates = gates
	p.GateRead = gates
	return AdvanceExpansion(context.Background(), p, 1, ExpandKnobs{
		Enabled: true, MinBudgetRate: 0.05, Whitelist: h.whitelist,
	}, 1.0)
}

// td22Pocket is the live shape at the moment the fleet was sealed in.
//
// X1-KP42 is ours and its gate HAS been read — it names X1-TD22 and nothing else passable, which is
// what the 53-exits-under-construction pocket looks like from inside. X1-TD22 carries a screening row,
// a fully-swept and fully-charted catalogue, and A SEED IN FLIGHT, so seedlessTargets excludes it on
// BOTH of its clauses at once. We hold no adjacency for it whatsoever, and beyond it lies X1-RV70,
// which the ledger has never heard of.
func td22Pocket() (*expandHarness, *gateStore) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-KP42", Verdict: VerdictInScope},
		{
			System: "X1-TD22", Verdict: VerdictInScope,
			SeedShip: "PROBE-INFLIGHT", SeedState: SeedStateDispatched,
		},
	}
	gates := newGateStore()
	gates.stored["X1-KP42"] = []string{"X1-TD22"}
	// X1-TD22 is ABSENT from `stored`: no rows at all. That is the whole bug.
	gates.live["X1-TD22"] = []string{"X1-KP42", "X1-RV70"}
	gates.live["X1-RV70"] = []string{"X1-SN89", "X1-KF69", "X1-TU10", "X1-ZA91", "X1-TD22"}

	// A hull of ours standing in X1-KP42, so the frontier ordering has an origin to measure from.
	h.ledger.slots = []QueuedSlot{{
		Waypoint: "X1-KP42-A", System: "X1-KP42", Kind: SlotKindSpare,
		State: SlotStateParked, AssignedShip: "PROBE-HOME",
	}}
	return h, gates
}

// THE TEST THAT MATTERS. X1-TD22 is in scope, has a seed in flight, and its gate has never been read.
// It must be read anyway.
//
// This is the exact system, in the exact state, that left 33 systems undiscovered and seven of them
// wrongly written off as walled in. Under the seedlessTargets-derived behaviour it is not a candidate
// for anything gate-related, so nothing in the engine asks it a question.
func TestAdvanceExpansion_ReadsTheGateOfAnInScopeSystemThatAlreadyHasASeedInFlight(t *testing.T) {
	h, gates := td22Pocket()

	rep, err := runGateRead(t, h, gates)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gates.readCount("X1-TD22") != 1 {
		t.Fatalf("gate reads were %v, want X1-TD22 read exactly once — it is IN SCOPE with an ACTIVE "+
			"SEED IN FLIGHT and NO stored adjacency, which is precisely the state that hid a whole "+
			"region behind one in-flight probe; a charted gate is readable with no ship present, so "+
			"waiting for that hull to arrive was never necessary", gates.reads)
	}
	if rep.GatesRead != 1 {
		t.Fatalf("GatesRead = %d, want 1", rep.GatesRead)
	}
	// And the read must have PERSISTED, because that is the only way the next tick learns X1-RV70
	// exists at all.
	if got := gates.stored["X1-TD22"]; len(got) != 2 {
		t.Fatalf("after the read the store holds %v for X1-TD22, want its two connections persisted", got)
	}
}

// THE SECOND EXCLUSION PATH. A system with nothing left to chart is equally invisible to
// seedlessTargets — `UnchartedCount == 0 && CatalogKnown` fails the membership rule on its own — and
// its gate can be just as unread.
//
// It is the more insidious of the two, because a fully-charted system LOOKS finished. Charting says
// what is INSIDE a system; it says nothing about where the system connects unless a hull happened to
// stand on the gate itself.
func TestAdvanceExpansion_ReadsTheGateOfAFullyChartedInScopeSystem(t *testing.T) {
	h, gates := td22Pocket()
	// No seed anywhere: X1-TD22 is simply swept, fully charted, and done.
	h.ledger.systems[1].SeedShip, h.ledger.systems[1].SeedState = "", ""

	rep, err := runGateRead(t, h, gates)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gates.readCount("X1-TD22") != 1 {
		t.Fatalf("gate reads were %v, want X1-TD22 read exactly once — a fully-charted system with no "+
			"outstanding charting work still has an unread gate, and charting says what is INSIDE a "+
			"system, never where it connects", gates.reads)
	}
	if rep.GatesRead != 1 {
		t.Fatalf("GatesRead = %d, want 1", rep.GatesRead)
	}
}

// A SYSTEM WHOSE ADJACENCY WE ALREADY HOLD IS NOT RE-READ. This is what keeps the pass bounded by the
// FRONTIER rather than by the size of the map, and it is the property that would turn a helpful pass
// into a per-tick API storm over everything the fleet has ever charted.
//
// X1-KP42 is the case that matters: the store holds exactly ONE passable exit for it, which under a
// "does Neighbours look thin?" test would read as barely-known. It has been read; there is nothing to
// learn.
func TestAdvanceExpansion_DoesNotRereadASystemWhoseAdjacencyIsAlreadyStored(t *testing.T) {
	h, gates := td22Pocket()
	// And the harder case beside it: a system we HAVE read whose every exit is under construction, so
	// it reports NO traversable neighbours at all. Mapped and "Neighbours returned something" disagree
	// here, and only the first is the right question.
	h.ledger.systems = append(h.ledger.systems, ExpandSystem{System: "X1-SEALED", Verdict: VerdictInScope})
	gates.stored["X1-KP42"] = []string{"X1-TD22", "X1-SEALED"}
	gates.stored["X1-SEALED"] = nil

	if _, err := runGateRead(t, h, gates); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gates.readCount("X1-KP42") != 0 {
		t.Fatalf("re-read X1-KP42, whose adjacency the store already holds; reads were %v", gates.reads)
	}
	if gates.readCount("X1-SEALED") != 0 {
		t.Fatalf("re-read X1-SEALED, whose every exit is under construction; reads were %v — it has "+
			"been read and reports no traversable neighbours, which is a DIFFERENT fact from never "+
			"having been read, and only the second is worth an API call", gates.reads)
	}
}

// wideFrontier is a fixture with STRICTLY MORE unread gates than the per-tick cap admits, so the bound
// is actually consulted. Five candidates against MaxGateReads=3.
//
// Every candidate hangs off X1-HOME at the SAME distance, so this fixture isolates the cap: which
// three are chosen is decided by the symbol tiebreak alone and the ordering rule cannot mask a broken
// bound.
func wideFrontier() (*expandHarness, *gateStore) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{{System: "X1-HOME", Verdict: VerdictInScope}}
	gates := newGateStore()
	var ring []string
	for _, sys := range []string{"X1-A1", "X1-B2", "X1-C3", "X1-D4", "X1-E5"} {
		h.ledger.systems = append(h.ledger.systems, ExpandSystem{System: sys, Verdict: VerdictInScope})
		ring = append(ring, sys)
		gates.live[sys] = []string{"X1-HOME"}
	}
	gates.stored["X1-HOME"] = ring
	h.ledger.slots = []QueuedSlot{{
		Waypoint: "X1-HOME-A", System: "X1-HOME", Kind: SlotKindSpare,
		State: SlotStateParked, AssignedShip: "PROBE-HOME",
	}}
	return h, gates
}

// THE PER-TICK CAP IS RESPECTED. Five gates are outstanding and three may be read, so the tick reads
// three and reports the backlog honestly.
//
// A gate read is one GetJumpGate plus a per-edge construction probe, so an unbounded pass would turn
// the first tick after a cold cache — or the tick that opens a 33-system region — into a burst of
// hundreds of calls at exactly the moment the rest of the fleet is also working.
func TestAdvanceExpansion_ReadsNoMoreGatesThanThePerTickCap(t *testing.T) {
	h, gates := wideFrontier()

	rep, err := runGateRead(t, h, gates)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gates.reads) != MaxGateReads {
		t.Fatalf("read %d gates (%v) with 5 outstanding, want exactly MaxGateReads=%d — an unbounded "+
			"pass bursts hundreds of API calls on the tick that opens a new region", len(gates.reads), gates.reads, MaxGateReads)
	}
	if rep.GatesRead != MaxGateReads {
		t.Fatalf("GatesRead = %d, want %d", rep.GatesRead, MaxGateReads)
	}
	if rep.GatesUnread != 5 {
		t.Fatalf("GatesUnread = %d, want 5 — the report must carry the WHOLE backlog, not the part "+
			"this tick happened to absorb, or the heartbeat cannot show the frontier draining", rep.GatesUnread)
	}
}

// FRONTIER-FIRST, THEN SYMBOL. The queue the cap truncates is ordered by how near the gate is to a
// system we actually hold, and ties break on the symbol so the order is reproducible tick to tick.
//
// The fixture separates every key. X1-ZED is a system WE OCCUPY whose own gate is unread — zero hops,
// and the most valuable read on the board since a hull is already standing there — yet its symbol
// sorts last, so a pass that ordered on the symbol alone would leave it out entirely. Two systems sit
// one hop out and two more sit two hops out, and within each of those pairs only the symbol separates
// them.
//
//	X1-ZED (WE ARE HERE, gate unread)  ── X1-HOME ── X1-B1, X1-A2   (1 hop, both unread)
//	                                                   └── X1-D3, X1-C4  (2 hops, both unread)
//
// Three of the six are read. Ordering on distance alone leaves the pair choice to Go's map iteration
// and the same three would never be retried; ordering on the symbol alone reads X1-A2, X1-B1, X1-C4
// and never asks the system we are standing in.
func TestAdvanceExpansion_ReadsTheGatesNearestTheSystemsWeHoldFirstThenBySymbol(t *testing.T) {
	h := newExpandHarness()
	gates := newGateStore()
	for _, sys := range []string{"X1-ZED", "X1-HOME", "X1-B1", "X1-A2", "X1-D3", "X1-C4"} {
		h.ledger.systems = append(h.ledger.systems, ExpandSystem{System: sys, Verdict: VerdictInScope})
		gates.live[sys] = []string{"X1-HOME"}
	}
	// Only X1-HOME's adjacency is stored; every other system's gate is unread.
	gates.stored["X1-HOME"] = []string{"X1-B1", "X1-A2"}
	gates.stored["X1-B1"] = []string{"X1-D3", "X1-C4"}
	gates.stored["X1-A2"] = []string{}
	delete(gates.live, "X1-B1")
	delete(gates.live, "X1-A2")
	// X1-B1 and X1-A2 are mapped by the two lines above, so the unread set is
	// X1-ZED (0 hops, we hold it), X1-D3 and X1-C4 (2 hops, via X1-B1), and X1-HOME is mapped.
	// Give the one-hop tier its own two unread systems so the tiebreak is exercised at a
	// non-zero distance too.
	gates.stored["X1-HOME"] = []string{"X1-B1", "X1-A2", "X1-F1", "X1-E2"}
	for _, sys := range []string{"X1-F1", "X1-E2"} {
		h.ledger.systems = append(h.ledger.systems, ExpandSystem{System: sys, Verdict: VerdictInScope})
		gates.live[sys] = []string{"X1-HOME"}
	}

	h.ledger.slots = []QueuedSlot{{
		Waypoint: "X1-ZED-A", System: "X1-ZED", Kind: SlotKindSpare,
		State: SlotStateParked, AssignedShip: "PROBE-HERE",
	}, {
		Waypoint: "X1-HOME-A", System: "X1-HOME", Kind: SlotKindSpare,
		State: SlotStateParked, AssignedShip: "PROBE-HOME",
	}}

	if _, err := runGateRead(t, h, gates); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// X1-ZED is zero hops (we are standing in it); X1-E2 and X1-F1 are one hop and tie on distance,
	// so the symbol decides. The two-hop pair never gets a turn.
	want := []string{"X1-ZED", "X1-E2", "X1-F1"}
	if len(gates.reads) != len(want) {
		t.Fatalf("read %v, want %v", gates.reads, want)
	}
	for i := range want {
		if gates.reads[i] != want[i] {
			t.Fatalf("read %v, want %v — the queue is ordered nearest-first with a symbol tiebreak; a "+
				"system WE OCCUPY is zero hops from itself and is the most valuable read on the board, "+
				"and without the tiebreak the truncated tail depends on Go's map iteration order and "+
				"the same gates would be retried at random forever", gates.reads, want)
		}
	}
}

// AN UNCHARTED GATE 400s, AND THAT IS ORDINARY. It means "this one genuinely needs a hull", which is
// the API answering honestly rather than failing. The tick must survive it, must not let it consume
// the whole read budget's worth of usefulness, and must not confuse it with a real fault.
//
// The fixture puts the unreadable gate FIRST in the queue — nearest to the system we hold — so a pass
// that aborted on it, or that counted it as done and stopped, would starve both readable gates behind
// it. That is the shape that matters: an uncharted frontier gate is exactly the kind that sits nearest
// the edge of what we hold.
func TestAdvanceExpansion_AnUnchartedGateIsSkippedWithoutFailingTheTickOrTheReadsBehindIt(t *testing.T) {
	h := newExpandHarness()
	gates := newGateStore()
	for _, sys := range []string{"X1-HOME", "X1-A1", "X1-B2", "X1-C3"} {
		h.ledger.systems = append(h.ledger.systems, ExpandSystem{System: sys, Verdict: VerdictInScope})
		gates.live[sys] = []string{"X1-HOME"}
	}
	gates.stored["X1-HOME"] = []string{"X1-A1", "X1-B2", "X1-C3"}
	// X1-A1 sorts first and nobody has ever charted its gate.
	gates.uncharted["X1-A1"] = true
	h.ledger.slots = []QueuedSlot{{
		Waypoint: "X1-HOME-A", System: "X1-HOME", Kind: SlotKindSpare,
		State: SlotStateParked, AssignedShip: "PROBE-HOME",
	}}

	rep, err := runGateRead(t, h, gates)
	if err != nil {
		t.Fatalf("an uncharted gate failed the tick: %v — a gate nobody has charted 400s without a "+
			"hull on it, which is the API telling the truth, not an error", err)
	}
	if rep.GatesUnreadable != 1 {
		t.Fatalf("GatesUnreadable = %d, want 1 — the skip must be COUNTED, or a fleet whose frontier "+
			"is entirely uncharted looks identical to one with nothing left to read", rep.GatesUnreadable)
	}
	if rep.GatesFailed != 0 {
		t.Fatalf("GatesFailed = %d, want 0 — an uncharted gate is ORDINARY; recording it as a fault "+
			"turns the common case on a young frontier into a permanent alarm", rep.GatesFailed)
	}
	if rep.GatesRead != 2 {
		t.Fatalf("GatesRead = %d, want 2 — the two charted gates behind the uncharted one must still "+
			"be read in the same tick", rep.GatesRead)
	}
	if gates.readCount("X1-B2") != 1 || gates.readCount("X1-C3") != 1 {
		t.Fatalf("reads were %v, want both X1-B2 and X1-C3 read behind the skipped X1-A1", gates.reads)
	}
	// And it must not be retried within the tick: one attempt, then on to the next candidate.
	if gates.readCount("X1-A1") != 1 {
		t.Fatalf("X1-A1 was attempted %d times in one tick (%v) — an uncharted gate must be skipped, "+
			"never retry-stormed", gates.readCount("X1-A1"), gates.reads)
	}
}

// A REAL FAULT IS NOT AN UNCHARTED GATE, and neither one may poison the tick.
//
// The distinction is why the pass matches gategraph.ErrGateUnreadable with errors.Is instead of
// reading a status or a message: a store fault, an expired token or a 5xx is NOT the API saying "this
// gate needs a probe", and treating it as ordinary would mask it forever. It is still not worth
// failing a tick that commands hulls, spends credits and drives the off-gate fallback — this pass
// writes no ledger row and moves nothing, so a lost read costs information and nothing else, and the
// next tick re-derives and retries for free.
func TestAdvanceExpansion_AGateReadFaultIsCountedAndDoesNotFailTheTickOrTheReadsBehindIt(t *testing.T) {
	h := newExpandHarness()
	gates := newGateStore()
	for _, sys := range []string{"X1-HOME", "X1-A1", "X1-B2"} {
		h.ledger.systems = append(h.ledger.systems, ExpandSystem{System: sys, Verdict: VerdictInScope})
		gates.live[sys] = []string{"X1-HOME"}
	}
	gates.stored["X1-HOME"] = []string{"X1-A1", "X1-B2"}
	gates.readErr["X1-A1"] = errors.New("gate store unhappy")
	h.ledger.slots = []QueuedSlot{{
		Waypoint: "X1-HOME-A", System: "X1-HOME", Kind: SlotKindSpare,
		State: SlotStateParked, AssignedShip: "PROBE-HOME",
	}}

	rep, err := runGateRead(t, h, gates)
	if err != nil {
		t.Fatalf("a single failed gate read failed the whole tick: %v — this pass writes no ledger "+
			"row and moves no hull, so one bad gate must never stop the seed machinery, the spare "+
			"claim and the off-gate fallback", err)
	}
	if rep.GatesRead != 1 || gates.readCount("X1-B2") != 1 {
		t.Fatalf("GatesRead = %d and reads were %v, want X1-B2 still read behind the failure",
			rep.GatesRead, gates.reads)
	}
	if rep.GatesFailed != 1 {
		t.Fatalf("GatesFailed = %d, want 1 — a failed read is not a completed one", rep.GatesFailed)
	}
	if rep.GatesUnreadable != 0 {
		t.Fatalf("GatesUnreadable = %d, want 0 — a store fault is NOT the API saying 'this gate needs "+
			"a probe', and counting it there hides a broken dependency inside a number that is "+
			"supposed to be large on a young frontier", rep.GatesUnreadable)
	}
}

// RECURSIVE BY CONSTRUCTION. A gate read names systems the ledger has never heard of; those become
// PENDING rows, and on the very next tick their OWN gates are read.
//
// This is the property that turns one read of X1-TD22 into the whole 33-system region. Nothing in the
// selection may prevent a freshly-revealed system from being picked up: it has no verdict worth
// anything, no catalogue, no uncharted count, and no hull — exactly the profile the seedlessTargets
// rule filtered out.
func TestAdvanceExpansion_ANewlyRevealedNeighbourHasItsOwnGateReadOnTheFollowingTick(t *testing.T) {
	h, gates := td22Pocket()

	if _, err := runGateRead(t, h, gates); err != nil {
		t.Fatalf("tick 1: unexpected error: %v", err)
	}
	if gates.readCount("X1-RV70") != 0 {
		t.Fatalf("tick 1 read X1-RV70 before anything had named it; reads were %v", gates.reads)
	}

	// TICK 2 re-derives everything from the store, which now holds X1-TD22 -> X1-RV70.
	rep, err := runGateRead(t, h, gates)
	if err != nil {
		t.Fatalf("tick 2: unexpected error: %v", err)
	}
	if gates.readCount("X1-RV70") != 1 {
		t.Fatalf("tick 2 reads were %v, want X1-RV70 read — it was revealed by tick 1's read of "+
			"X1-TD22 and recorded PENDING, and reading ITS gate is what turns one gate read into the "+
			"33-system region beyond", gates.reads)
	}
	if gates.readCount("X1-TD22") != 1 {
		t.Fatalf("X1-TD22 was read %d times across two ticks (%v), want once — its adjacency is stored "+
			"now", gates.readCount("X1-TD22"), gates.reads)
	}
	// The neighbour was also recorded in the ledger, which is what puts it in front of the screen.
	pending := false
	for _, record := range h.ledger.upsertedSystem {
		if record.System == "X1-RV70" && record.Verdict == VerdictPending {
			pending = true
		}
	}
	if !pending {
		t.Fatalf("X1-RV70 was never recorded PENDING; upserts were %v", h.ledger.upsertedSystem)
	}
	if rep.GatesRead != 1 {
		t.Fatalf("tick 2 GatesRead = %d, want 1", rep.GatesRead)
	}
}

// AN UNREADABLE MAPPING READ FAILS THE TICK rather than quietly shrinking the candidate set.
//
// The fake answers "mapped" alongside its error, so a pass that swallowed it would decide the whole
// map is already known and read nothing at all — the fleet stops growing and the tick reports success.
// That is the exact silent failure this file exists to prevent, arriving through a database hiccup
// instead of an ordering bug.
func TestAdvanceExpansion_AnUnreadableGateMappingFailsTheGateReadTick(t *testing.T) {
	h, gates := td22Pocket()
	gates.mappedErr = errors.New("gate store unhappy")

	_, err := runGateRead(t, h, gates)
	if err == nil {
		t.Fatalf("the tick succeeded on an unreadable gate-mapping read — an unread store must never " +
			"present as 'this system's gate is already known'")
	}
	if len(gates.reads) != 0 {
		t.Fatalf("read %v off an unreadable mapping", gates.reads)
	}
}

// WITH NO READER WIRED THE TICK IS EXACTLY WHAT IT WAS. A nil port is a wiring gap, not a switch, and
// the pass degrades to doing nothing rather than panicking — the same contract OffGatePorts carries.
//
// It also pins the cost: with no reader there is nothing to build a candidate set for, so the pass
// must not spend a per-system Mapped read on the whole ledger either.
func TestAdvanceExpansion_WithNoGateReaderWiredTheTickIsUnchanged(t *testing.T) {
	h, gates := td22Pocket()

	for i := range h.ledger.systems {
		h.ledger.systems[i].CatalogKnown = true
	}
	p := h.ports()
	p.Gates = gates // GateRead deliberately left nil

	rep, err := AdvanceExpansion(context.Background(), p, 1, ExpandKnobs{
		Enabled: true, MinBudgetRate: 0.05, Whitelist: h.whitelist,
	}, 1.0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gates.reads) != 0 || rep.GatesRead != 0 || rep.GatesUnread != 0 {
		t.Fatalf("an unwired reader still did work: reads=%v report=%+v", gates.reads, rep)
	}
	if gates.mappedCalls != 0 {
		t.Fatalf("Mapped was read %d times with no reader wired — with nothing to read, there is no "+
			"candidate set to build and the ledger-wide mapping sweep must not happen at all", gates.mappedCalls)
	}
}
