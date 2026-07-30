package parkedsensing

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// --- cross-system buying: the placement's own system has no counter ---
//
// THE FIXTURE IS DELIBERATELY ASYMMETRIC, and both one-way edges earn their place:
//
//	X1-SRC -> X1-TGT   (no reverse) — a hull bought at SRC CAN fly to the target
//	X1-TGT -> X1-DEC   (no reverse) — a hull bought at DEC can NEVER arrive
//
// X1-DEC is a full decoy: probe yard, docked buyer, parked placement, and a
// CHEAPER hull. It is reachable FROM the target, so a search that walks forward
// out of the target — which is what the reverted 4f4ca8ce did — offers DEC and
// strands the hull. Only a search asking "can this source reach the target?"
// picks SRC. A symmetric fixture could not tell the two apart.

func ferryGates() *fakeGates {
	return &fakeGates{adjacency: map[string][]string{
		"X1-SRC": {"X1-TGT"},
		"X1-TGT": {"X1-DEC"},
		"X1-DEC": {},
	}}
}

// ferryPorts wires a target system with NO probe yard, a reachable funder (SRC)
// and an unreachable decoy funder (DEC).
func ferryPorts() (BuyPorts, *fakeBuyLedger, *fakePurchaser) {
	led := &fakeBuyLedger{
		slots: []QueuedSlot{
			// The placement that cannot fund itself: X1-TGT has no yard at all.
			{Waypoint: "X1-TGT-M1", System: "X1-TGT", Kind: SlotKindMarket, State: SlotStateWanted},
			// Our presence: a parked hull in each funder system, so BOTH are in the
			// broker's candidate set and only reachability can separate them.
			{Waypoint: "X1-SRC-M1", System: "X1-SRC", Kind: SlotKindMarket, State: SlotStateParked,
				AssignedShip: "SRC-PROBE", WhitelistGoods: []string{"FUEL"}},
			{Waypoint: "X1-DEC-M1", System: "X1-DEC", Kind: SlotKindMarket, State: SlotStateParked,
				AssignedShip: "DEC-PROBE", WhitelistGoods: []string{"FUEL"}},
		},
		systems: []ScreenedSystem{{System: "X1-TGT", DepthCredits: 900}},
	}
	pur := &fakePurchaser{price: 24_356}
	return BuyPorts{
		Treasury:   &fakeTreasury{credits: 3_800_000},
		CargoSpend: &fakeCargoSpend{},
		Purchaser:  pur,
		Ledger:     led,
		Yards: &fakeYards{yards: map[string][]string{
			// X1-TGT deliberately absent — the whole point.
			"X1-SRC": {"X1-SRC-Y1"},
			"X1-DEC": {"X1-DEC-Y1"},
		}},
		Ships: &fakeShipReader{docked: map[string]string{
			"X1-SRC-Y1": "SRC-PROBE",
			"X1-DEC-Y1": "DEC-PROBE",
		}},
		Fleet: &fakeFleet{},
		Gates: ferryGates(),
	}, led, pur
}

func TestDrain_BuysAtAReachableRemoteYardWhenTheTargetSystemHasNoCounter(t *testing.T) {
	ports, led, pur := ferryPorts()

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}

	// ASSERTED ON THE ATTEMPT, not on "0 bought stopped happening": a tick that
	// merely stopped short would also report no purchase, so the attempt count is
	// what proves the buy path was actually entered.
	if rep.Attempts == 0 {
		t.Fatalf("Attempts = 0 — the placement never reached a counter at all; report %+v", rep)
	}
	if len(pur.quotes) == 0 || pur.quotes[0] != "X1-SRC-Y1" {
		t.Fatalf("quotes = %v, want the REACHABLE remote yard X1-SRC-Y1 first", pur.quotes)
	}
	if len(pur.buys) != 1 || pur.buys[0].yard != "X1-SRC-Y1" {
		t.Fatalf("buys = %v, want exactly one purchase at X1-SRC-Y1", pur.buys)
	}
	if rep.Bought != 1 || rep.Ferried != 1 {
		t.Fatalf("Bought=%d Ferried=%d, want 1/1 — a cross-system buy is a subset of Bought", rep.Bought, rep.Ferried)
	}
	// The drain's own outcome is BOUGHT with the hull named; the placement machine
	// carries it to IN_TRANSIT and onward, one RouteAcross step per tick.
	got := slotAt(t, led, "X1-TGT-M1", SlotKindMarket)
	if got.State != SlotStateBought || got.AssignedShip == "" {
		t.Fatalf("target slot = %+v, want BOUGHT naming the hull the placement machine will fly", got)
	}
}

func TestDrain_NeverBuysAtASourceThatCannotReachThePlacement(t *testing.T) {
	// The decoy alone. X1-DEC has a yard, a docked buyer and a parked hull, and the
	// TARGET can reach it — but it cannot reach the target, so a hull bought there
	// would strand. Buying nothing is the correct outcome.
	ports, led, pur := ferryPorts()
	ports.Gates = &fakeGates{adjacency: map[string][]string{
		"X1-TGT": {"X1-DEC"},
		"X1-DEC": {},
	}}
	led.slots = []QueuedSlot{
		{Waypoint: "X1-TGT-M1", System: "X1-TGT", Kind: SlotKindMarket, State: SlotStateWanted},
		{Waypoint: "X1-DEC-M1", System: "X1-DEC", Kind: SlotKindMarket, State: SlotStateParked,
			AssignedShip: "DEC-PROBE", WhitelistGoods: []string{"FUEL"}},
	}

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.buys) != 0 {
		t.Fatalf("bought %v at a source that cannot reach the placement — that hull would strand", pur.buys)
	}
	if rep.Bought != 0 || rep.Ferried != 0 {
		t.Fatalf("report = %+v, want no purchase", rep)
	}
	if got := slotAt(t, led, "X1-TGT-M1", SlotKindMarket); got.State != SlotStateWanted {
		t.Fatalf("target slot = %+v, want it left WANTED for a later tick", got)
	}
}

func TestDrain_ALocalCounterIsPreferredAndTheGateStoreIsNeverRead(t *testing.T) {
	// Local-first, and the proof is that the topology is never consulted: the
	// short-circuit is what keeps the ordinary fill exactly as cheap as before.
	ports, led, pur := ferryPorts()
	gates := ferryGates()
	ports.Gates = gates
	ports.Yards = &fakeYards{yards: map[string][]string{
		"X1-TGT": {"X1-TGT-Y1"}, // the target CAN fund itself now
		"X1-SRC": {"X1-SRC-Y1"},
		"X1-DEC": {"X1-DEC-Y1"},
	}}
	ports.Ships = &fakeShipReader{docked: map[string]string{
		"X1-TGT-Y1": "TGT-PROBE",
		"X1-SRC-Y1": "SRC-PROBE",
		"X1-DEC-Y1": "DEC-PROBE",
	}}

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.buys) != 1 || pur.buys[0].yard != "X1-TGT-Y1" {
		t.Fatalf("buys = %v, want the LOCAL counter X1-TGT-Y1", pur.buys)
	}
	if rep.Ferried != 0 {
		t.Fatalf("Ferried = %d, want 0 — a locally-fundable placement never crosses a gate", rep.Ferried)
	}
	if gates.calls != 0 {
		t.Fatalf("gate store read %d times for a locally-fundable placement — the local short-circuit is gone", gates.calls)
	}
	_ = led
}

func TestDrain_NoRoutableFunderBuysNothingAndTheTickContinues(t *testing.T) {
	// No presence anywhere but the target's own unfundable system: nothing to buy
	// through at any distance. The drain must return cleanly rather than error.
	ports, led, pur := ferryPorts()
	led.slots = []QueuedSlot{
		{Waypoint: "X1-TGT-M1", System: "X1-TGT", Kind: SlotKindMarket, State: SlotStateWanted},
	}

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("a placement with no routable funder must not error the tick: %v", err)
	}
	if len(pur.buys) != 0 || rep.Bought != 0 {
		t.Fatalf("bought %v with no funder anywhere", pur.buys)
	}
	if rep.SkippedNoYard == 0 {
		t.Fatalf("report = %+v, want the placement recorded as skipped for want of a counter", rep)
	}
}

func TestDrain_APlacementAlreadyInFlightIsNeverBoughtASecondHull(t *testing.T) {
	// Idempotence. A slot past QUEUED already names a hull we have paid for, so it
	// must never re-enter the buy path — drainCandidates admits WANTED and QUEUED
	// only, and this pins that a hull in flight is not re-bought.
	ports, led, pur := ferryPorts()
	led.slots = []QueuedSlot{
		{Waypoint: "X1-TGT-M1", System: "X1-TGT", Kind: SlotKindMarket, State: SlotStateInTransit,
			AssignedShip: "ALREADY-BOUGHT"},
		{Waypoint: "X1-SRC-M1", System: "X1-SRC", Kind: SlotKindMarket, State: SlotStateParked,
			AssignedShip: "SRC-PROBE", WhitelistGoods: []string{"FUEL"}},
	}

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.buys) != 0 || rep.Bought != 0 {
		t.Fatalf("bought %v for a placement whose hull is already in flight", pur.buys)
	}
	if got := slotAt(t, led, "X1-TGT-M1", SlotKindMarket); got.AssignedShip != "ALREADY-BOUGHT" {
		t.Fatalf("in-flight slot was disturbed: %+v", got)
	}
}

// --- reach: the buy must walk as far as the ROUTER can actually deliver ---

// chainGates builds a one-way chain X1-F -> X1-H1 -> X1-H2 -> ... -> X1-Hn.
// The funder X1-F sits at the head; the placement sits at the tail, n hops out.
//
// ONE-WAY THROUGHOUT, deliberately: it keeps the direction fix honest at every
// ring, so a search that ever walked backwards out of the target would find
// nothing rather than accidentally succeeding on a symmetric edge.
func chainGates(n int) *fakeGates {
	adj := map[string][]string{"X1-F": {"X1-H1"}}
	for i := 1; i < n; i++ {
		adj[fmt.Sprintf("X1-H%d", i)] = []string{fmt.Sprintf("X1-H%d", i+1)}
	}
	adj[fmt.Sprintf("X1-H%d", n)] = []string{}
	return &fakeGates{adjacency: adj}
}

// chainPorts puts the only funder at the head of an n-hop chain and the only
// pending placement at its tail.
func chainPorts(n int) (BuyPorts, *fakeBuyLedger, *fakePurchaser) {
	target := fmt.Sprintf("X1-H%d", n)
	led := &fakeBuyLedger{
		slots: []QueuedSlot{
			{Waypoint: target + "-M1", System: target, Kind: SlotKindMarket, State: SlotStateWanted},
			{Waypoint: "X1-F-M1", System: "X1-F", Kind: SlotKindMarket, State: SlotStateParked,
				AssignedShip: "F-PROBE", WhitelistGoods: []string{"FUEL"}},
		},
		systems: []ScreenedSystem{{System: target, DepthCredits: 900}},
	}
	pur := &fakePurchaser{price: 24_356}
	return BuyPorts{
		Treasury:   &fakeTreasury{credits: 3_800_000},
		CargoSpend: &fakeCargoSpend{},
		Purchaser:  pur,
		Ledger:     led,
		Yards:      &fakeYards{yards: map[string][]string{"X1-F": {"X1-F-Y1"}}},
		Ships:      &fakeShipReader{docked: map[string]string{"X1-F-Y1": "F-PROBE"}},
		Fleet:      &fakeFleet{},
		Gates:      chainGates(n),
	}, led, pur
}

func TestDrain_BuysForAPlacementBeyondThePlacementWalksOwnRing(t *testing.T) {
	// FOUR hops: past MaxWalkRings (2), well inside the router's real reach.
	//
	// The bound this path needs is not "how far does the foothold draw a scanning
	// hull" but "how far can nextHopToward actually name a next system" — and the
	// adapter resolves that at maxFerryHops. Measured live, 143 of 238 pending
	// placements sit at 3-9 hops from their nearest funder: routable, and refused
	// only by a bound that was never the router's.
	ports, led, pur := chainPorts(4)

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if rep.Attempts == 0 {
		t.Fatalf("Attempts = 0 — a placement 4 hops from the only funder never reached a counter; report %+v", rep)
	}
	if len(pur.buys) != 1 || pur.buys[0].yard != "X1-F-Y1" {
		t.Fatalf("buys = %v, want one purchase at the 4-hop-distant funder X1-F-Y1", pur.buys)
	}
	if rep.Ferried != 1 {
		t.Fatalf("Ferried = %d, want 1", rep.Ferried)
	}
	if got := slotAt(t, led, "X1-H4-M1", SlotKindMarket); got.State != SlotStateBought || got.AssignedShip == "" {
		t.Fatalf("target slot = %+v, want BOUGHT naming the hull the placement machine will fly", got)
	}
}

func TestDrain_BuysAtTheOuterEdgeOfTheRoutersReach(t *testing.T) {
	// Exactly maxFerryHops. The boundary is asserted from inside so the bound
	// is a real threshold rather than an artefact of a short fixture.
	ports, _, pur := chainPorts(maxFerryHops)

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()}); err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.buys) != 1 {
		t.Fatalf("buys = %v, want a purchase for a placement exactly %d hops out — the router resolves that far",
			pur.buys, maxFerryHops)
	}
}

func TestDrain_RefusesAPlacementBeyondWhatTheRouterCanResolve(t *testing.T) {
	// One hop past the router's reach. nextHopToward would name NO next system,
	// so the hull would sit IN_TRANSIT forever holding probe-cap headroom while
	// never arriving — strictly worse than not buying. The tick must continue.
	ports, led, pur := chainPorts(maxFerryHops + 1)

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("a placement beyond reach must not error the tick: %v", err)
	}
	if len(pur.buys) != 0 || rep.Bought != 0 {
		t.Fatalf("bought %v for a placement the router cannot route to — that hull would strand", pur.buys)
	}
	target := fmt.Sprintf("X1-H%d-M1", maxFerryHops+1)
	if got := slotAt(t, led, target, SlotKindMarket); got.State != SlotStateWanted {
		t.Fatalf("target slot = %+v, want it left WANTED rather than claimed for an unroutable buy", got)
	}
}
