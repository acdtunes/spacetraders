package parkedsensing

import (
	"context"
	"testing"
	"time"
)

// saturation_test.go pins the ordering the Admiral's design turns on: "seed a
// system, find its shipyard, buy enough probes to park a probe in each
// marketplace, then seed the following system." A system the fleet has ENTERED
// is filled to saturation before a probe is spent entering a new one.
//
// EVERY TEST HERE CARRIES ITS OLD-BEHAVIOUR ASSERTION IN THE COMMENT, because
// the ordering it replaces was deliberate and documented: coverage-ascending,
// which put every untouched system ahead of any held system's SECOND placement.
// Measured live on 2026-08-08 that produced 118 probes over 104 systems — 1.1
// each — with 96 of those 104 systems still carrying 804 unfilled placements,
// behind 17,337 placements in systems no probe had ever visited.

// saturationPorts builds the pivotal fixture: X1-HELD is entered (one probe
// parked, three markets still dark) and X1-NEW has never been entered (two dark
// markets) and is the DEEPER of the two, so every sort key below the saturation
// tier points at X1-NEW.
func saturationPorts() (BuyPorts, *fakePurchaser) {
	led := &fakeBuyLedger{
		slots: []QueuedSlot{
			// The entered system, its parked probe first so the fixture cannot pass
			// by accident on ledger order alone.
			{Waypoint: "X1-HELD-P1", System: "X1-HELD", Kind: SlotKindMarket, State: SlotStateParked, AssignedShip: "PROBE-H1"},
			{Waypoint: "X1-HELD-M1", System: "X1-HELD", Kind: SlotKindMarket, State: SlotStateWanted},
			{Waypoint: "X1-HELD-M2", System: "X1-HELD", Kind: SlotKindMarket, State: SlotStateWanted},
			{Waypoint: "X1-HELD-M3", System: "X1-HELD", Kind: SlotKindMarket, State: SlotStateWanted},
			// The untouched system, richer.
			{Waypoint: "X1-NEW-M1", System: "X1-NEW", Kind: SlotKindMarket, State: SlotStateWanted},
			{Waypoint: "X1-NEW-M2", System: "X1-NEW", Kind: SlotKindMarket, State: SlotStateWanted},
		},
		systems: []ScreenedSystem{
			{System: "X1-HELD", DepthCredits: 3_000},
			{System: "X1-NEW", DepthCredits: 9_000},
		},
	}
	pur := &fakePurchaser{price: 1_000}
	return BuyPorts{
		Treasury:   &fakeTreasury{credits: 10_000_000},
		CargoSpend: &fakeCargoSpend{},
		Purchaser:  pur,
		Ledger:     led,
		Yards: &fakeYards{yards: map[string][]string{
			"X1-HELD": {"X1-HELD-Y1"},
			"X1-NEW":  {"X1-NEW-Y1"},
		}},
		Ships: &fakeShipReader{docked: map[string]string{
			"X1-HELD-Y1": "BUYER-HELD",
			"X1-NEW-Y1":  "BUYER-NEW",
		}},
		// Both systems walkable from the probe standing in X1-HELD, so the
		// reachability partition keeps every fill and the surface is READABLE —
		// which is what makes the paused-tick assertions below about a measured
		// surface rather than an unresolvable one.
		Gates: &fakeGates{adjacency: map[string][]string{
			"X1-HELD": {"X1-NEW"},
			"X1-NEW":  {"X1-HELD"},
		}},
		Fleet: &fakeFleet{},
	}, pur
}

func TestDrain_SaturatesAnEnteredSystemBeforeEnteringANewOne(t *testing.T) {
	// THE PIVOTAL ASSERTION. X1-HELD's three remaining markets are all bought
	// before X1-NEW is entered at all.
	//
	// THE OLD ORDER WAS THE EXACT INVERSE: coverage-ascending ranked X1-NEW's
	// first placement at 0 and its second at 1, while X1-HELD's three ranked at
	// 1, 2 and 3 — so the fleet bought NEW, NEW, HELD, HELD, HELD, taking a first
	// probe into the new system before finishing the one it already stood in.
	// With a real map that "new system" queue is 1,924 systems deep and no held
	// system's second placement is ever reached.
	ports, pur := saturationPorts()

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()}); err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}

	want := []string{"X1-HELD-Y1", "X1-HELD-Y1", "X1-HELD-Y1", "X1-NEW-Y1", "X1-NEW-Y1"}
	if len(pur.buys) != len(want) {
		t.Fatalf("made %d purchases, want %d: %v", len(pur.buys), len(want), pur.buys)
	}
	for i, w := range want {
		if pur.buys[i].yard != w {
			t.Fatalf("purchase %d was at %q, want %q (full order %v)", i, pur.buys[i].yard, w, pur.buys)
		}
	}
}

func TestDrain_TheHeldSystemNEARESTSaturationFinishesFirst(t *testing.T) {
	// Within the entered tier the system with the FEWEST placements left is
	// finished first, so systems complete and drop out of the queue one at a
	// time. Ordering the tier by coverage instead would round-robin the entered
	// systems — every one to two probes, then every one to three — which
	// saturates none of them until it has saturated all of them.
	//
	// X1-FAR is the DEEPER system and holds FEWER probes, so both of the keys
	// below this one point at it; only nearness-to-saturation puts X1-NEAR first.
	led := &fakeBuyLedger{
		slots: []QueuedSlot{
			{Waypoint: "X1-FAR-P1", System: "X1-FAR", Kind: SlotKindMarket, State: SlotStateParked, AssignedShip: "PROBE-F1"},
			{Waypoint: "X1-FAR-M1", System: "X1-FAR", Kind: SlotKindMarket, State: SlotStateWanted},
			{Waypoint: "X1-FAR-M2", System: "X1-FAR", Kind: SlotKindMarket, State: SlotStateWanted},
			{Waypoint: "X1-FAR-M3", System: "X1-FAR", Kind: SlotKindMarket, State: SlotStateWanted},
			{Waypoint: "X1-NEAR-P1", System: "X1-NEAR", Kind: SlotKindMarket, State: SlotStateParked, AssignedShip: "PROBE-N1"},
			{Waypoint: "X1-NEAR-P2", System: "X1-NEAR", Kind: SlotKindMarket, State: SlotStateParked, AssignedShip: "PROBE-N2"},
			{Waypoint: "X1-NEAR-M1", System: "X1-NEAR", Kind: SlotKindMarket, State: SlotStateWanted},
		},
		systems: []ScreenedSystem{
			{System: "X1-FAR", DepthCredits: 9_000},
			{System: "X1-NEAR", DepthCredits: 1_000},
		},
	}
	pur := &fakePurchaser{price: 1_000}
	ports := BuyPorts{
		Treasury:   &fakeTreasury{credits: 10_000_000},
		CargoSpend: &fakeCargoSpend{},
		Purchaser:  pur,
		Ledger:     led,
		Yards: &fakeYards{yards: map[string][]string{
			"X1-FAR":  {"X1-FAR-Y1"},
			"X1-NEAR": {"X1-NEAR-Y1"},
		}},
		Ships: &fakeShipReader{docked: map[string]string{
			"X1-FAR-Y1":  "BUYER-FAR",
			"X1-NEAR-Y1": "BUYER-NEAR",
		}},
		Fleet: &fakeFleet{},
	}

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()}); err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}

	want := []string{"X1-NEAR-Y1", "X1-FAR-Y1", "X1-FAR-Y1", "X1-FAR-Y1"}
	if len(pur.buys) != len(want) {
		t.Fatalf("made %d purchases, want %d: %v", len(pur.buys), len(want), pur.buys)
	}
	for i, w := range want {
		if pur.buys[i].yard != w {
			t.Fatalf("purchase %d was at %q, want %q (full order %v)", i, pur.buys[i].yard, w, pur.buys)
		}
	}
}

func TestDrain_UnenteredSystemsKeepTheirCoverageOrderingAmongThemselves(t *testing.T) {
	// The saturation tier changes which systems go FIRST, never how the systems
	// nobody has entered are ordered against each other. With no held system in
	// the fixture the queue must behave EXACTLY as it did before this tier
	// existed: coverage-ascending, depth as the tiebreak, so the deeper system's
	// FIRST placement leads and its SECOND still waits behind the shallower
	// system's first.
	//
	// The anti-vacuity control for the two tests above: a change that simply
	// reversed the order, or that read "held" as "has any placement at all",
	// fails here.
	led := &fakeBuyLedger{
		slots: []QueuedSlot{
			{Waypoint: "X1-DEEP-M1", System: "X1-DEEP", Kind: SlotKindMarket, State: SlotStateWanted},
			{Waypoint: "X1-DEEP-M2", System: "X1-DEEP", Kind: SlotKindMarket, State: SlotStateWanted},
			{Waypoint: "X1-SHALLOW-M1", System: "X1-SHALLOW", Kind: SlotKindMarket, State: SlotStateWanted},
		},
		systems: []ScreenedSystem{
			{System: "X1-DEEP", DepthCredits: 9_000},
			{System: "X1-SHALLOW", DepthCredits: 1_000},
		},
	}
	pur := &fakePurchaser{price: 1_000}
	ports := BuyPorts{
		Treasury:   &fakeTreasury{credits: 10_000_000},
		CargoSpend: &fakeCargoSpend{},
		Purchaser:  pur,
		Ledger:     led,
		Yards: &fakeYards{yards: map[string][]string{
			"X1-DEEP":    {"X1-DEEP-Y1"},
			"X1-SHALLOW": {"X1-SHALLOW-Y1"},
		}},
		Ships: &fakeShipReader{docked: map[string]string{
			"X1-DEEP-Y1":    "BUYER-DEEP",
			"X1-SHALLOW-Y1": "BUYER-SHALLOW",
		}},
		Fleet: &fakeFleet{},
	}

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()}); err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}

	want := []string{"X1-DEEP-Y1", "X1-SHALLOW-Y1", "X1-DEEP-Y1"}
	if len(pur.buys) != len(want) {
		t.Fatalf("made %d purchases, want %d: %v", len(pur.buys), len(want), pur.buys)
	}
	for i, w := range want {
		if pur.buys[i].yard != w {
			t.Fatalf("purchase %d was at %q, want %q (full order %v)", i, pur.buys[i].yard, w, pur.buys)
		}
	}
}

func TestDrain_SaturationBacklogIsPublishedWhileSpendingIsPaused(t *testing.T) {
	// BLOCKED-BY-HOLD MUST NOT LOOK LIKE BLOCKED-BY-BUG. Under the Admiral's hold
	// (SpendEnabled off) the queue buys nothing, and the demand it is holding must
	// still be VISIBLE: the ordering runs, the surface is measured, and the
	// heartbeat publishes a non-zero backlog beside SpendingPaused.
	//
	// Without this the two states are indistinguishable — an engine that computes
	// no demand and an engine forbidden to act on the demand it computed both
	// report zeros — which is the blindness that let this defect run unseen.
	ports, pur := saturationPorts()

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: false, ProbeCap: 100}, fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}

	if !rep.SpendingPaused {
		t.Fatalf("SpendingPaused = false, want true with SpendEnabled off")
	}
	if len(pur.buys) != 0 {
		t.Fatalf("bought %d probes while paused, want 0: %v", len(pur.buys), pur.buys)
	}
	// Five dark markets in the fixture: three in the entered system, two in the
	// system nobody has entered.
	if rep.DarkMarkets != 5 {
		t.Fatalf("DarkMarkets = %d, want 5 while paused", rep.DarkMarkets)
	}
	// Of those, the three in X1-HELD are the SATURATION backlog — the number this
	// ordering exists to drive to zero, and the one an operator watches.
	if rep.DarkMarketsHeld != 3 {
		t.Fatalf("DarkMarketsHeld = %d, want 3 while paused", rep.DarkMarketsHeld)
	}
	if !rep.DarkMarketsReadable {
		t.Fatalf("DarkMarketsReadable = false, want true — the surface was measured")
	}
}

func TestDrain_ABlindReachabilityReadIsNotReportedAsReadable(t *testing.T) {
	// UNKNOWN IS NOT "EVERYTHING IS WALKABLE". With no gate port the reachability
	// walk cannot be resolved at all, so the queue filters nothing (it fails OPEN,
	// deliberately) and the surface must SAY it could not answer: the whole thing
	// falls into DarkMarkets unpartitioned, and unreached reads 0 because nothing
	// was measured rather than because nothing is out of reach.
	//
	// Without this assertion the readable flag could be hard-wired true and every
	// blind tick would publish a confident zero — the flag's only job is to keep
	// that zero from being read as a verdict.
	ports, _ := saturationPorts()
	ports.Gates = nil

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: false, ProbeCap: 100}, fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}

	if rep.DarkMarketsReadable {
		t.Fatalf("DarkMarketsReadable = true with no gate port; an unresolvable walk must report itself")
	}
	if rep.DarkMarketsUnreached != 0 {
		t.Fatalf("DarkMarketsUnreached = %d on a blind read, want 0 — nothing was partitioned", rep.DarkMarketsUnreached)
	}
	// And the demand itself is still published: a blind reachability read is no
	// reason to stop reporting how much of the map is dark.
	if rep.DarkMarkets != 5 {
		t.Fatalf("DarkMarkets = %d on a blind read, want 5 (the whole surface, unpartitioned)", rep.DarkMarkets)
	}
}

func TestDrain_AnUnreachableDarkMarketIsCountedApartFromAnInReachOne(t *testing.T) {
	// The surface separates what the fleet COULD fill from what it cannot walk
	// to. Published as one number the two are indistinguishable, and a fleet
	// sealed behind an unbuilt gate reads identically to one simply not buying.
	//
	// X1-WALLED is MAPPED and connects to nothing we stand in, which is the only
	// evidence that justifies calling a system unreachable (see reachableFills).
	led := &fakeBuyLedger{
		slots: []QueuedSlot{
			{Waypoint: "X1-HOME-P1", System: "X1-HOME", Kind: SlotKindMarket, State: SlotStateParked, AssignedShip: "PROBE-H1"},
			{Waypoint: "X1-HOME-M1", System: "X1-HOME", Kind: SlotKindMarket, State: SlotStateWanted},
			{Waypoint: "X1-WALLED-M1", System: "X1-WALLED", Kind: SlotKindMarket, State: SlotStateWanted},
			{Waypoint: "X1-WALLED-M2", System: "X1-WALLED", Kind: SlotKindMarket, State: SlotStateWanted},
		},
		systems: []ScreenedSystem{
			{System: "X1-HOME", DepthCredits: 1_000},
			{System: "X1-WALLED", DepthCredits: 9_000},
		},
	}
	pur := &fakePurchaser{price: 1_000}
	ports := BuyPorts{
		Treasury:   &fakeTreasury{credits: 10_000_000},
		CargoSpend: &fakeCargoSpend{},
		Purchaser:  pur,
		Ledger:     led,
		// X1-WALLED has live adjacency of its own but no path back to the footprint
		// — a disconnected component, which is the only shape that is positive
		// evidence of unreachability rather than an unread map.
		Gates: &fakeGates{adjacency: map[string][]string{
			"X1-HOME":   nil,
			"X1-WALLED": {"X1-BEYOND"},
			"X1-BEYOND": {"X1-WALLED"},
		}},
		Yards: &fakeYards{yards: map[string][]string{
			"X1-HOME":   {"X1-HOME-Y1"},
			"X1-WALLED": {"X1-WALLED-Y1"},
		}},
		Ships: &fakeShipReader{docked: map[string]string{
			"X1-HOME-Y1":   "BUYER-HOME",
			"X1-WALLED-Y1": "BUYER-WALLED",
		}},
		Fleet: &fakeFleet{},
	}

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}

	if rep.DarkMarkets != 1 {
		t.Fatalf("DarkMarkets = %d, want 1 (only X1-HOME's market is walkable)", rep.DarkMarkets)
	}
	if rep.DarkMarketsUnreached != 2 {
		t.Fatalf("DarkMarketsUnreached = %d, want 2 (both of X1-WALLED's)", rep.DarkMarketsUnreached)
	}
	if rep.DarkMarketsHeld != 1 {
		t.Fatalf("DarkMarketsHeld = %d, want 1", rep.DarkMarketsHeld)
	}
}
