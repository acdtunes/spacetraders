package parkedsensing

import (
	"context"
	"errors"
	"testing"
	"time"
)

// buyqueue_lentbuyer_test.go pins the second half of the cold-start escape: the buy
// queue transacts through a NON-PROBE hull standing at the counter, and every money
// guard it has binds exactly as before.
//
// WHY IT IS SOUND AT ALL: SpaceTraders sells a hull wherever a hull of ours is
// DOCKED and does not care which. The purchase machinery has never cared either —
// PurchaseShipCommand takes a purchasing ship symbol and no role — so the only thing
// that ever restricted it was the reader that chose the buyer.
//
// WHY IT COSTS THE LENDER NOTHING: the buyer is engaged for the length of one
// purchase and named by NO ledger row afterwards, which is the relationship the
// DockedProbeAt fallback has always had with a probe the ledger does not account
// for. The hull that FILLS the placement is the probe that gets bought.

// lentBuyerPorts is oneFillPorts with the probe taken off the counter and a borrowed
// hull put there instead: one WANTED MARKET slot in an IN_SCOPE system, one probe
// yard, and the only hull standing on it is not a probe.
func lentBuyerPorts(treasury int64) (BuyPorts, *fakeBuyLedger, *fakePurchaser, *fakeFleet) {
	led := &fakeBuyLedger{
		slots: []QueuedSlot{{
			Waypoint: "X1-AA-M1", System: "X1-AA", Kind: SlotKindMarket, State: SlotStateWanted, DepthCredits: 900,
		}},
		systems: []ScreenedSystem{{System: "X1-AA", DepthCredits: 900}},
	}
	pur := &fakePurchaser{price: 23_540}
	fleet := &fakeFleet{}
	return BuyPorts{
		Treasury:   &fakeTreasury{credits: treasury},
		CargoSpend: &fakeCargoSpend{spend: 300_000},
		Purchaser:  pur,
		Ledger:     led,
		Yards:      &fakeYards{yards: map[string][]string{"X1-AA": {"X1-AA-Y1"}}},
		// No probe is docked anywhere; TORWIND-1 is.
		Ships: &fakeShipReader{lent: map[string]string{"X1-AA-Y1": "TORWIND-1"}},
		Fleet: fleet,
	}, led, pur, fleet
}

// THE HEADLINE. With no probe on the counter the drain used to skip the placement
// forever (SkippedNoYard). It now buys through the borrowed hull.
func TestDrain_BuysThroughALentNonProbeHull(t *testing.T) {
	ports, _, pur, _ := lentBuyerPorts(10_000_000)

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID,
		BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if rep.Bought != 1 {
		t.Fatalf("Bought = %d, want 1 — TORWIND-1 is docked at X1-AA-Y1, which is all the counter requires",
			rep.Bought)
	}
	if len(pur.buys) != 1 || pur.buys[0].ship != "TORWIND-1" || pur.buys[0].yard != "X1-AA-Y1" {
		t.Fatalf("purchases = %v, want one through TORWIND-1 at X1-AA-Y1", pur.buys)
	}
	if rep.SkippedNoYard != 0 {
		t.Fatalf("SkippedNoYard = %d, want 0 — the counter was executable", rep.SkippedNoYard)
	}
}

// A PROBE ON THE COUNTER STILL WINS. The borrowed-hull read is asked LAST, so a
// fleet that has its own probe standing there never engages another coordinator's
// hull — and every fixture written before the escape existed behaves identically.
func TestDrain_PrefersAProbeOverALentHullAtTheSameCounter(t *testing.T) {
	ports, _, pur, _ := lentBuyerPorts(10_000_000)
	ports.Ships = &fakeShipReader{
		docked: map[string]string{"X1-AA-Y1": "PROBE-OLD"},
		lent:   map[string]string{"X1-AA-Y1": "TORWIND-1"},
	}

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID,
		BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()}); err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.buys) != 1 || pur.buys[0].ship != "PROBE-OLD" {
		t.Fatalf("bought through %v, want PROBE-OLD — a probe of ours is standing right there and borrowing "+
			"another coordinator's hull instead is a cost with no benefit", pur.buys)
	}
}

// THE LENT HULL IS NEVER DEDICATED. Only the hull that was BOUGHT is tagged into the
// sensing fleet; tagging the buyer would make FindIdleLightHaulers skip it and the
// contract fleet could never claim it again — the definition of stranding it.
func TestDrain_DoesNotDedicateTheLentBuyerIntoTheSensingFleet(t *testing.T) {
	ports, _, pur, fleet := lentBuyerPorts(10_000_000)

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID,
		BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()}); err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.buys) != 1 {
		t.Fatalf("the fixture must actually buy for this to prove anything: %v", pur.buys)
	}
	for _, assign := range fleet.assigns {
		if assign.ship == "TORWIND-1" {
			t.Fatalf("tagged the borrowed buyer into %q — a dedicated_fleet tag is what removes a hull from "+
				"every other coordinator's pool, permanently", assign.fleet)
		}
	}
	if len(fleet.assigns) != 1 || fleet.assigns[0].fleet != SensingParkedFleetTag {
		t.Fatalf("fleet writes = %v, want exactly one: the BOUGHT probe tagged into the sensing fleet",
			fleet.assigns)
	}
}

// THE LENT HULL GETS NO PLACEMENT ROW EITHER. CountOwnedProbes selects on state and
// assigned_ship and never on role, so a row naming a frigate would inflate the probe
// cap against a hull that is not a probe.
func TestDrain_WritesNoPlacementRowNamingTheLentBuyer(t *testing.T) {
	ports, led, pur, _ := lentBuyerPorts(10_000_000)

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID,
		BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()}); err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.buys) != 1 {
		t.Fatalf("the fixture must actually buy for this to prove anything: %v", pur.buys)
	}
	for _, tr := range led.transitions {
		if tr.assignedShip != nil && *tr.assignedShip == "TORWIND-1" {
			t.Fatalf("placement %s was pointed at the borrowed buyer — the probe cap counts exactly these "+
				"rows, and a frigate inside it buys FEWER probes while never being one", tr.waypoint)
		}
	}
}

// --- the money guards, asserted rather than assumed ---------------------------

// THE FLOOR STILL BINDS. Reachability is all that changed: a borrowed buyer makes a
// counter transactable, it does not make a probe affordable. Same arithmetic as
// TestDrain_RefusesBuyBelowDynamicFloor — 770_000 − 23_540 = 746_460 < 750_000.
func TestDrain_LentBuyerDoesNotRelaxTheDynamicFloor(t *testing.T) {
	ports, _, pur, _ := lentBuyerPorts(770_000)

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, capexKnobs, fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.buys) != 0 {
		t.Fatalf("bought %v below the dynamic floor — RULINGS #4: the escape changes which counters can "+
			"transact, never how much of the treasury may be spent", pur.buys)
	}
	if !rep.FloorHeld {
		t.Fatalf("FloorHeld not reported: %+v", rep)
	}
}

// AND SO DOES THE PROBE CAP. A borrowed hull standing at a counter is not a licence
// to exceed the hull ceiling.
func TestDrain_LentBuyerDoesNotRelaxTheProbeCap(t *testing.T) {
	ports, led, pur, _ := lentBuyerPorts(10_000_000)
	led.owned = 7

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID,
		BuyKnobs{SpendEnabled: true, ProbeCap: 7}, fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.buys) != 0 {
		t.Fatalf("bought %v at the probe cap", pur.buys)
	}
	if !rep.CapHeld {
		t.Fatalf("CapHeld not reported: %+v", rep)
	}
}

// A SPEND PAUSE STILL PAYS NOTHING. The borrowed buyer does not become a way past
// the operator's switch: no quote is taken and no counter is paid.
func TestDrain_LentBuyerIsStillSubjectToTheSpendPause(t *testing.T) {
	ports, _, pur, _ := lentBuyerPorts(10_000_000)

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID,
		BuyKnobs{SpendEnabled: false, ProbeCap: 100}, fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.quotes) != 0 || len(pur.buys) != 0 {
		t.Fatalf("a paused drain priced %v and bought %v", pur.quotes, pur.buys)
	}
	if !rep.SpendingPaused {
		t.Fatalf("SpendingPaused not reported: %+v", rep)
	}
}

// A FAILED BORROWED-BUYER READ FAILS THE BUY CLOSED, like every other read in this
// queue. The fake hands back a usable buyer alongside the error, so a caller that
// leaks it engages a hull it cannot prove is claimable.
func TestDrain_ALentBuyerReadFailureBuysNothing(t *testing.T) {
	ports, _, pur, _ := lentBuyerPorts(10_000_000)
	ports.Ships = &fakeShipReader{lentErr: errors.New("ships table unavailable")}

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID,
		BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()}); err == nil {
		t.Fatal("an unreadable ships table did not stop the drain")
	}
	if len(pur.buys) != 0 {
		t.Fatalf("bought %v off a failed read", pur.buys)
	}
}
