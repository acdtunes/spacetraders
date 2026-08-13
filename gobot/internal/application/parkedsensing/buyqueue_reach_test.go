package parkedsensing

import (
	"context"
	"errors"
	"testing"
)

// reachPorts builds a drain whose fills sit in three systems: one the fleet can walk to, one it
// cannot, and one it is only "in" because a hull is already flying there.
//
// The footprint is X1-HOME, where a probe is PARKED. X1-NEXT is one gate hop away. X1-FAR has live
// adjacency of its own but no path back to the footprint — a disconnected component, which is what
// 65 of the 126 live unreachable systems actually are; a check that only asked "does this system
// have gate edges?" would pass it.
func reachPorts() (BuyPorts, *fakeBuyLedger, *fakeGates) {
	led := &fakeBuyLedger{
		slots: []QueuedSlot{
			// The footprint: a probe standing in X1-HOME.
			{Waypoint: "X1-HOME-M9", System: "X1-HOME", Kind: SlotKindMarket, State: SlotStateParked},
			// Reachable fill, one hop out.
			{Waypoint: "X1-NEXT-M1", System: "X1-NEXT", Kind: SlotKindMarket, State: SlotStateWanted, DepthCredits: 900},
			// Unreachable fill: its own component, no path home.
			{Waypoint: "X1-FAR-M1", System: "X1-FAR", Kind: SlotKindMarket, State: SlotStateWanted, DepthCredits: 900},
		},
		systems: []ScreenedSystem{
			{System: "X1-NEXT", DepthCredits: 900},
			{System: "X1-FAR", DepthCredits: 900},
		},
	}
	gates := &fakeGates{adjacency: map[string][]string{
		"X1-HOME": {"X1-NEXT"},
		"X1-NEXT": {"X1-HOME"},
		// X1-FAR is mapped and has a live edge — to another system in its own pocket.
		"X1-FAR":    {"X1-BEYOND"},
		"X1-BEYOND": {"X1-FAR"},
	}}
	return BuyPorts{Ledger: led, Gates: gates}, led, gates
}

func systemsOf(slots []QueuedSlot) []string {
	out := make([]string, 0, len(slots))
	for _, s := range slots {
		out = append(out, s.System)
	}
	return out
}

func hasSystem(systems []string, want string) bool {
	for _, s := range systems {
		if s == want {
			return true
		}
	}
	return false
}

// TestDrainCandidates_DoesNotFundAnUnreachableSystem is the bead.
//
// Neither the screen that declares a placement nor the queue that funds it asked whether a hull
// could ever walk there. Measured live: 1,214 of 6,959 WANTED slots (17.4%) name a system that is
// not reachable through the walker's own effective graph, and 5 already-funded slots are in flight
// to targets they can never reach.
//
// The ordering makes it systematic rather than incidental. drainCandidates ranks fills
// coverage-ascending, and an unreachable system has coverage EXACTLY ZERO — measured, not assumed:
// of the 1,214 unreachable WANTED slots across 126 systems, not one sits in a system holding any
// parked probe. So the impossible entries are promoted to the very head of the queue, ahead of all
// 5,745 reachable ones, at ~59,584 cr each for a hull that will never arrive.
func TestDrainCandidates_DoesNotFundAnUnreachableSystem(t *testing.T) {
	ports, _, _ := reachPorts()

	got, _, _, _, err := drainCandidates(context.Background(), ports, testPlayerID)
	if err != nil {
		t.Fatalf("drainCandidates returned error: %v", err)
	}

	systems := systemsOf(got)
	if hasSystem(systems, "X1-FAR") {
		t.Fatalf("funded a fill in X1-FAR, which no hull can walk to from the fleet's footprint. "+
			"Its coverage is zero precisely BECAUSE it is unreachable, so coverage-ascending ordering "+
			"promotes it to the head of the queue. got=%v", systems)
	}
	if !hasSystem(systems, "X1-NEXT") {
		t.Fatalf("dropped the REACHABLE fill in X1-NEXT — the filter must remove only what cannot be walked to. got=%v", systems)
	}
}

// TestDrainCandidates_AnInFlightTargetDoesNotSeedItsOwnFootprint pins the trap in choosing the
// walk's origin.
//
// The obvious footprint is "systems the ledger says we hold", and coverageBySystem already computes
// exactly that — but it counts BOUGHT and IN_TRANSIT alongside PARKED. Seeding the walk from those
// would let a hull already flying to an unreachable system mark that system reachable FROM ITSELF,
// which re-admits precisely the slots this filter exists to reject and makes the defect
// self-justifying. Only a PARKED slot is evidence a hull actually stands somewhere.
func TestDrainCandidates_AnInFlightTargetDoesNotSeedItsOwnFootprint(t *testing.T) {
	ports, led, _ := reachPorts()
	// A hull is already en route to the unreachable system — one of the live in-flight slots.
	led.slots = append(led.slots, QueuedSlot{
		Waypoint: "X1-FAR-M2", System: "X1-FAR", Kind: SlotKindMarket,
		State: SlotStateInTransit, AssignedShip: "PROBE-DOOMED",
	})

	got, _, _, _, err := drainCandidates(context.Background(), ports, testPlayerID)
	if err != nil {
		t.Fatalf("drainCandidates returned error: %v", err)
	}
	if hasSystem(systemsOf(got), "X1-FAR") {
		t.Fatalf("an IN_TRANSIT hull bound for X1-FAR made X1-FAR count as reachable, so the queue funded "+
			"MORE hulls for it. A hull in flight to a place it cannot reach is the defect, not evidence against it. got=%v", systemsOf(got))
	}
}

// TestDrainCandidates_FailsOpenWhenReachabilityIsUnknowable is the direction this guard must fail.
//
// Reachability is TIME-VARYING: a wall refreshes, a gate finishes construction, and a system that
// was unreachable becomes reachable. 179 walls churn on a 24h TTL against refresh at ~121
// systems/hour, so that movement is constant rather than hypothetical. A filter that fails CLOSED
// on an unreadable graph would stop the fleet buying anything at all for a tick — and a filter that
// PERSISTED its answer would blacklist a system permanently on one transient read.
//
// So an unreadable or unwired graph means "do not filter", never "reject everything".
func TestDrainCandidates_FailsOpenWhenReachabilityIsUnknowable(t *testing.T) {
	t.Run("the gate port is not wired at all", func(t *testing.T) {
		ports, _, _ := reachPorts()
		ports.Gates = nil

		got, _, _, _, err := drainCandidates(context.Background(), ports, testPlayerID)
		if err != nil {
			t.Fatalf("an unwired gate port must not fail the drain: %v", err)
		}
		if !hasSystem(systemsOf(got), "X1-NEXT") {
			t.Fatalf("an unwired gate port stopped the drain funding anything — it must fail OPEN. got=%v", systemsOf(got))
		}
	})

	t.Run("the gate store errors mid-walk", func(t *testing.T) {
		ports, _, gates := reachPorts()
		gates.err = errors.New("gate edge store unreachable")

		got, _, _, _, err := drainCandidates(context.Background(), ports, testPlayerID)
		if err != nil {
			t.Fatalf("an unreadable gate store must not fail the drain: %v", err)
		}
		if len(got) == 0 {
			t.Fatalf("an unreadable gate store blacklisted every system — one transient read must never stop the fleet buying")
		}
	})
}

// TestDrainCandidates_ReachabilityIsDerivedNeverPersisted pins RULINGS #2 structurally.
//
// The one thing this must never do is write an "unreachable" flag. Reachability is a property of
// the graph RIGHT NOW, and a system is unreachable until the moment a wall refreshes or a gate
// finishes — at which point a persisted verdict would keep it condemned forever, on evidence that
// has since expired. Asserted by watching the ledger: computing the filter must move no state at
// all, so there is nothing that could outlive the tick.
func TestDrainCandidates_ReachabilityIsDerivedNeverPersisted(t *testing.T) {
	ports, led, _ := reachPorts()

	if _, _, _, _, err := drainCandidates(context.Background(), ports, testPlayerID); err != nil {
		t.Fatalf("drainCandidates returned error: %v", err)
	}

	if len(led.transitions) != 0 {
		t.Fatalf("the reachability filter wrote %d ledger transition(s): %+v. It must derive per tick and "+
			"persist NOTHING — a stored 'unreachable' verdict outlives the staleness that produced it and "+
			"blacklists the system permanently.", len(led.transitions), led.transitions)
	}
}

// TestDrainCandidates_UnchartedIsNotUnreachable is the cold-start guard, and it is the reason this
// filter rejects on positive evidence rather than on absence of it.
//
// A system whose gates we have READ and which connects nowhere we stand is a dead end. A system we
// have never charted is UNKNOWN — and the two look identical to a walk, because neither appears in
// the reachable set. Conflating them turns a waste filter into a deadlock: on a fresh era, after a
// failed crawl, or behind a walled graph, almost nothing is reachable, and a filter that cannot
// tell "we looked and there is no way" from "we have not looked" stops the drain buying at all —
// starving the very charting that would make the targets reachable.
//
// The first cut of this change did exactly that: four coordinator tests went to `bought 0`. Live,
// the split is 65 genuinely disconnected systems against 59 merely unread.
func TestDrainCandidates_UnchartedIsNotUnreachable(t *testing.T) {
	ports, led, gates := reachPorts()
	// A fill in a system nobody has ever charted: absent from the graph entirely, so it is
	// neither passable-from nor mapped.
	led.slots = append(led.slots, QueuedSlot{
		Waypoint: "X1-UNKNOWN-M1", System: "X1-UNKNOWN", Kind: SlotKindMarket,
		State: SlotStateWanted, DepthCredits: 900,
	})
	led.systems = append(led.systems, ScreenedSystem{System: "X1-UNKNOWN", DepthCredits: 900})
	if _, mapped := gates.adjacency["X1-UNKNOWN"]; mapped {
		t.Fatalf("fixture error: X1-UNKNOWN must be absent from the graph for this test to mean anything")
	}

	got, _, _, _, err := drainCandidates(context.Background(), ports, testPlayerID)
	if err != nil {
		t.Fatalf("drainCandidates returned error: %v", err)
	}

	systems := systemsOf(got)
	if !hasSystem(systems, "X1-UNKNOWN") {
		t.Fatalf("refused to fund X1-UNKNOWN, which we have simply never charted. Unknown is not "+
			"impossible, and rejecting on absence of evidence is what makes this filter a cold-start "+
			"deadlock — a fresh era reaches almost nothing and would buy nothing. got=%v", systems)
	}
	// The genuinely-disconnected system is still refused: this widens nothing.
	if hasSystem(systems, "X1-FAR") {
		t.Fatalf("funded X1-FAR, which IS mapped and IS disconnected — the uncharted allowance must not "+
			"leak into systems we have positively read. got=%v", systems)
	}
}

// TestReachableSystems_ReportsWhenItCannotAnswer pins the predicate's own contract, because the
// filter above is DEFENCE IN DEPTH against it and therefore hides it.
//
// reachableFills rejects only mapped-and-disconnected systems, so an empty graph maps nothing and
// rejects nothing — which means a caller that wrongly treated "unreadable" as "answered, and the
// answer is empty" behaves identically to one that failed open, and no fixture through
// drainCandidates can tell them apart. That equivalence is a happy accident of today's rule, not a
// property to rely on: the moment the filter learns any rule that does not key on Mapped, a
// silently-fabricated empty answer becomes a silently-rejected fleet.
//
// So the honest ok=false is asserted where it lives.
func TestReachableSystems_ReportsWhenItCannotAnswer(t *testing.T) {
	t.Run("no gate port wired", func(t *testing.T) {
		ports, _, _ := reachPorts()
		ports.Gates = nil
		if _, _, ok := reachableSystems(context.Background(), ports, testPlayerID); ok {
			t.Fatalf("reported an answer with no gate port to read — an unwired port is unknowable, not empty")
		}
	})

	t.Run("the graph read failed", func(t *testing.T) {
		ports, _, gates := reachPorts()
		gates.err = errors.New("gate edge store unreachable")
		if _, _, ok := reachableSystems(context.Background(), ports, testPlayerID); ok {
			t.Fatalf("reported an answer from a failed graph read — the fake hands back a POPULATED graph " +
				"alongside its error precisely so a caller that ignores err is caught here")
		}
	})

	t.Run("no probe stands anywhere", func(t *testing.T) {
		ports, led, _ := reachPorts()
		led.slots = []QueuedSlot{{
			Waypoint: "X1-NEXT-M1", System: "X1-NEXT", Kind: SlotKindMarket, State: SlotStateWanted,
		}}
		if _, _, ok := reachableSystems(context.Background(), ports, testPlayerID); ok {
			t.Fatalf("reported an answer with no parked probe to walk FROM — a fleet that has not placed " +
				"its first probe has no origin, and 'nothing is reachable' would refuse every purchase")
		}
	})

	t.Run("a healthy graph does answer", func(t *testing.T) {
		ports, _, _ := reachPorts()
		_, reachable, ok := reachableSystems(context.Background(), ports, testPlayerID)
		if !ok {
			t.Fatalf("refused to answer on a perfectly readable graph")
		}
		if !reachable["X1-NEXT"] || reachable["X1-FAR"] {
			t.Fatalf("walk = %v, want X1-NEXT reachable and X1-FAR not", reachable)
		}
	})
}
