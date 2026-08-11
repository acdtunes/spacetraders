package commands

import (
	"context"
	"slices"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
)

// MONEY-PATH DRILLS at heavy magnitude, driven through the growth coordinator's UNCHANGED shared
// guard stack: a clean heavy buy fires and dedicates, and each guard still BLOCKS a bad one. The
// coverage lives here because the coordinator that spends is the one whose refusals matter — a
// drill against a coordinator that no longer buys heavies proves nothing about the money path.

// growthBlockRecorder captures the guard each blocked decision named, which is what lets a drill
// assert WHICH guard refused rather than merely that nothing was bought.
type growthBlockRecorder struct {
	noopGrowthSink
	blocked []GuardName
}

func (r *growthBlockRecorder) RecordBlocked(_ HullClass, guard GuardName) {
	r.blocked = append(r.blocked, guard)
}

// armedForHeavy wires the coordinator for a heavy-magnitude buy that clears every guard: readable
// unserved-lane demand, the streak already satisfied, a deep treasury and a priced yard.
func armedForHeavy(t *testing.T, f growthFixture) (*RunFleetGrowthCoordinatorHandler, *growthPurchaseRecorder, *growthBlockRecorder) {
	t.Helper()
	if f.lanes == nil {
		f.lanes = &fakeLanes{count: 2, readable: true}
	}
	if f.treasury == 0 {
		f.treasury = 8_000_000
	}
	if f.yardAsk == 0 {
		f.yardAsk = 1_400_000
	}
	if f.shortfallHeld == 0 {
		f.shortfallHeld = growthSettledWindow
	}
	h := newGrowthHandlerWith(t, f)
	buyer := &growthPurchaseRecorder{}
	blocks := &growthBlockRecorder{}
	h.SetPurchaser(buyer)
	h.SetMetricsSink(blocks)
	return h, buyer, blocks
}

// THE MONEY PATH: readable unserved-lane demand + a clearing economy + the streak satisfied ⇒ ONE
// heavy is bought and dedicated to the trade fleet through the unchanged guard stack.
func TestGrowthHeavyBuy_FiresThroughGuardStack_WhenDemandAndEconomicsClear(t *testing.T) {
	h, buyer, blocks := armedForHeavy(t, growthFixture{})

	res, err := h.reconcileOnce(context.Background(), growthCmd())
	if err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if res.Purchased != 1 || buyer.calls != 1 {
		t.Fatalf("a clean heavy demand must buy exactly ONE hull, got purchased=%d calls=%d blocked=%v", res.Purchased, buyer.calls, blocks.blocked)
	}
	if buyer.lastOrder.Class != HullClassHeavy || buyer.lastOrder.ShipType != defaultGrowthShipTypeHeavies {
		t.Fatalf("wrong heavy order: class=%s ship=%s", buyer.lastOrder.Class, buyer.lastOrder.ShipType)
	}
	if buyer.lastOrder.Yard != "X1-AA-YARD" {
		t.Fatalf("the heavy buy must target the resolved yard, got %s", buyer.lastOrder.Yard)
	}
}

// TREASURY NET OF THE IMMUTABLE FLOOR: when the live treasury less the flat reserve floor cannot
// cover price + margin, the buy is blocked. This runs at the SHIPPED configuration — the
// percentage-of-treasury ceiling unapplied — so the floor term is the only thing that can refuse,
// and it is what proves the revocation left the term that actually prevents insolvency untouched.
func TestGrowthHeavyBuy_BlockedByTreasuryFloor(t *testing.T) {
	h, buyer, blocks := armedForHeavy(t, growthFixture{treasury: 6_000_000})
	cmd := growthCmd()
	cmd.PurchaseMarginOverFloor = 4_600_000

	if _, err := h.reconcileOnce(context.Background(), cmd); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if buyer.calls != 0 {
		t.Fatalf("treasury net of the reserve floor cannot cover the buy — must block, bought %d", buyer.calls)
	}
	if len(blocks.blocked) == 0 || blocks.blocked[0] != GuardAffordability {
		t.Fatalf("expected an affordability block, got %v", blocks.blocked)
	}
}

// THE REVOKED CEILING NO LONGER REFUSES, at the coordinator that actually spends. A 1.4M hull
// against a 5M treasury is affordable to the floor term (5,000,000 − 50,000 >= 1,400,000 + 200,000)
// and was refused by the percentage ceiling, which wanted a treasury above 5.6M. It buys now.
//
// This is the whole behaviour change, driven through the real resolve and the real guard stack
// rather than a hand-built request — the coordinator's resolved knobs are what the revocation
// changed, so a guard-level test alone would not have caught a resolve that re-armed the rule.
func TestGrowthHeavyBuy_RevokedCeilingNoLongerRefusesWhatTheFloorAffords(t *testing.T) {
	h, buyer, blocks := armedForHeavy(t, growthFixture{treasury: 5_000_000})

	res, err := h.reconcileOnce(context.Background(), growthCmd())
	if err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if res.Purchased != 1 || buyer.calls != 1 {
		t.Fatalf("the floor term affords this hull — it must buy, got purchased=%d calls=%d blocked=%v", res.Purchased, buyer.calls, blocks.blocked)
	}
}

// AND IT STILL REFUSES WHEN AN OPERATOR RESTORES IT. Identical economics to the drill above; the
// only difference is the knob, which is what makes this the counterfactual for it — the ceiling
// really would have refused that buy, and the rule can be put back without a merge.
func TestGrowthHeavyBuy_RestoredCeilingStillRefuses(t *testing.T) {
	h, buyer, blocks := armedForHeavy(t, growthFixture{treasury: 5_000_000})
	cmd := growthCmd()
	cmd.TreasuryPctPerPurchase = 25

	if _, err := h.reconcileOnce(context.Background(), cmd); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if buyer.calls != 0 {
		t.Fatalf("a restored percentage ceiling must refuse this buy, bought %d", buyer.calls)
	}
	if len(blocks.blocked) == 0 || blocks.blocked[0] != GuardAffordability {
		t.Fatalf("expected an affordability block, got %v", blocks.blocked)
	}
}

// heavy_cap is the ONLY count-based bound on the class, and a fleet at its cap must refuse whatever
// the lane surface says.
//
// IT BINDS THROUGH THE RESERVATION, ONE RUNG AHEAD OF THE GUARD. At the cap there is nothing left
// to save toward, so the reachability clause has no target and the wave is PROBE — the buy path is
// never entered at all. guardHeavyCap remains behind it as the fail-closed backstop; this asserts
// the rung that actually fires, because a test naming the guard here would pass only if the wave
// gate had stopped working.
func TestGrowthHeavyBuy_AtTheCapTheWaveIsProbeAndNothingIsBought(t *testing.T) {
	h, buyer, _ := armedForHeavy(t, growthFixture{heaviesOwned: 3})
	waves := &recordingWaveSink{}
	h.SetMetricsSink(waves)
	cmd := growthCmd()
	cap3 := 3
	cmd.HeavyCap = &cap3 // owned == cap

	if _, err := h.reconcileOnce(context.Background(), cmd); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if buyer.calls != 0 {
		t.Fatalf("heavy_cap must still refuse at the cap, bought %d", buyer.calls)
	}
	if waves.waves[0] != common.WaveProbe || waves.reasons[0] != common.WaveProbeReasonUnreachable {
		t.Fatalf("at the cap expected PROBE/unreachable, got %q/%q", waves.waves[0], waves.reasons[0])
	}
}

// AND THE CAP IS STILL A GUARD. The backstop must not decay just because the wave normally fires
// first: an unreadable heavy census leaves the cap unjudgeable, and the guard refuses on that.
func TestGrowthHeavyBuy_UnreadableHeavyCensusFailsTheCapGuardClosed(t *testing.T) {
	verdict := EvaluateGuards(PurchaseRequest{
		Class:                HullClassHeavy,
		HeaviesOwned:         0,
		HeavyCap:             5,
		HeaviesOwnedReadable: false,
	})
	if verdict.Approved {
		t.Fatal("an unreadable heavy census must never approve a heavy purchase")
	}
	if verdict.BlockedBy != GuardHeavyCap && verdict.BlockedBy != GuardDemand {
		t.Fatalf("expected the cap (or the demand rung ahead of it) to refuse, got %q", verdict.BlockedBy)
	}
}

// POOL SIZE REFUSES NOTHING. A large trade pool used to sit on a fleet-wide ceiling and block
// despite readable unserved-lane demand; with that guard gone the buy proceeds on its economics
// alone. This is the test that fails if a count-based class ceiling is ever reintroduced.
func TestGrowthHeavyBuy_PoolSizeDoesNotBlock(t *testing.T) {
	h, buyer, blocks := armedForHeavy(t, growthFixture{
		lanes: &fakeLanes{count: 2, readable: true}, tradeHulls: 15,
		outflow: &fakeOutflow{obs: observedOutflow(10_000, 0, 5_000)},
	})

	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if buyer.calls != 1 {
		t.Fatalf("a 15-hull pool must not block on size, bought %d (blocked by %v)", buyer.calls, blocks.blocked)
	}
}

// THE PREMIUM BINDS ON THE PRICE ACTUALLY PAID, not on the quote the guards judged. A premium
// measured against the quote would always read 0 and the series would be worthless.
func TestGrowthHeavyBuy_PremiumBindsOnTheExecutedPrice(t *testing.T) {
	h, _, _ := armedForHeavy(t, growthFixture{})
	premium := &recordingPremiumSink{}
	h.SetMetricsSink(premium)
	h.SetPurchaser(&overpayingPurchaser{paid: 1_650_000})

	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if premium.calls != 1 {
		t.Fatalf("expected one premium observation, got %d", premium.calls)
	}
	if premium.paid != 1_650_000 {
		t.Fatalf("the premium must bind on the executed price, got %d", premium.paid)
	}
}

type recordingPremiumSink struct {
	noopGrowthSink
	calls          int
	paid, cheapest int64
}

func (r *recordingPremiumSink) ObserveHeavyPricePremium(_ string, paid, cheapestKnown int64) {
	r.calls++
	r.paid, r.cheapest = paid, cheapestKnown
}

// overpayingPurchaser executes at a price ABOVE the quote — the presence-lag case the premium
// series exists to measure.
type overpayingPurchaser struct{ paid int64 }

func (p *overpayingPurchaser) BuyAndDedicate(_ context.Context, order BuyOrder) (BuyResult, error) {
	return BuyResult{ShipSymbol: "SHIP-1", Price: p.paid, Dedicated: true}, nil
}

// THE TRADE-HULL FALLBACK. The pool is configured to buy the preferred heavy and no discovered
// shipyard sells one, so the price guard would block every tick while profitable lanes sat
// unflown. The fallback widens WHICH hull is offered to the guards; the preferred type is asked
// FIRST every tick, so the moment a yard selling it is found it wins back with no intervention.
func TestGrowthHeavyBuy_FallsBackToThePriceableTradeHull(t *testing.T) {
	h, buyer, _ := armedForHeavy(t, growthFixture{})
	yards := &fakeTypedYardPrice{byType: map[string]int64{"SHIP_LIGHT_HAULER": 374176}}
	h.SetYardPriceReader(yards)

	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if buyer.calls != 1 {
		t.Fatalf("expected one buy on the fallback hull, got %d", buyer.calls)
	}
	o := buyer.lastOrder
	if o.ShipType != "SHIP_LIGHT_HAULER" || o.Class != HullClassHeavy {
		t.Fatalf("order = %+v, want the priceable fallback joining the TRADE pool", o)
	}
	if o.ExpectedPrice != 374176 || o.Yard != "SHIP_LIGHT_HAULER-YARD" {
		t.Fatalf("order = %+v, want the fallback type's OWN price and yard", o)
	}
	if len(yards.asked) == 0 || yards.asked[0] != defaultGrowthShipTypeHeavies {
		t.Fatalf("price attempts = %v, want the preferred %s asked FIRST", yards.asked, defaultGrowthShipTypeHeavies)
	}
}

// ONE WALK ANSWERS EVERY CANDIDATE. A shipyard read returns the whole listing array, so asking the
// port once per candidate type re-walks the same counters and multiplies an Earning read a money
// guard is waiting on. The fallback here runs to the LAST type, which is the worst case.
func TestGrowthHeavyBuy_PricesEveryCandidateHullInOneWalk(t *testing.T) {
	h, buyer, _ := armedForHeavy(t, growthFixture{})
	yards := &fakeTypedYardPrice{byType: map[string]int64{"SHIP_LIGHT_HAULER": 374176}}
	h.SetYardPriceReader(yards)

	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if buyer.calls != 1 {
		t.Fatalf("expected the fallback buy to happen, got %d — the fixture no longer exercises the full fallback", buyer.calls)
	}
	if yards.calls != 1 {
		t.Fatalf("yard walks = %d, want 1: every candidate type must be priced from the same walk", yards.calls)
	}
	for _, want := range append([]string{defaultGrowthShipTypeHeavies}, shipyard.TradeHullPreferenceOrder...) {
		if !slices.Contains(yards.asked, want) {
			t.Fatalf("asked = %v, want %s covered by the single walk", yards.asked, want)
		}
	}
}

// Self-correction: with BOTH types priceable the preferred one must win, or the fallback becomes a
// permanent downgrade. A fixture that priced only one could not tell the two apart.
func TestGrowthHeavyBuy_PreferredTypeWinsAgainOnceItIsPriceable(t *testing.T) {
	h, buyer, _ := armedForHeavy(t, growthFixture{})
	h.SetYardPriceReader(&fakeTypedYardPrice{byType: map[string]int64{
		defaultGrowthShipTypeHeavies: 900000,
		"SHIP_LIGHT_HAULER":          374176,
	}})

	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if buyer.calls != 1 || buyer.lastOrder.ShipType != defaultGrowthShipTypeHeavies {
		t.Fatalf("bought %q, want the preferred %s", buyer.lastOrder.ShipType, defaultGrowthShipTypeHeavies)
	}
}

// Fail CLOSED: no trade-capable type priceable ⇒ no buy, blocked on the price guard. The fallback
// widens which hulls are offered to the guards; it never invents a purchase.
func TestGrowthHeavyBuy_NothingPriceableBuysNothing(t *testing.T) {
	h, buyer, blocks := armedForHeavy(t, growthFixture{})
	h.SetYardPriceReader(&fakeTypedYardPrice{byType: map[string]int64{}})

	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if buyer.calls != 0 {
		t.Fatalf("want no buy when nothing can be priced, got %d", buyer.calls)
	}
	if len(blocks.blocked) == 0 || blocks.blocked[0] != GuardPrice {
		t.Fatalf("blocked guards = %v, want price", blocks.blocked)
	}
}

// The fallback decides only WHICH hull is offered to the guard stack. Here the fallback hull IS
// priceable, so price passes — and the treasury is unreadable, so the money guard must still
// refuse. A fallback that bypassed a guard would buy.
func TestGrowthHeavyBuy_FallbackStillRunsEveryGuard(t *testing.T) {
	h, buyer, blocks := armedForHeavy(t, growthFixture{})
	h.SetYardPriceReader(&fakeTypedYardPrice{byType: map[string]int64{"SHIP_LIGHT_HAULER": 374176}})
	h.SetTreasuryReader(&fakeGrowthTreasury{readable: false})

	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if buyer.calls != 0 {
		t.Fatalf("want the money guard to refuse the fallback hull, bought %d", buyer.calls)
	}
	if len(blocks.blocked) == 0 || blocks.blocked[0] != GuardAffordability {
		t.Fatalf("blocked guards = %v, want affordability — every guard still applies to the fallback type", blocks.blocked)
	}
}
