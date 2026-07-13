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

type fakeEra struct {
	hours float64
	ok    bool
	err   error
}

func (f *fakeEra) HoursToEraEnd(ctx context.Context) (float64, bool, error) {
	return f.hours, f.ok, f.err
}

type fakeAPIUtil struct {
	pct float64
	ok  bool
	err error
}

func (f *fakeAPIUtil) UtilizationPct(ctx context.Context) (float64, bool, error) {
	return f.pct, f.ok, f.err
}

type fakeFleetSize struct {
	total int
	err   error
}

func (f *fakeFleetSize) TotalHulls(ctx context.Context, playerID int) (int, error) {
	return f.total, f.err
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
}

func (m *recordingMetrics) RecordDemand(class HullClass, demand, current int) { m.demand++ }
func (m *recordingMetrics) RecordPurchase(class HullClass)                    { m.purchase++ }
func (m *recordingMetrics) RecordBlocked(class HullClass, guard GuardName) {
	m.blocked++
	m.blockedGuards = append(m.blockedGuards, guard)
}
func (m *recordingMetrics) RecordZeroEffectAlarm() { m.alarm++ }

// armedHandler wires a coordinator with all buy-path readers healthy (so a shortfall class buys),
// returning the handler plus the purchaser/metrics/notifier for assertions.
func armedHandler(providers ...ClassDemandProvider) (*RunFleetAutosizerCoordinatorHandler, *recordingPurchaser, *recordingMetrics, *recordingNotifier) {
	h := NewRunFleetAutosizerCoordinatorHandler(nil)
	for _, p := range providers {
		h.AddDemandProvider(p)
	}
	h.SetTreasuryReader(&fakeTreasury{credits: 5000000, ok: true})
	h.SetEraClockReader(&fakeEra{hours: 20, ok: true})
	h.SetAPIUtilizationReader(&fakeAPIUtil{pct: 40, ok: true})
	h.SetFleetSizeReader(&fakeFleetSize{total: 20})
	h.SetYardPriceReader(&fakeYardPrice{price: 437000, cheapest: 400000, yard: "KA42-A2", ok: true})
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
		Demand: 5, Current: 2, MarginalRate: 80000, FleetAvgRate: 90000, RateReadable: true, Readable: true,
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
		Demand: 9, Current: 6, MarginalRate: 450000, FleetAvgRate: 500000, RateReadable: true, Readable: true,
	}}
	h, purchaser, _, _ := armedHandler(light, heavy)
	// Heavy needs its streak; give it enough ticks, but the CAP must still bound each tick to 1.
	cmd := &RunFleetAutosizerCoordinatorCommand{PlayerID: 1, ContainerID: "c1", HeavyUnservedLanesMin: 1}
	res, _ := h.reconcileOnce(context.Background(), cmd)
	if res.Purchased != 1 {
		t.Fatalf("per-tick cap 1 must bound buys to 1 even with two shortfall classes, got %d", res.Purchased)
	}
	if len(purchaser.orders) != 1 {
		t.Fatalf("expected exactly 1 buy under the cap, got %d", len(purchaser.orders))
	}
}

// Dry-run evaluates and logs the APPROVED buy but spends nothing.
func TestReconcile_DryRun_NoSpend(t *testing.T) {
	h, purchaser, _, _ := armedHandler(lightShortfall())
	res, _ := h.reconcileOnce(context.Background(), &RunFleetAutosizerCoordinatorCommand{PlayerID: 1, ContainerID: "c1", DryRun: true})
	if res.Purchased != 0 || len(purchaser.orders) != 0 {
		t.Fatalf("dry-run must not spend: purchased=%d orders=%d", res.Purchased, len(purchaser.orders))
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
	if metrics.blocked == 0 || metrics.blockedGuards[0] != GuardTreasuryFloor {
		t.Fatalf("expected a treasury_floor block metered, got %v", metrics.blockedGuards)
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
		Demand: 9, Current: 6, MarginalRate: 450000, FleetAvgRate: 500000, RateReadable: true, Readable: true,
	}}
	h, purchaser, _, _ := armedHandler(heavy)
	cmd := &RunFleetAutosizerCoordinatorCommand{PlayerID: 1, ContainerID: "c1", HeavyUnservedLanesMin: 3}

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
// zero_effect_alarm_ticks consecutive ticks — the mechanized silent-dry-run lesson.
func TestReconcile_ZeroEffectAlarm_EdgeTriggered(t *testing.T) {
	h, _, metrics, _ := armedHandler(lightShortfall())
	h.SetTreasuryReader(&fakeTreasury{ok: false}) // every tick blocks on treasury_floor
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

// sp-a5dq: when API utilization is at/over the ceiling, the autosizer does NOT increase
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

// sp-a5dq: when the utilization metric is unreadable, the autosizer fails CLOSED — it HOLDS growth
// (no buy) rather than the old fail-OPEN that grew concurrency into an unmeasured API. A transient
// read failure holds steady; it never errors or tears the fleet down (the autosizer only ever buys).
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

// sp-a5dq non-regression: an UNWIRED utilization reader (nil) fails CLOSED too — a mis-wired
// coordinator holds growth rather than silently permitting unbounded concurrency.
func TestReconcile_APIUtilReaderUnwired_HoldsGrowth(t *testing.T) {
	h, purchaser, _, _ := armedHandler(lightShortfall())
	h.SetAPIUtilizationReader(nil) // never wired
	res, _ := h.reconcileOnce(context.Background(), &RunFleetAutosizerCoordinatorCommand{PlayerID: 1, ContainerID: "c1"})
	if res.Purchased != 0 || len(purchaser.orders) != 0 {
		t.Fatalf("an unwired api_util reader must fail closed: purchased=%d orders=%d", res.Purchased, len(purchaser.orders))
	}
}

// In-tick total accounting: a buy advances the total hull count so a later class in the SAME tick
// sees the updated fleet size and is blocked by the absolute ceiling.
func TestReconcile_InTickTotalAccounting(t *testing.T) {
	light := lightShortfall()
	heavy := &fakeDemandProvider{class: HullClassHeavy, demand: ClassDemand{
		Demand: 9, Current: 6, MarginalRate: 450000, FleetAvgRate: 500000, RateReadable: true, Readable: true,
	}}
	h, purchaser, metrics, _ := armedHandler(light, heavy)
	h.SetFleetSizeReader(&fakeFleetSize{total: 49}) // one below the total ceiling (default 50)
	cmd := &RunFleetAutosizerCoordinatorCommand{PlayerID: 1, ContainerID: "c1", PurchaseCapPerTick: 2, HeavyUnservedLanesMin: 1}

	res, _ := h.reconcileOnce(context.Background(), cmd)
	if res.Purchased != 1 {
		t.Fatalf("only 1 buy should fit under the absolute ceiling (49→50), got %d", res.Purchased)
	}
	if len(purchaser.orders) != 1 || purchaser.orders[0].Class != HullClassLight {
		t.Fatalf("the first class (light) should take the last ceiling slot")
	}
	// The heavy must have been blocked by the fleet ceiling once the total hit 50 in-tick.
	if !containsGuard(metrics.blockedGuards, GuardFleetCeiling) {
		t.Fatalf("heavy must be blocked by fleet_ceiling after the in-tick buy filled the total, got %v", metrics.blockedGuards)
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
