package commands

import (
	"context"
	"testing"
)

// MONEY-PATH DRILLS: these drive the REAL HeavyDemandProvider (over faked sources returning
// READABLE demand) through the UNCHANGED guard stack at heavy magnitude (~1.4M), proving (1) a
// clean heavy buy fires + dedicates, and (2) each guard still BLOCKS a bad heavy buy.

// heavyProvider builds the real provider over readable heavy sources (the wired-seam happy state).
func heavyProvider(heavies, unservedLanes int) *HeavyDemandProvider {
	return NewHeavyDemandProvider(&fakeHeavySources{
		heavies: heavies, lanes: unservedLanes, lanesOK: true,
	})
}

// armedForHeavy wires the armed handler with a heavy-magnitude yard price and a treasury deep enough
// to clear the 25% single-hull affordability rule (a ~1.4M heavy needs > 5.6M treasury).
func armedForHeavy(p *HeavyDemandProvider) (*RunFleetAutosizerCoordinatorHandler, *recordingPurchaser, *recordingMetrics, *recordingNotifier) {
	h, purchaser, metrics, notifier := armedHandler(p)
	h.SetTreasuryReader(&fakeTreasury{credits: 8000000, ok: true})
	h.SetYardPriceReader(&fakeYardPrice{price: 1400000, cheapest: 1400000, yard: "KA42-A2", ok: true})
	// The heavy census must be READABLE or the heavy cap guard fails closed. These drills own
	// no heavy HULL yet (the provider's count is the tag-scoped TRADE POOL, a different fact),
	// so the cap has room and the drills keep isolating the guard each one is about.
	h.SetHeavyCensusReader(&fakeHeavyCensus{owned: 0})
	h.SetHeavyYardReader(&fakeHeavyYard{price: 1400000, found: true})
	return h, purchaser, metrics, notifier
}

// heavyCmd is a launch command with the anti-thrash streak satisfied on the first tick (min 1) so the
// drills isolate the GUARD under test, not the streak.
func heavyCmd() *RunFleetAutosizerCoordinatorCommand {
	return &RunFleetAutosizerCoordinatorCommand{PlayerID: 1, ContainerID: "c1", HeavyUnservedLanesMin: 1, HeavyCap: intPtr(50)}
}

// THE MONEY PATH: readable unserved-lane demand + a clearing economy + the streak satisfied ⇒ ONE
// heavy is bought and dedicated to the trade fleet through the unchanged guard stack.
func TestHeavyBuy_FiresThroughGuardStack_WhenDemandAndEconomicsClear(t *testing.T) {
	h, purchaser, metrics, notifier := armedForHeavy(heavyProvider(6, 2))
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

// G9 treasury-net-of-floor: when live treasury net of the flat reserve floor cannot cover
// price+margin, the buy is blocked (the 25% rule passes but the floor+margin stack does not).
func TestHeavyBuy_BlockedByTreasuryFloor(t *testing.T) {
	h, purchaser, metrics, _ := armedForHeavy(heavyProvider(6, 2))
	h.SetTreasuryReader(&fakeTreasury{credits: 6000000, ok: true})
	// Floor is flat (sp-05glh): spendable = 6,000,000 − 50,000(ImmutableReserveFloor) = 5,950,000.
	// A high margin-over-floor requirement pushes need to 1,400,000 + 4,600,000 = 6,000,000 >
	// spendable → blocked by exactly the immutable floor. (25% rule: cap 1.5M ≥ price 1.4M, passes.)
	cmd := heavyCmd()
	cmd.PurchaseMarginOverFloor = 4_600_000
	res, _ := h.reconcileOnce(context.Background(), cmd)
	if res.Purchased != 0 || len(purchaser.orders) != 0 {
		t.Fatalf("treasury net of the reserve floor cannot cover the buy — must block, bought %d", res.Purchased)
	}
	if len(metrics.blockedGuards) == 0 || metrics.blockedGuards[0] != GuardAffordability {
		t.Fatalf("expected affordability block, got %v", metrics.blockedGuards)
	}
}

// G2 — sp-r7eiu, THE REMOVAL ITSELF. A pool of 15 trade hulls used to sit exactly on the old
// fleet_ceiling_heavies default and block on class_ceiling despite readable unserved-lane demand.
// With the guard gone, pool SIZE no longer refuses anything: the buy proceeds on its economics.
//
// This is the test that fails if class_ceiling is ever reintroduced.
func TestHeavyBuy_PoolSizeNoLongerBlocks(t *testing.T) {
	h, purchaser, metrics, _ := armedForHeavy(heavyProvider(15, 2)) // demand 17, current 15, shortfall 2
	res, _ := h.reconcileOnce(context.Background(), heavyCmd())
	if res.Purchased != 1 || len(purchaser.orders) != 1 {
		t.Fatalf("a 15-hull pool must no longer block on size, bought %d (blocked by %v)", res.Purchased, metrics.blockedGuards)
	}
}

// THE GUARD-NOT-WEAKENED PROOF, end-to-end and at heavy magnitude. The SAME oversized pool that
// TestHeavyBuy_PoolSizeNoLongerBlocks buys through is refused the moment the money guard bites: a
// treasury too thin for the 25%-per-hull rule blocks on AFFORDABILITY, not on any ceiling.
//
// Removing a refusing guard necessarily permits the purchases that guard alone was blocking — that
// is the Admiral's intent. What must NOT change is which purchases the MONEY guards refuse, and
// this pins exactly that: with the pool bound deleted, an unaffordable hull is still not bought.
// Reaching affordability at all is what proves no ceiling silently short-circuited ahead of it.
func TestHeavyBuy_CeilingRemovalDoesNotWeakenAffordability(t *testing.T) {
	h, purchaser, metrics, _ := armedForHeavy(heavyProvider(15, 2))
	// A 1.4M hull needs > 5.6M treasury to clear price <= 25% x treasury; 5M yields a 1.25M cap.
	// The floor term still passes (5M - 50k >= 1.4M + 200k), so this pins the PERCENTAGE rule.
	h.SetTreasuryReader(&fakeTreasury{credits: 5_000_000, ok: true})

	res, _ := h.reconcileOnce(context.Background(), heavyCmd())
	if res.Purchased != 0 || len(purchaser.orders) != 0 {
		t.Fatalf("the 25%% affordability rule must still refuse this buy, bought %d", res.Purchased)
	}
	if len(metrics.blockedGuards) == 0 || metrics.blockedGuards[0] != GuardAffordability {
		t.Fatalf("expected an affordability block (proving no ceiling short-circuits ahead of it), got %v", metrics.blockedGuards)
	}
}

// The same, for the OTHER count bound that survived: heavy_cap. With class_ceiling gone it is the
// only thing standing between a demand signal and unbounded heavy growth, so a fleet at its cap
// must still refuse — whatever the pool size says.
func TestHeavyBuy_CeilingRemovalDoesNotWeakenHeavyCap(t *testing.T) {
	h, purchaser, metrics, _ := armedForHeavy(heavyProvider(15, 2))
	h.SetHeavyCensusReader(&fakeHeavyCensus{owned: 3})
	cmd := heavyCmd()
	cmd.HeavyCap = intPtr(3) // owned == cap

	res, _ := h.reconcileOnce(context.Background(), cmd)
	if res.Purchased != 0 || len(purchaser.orders) != 0 {
		t.Fatalf("heavy_cap must still refuse at the cap, bought %d", res.Purchased)
	}
	if len(metrics.blockedGuards) == 0 || metrics.blockedGuards[0] != GuardHeavyCap {
		t.Fatalf("expected a heavy_cap block, got %v", metrics.blockedGuards)
	}
}

// G3 per-tick cap: a light buy consumes the single per-tick slot, so a heavy shortfall in the SAME
// tick defers (no second buy) — the cap binds across classes. Uses the armed handler's default cheap
// yard price so the light clears (a heavy-magnitude price would block the light on era-payback); the
// cap gates the heavy BEFORE any guard, so the heavy's price is irrelevant here.
func TestHeavyBuy_BlockedByPerTickCap(t *testing.T) {
	h, purchaser, _, _ := armedHandler(lightShortfall(), heavyProvider(6, 2))
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
	h, purchaser, _, _ := armedForHeavy(heavyProvider(6, 2))
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
