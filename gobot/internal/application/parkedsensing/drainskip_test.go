package parkedsensing

import (
	"context"
	"testing"
	"time"
)

// drainskip_test.go pins what a placement in a system with NO purchasing hull
// actually costs the drain — and what it does not.
//
// This is worth pinning precisely because it is easy to get wrong in the
// expensive direction. On the live fleet 121 outstanding MARKET placements sit
// in systems holding no hull of ours, and the natural assumption is that they
// are eating the six-attempt budget. They are not: the budget is an API burst
// limit, and a placement that never reaches a counter never touches the API.
//
// So there is no budget to reclaim by skipping them, and a change that tried to
// would buy nothing. Pinned so the assumption is not made again — and so that
// when the queue does look starved, the cause is looked for where it actually
// is: the genuinely FUNDABLE fills, which do spend attempts and which sort ahead
// of the seeds.

// unbuyablePorts builds `count` WANTED MARKET placements in ONE system that has
// probe yards but no hull of ours standing at any of them, plus — when
// `withBuyer` — one system that can genuinely fund its placement.
func unbuyablePorts(count int, withBuyer bool) (BuyPorts, *fakePurchaser) {
	led := &fakeBuyLedger{}
	yards := map[string][]string{"X1-EMPTY": {"X1-EMPTY-Y1", "X1-EMPTY-Y2"}}
	docked := map[string]string{}

	for i := 0; i < count; i++ {
		led.slots = append(led.slots, QueuedSlot{
			Waypoint: "X1-EMPTY-M" + string(rune('A'+i)), System: "X1-EMPTY",
			Kind: SlotKindMarket, State: SlotStateWanted, DepthCredits: 9000,
		})
	}
	led.systems = []ScreenedSystem{{System: "X1-EMPTY", DepthCredits: 9000}}

	if withBuyer {
		led.slots = append(led.slots, QueuedSlot{
			Waypoint: "X1-GOOD-M1", System: "X1-GOOD", Kind: SlotKindMarket,
			State: SlotStateWanted, DepthCredits: 100, // shallower: sorts AFTER the empty system
		})
		led.systems = append(led.systems, ScreenedSystem{System: "X1-GOOD", DepthCredits: 100})
		yards["X1-GOOD"] = []string{"X1-GOOD-Y1"}
		docked["X1-GOOD-Y1"] = "PROBE-GOOD"
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

// TestDrain_PlacementsWithNoLocalBuyerCostNoAttempts is the premise correction.
//
// A placement whose system holds no purchasing hull resolves no candidates, so
// it never reaches Quote or Buy and never increments Attempts — it is recorded
// as SkippedNoYard and the loop moves on. Twenty such placements therefore leave
// the whole six-attempt budget intact.
//
// Pinned so nobody "frees up" a budget that was never being spent, and so the
// real cause of a starved queue is looked for where it actually is.
func TestDrain_PlacementsWithNoLocalBuyerCostNoAttempts(t *testing.T) {
	ports, pur := unbuyablePorts(20, false)

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID,
		BuyKnobs{ProbeCap: 100}, fixedClock{time.Unix(1_700_000_000, 0)})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if rep.Attempts != 0 {
		t.Fatalf("Attempts = %d for 20 placements with no purchasing hull, want 0 — "+
			"the budget is an API burst limit and none of these touched the API", rep.Attempts)
	}
	if rep.SkippedNoYard != 20 {
		t.Fatalf("SkippedNoYard = %d, want 20: %+v", rep.SkippedNoYard, rep)
	}
	if len(pur.quotes) != 0 || len(pur.buys) != 0 {
		t.Fatalf("touched the API for an unfundable placement: quotes=%v buys=%v", pur.quotes, pur.buys)
	}
}

// TestDrain_UnbuyablePlacementsDoNotStarveAFundableOne is the consequence, and
// the claim that actually matters operationally: a deep system full of
// unfundable placements sorts FIRST, and must still not consume the budget a
// shallower fundable placement behind it needs.
func TestDrain_UnbuyablePlacementsDoNotStarveAFundableOne(t *testing.T) {
	ports, pur := unbuyablePorts(20, true)

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID,
		BuyKnobs{ProbeCap: 100}, fixedClock{time.Unix(1_700_000_000, 0)})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if rep.Bought != 1 {
		t.Fatalf("Bought = %d, want 1 — the fundable placement sits behind 20 unfundable ones and must still be reached", rep.Bought)
	}
	if len(pur.buys) != 1 || pur.buys[0].yard != "X1-GOOD-Y1" {
		t.Fatalf("buys = %v, want one at X1-GOOD-Y1", pur.buys)
	}
}
