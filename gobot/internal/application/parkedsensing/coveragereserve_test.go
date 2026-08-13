package parkedsensing

import (
	"context"
	"strings"
	"testing"
	"time"
)

// coveragereserve_test.go pins BuyKnobs.CoverageReserve: a share of the FILL
// tier's own attempts held back for the best placement in a system the fleet
// has never entered, so a deep backlog of already-held systems cannot
// indefinitely starve every other one. Zero (the default) is a no-op — the
// saturate-first order from drainorder.go runs untouched until armed.

// coverageReserveHeldSystemSlots returns n WANTED market slots plus one PARKED
// probe in X1-HELD, so the system carries a real backlog behind its "entered"
// status rather than a single placement that would clear in one attempt.
func coverageReserveHeldSystemSlots(n int) []QueuedSlot {
	slots := []QueuedSlot{
		{Waypoint: "X1-HELD-P1", System: "X1-HELD", Kind: SlotKindMarket, State: SlotStateParked, AssignedShip: "PROBE-H1"},
	}
	for i := 0; i < n; i++ {
		slots = append(slots, QueuedSlot{
			Waypoint: "X1-HELD-M" + string(rune('1'+i)), System: "X1-HELD", Kind: SlotKindMarket, State: SlotStateWanted,
		})
	}
	return slots
}

func TestDrain_CoverageReserveArmed_ReachesNeverEnteredSystemWithinBudget(t *testing.T) {
	// X1-HELD carries 6 outstanding placements — the whole tick's budget on its
	// own — and is DEEPER than X1-NEW, which has never held a probe. Without the
	// reserve every attempt goes to X1-HELD, exactly as TestDrain_
	// SaturatesAnEnteredSystemBeforeEnteringANewOne already pins.
	led := &fakeBuyLedger{
		slots: append(coverageReserveHeldSystemSlots(6),
			QueuedSlot{Waypoint: "X1-NEW-M1", System: "X1-NEW", Kind: SlotKindMarket, State: SlotStateWanted},
		),
		systems: []ScreenedSystem{
			{System: "X1-HELD", DepthCredits: 9_000},
			{System: "X1-NEW", DepthCredits: 1_000},
		},
	}
	pur := &fakePurchaser{price: 1_000}
	ports := BuyPorts{
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
		Fleet: &fakeFleet{},
	}

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID,
		BuyKnobs{SpendEnabled: true, ProbeCap: 100, CoverageReserve: 2}, fixedClock{time.Now()}); err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}

	want := []string{"X1-HELD-Y1", "X1-HELD-Y1", "X1-HELD-Y1", "X1-HELD-Y1", "X1-NEW-Y1"}
	if len(pur.buys) != len(want) {
		t.Fatalf("made %d purchases, want %d: %v", len(pur.buys), len(want), pur.buys)
	}
	for i, w := range want {
		if pur.buys[i].yard != w {
			t.Fatalf("purchase %d was at %q, want %q (full order %v)", i, pur.buys[i].yard, w, pur.buys)
		}
	}
}

func TestDrain_CoverageReserveDoesNotChangeOrderWhenDisarmedOrNothingToAdvanceTo(t *testing.T) {
	cases := []struct {
		name                string
		reserve             int
		includeNeverEntered bool
	}{
		{"disarmed, a never-entered system is waiting", 0, true},
		{"armed, nothing to advance to", 2, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			led := &fakeBuyLedger{
				slots:   coverageReserveHeldSystemSlots(6),
				systems: []ScreenedSystem{{System: "X1-HELD", DepthCredits: 9_000}},
			}
			yards := map[string][]string{"X1-HELD": {"X1-HELD-Y1"}}
			docked := map[string]string{"X1-HELD-Y1": "BUYER-HELD"}
			if tc.includeNeverEntered {
				led.slots = append(led.slots, QueuedSlot{Waypoint: "X1-NEW-M1", System: "X1-NEW", Kind: SlotKindMarket, State: SlotStateWanted})
				led.systems = append(led.systems, ScreenedSystem{System: "X1-NEW", DepthCredits: 1_000})
				yards["X1-NEW"] = []string{"X1-NEW-Y1"}
				docked["X1-NEW-Y1"] = "BUYER-NEW"
			}
			pur := &fakePurchaser{price: 1_000}
			ports := BuyPorts{
				Treasury:   &fakeTreasury{credits: 10_000_000},
				CargoSpend: &fakeCargoSpend{},
				Purchaser:  pur,
				Ledger:     led,
				Yards:      &fakeYards{yards: yards},
				Ships:      &fakeShipReader{docked: docked},
				Fleet:      &fakeFleet{},
			}

			rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID,
				BuyKnobs{SpendEnabled: true, ProbeCap: 100, CoverageReserve: tc.reserve}, fixedClock{time.Now()})
			if err != nil {
				t.Fatalf("DrainBuyQueue returned error: %v", err)
			}
			if rep.Attempts != maxDrainAttempts {
				t.Fatalf("spent %d attempts, want the full %d — an inert reserve must not idle the budget", rep.Attempts, maxDrainAttempts)
			}
			if len(pur.buys) != maxDrainAttempts {
				t.Fatalf("bought %d probes, want %d: %v", len(pur.buys), maxDrainAttempts, pur.buys)
			}
			for _, b := range pur.buys {
				if b.yard != "X1-HELD-Y1" {
					t.Fatalf("bought at %q, want every attempt still spent inside the already-held system: %v", b.yard, pur.buys)
				}
			}
		})
	}
}

func TestDrain_CoverageReserveArmed_DarkYardStillOutranksNeverEnteredSystem(t *testing.T) {
	// X1-HELD offers FIVE dark yards — more than the reduced coverage budget
	// (maxDrainAttempts=6, reserve=2 -> 4) — so if the reserve reached into the
	// yard tier, the 5th and 6th would be skipped for X1-NEW. The Admiral's
	// directive is unconditional: dark shipyards outrank everything.
	led := &fakeBuyLedger{
		slots: []QueuedSlot{
			{Waypoint: "X1-HELD-P1", System: "X1-HELD", Kind: SlotKindMarket, State: SlotStateParked, AssignedShip: "PROBE-H1"},
			{Waypoint: "X1-HELD-Y1", System: "X1-HELD", Kind: SlotKindMarket, State: SlotStateWanted},
			{Waypoint: "X1-HELD-Y2", System: "X1-HELD", Kind: SlotKindMarket, State: SlotStateWanted},
			{Waypoint: "X1-HELD-Y3", System: "X1-HELD", Kind: SlotKindMarket, State: SlotStateWanted},
			{Waypoint: "X1-HELD-Y4", System: "X1-HELD", Kind: SlotKindMarket, State: SlotStateWanted},
			{Waypoint: "X1-HELD-Y5", System: "X1-HELD", Kind: SlotKindMarket, State: SlotStateWanted},
			{Waypoint: "X1-NEW-M1", System: "X1-NEW", Kind: SlotKindMarket, State: SlotStateWanted},
		},
		systems: []ScreenedSystem{
			{System: "X1-HELD", DepthCredits: 9_000},
			{System: "X1-NEW", DepthCredits: 1_000},
		},
	}
	pur := &fakePurchaser{price: 1_000}
	ports := BuyPorts{
		Treasury:   &fakeTreasury{credits: 10_000_000},
		CargoSpend: &fakeCargoSpend{},
		Purchaser:  pur,
		Ledger:     led,
		Yards: &fakeYards{yards: map[string][]string{
			"X1-HELD": {"X1-HELD-Y1", "X1-HELD-Y2", "X1-HELD-Y3", "X1-HELD-Y4", "X1-HELD-Y5"},
			"X1-NEW":  {"X1-NEW-Y1"},
		}},
		Ships: &fakeShipReader{docked: map[string]string{
			"X1-HELD-Y1": "BUYER-1", "X1-HELD-Y2": "BUYER-2", "X1-HELD-Y3": "BUYER-3",
			"X1-HELD-Y4": "BUYER-4", "X1-HELD-Y5": "BUYER-5",
			"X1-NEW-Y1": "BUYER-NEW",
		}},
		Fleet: &fakeFleet{},
		YardDemand: &fakeYardDemand{yards: []fakeYard{
			darkYard("X1-HELD-Y1", "X1-HELD", false), darkYard("X1-HELD-Y2", "X1-HELD", false),
			darkYard("X1-HELD-Y3", "X1-HELD", false), darkYard("X1-HELD-Y4", "X1-HELD", false),
			darkYard("X1-HELD-Y5", "X1-HELD", false),
		}},
	}

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID,
		BuyKnobs{SpendEnabled: true, ProbeCap: 100, CoverageReserve: 2}, fixedClock{time.Now()}); err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}

	if len(pur.buys) != 6 {
		t.Fatalf("made %d purchases, want 6: %v", len(pur.buys), pur.buys)
	}
	for i := 0; i < 5; i++ {
		if !strings.HasPrefix(pur.buys[i].yard, "X1-HELD-Y") {
			t.Fatalf("purchase %d was at %q, want a dark yard in X1-HELD (full order %v)", i, pur.buys[i].yard, pur.buys)
		}
	}
	if pur.buys[5].yard != "X1-NEW-Y1" {
		t.Fatalf("purchase 6 was at %q, want the reserved attempt to reach X1-NEW (full order %v)", pur.buys[5].yard, pur.buys)
	}
}
