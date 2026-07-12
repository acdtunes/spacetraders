package commands

import (
	"context"
	"testing"
)

// MONEY-PATH DRILLS (sp-4ewi). Heavies went from never-fires to can-fire, so every guard that never
// mattered for heavies now does. These drive the REAL HeavyDemandProvider (over faked sources that
// now return READABLE demand — the seam wired) through the UNCHANGED guard stack at heavy magnitude
// (~1.4M), proving (1) a clean heavy buy fires + dedicates, and (2) each guard still BLOCKS a bad
// heavy buy. The seam only made demand readable; it relaxed no guard.

// heavyProvider builds the real provider over readable heavy sources (the wired-seam happy state).
func heavyProvider(heavies, unservedLanes int, fleetAvg, marginal float64, declining bool) *HeavyDemandProvider {
	return NewHeavyDemandProvider(&fakeHeavySources{
		heavies: heavies, lanes: unservedLanes, lanesOK: true,
		fleetAvg: fleetAvg, marginal: marginal, declining: declining, rateOK: true,
	})
}

// armedForHeavy wires the armed handler with a heavy-magnitude yard price and a treasury deep enough
// to clear the 25% single-hull affordability rule (a ~1.4M heavy needs > 5.6M treasury).
func armedForHeavy(p *HeavyDemandProvider) (*RunFleetAutosizerCoordinatorHandler, *recordingPurchaser, *recordingMetrics, *recordingNotifier) {
	h, purchaser, metrics, notifier := armedHandler(p)
	h.SetTreasuryReader(&fakeTreasury{credits: 8000000, ok: true})
	h.SetYardPriceReader(&fakeYardPrice{price: 1400000, cheapest: 1400000, yard: "KA42-A2", ok: true})
	return h, purchaser, metrics, notifier
}

// heavyCmd is a launch command with the anti-thrash streak satisfied on the first tick (min 1) so the
// drills isolate the GUARD under test, not the streak.
func heavyCmd() *RunFleetAutosizerCoordinatorCommand {
	return &RunFleetAutosizerCoordinatorCommand{PlayerID: 1, ContainerID: "c1", HeavyUnservedLanesMin: 1}
}

// THE MONEY PATH: readable unserved-lane demand + a clearing economy + the streak satisfied ⇒ ONE
// heavy is bought and dedicated to the trade fleet through the unchanged guard stack.
func TestHeavyBuy_FiresThroughGuardStack_WhenDemandAndEconomicsClear(t *testing.T) {
	h, purchaser, metrics, notifier := armedForHeavy(heavyProvider(6, 2, 500000, 450000, false))
	res, err := h.reconcileOnce(context.Background(), heavyCmd())
	if err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if res.Purchased != 1 {
		t.Fatalf("a clean heavy demand must buy exactly ONE hull, got %d", res.Purchased)
	}
	if len(purchaser.orders) != 1 {
		t.Fatalf("expected one buy order, got %d", len(purchaser.orders))
	}
	o := purchaser.orders[0]
	if o.Class != HullClassHeavy || o.ShipType != defaultShipTypeHeavies {
		t.Fatalf("wrong heavy order: class=%s ship=%s", o.Class, o.ShipType)
	}
	if o.Yard != "KA42-A2" {
		t.Fatalf("heavy buy must target the resolved yard, got %s", o.Yard)
	}
	if metrics.purchase != 1 || notifier.count != 1 {
		t.Fatalf("a heavy buy is real news: purchase-metric=%d notice=%d, want 1/1", metrics.purchase, notifier.count)
	}
}

// G6 era-clock payback: an expensive heavy whose price exceeds marginalRate × hoursRemaining ×
// payback_safety is BLOCKED (it cannot pay back before the reset).
func TestHeavyBuy_BlockedByEraPayback(t *testing.T) {
	h, purchaser, metrics, _ := armedForHeavy(heavyProvider(6, 2, 500000, 450000, false))
	// 5h left (> the 3h hard cutoff, so the payback MATH is what blocks): 450k × 5h × 0.5 = 1.125M < 1.4M.
	h.SetEraClockReader(&fakeEra{hours: 5, ok: true})
	res, _ := h.reconcileOnce(context.Background(), heavyCmd())
	if res.Purchased != 0 || len(purchaser.orders) != 0 {
		t.Fatalf("a heavy that cannot pay back before reset must be blocked, bought %d", res.Purchased)
	}
	if len(metrics.blockedGuards) == 0 || metrics.blockedGuards[0] != GuardEraPayback {
		t.Fatalf("expected era_payback block, got %v", metrics.blockedGuards)
	}
}

// G7 decay stop: a DECLINING fleet-average rate (absorption saturating) blocks the buy even with the
// marginal above the floor.
func TestHeavyBuy_BlockedByDecayingRate(t *testing.T) {
	h, purchaser, metrics, _ := armedForHeavy(heavyProvider(6, 2, 500000, 450000, true)) // declining
	res, _ := h.reconcileOnce(context.Background(), heavyCmd())
	if res.Purchased != 0 || len(purchaser.orders) != 0 {
		t.Fatalf("a declining rate must stop the buy, bought %d", res.Purchased)
	}
	if len(metrics.blockedGuards) == 0 || metrics.blockedGuards[0] != GuardRealizedRate {
		t.Fatalf("expected realized_rate (decay) block, got %v", metrics.blockedGuards)
	}
}

// G7 floor: a marginal rate BELOW heavy_marginal_rate_floor × fleet-avg is blocked (the lowest heavy
// no longer clears the floor).
func TestHeavyBuy_BlockedByMarginalRateFloor(t *testing.T) {
	// marginal 300k vs floor 0.7 × 500k = 350k → below floor.
	h, purchaser, metrics, _ := armedForHeavy(heavyProvider(6, 2, 500000, 300000, false))
	res, _ := h.reconcileOnce(context.Background(), heavyCmd())
	if res.Purchased != 0 || len(purchaser.orders) != 0 {
		t.Fatalf("a marginal below the rate floor must block, bought %d", res.Purchased)
	}
	if len(metrics.blockedGuards) == 0 || metrics.blockedGuards[0] != GuardRealizedRate {
		t.Fatalf("expected realized_rate (floor) block, got %v", metrics.blockedGuards)
	}
}

// G9 treasury-net-of-floor: when live treasury net of the reserve-floor stack cannot cover
// price+margin, the buy is blocked (the 25% rule passes but the floor stack does not).
func TestHeavyBuy_BlockedByTreasuryFloor(t *testing.T) {
	h, purchaser, metrics, _ := armedForHeavy(heavyProvider(6, 2, 500000, 450000, false))
	h.SetTreasuryReader(&fakeTreasury{credits: 6000000, ok: true})
	// Reserve floor 95% of 6M = 5.7M → spendable 300k < price 1.4M + margin 200k. (25% rule: 1.5M ≥ 1.4M, passes.)
	cmd := heavyCmd()
	cmd.Reserve = 6000000
	cmd.ReserveTreasuryPct = 95
	res, _ := h.reconcileOnce(context.Background(), cmd)
	if res.Purchased != 0 || len(purchaser.orders) != 0 {
		t.Fatalf("treasury net of the reserve floor cannot cover the buy — must block, bought %d", res.Purchased)
	}
	if len(metrics.blockedGuards) == 0 || metrics.blockedGuards[0] != GuardTreasuryFloor {
		t.Fatalf("expected treasury_floor block, got %v", metrics.blockedGuards)
	}
}

// G2 per-class ceiling: heavies already at the class ceiling block the buy despite unserved demand.
func TestHeavyBuy_BlockedByCeiling(t *testing.T) {
	// 15 heavies = the default FleetCeilingHeavies; demand 17, shortfall 2, but the ceiling binds.
	h, purchaser, metrics, _ := armedForHeavy(heavyProvider(15, 2, 500000, 450000, false))
	res, _ := h.reconcileOnce(context.Background(), heavyCmd())
	if res.Purchased != 0 || len(purchaser.orders) != 0 {
		t.Fatalf("heavies at the class ceiling must not buy, bought %d", res.Purchased)
	}
	if len(metrics.blockedGuards) == 0 || metrics.blockedGuards[0] != GuardFleetCeiling {
		t.Fatalf("expected fleet_ceiling block, got %v", metrics.blockedGuards)
	}
}

// G3 per-tick cap: a light buy consumes the single per-tick slot, so a heavy shortfall in the SAME
// tick defers (no second buy) — the cap binds across classes. Uses the armed handler's default cheap
// yard price so the light clears (a heavy-magnitude price would block the light on era-payback); the
// cap gates the heavy BEFORE any guard, so the heavy's price is irrelevant here.
func TestHeavyBuy_BlockedByPerTickCap(t *testing.T) {
	h, purchaser, _, _ := armedHandler(lightShortfall(), heavyProvider(6, 2, 500000, 450000, false))
	cmd := heavyCmd()
	cmd.PurchaseCapPerTick = 1
	res, _ := h.reconcileOnce(context.Background(), cmd)
	if res.Purchased != 1 {
		t.Fatalf("per-tick cap 1 must bound to a single buy, got %d", res.Purchased)
	}
	if len(purchaser.orders) != 1 || purchaser.orders[0].Class != HullClassLight {
		t.Fatalf("the light takes the single slot; the heavy defers this tick")
	}
}

// The anti-thrash streak: the unserved-lane shortfall must persist heavy_unserved_lanes_min
// consecutive ticks before a heavy is bought — the readable seam does not bypass it.
func TestHeavyBuy_BlockedByAntiThrashStreak(t *testing.T) {
	h, purchaser, _, _ := armedForHeavy(heavyProvider(6, 2, 500000, 450000, false))
	cmd := &RunFleetAutosizerCoordinatorCommand{PlayerID: 1, ContainerID: "c1", HeavyUnservedLanesMin: 3}
	for tick := 1; tick <= 2; tick++ {
		h.reconcileOnce(context.Background(), cmd)
		if len(purchaser.orders) != 0 {
			t.Fatalf("heavy must hold for the streak (tick %d)", tick)
		}
	}
	h.reconcileOnce(context.Background(), cmd) // 3rd consecutive tick meets the streak
	if len(purchaser.orders) != 1 {
		t.Fatalf("heavy must buy once the 3-tick streak is met, got %d", len(purchaser.orders))
	}
}
