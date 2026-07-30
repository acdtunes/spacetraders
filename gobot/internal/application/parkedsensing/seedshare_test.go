package parkedsensing

import (
	"context"
	"testing"
	"time"
)

// seedshare_test.go pins the share of each tick's attempt budget held for SEEDS.
//
// WHY IT IS LOAD-BEARING, not a nicety. The queue works FILLS first (deepest
// system first) and SEEDS last, and that ordering is correct on its own terms —
// a fill watches a market we have already justified, a seed is speculative. It
// was survivable only while it did not actually starve anything.
//
// Two live facts make it starve now. There are more genuinely FUNDABLE fills
// than maxDrainAttempts, so the budget is consumed before the loop ever reaches
// a seed — and unfundable placements cost no attempts (see drainskip_test.go),
// so nothing about them clears the queue either. Meanwhile every seed alive
// today was acquired by re-tasking an already-parked spare, not by purchase, and
// that pool is nearly out. Once it empties, sustained expansion needs BOUGHT
// seeds; under the old ordering none can ever be bought, at any treasury.

// seedShareClock is the fixed clock these ticks drain against.
var seedShareClock = fixedClock{time.Unix(1_700_000_000, 0)}

// fundableFillPorts builds `fills` MARKET placements, each in its own IN_SCOPE
// self-funding system so every one of them genuinely costs an attempt, and
// optionally one SPARE seed — also fundable — which sorts LAST by construction.
//
// The fill count deliberately exceeds maxDrainAttempts: a reserve that is never
// reached proves nothing, so the fixture must supply demand past the bound.
func fundableFillPorts(fills int, withSeed bool) (BuyPorts, *fakePurchaser) {
	led := &fakeBuyLedger{}
	yards := map[string][]string{}
	docked := map[string]string{}

	for i := 0; i < fills; i++ {
		sys := "X1-F" + string(rune('A'+i))
		led.slots = append(led.slots, QueuedSlot{
			Waypoint: sys + "-M1", System: sys, Kind: SlotKindMarket,
			State: SlotStateWanted, DepthCredits: 9000,
		})
		led.systems = append(led.systems, ScreenedSystem{System: sys, DepthCredits: 9000})
		yards[sys] = []string{sys + "-Y1"}
		docked[sys+"-Y1"] = "PROBE-" + sys
	}

	if withSeed {
		led.slots = append(led.slots, QueuedSlot{
			Waypoint: "X1-SEED-Y1", System: "X1-SEED", Kind: SlotKindSpare, State: SlotStateWanted,
		})
		yards["X1-SEED"] = []string{"X1-SEED-Y1"}
		docked["X1-SEED-Y1"] = "PROBE-SEEDBUYER"
	}

	pur := &fakePurchaser{price: 1_000}
	return BuyPorts{
		Treasury:   &fakeTreasury{credits: 50_000_000},
		CargoSpend: &fakeCargoSpend{},
		Purchaser:  pur,
		Ledger:     led,
		Yards:      &fakeYards{yards: yards},
		Ships:      &fakeShipReader{docked: docked},
		Fleet:      &fakeFleet{},
	}, pur
}

// TestDrain_ASeedIsReachedEvenBehindMoreFillsThanTheBudget is the starvation
// test. Eight fundable fills sort ahead of one seed against a six-attempt
// budget, so without a reserve the seed is never attempted at all.
func TestDrain_ASeedIsReachedEvenBehindMoreFillsThanTheBudget(t *testing.T) {
	ports, pur := fundableFillPorts(8, true)

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID,
		BuyKnobs{SpendEnabled: true, ProbeCap: 100}, seedShareClock)
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if rep.Attempts > maxDrainAttempts {
		t.Fatalf("spent %d attempts, over the %d budget — the reserve must SPLIT the budget, never extend it",
			rep.Attempts, maxDrainAttempts)
	}

	seedBought := false
	for _, b := range pur.buys {
		if b.yard == "X1-SEED-Y1" {
			seedBought = true
		}
	}
	if !seedBought {
		t.Fatalf("no attempt reached the seed behind 8 fundable fills; the frontier can never buy a hull (buys=%v)", pur.buys)
	}
}

// TestDrain_FillsKeepTheWholeBudgetWithNoSeedsOutstanding pins that the reserve
// is not a standing tax. With nothing on the frontier to protect, the fills get
// every attempt — otherwise this change would quietly cost two purchases a tick
// forever in the steady state, which is most ticks.
func TestDrain_FillsKeepTheWholeBudgetWithNoSeedsOutstanding(t *testing.T) {
	ports, _ := fundableFillPorts(8, false)

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID,
		BuyKnobs{SpendEnabled: true, ProbeCap: 100}, seedShareClock)
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if rep.Attempts != maxDrainAttempts {
		t.Fatalf("spent %d attempts with no seed outstanding, want the full %d", rep.Attempts, maxDrainAttempts)
	}
	if rep.Bought != maxDrainAttempts {
		t.Fatalf("bought %d probes, want %d — an unused reserve must not idle the budget", rep.Bought, maxDrainAttempts)
	}
}

// TestDrain_FillsStillOutrankSeedsWithinTheirShare pins that the reserve
// REDISTRIBUTES the budget without inverting the queue's documented priority: a
// known-good fill still outranks a speculative seed for the majority of the
// tick. Four of the six attempts remain the fills'.
func TestDrain_FillsStillOutrankSeedsWithinTheirShare(t *testing.T) {
	ports, pur := fundableFillPorts(8, true)

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID,
		BuyKnobs{SpendEnabled: true, ProbeCap: 100}, seedShareClock)
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}

	fillBuys := 0
	for _, b := range pur.buys {
		if b.yard != "X1-SEED-Y1" {
			fillBuys++
		}
	}
	if want := maxDrainAttempts - seedAttemptReserve; fillBuys != want {
		t.Fatalf("fills took %d of %d attempts, want %d — the reserve must hold back exactly %d",
			fillBuys, rep.Attempts, want, seedAttemptReserve)
	}
}

// TestDrain_ASeedAloneDoesNotStrandTheBudget pins the degenerate direction: with
// seeds and nothing else, the reserve must not somehow cap what the seeds
// themselves may spend. A SPARE never yields to itself.
func TestDrain_ASeedAloneDoesNotStrandTheBudget(t *testing.T) {
	led := &fakeBuyLedger{}
	yards := map[string][]string{}
	docked := map[string]string{}
	for i := 0; i < 8; i++ {
		sys := "X1-S" + string(rune('A'+i))
		led.slots = append(led.slots, QueuedSlot{
			Waypoint: sys + "-Y1", System: sys, Kind: SlotKindSpare, State: SlotStateWanted,
		})
		yards[sys] = []string{sys + "-Y1"}
		docked[sys+"-Y1"] = "PROBE-" + sys
	}
	pur := &fakePurchaser{price: 1_000}
	ports := BuyPorts{
		Treasury:   &fakeTreasury{credits: 50_000_000},
		CargoSpend: &fakeCargoSpend{},
		Purchaser:  pur,
		Ledger:     led,
		Yards:      &fakeYards{yards: yards},
		Ships:      &fakeShipReader{docked: docked},
		Fleet:      &fakeFleet{},
	}

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID,
		BuyKnobs{SpendEnabled: true, ProbeCap: 100}, seedShareClock)
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if rep.Attempts != maxDrainAttempts {
		t.Fatalf("seeds alone spent %d attempts, want the full %d", rep.Attempts, maxDrainAttempts)
	}
}
