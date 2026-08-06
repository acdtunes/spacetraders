package commands

import (
	"context"
	"testing"
)

// --- fake buy-path ports ---

type fakeTreasury struct {
	credits int64
	ok      bool
	err     error
}

func (f *fakeTreasury) Treasury(ctx context.Context, playerID int) (int64, bool, error) {
	return f.credits, f.ok, f.err
}

type fakeAPIUtil struct {
	pct float64
	ok  bool
	err error
}

func (f *fakeAPIUtil) UtilizationPct(ctx context.Context) (float64, bool, error) {
	return f.pct, f.ok, f.err
}

type fakeYardPrice struct {
	price    int64
	cheapest int64
	yard     string
	ok       bool
	err      error
}

func (f *fakeYardPrice) PriceFor(ctx context.Context, playerID int, class HullClass, shipType string, preferProximal bool) (int64, int64, string, bool, error) {
	return f.price, f.cheapest, f.yard, f.ok, f.err
}

type recordingPurchaser struct {
	orders []BuyOrder
	err    error
}

func (f *recordingPurchaser) BuyAndDedicate(ctx context.Context, order BuyOrder) (BuyResult, error) {
	f.orders = append(f.orders, order)
	if f.err != nil {
		return BuyResult{}, f.err
	}
	return BuyResult{ShipSymbol: "SHIP-" + order.ShipType, Price: order.ExpectedPrice, Dedicated: true}, nil
}

type recordingNotifier struct{ count int }

func (f *recordingNotifier) NotifyPurchase(ctx context.Context, playerID int, class HullClass, shipType string, price int64, note string) error {
	f.count++
	return nil
}

type recordingMetrics struct {
	demand        int
	purchase      int
	blocked       int
	alarm         int
	blockedGuards []GuardName
	// The heavy-trade observations (sp-fwk8z). heavyReserveCalls counts EVERY tick's
	// emission, which is the property that matters: the series only distinguishes
	// "saving" from "stuck" if it is always present.
	heavyReserveCalls int
	// lastReserve is the credits ACTUALLY withheld; lastTarget the ask they are withheld
	// toward. Both are captured because since sp-zg71k they diverge, and a test that watched
	// only the hold could not tell "out of reach, holding nothing" from "nothing to save for".
	lastReserve   int64
	lastTarget    int64
	lastOwned     int
	lastCap       int
	pricePremiums [][2]int64
	// The master-switch gauge (sp-k4wdd). sizingStates records EVERY tick's emission in order,
	// so a test can assert both the value and that the series is continuous across a pause.
	sizingStates []bool
}

func (m *recordingMetrics) RecordDemand(class HullClass, demand, current int) { m.demand++ }
func (m *recordingMetrics) RecordPurchase(class HullClass)                    { m.purchase++ }
func (m *recordingMetrics) RecordBlocked(class HullClass, guard GuardName) {
	m.blocked++
	m.blockedGuards = append(m.blockedGuards, guard)
}
func (m *recordingMetrics) RecordZeroEffectAlarm() { m.alarm++ }
func (m *recordingMetrics) RecordHeavyReserve(_ string, reserve, target int64, owned, cap int) {
	m.heavyReserveCalls++
	m.lastReserve, m.lastTarget, m.lastOwned, m.lastCap = reserve, target, owned, cap
}
func (m *recordingMetrics) ObserveHeavyPricePremium(_ string, paid, cheapestKnown int64) {
	m.pricePremiums = append(m.pricePremiums, [2]int64{paid, cheapestKnown})
}
func (m *recordingMetrics) RecordSizingEnabled(_ string, enabled bool) {
	m.sizingStates = append(m.sizingStates, enabled)
}

// armedHandler wires a coordinator with all buy-path readers healthy (so a shortfall class buys),
// returning the handler plus the purchaser/metrics/notifier for assertions.
func armedHandler(providers ...ClassDemandProvider) (*RunFleetAutosizerCoordinatorHandler, *recordingPurchaser, *recordingMetrics, *recordingNotifier) {
	h := NewRunFleetAutosizerCoordinatorHandler(nil)
	for _, p := range providers {
		h.AddDemandProvider(p)
	}
	h.SetTreasuryReader(&fakeTreasury{credits: 5000000, ok: true})
	h.SetAPIUtilizationReader(&fakeAPIUtil{pct: 40, ok: true})
	h.SetYardPriceReader(&fakeYardPrice{price: 437000, cheapest: 400000, yard: "KA42-A2", ok: true})
	// The owned-heavy census must be READABLE or the heavy cap guard fails closed on every
	// heavy candidate. The harness owns no heavy hull, so the cap has room and each test keeps
	// pinning the guard it is about.
	h.SetHeavyCensusReader(&fakeHeavyCensus{owned: 0})
	purchaser := &recordingPurchaser{}
	metrics := &recordingMetrics{}
	notifier := &recordingNotifier{}
	h.SetPurchaser(purchaser)
	h.SetMetricsSink(metrics)
	h.SetPurchaseNotifier(notifier)
	return h, purchaser, metrics, notifier
}

func lightShortfall() *fakeDemandProvider {
	return &fakeDemandProvider{class: HullClassLight, demand: ClassDemand{
		Demand: 5, Current: 2, Readable: true,
	}}
}

// Happy path: a shortfall class whose guards all pass buys ONE hull, dedicated to its class, with
// the purchase recorded and the captain notified.
func TestReconcile_HappyPath_BuysAndDedicates(t *testing.T) {
	h, purchaser, metrics, notifier := armedHandler(lightShortfall())
	res, err := h.reconcileOnce(context.Background(), &RunFleetAutosizerCoordinatorCommand{PlayerID: 1, ContainerID: "c1"})
	if err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if res.Purchased != 1 {
		t.Fatalf("expected 1 purchase, got %d", res.Purchased)
	}
	if len(purchaser.orders) != 1 {
		t.Fatalf("expected purchaser called once, got %d", len(purchaser.orders))
	}
	o := purchaser.orders[0]
	if o.Class != HullClassLight || o.ShipType != defaultShipTypeLights || o.Yard != "KA42-A2" {
		t.Fatalf("wrong buy order: class=%s ship=%s yard=%s", o.Class, o.ShipType, o.Yard)
	}
	if metrics.purchase != 1 {
		t.Fatalf("expected purchase metric recorded, got %d", metrics.purchase)
	}
	if notifier.count != 1 {
		t.Fatalf("expected 1 captain purchase notice, got %d", notifier.count)
	}
}

// The per-tick cap bounds total buys across classes: two shortfall classes, cap 1 → one buy.
func TestReconcile_PerTickCap_BoundsBuys(t *testing.T) {
	light := lightShortfall()
	heavy := &fakeDemandProvider{class: HullClassHeavy, demand: ClassDemand{
		Demand: 9, Current: 6, Readable: true,
	}}
	h, purchaser, _, _ := armedHandler(light, heavy)
	// Heavy needs its streak; give it enough ticks, but the CAP must still bound each tick to 1.
	cmd := &RunFleetAutosizerCoordinatorCommand{PlayerID: 1, ContainerID: "c1", HeavyUnservedLanesMin: 1, HeavyCap: intPtr(50)}
	res, _ := h.reconcileOnce(context.Background(), cmd)
	if res.Purchased != 1 {
		t.Fatalf("per-tick cap 1 must bound buys to 1 even with two shortfall classes, got %d", res.Purchased)
	}
	if len(purchaser.orders) != 1 {
		t.Fatalf("expected exactly 1 buy under the cap, got %d", len(purchaser.orders))
	}
}

// An unreadable treasury fails the money guards CLOSED — no buy, and the block is metered.
func TestReconcile_TreasuryUnreadable_FailsClosed(t *testing.T) {
	h, purchaser, metrics, _ := armedHandler(lightShortfall())
	h.SetTreasuryReader(&fakeTreasury{ok: false}) // unreadable
	res, _ := h.reconcileOnce(context.Background(), &RunFleetAutosizerCoordinatorCommand{PlayerID: 1, ContainerID: "c1"})
	if res.Purchased != 0 || len(purchaser.orders) != 0 {
		t.Fatalf("unreadable treasury must fail closed: purchased=%d", res.Purchased)
	}
	if metrics.blocked == 0 || metrics.blockedGuards[0] != GuardAffordability {
		t.Fatalf("expected an affordability block metered, got %v", metrics.blockedGuards)
	}
}

// A nil purchaser (mis-wire) still evaluates + approves but cannot spend — no buy, surfaced.
func TestReconcile_NoPurchaser_NoSpend(t *testing.T) {
	h, _, _, _ := armedHandler(lightShortfall())
	h.SetPurchaser(nil)
	res, _ := h.reconcileOnce(context.Background(), &RunFleetAutosizerCoordinatorCommand{PlayerID: 1, ContainerID: "c1"})
	if res.Purchased != 0 {
		t.Fatalf("a nil purchaser must not spend, got %d", res.Purchased)
	}
}

// The heavy anti-thrash streak: an unserved-lane shortfall must persist heavy_unserved_lanes_min
// consecutive ticks before a heavy is bought.
func TestReconcile_HeavyStreakGate(t *testing.T) {
	heavy := &fakeDemandProvider{class: HullClassHeavy, demand: ClassDemand{
		Demand: 9, Current: 6, Readable: true,
	}}
	h, purchaser, _, _ := armedHandler(heavy)
	cmd := &RunFleetAutosizerCoordinatorCommand{PlayerID: 1, ContainerID: "c1", HeavyUnservedLanesMin: 3, HeavyCap: intPtr(50)}

	for tick := 1; tick <= 2; tick++ {
		h.reconcileOnce(context.Background(), cmd)
		if len(purchaser.orders) != 0 {
			t.Fatalf("heavy must NOT buy before the streak is met (tick %d)", tick)
		}
	}
	// Third consecutive tick meets the streak → buy.
	h.reconcileOnce(context.Background(), cmd)
	if len(purchaser.orders) != 1 {
		t.Fatalf("heavy must buy once the %d-tick streak is met, got %d buys", 3, len(purchaser.orders))
	}
	if purchaser.orders[0].ShipType != defaultShipTypeHeavies {
		t.Fatalf("heavy buy must use the heavy ship type, got %s", purchaser.orders[0].ShipType)
	}
}

// The zero-effect alarm fires ONCE (edge-triggered) after demand persists with no buy for
// zero_effect_alarm_ticks consecutive ticks.
func TestReconcile_ZeroEffectAlarm_EdgeTriggered(t *testing.T) {
	h, _, metrics, _ := armedHandler(lightShortfall())
	h.SetTreasuryReader(&fakeTreasury{ok: false}) // every tick blocks on affordability
	cmd := &RunFleetAutosizerCoordinatorCommand{PlayerID: 1, ContainerID: "c1", ZeroEffectAlarmTicks: 4}

	for tick := 1; tick <= 3; tick++ {
		h.reconcileOnce(context.Background(), cmd)
		if metrics.alarm != 0 {
			t.Fatalf("alarm must not fire before %d ticks (fired at tick %d)", 4, tick)
		}
	}
	h.reconcileOnce(context.Background(), cmd) // 4th consecutive stuck tick
	if metrics.alarm != 1 {
		t.Fatalf("alarm must fire once at the %d-tick threshold, got %d", 4, metrics.alarm)
	}
	h.reconcileOnce(context.Background(), cmd) // 5th — must NOT re-fire (edge-triggered)
	if metrics.alarm != 1 {
		t.Fatalf("alarm must be edge-triggered (fire once per episode), got %d", metrics.alarm)
	}
}

// When API utilization is at/over the ceiling, the autosizer does NOT increase
// concurrency — the shortfall class is held (no buy) and the block is metered against api_util.
func TestReconcile_APIUtilSaturated_HoldsGrowth(t *testing.T) {
	h, purchaser, metrics, _ := armedHandler(lightShortfall())
	h.SetAPIUtilizationReader(&fakeAPIUtil{pct: 90, ok: true}) // above the default 85 ceiling
	res, err := h.reconcileOnce(context.Background(), &RunFleetAutosizerCoordinatorCommand{PlayerID: 1, ContainerID: "c1"})
	if err != nil {
		t.Fatalf("a saturated API must hold growth, not error: %v", err)
	}
	if res.Purchased != 0 || len(purchaser.orders) != 0 {
		t.Fatalf("saturated API must NOT grow the fleet: purchased=%d orders=%d", res.Purchased, len(purchaser.orders))
	}
	if !containsGuard(metrics.blockedGuards, GuardAPIUtil) {
		t.Fatalf("expected an api_util block metered, got %v", metrics.blockedGuards)
	}
}

// When the utilization metric is unreadable, the autosizer fails CLOSED — it HOLDS growth (no
// buy). A transient read failure holds steady; it never errors or tears the fleet down (the
// autosizer only ever buys).
func TestReconcile_APIUtilUnreadable_HoldsGrowth(t *testing.T) {
	h, purchaser, metrics, _ := armedHandler(lightShortfall())
	h.SetAPIUtilizationReader(&fakeAPIUtil{ok: false}) // utilization surface unreadable
	res, err := h.reconcileOnce(context.Background(), &RunFleetAutosizerCoordinatorCommand{PlayerID: 1, ContainerID: "c1"})
	if err != nil {
		t.Fatalf("an unreadable utilization must hold steady, not error: %v", err)
	}
	if res.Purchased != 0 || len(purchaser.orders) != 0 {
		t.Fatalf("unreadable utilization must fail closed (hold growth): purchased=%d orders=%d", res.Purchased, len(purchaser.orders))
	}
	if !containsGuard(metrics.blockedGuards, GuardAPIUtil) {
		t.Fatalf("expected an api_util block metered on the unreadable signal, got %v", metrics.blockedGuards)
	}
}

// An UNWIRED utilization reader (nil) fails CLOSED too — a mis-wired coordinator holds growth
// rather than silently permitting unbounded concurrency.
func TestReconcile_APIUtilReaderUnwired_HoldsGrowth(t *testing.T) {
	h, purchaser, _, _ := armedHandler(lightShortfall())
	h.SetAPIUtilizationReader(nil) // never wired
	res, _ := h.reconcileOnce(context.Background(), &RunFleetAutosizerCoordinatorCommand{PlayerID: 1, ContainerID: "c1"})
	if res.Purchased != 0 || len(purchaser.orders) != 0 {
		t.Fatalf("an unwired api_util reader must fail closed: purchased=%d orders=%d", res.Purchased, len(purchaser.orders))
	}
}

func containsGuard(gs []GuardName, want GuardName) bool {
	for _, g := range gs {
		if g == want {
			return true
		}
	}
	return false
}

// fakeHeavyCensus is the tag-independent owned-heavy census. err ⇒ unreadable ⇒ the heavy cap
// guard fails closed.
type fakeHeavyCensus struct {
	owned int
	err   error
}

func (f *fakeHeavyCensus) HeaviesOwned(_ context.Context, _ int) (int, error) {
	return f.owned, f.err
}

// fakeHeavyYard is the SHARED heavy-target read behind the reservation: the yard the purchase
// path would target and its ask, NOT the cheapest ask on the map (sp-fwk8z).
//
// found doubles as both halves of the real answer — a priced target implies an open capability —
// because every existing test that sets it means "a heavy is buyable". The capability-open-but-
// unpriced state, which found cannot express, is exercised explicitly where it matters.
type fakeHeavyYard struct {
	price int64
	found bool
	// capabilityOpenUnpriced is the production state a priced-only fake cannot reach: a known
	// heavy yard whose ask nobody has read. It opens the capability with no usable price.
	capabilityOpenUnpriced bool
	err                    error
}

// The target comes back ALONGSIDE any error, mirroring fakeHeavyCensus (which returns owned
// alongside its own). A fake that zeroed its answer on failure would make the coordinator's
// `err == nil` gate untestable: with a zero target the reserve is 0 by ARITHMETIC rather than by
// the guard, so swallowing the error would leave the suite green while a blind price read held
// treasury against a number nobody could see.
func (f *fakeHeavyYard) HeavyTarget(_ context.Context, _ int) (HeavyTargetYard, error) {
	if !f.found {
		return HeavyTargetYard{CapabilityOpen: f.capabilityOpenUnpriced}, f.err
	}
	return HeavyTargetYard{CapabilityOpen: true, Priced: true, WaypointSymbol: "X1-QR78-AE4F", PurchasePrice: f.price}, f.err
}

// intPtr is the *int helper for HeavyCap, whose pointer-ness is what lets an explicit 0
// (operator hold) be told from unset.
func intPtr(v int) *int { return &v }

// --- the trade-hull fallback (the preferred type cannot be priced anywhere) ---

// fakeTypedYardPrice prices ONE named set of ship types and refuses every other, which is
// the whole point: a fake that answers the same price for any type makes the preferred
// type and the fallback INDISTINGUISHABLE, and a fallback that never fired would still
// pass. Every asked-for type is recorded so a test can pin the ORDER of the attempts.
type fakeTypedYardPrice struct {
	byType map[string]int64
	asked  []string
}

func (f *fakeTypedYardPrice) PriceFor(_ context.Context, _ int, _ HullClass, shipType string, _ bool) (int64, int64, string, bool, error) {
	f.asked = append(f.asked, shipType)
	price, priced := f.byType[shipType]
	if !priced {
		return 0, 0, "", false, nil // unpriceable at every reachable yard
	}
	return price, price, shipType + "-YARD", true, nil
}

// tradeShortfall is a heavy (trade) class with unserved lanes and healthy economics, so the
// only thing left to decide the outcome is which hull the price step resolves.
func tradeShortfall() *fakeDemandProvider {
	return &fakeDemandProvider{class: HullClassHeavy, demand: ClassDemand{
		Demand: 9, Current: 6, Readable: true,
	}}
}

func tradeCmd() *RunFleetAutosizerCoordinatorCommand {
	return &RunFleetAutosizerCoordinatorCommand{
		PlayerID: 1, ContainerID: "c1", HeavyUnservedLanesMin: 1, HeavyCap: intPtr(50),
	}
}

func TestReconcile_TradeHullFallback_BuysThePriceableTypeWhenThePreferredCannotBePriced(t *testing.T) {
	// The live defect: the trade pool is configured to buy SHIP_HEAVY_FREIGHTER and no
	// shipyard discovered this era sells one, so the price guard blocked every tick while 8
	// profitable lanes sat unflown. Only the light hauler is priceable here.
	h, purchaser, _, _ := armedHandler(tradeShortfall())
	yards := &fakeTypedYardPrice{byType: map[string]int64{"SHIP_LIGHT_HAULER": 374176}}
	h.SetYardPriceReader(yards)

	res, err := h.reconcileOnce(context.Background(), tradeCmd())
	if err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if res.Purchased != 1 || len(purchaser.orders) != 1 {
		t.Fatalf("purchased=%d orders=%d, want one buy on the fallback hull", res.Purchased, len(purchaser.orders))
	}
	o := purchaser.orders[0]
	if o.ShipType != "SHIP_LIGHT_HAULER" {
		t.Fatalf("bought %q, want the priceable fallback SHIP_LIGHT_HAULER", o.ShipType)
	}
	if o.Class != HullClassHeavy {
		t.Fatalf("class = %s, want HullClassHeavy — the fallback hull still joins the TRADE pool", o.Class)
	}
	if o.ExpectedPrice != 374176 || o.Yard != "SHIP_LIGHT_HAULER-YARD" {
		t.Fatalf("order = %+v, want the fallback type's OWN price and yard, not the preferred type's", o)
	}
	// The preferred type must be tried FIRST, or the fallback is not a fallback.
	if len(yards.asked) == 0 || yards.asked[0] != defaultShipTypeHeavies {
		t.Fatalf("price attempts = %v, want the preferred %s asked first", yards.asked, defaultShipTypeHeavies)
	}
}

func TestReconcile_TradeHullFallback_PreferredTypeWinsAgainOnceItIsPriceable(t *testing.T) {
	// Self-correction: exploration is actively hunting a heavy yard, and the moment one is
	// found the preferred type must win back with no intervention. BOTH types are priceable
	// here — a fixture that priced only one could not tell a fallback from a downgrade.
	h, purchaser, _, _ := armedHandler(tradeShortfall())
	h.SetYardPriceReader(&fakeTypedYardPrice{byType: map[string]int64{
		// Priced so every OTHER guard passes: the only thing this test may turn on is
		// which of the two priceable types the resolver picks.
		"SHIP_HEAVY_FREIGHTER": 900000,
		"SHIP_LIGHT_HAULER":    374176,
	}})

	if _, err := h.reconcileOnce(context.Background(), tradeCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if len(purchaser.orders) != 1 {
		t.Fatalf("orders = %d, want 1", len(purchaser.orders))
	}
	if got := purchaser.orders[0].ShipType; got != defaultShipTypeHeavies {
		t.Fatalf("bought %q, want the preferred %s — the fallback must not become a permanent downgrade", got, defaultShipTypeHeavies)
	}
}

func TestReconcile_TradeHullFallback_NothingPriceableBuysNothing(t *testing.T) {
	// Fail CLOSED, exactly as today: no trade-capable type priceable → no buy, blocked on
	// the price guard. The fallback widens WHICH hulls are offered to the guards; it never
	// invents a purchase.
	h, purchaser, metrics, _ := armedHandler(tradeShortfall())
	h.SetYardPriceReader(&fakeTypedYardPrice{byType: map[string]int64{}})

	res, _ := h.reconcileOnce(context.Background(), tradeCmd())
	if res.Purchased != 0 || len(purchaser.orders) != 0 {
		t.Fatalf("purchased=%d orders=%d, want no buy when nothing can be priced", res.Purchased, len(purchaser.orders))
	}
	if len(metrics.blockedGuards) == 0 || metrics.blockedGuards[0] != GuardPrice {
		t.Fatalf("blocked guards = %v, want price", metrics.blockedGuards)
	}
}

func TestReconcile_TradeHullFallback_StillRunsEveryGuard(t *testing.T) {
	// The fallback decides only WHICH hull is offered to the guard stack. Here the fallback
	// hull IS priceable, so price passes — and the treasury is unreadable, so the money
	// guard must still refuse. A fallback that bypassed a guard would buy.
	h, purchaser, metrics, _ := armedHandler(tradeShortfall())
	h.SetYardPriceReader(&fakeTypedYardPrice{byType: map[string]int64{"SHIP_LIGHT_HAULER": 374176}})
	h.SetTreasuryReader(&fakeTreasury{ok: false})

	res, _ := h.reconcileOnce(context.Background(), tradeCmd())
	if res.Purchased != 0 || len(purchaser.orders) != 0 {
		t.Fatalf("purchased=%d orders=%d, want the money guard to refuse the fallback hull", res.Purchased, len(purchaser.orders))
	}
	// treasury_pct, not treasury_floor: the trade class carries the 25% big-ticket
	// affordability rule (lights do not) and it is judged first, so it is the guard an
	// unreadable treasury trips for a heavy candidate.
	if len(metrics.blockedGuards) == 0 || metrics.blockedGuards[0] != GuardAffordability {
		t.Fatalf("blocked guards = %v, want affordability — every guard still applies to the fallback type", metrics.blockedGuards)
	}
}

func TestReconcile_TradeHullFallback_DoesNotSubstituteForNonTradeClasses(t *testing.T) {
	// The fallback is TRADE-scoped. The explorer buys REACH — a substituted freighter cannot
	// warp off the gate network — and the light pool's own type is already priceable, so
	// neither may ever be silently swapped. An unpriceable light simply does not buy.
	h, purchaser, _, _ := armedHandler(lightShortfall())
	yards := &fakeTypedYardPrice{byType: map[string]int64{"SHIP_HEAVY_FREIGHTER": 2000000}}
	h.SetYardPriceReader(yards)

	res, _ := h.reconcileOnce(context.Background(), &RunFleetAutosizerCoordinatorCommand{PlayerID: 1, ContainerID: "c1"})
	if res.Purchased != 0 || len(purchaser.orders) != 0 {
		t.Fatalf("purchased=%d orders=%d, want no substitution outside the trade pool", res.Purchased, len(purchaser.orders))
	}
	for _, asked := range yards.asked {
		if asked != defaultShipTypeLights {
			t.Fatalf("light class priced %q — only its own configured type may be asked for", asked)
		}
	}
}
