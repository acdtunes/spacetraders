package parkedsensing

import (
	"context"
	"testing"
	"time"
)

// purchase_stale_yard_test.go is sp-g4wzg: candidatesInSystem trusted a QUEUED
// slot's recorded PurchaseYard even when it named a waypoint in a DIFFERENT
// system, bypassing ferry.candidates' gate-reachability check entirely. Live,
// this bought probes through a remembered yard whose gate cache had gone stale
// (all its edges condemned), stranding the hull: RouteAcross's own walk reads
// the SAME store fresh and correctly refuses a route to nowhere, so the bought
// hull sat IN_TRANSIT retrying forever.

// TestDrain_DoesNotBuyThroughARecordedRemoteYardThatCanNoLongerReachThePlacement
// is the bead. X1-DARKHUB is a real, gate-mapped system we hold a probe in — not
// unknown territory — but its stored adjacency now names nowhere, exactly what a
// condemned (stale-cache) edge set looks like to the router. A buyer is still
// standing at the yard the earlier tick recorded, so a blind reuse of that
// preference buys anyway; only a fresh reachability check catches it.
func TestDrain_DoesNotBuyThroughARecordedRemoteYardThatCanNoLongerReachThePlacement(t *testing.T) {
	led := &fakeBuyLedger{
		slots: []QueuedSlot{
			{Waypoint: "X1-TGT-M1", System: "X1-TGT", Kind: SlotKindMarket, State: SlotStateQueued,
				PurchaseYard: "X1-DARKHUB-Y1"},
			{Waypoint: "X1-DARKHUB-P1", System: "X1-DARKHUB", Kind: SlotKindMarket, State: SlotStateParked,
				AssignedShip: "DARKHUB-PROBE"},
		},
		systems: []ScreenedSystem{{System: "X1-TGT", DepthCredits: 900}},
	}
	pur := &fakePurchaser{price: 1_000}
	ports := BuyPorts{
		Treasury:   &fakeTreasury{credits: 10_000_000},
		CargoSpend: &fakeCargoSpend{},
		Purchaser:  pur,
		Ledger:     led,
		Yards:      &fakeYards{yards: map[string][]string{}}, // X1-TGT has no yard of its own
		Ships:      &fakeShipReader{docked: map[string]string{"X1-DARKHUB-Y1": "DARKHUB-BUYER"}},
		Fleet:      &fakeFleet{},
		Gates:      &fakeGates{adjacency: map[string][]string{"X1-DARKHUB": {}}},
	}

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.buys) != 0 {
		t.Fatalf("bought %v through a remembered yard the gate graph can no longer route to — "+
			"that hull would sit IN_TRANSIT forever exactly like RouteAcross's own refusal describes", pur.buys)
	}
	if rep.Bought != 0 {
		t.Fatalf("report says Bought=%d, want 0", rep.Bought)
	}
	if got := slotAt(t, led, "X1-TGT-M1", SlotKindMarket); got.State != SlotStateQueued {
		t.Fatalf("target slot = %+v, want it left QUEUED for a later tick rather than stranding a hull", got)
	}
}

// TestDrain_RetriesARecordedRemoteYardThatIsStillReachable pins the other half:
// the fix must fall through to ferry.candidates, not refuse every remote
// preference outright. A recorded remote yard that genuinely still has a route
// is bought exactly as before.
func TestDrain_RetriesARecordedRemoteYardThatIsStillReachable(t *testing.T) {
	led := &fakeBuyLedger{
		slots: []QueuedSlot{
			{Waypoint: "X1-TGT-M1", System: "X1-TGT", Kind: SlotKindMarket, State: SlotStateQueued,
				PurchaseYard: "X1-SRC-Y1"},
			{Waypoint: "X1-SRC-M1", System: "X1-SRC", Kind: SlotKindMarket, State: SlotStateParked,
				AssignedShip: "SRC-PROBE", WhitelistGoods: []string{"FUEL"}},
		},
		systems: []ScreenedSystem{{System: "X1-TGT", DepthCredits: 900}},
	}
	pur := &fakePurchaser{price: 1_000}
	ports := BuyPorts{
		Treasury:   &fakeTreasury{credits: 10_000_000},
		CargoSpend: &fakeCargoSpend{},
		Purchaser:  pur,
		Ledger:     led,
		Yards:      &fakeYards{yards: map[string][]string{"X1-SRC": {"X1-SRC-Y1"}}},
		Ships:      &fakeShipReader{docked: map[string]string{"X1-SRC-Y1": "SRC-PROBE"}},
		Fleet:      &fakeFleet{},
		Gates:      &fakeGates{adjacency: map[string][]string{"X1-SRC": {"X1-TGT"}}},
	}

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.buys) != 1 || pur.buys[0].yard != "X1-SRC-Y1" {
		t.Fatalf("buys = %v, want one purchase at the still-reachable recorded yard X1-SRC-Y1", pur.buys)
	}
	if rep.Bought != 1 {
		t.Fatalf("report says Bought=%d, want 1", rep.Bought)
	}
}
