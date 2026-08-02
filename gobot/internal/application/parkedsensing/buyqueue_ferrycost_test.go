package parkedsensing

import (
	"context"
	"testing"
	"time"

	domainSensing "github.com/andrescamacho/spacetraders-go/internal/domain/parkedsensing"
)

// --- sp-e46yc: the buy floor binds on LANDED cost, not sticker ----------------
//
// THE FIXTURE IS TUNED TO SEPARATE THREE HOPS FROM BOTH NEIGHBOURS, which is the
// whole point of it and the reason the arithmetic is spelled out. Floor 50,000
// (immutable only: no capex reserve, no cargo runway), quote 24,356, treasury
// 85,000, gate fee 5,900:
//
//	0 hops (local)   landed 24,356   →  85,000 − 24,356 = 60,644  ≥ 50,000  BUYS
//	1 hop            landed 30,256   →  85,000 − 30,256 = 54,744  ≥ 50,000  BUYS
//	3 hops (ferried) landed 42,056   →  85,000 − 42,056 = 42,944  < 50,000  REFUSED
//
// The 1-hop row is the one that earns its keep. FerryHops charges a one-gate
// MINIMUM whenever `ferried` is set, so a fixture that only separated "some ferry
// cost" from "none" would pass even if the walked hop count were never read at
// all — the minimum alone would carry it. At this treasury one hop still buys, so
// the refusal below can only come from the count actually being 3.

// ferryDistanceGates is a straight three-hop chain: the only funder is exactly
// three gates from the placement it funds.
//
//	X1-SRC → X1-H1 → X1-H2 → X1-TGT
func ferryDistanceGates() *fakeGates {
	return &fakeGates{adjacency: map[string][]string{
		"X1-SRC": {"X1-H1"},
		"X1-H1":  {"X1-H2"},
		"X1-H2":  {"X1-TGT"},
		"X1-TGT": {},
	}}
}

// remoteCounterPorts puts the only probe yard three gates from the placement, so
// every purchase must be ferried.
func remoteCounterPorts(treasury int64) (BuyPorts, *fakePurchaser) {
	led := &fakeBuyLedger{
		slots: []QueuedSlot{
			{Waypoint: "X1-TGT-M1", System: "X1-TGT", Kind: SlotKindMarket, State: SlotStateWanted},
			// Our presence at the funder, so the ferry broker has a candidate source.
			{Waypoint: "X1-SRC-M1", System: "X1-SRC", Kind: SlotKindMarket, State: SlotStateParked,
				AssignedShip: "SRC-PROBE", WhitelistGoods: []string{"FUEL"}},
		},
		systems: []ScreenedSystem{{System: "X1-TGT", DepthCredits: 900}},
	}
	pur := &fakePurchaser{price: 24_356}
	return BuyPorts{
		Treasury:   &fakeTreasury{credits: treasury},
		CargoSpend: &fakeCargoSpend{},
		Purchaser:  pur,
		Ledger:     led,
		// X1-TGT deliberately has no yard: the buy can only happen at range.
		Yards: &fakeYards{yards: map[string][]string{"X1-SRC": {"X1-SRC-Y1"}}},
		Ships: &fakeShipReader{docked: map[string]string{"X1-SRC-Y1": "SRC-PROBE"}},
		Fleet: &fakeFleet{},
		Gates: ferryDistanceGates(),
	}, pur
}

// localCounterPorts is the same placement, same price, same treasury, bought at a
// counter in its OWN system — the control that proves the refusal below is about
// distance and nothing else.
func localCounterPorts(treasury int64) (BuyPorts, *fakePurchaser) {
	led := &fakeBuyLedger{
		slots: []QueuedSlot{
			{Waypoint: "X1-TGT-M1", System: "X1-TGT", Kind: SlotKindMarket, State: SlotStateWanted},
		},
		systems: []ScreenedSystem{{System: "X1-TGT", DepthCredits: 900}},
	}
	pur := &fakePurchaser{price: 24_356}
	return BuyPorts{
		Treasury:   &fakeTreasury{credits: treasury},
		CargoSpend: &fakeCargoSpend{},
		Purchaser:  pur,
		Ledger:     led,
		Yards:      &fakeYards{yards: map[string][]string{"X1-TGT": {"X1-TGT-Y1"}}},
		Ships:      &fakeShipReader{docked: map[string]string{"X1-TGT-Y1": "TGT-PROBE"}},
		Fleet:      &fakeFleet{},
		Gates:      ferryDistanceGates(),
	}, pur
}

// ferryFloorKnobs leave the floor at the immutable 50,000 exactly, so the test's
// arithmetic has one term the fixture controls and no hidden ones.
var ferryFloorKnobs = BuyKnobs{SpendEnabled: true, ProbeCap: 100, CapexReserve: 0, KMilli: 0}

const ferryTestTreasury int64 = 85_000

// TestDrain_LocalCounterIsAffordableAtThisTreasury is the POSITIVE CONTROL, and
// the ferried test is worthless without it: "0 bought" is also what a broken
// fixture produces, so this proves the treasury, floor and quote admit a purchase
// whenever no gate has to be crossed.
func TestDrain_LocalCounterIsAffordableAtThisTreasury(t *testing.T) {
	ports, pur := localCounterPorts(ferryTestTreasury)

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, ferryFloorKnobs, fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.buys) != 1 {
		t.Fatalf("bought %d probes at a LOCAL counter, want 1 — the fixture cannot afford anything, "+
			"so the ferried test below would prove nothing (report %+v)", len(pur.buys), rep)
	}
	if rep.FloorHeld {
		t.Fatalf("FloorHeld on a local buy the treasury covers: %+v", rep)
	}
}

// TestDrain_RefusesAFerriedProbeTheLandedCostCannotAfford is the sp-e46yc
// acceptance test: the same probe, the same price and the same treasury as the
// control above, refused solely because it has to be flown three gates.
//
// Before this bead the buy floor compared `credits − quote`, so this purchase was
// admitted and the 17,700 of gate fees was discovered only in the ledger.
func TestDrain_RefusesAFerriedProbeTheLandedCostCannotAfford(t *testing.T) {
	ports, pur := remoteCounterPorts(ferryTestTreasury)

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, ferryFloorKnobs, fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}

	// FIXTURE ASSERTION FIRST. A placement that never reached a counter also buys
	// nothing, and would pass every assertion below while exercising none of the
	// new code. The attempt count is what proves the remote candidate was found,
	// quoted, and then refused BY THE FLOOR.
	if rep.Attempts == 0 {
		t.Fatalf("Attempts = 0 — the remote counter was never reached, so the landed-cost check "+
			"never ran; this fixture proves nothing (report %+v)", rep)
	}
	if !rep.FloorHeld {
		t.Fatalf("FloorHeld not set: the ferried probe was not refused by the buy floor. "+
			"Landed cost is %d (quote 24,356 + 3 hops x %d) against a 50,000 floor and an %d "+
			"treasury, so the floor must bind (report %+v)",
			24_356+3*domainSensing.DefaultGateFeeCredits, domainSensing.DefaultGateFeeCredits,
			ferryTestTreasury, rep)
	}
	if len(pur.buys) != 0 {
		t.Fatalf("bought %d probes whose LANDED cost breaches the floor, want 0 — the guard is "+
			"pricing sticker, not landed (%v)", len(pur.buys), pur.buys)
	}
}

// TestFerryCandidates_CarryTheWalkedHopCount pins the wiring the test above
// depends on: the count must be the REAL distance, not the one-gate minimum.
//
// Without this, a regression that stopped setting ferryHops would still leave the
// acceptance test passing at some treasuries, because FerryHops' minimum charges
// one hop regardless. Here the fixture's only funder is three gates out, so the
// distinction is visible directly.
func TestFerryCandidates_CarryTheWalkedHopCount(t *testing.T) {
	ports, _ := remoteCounterPorts(ferryTestTreasury)
	broker := &ferryBroker{}
	slot := QueuedSlot{Waypoint: "X1-TGT-M1", System: "X1-TGT", Kind: SlotKindMarket, State: SlotStateWanted}

	candidates, err := broker.candidates(context.Background(), ports, testPlayerID, slot)
	if err != nil {
		t.Fatalf("ferryBroker.candidates returned error: %v", err)
	}
	if len(candidates) == 0 {
		t.Fatal("no ferry candidates: the fixture never reached the code under test")
	}
	for _, c := range candidates {
		if !c.ferried {
			t.Fatalf("candidate %+v is not marked ferried, but its yard is in another system", c)
		}
		if c.ferryHops != 3 {
			t.Fatalf("candidate %+v carries ferryHops=%d, want 3 (X1-SRC → X1-H1 → X1-H2 → X1-TGT); "+
				"a 0 here silently falls back to the one-gate minimum and under-prices the ferry",
				c, c.ferryHops)
		}
	}
}
