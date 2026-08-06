package commands

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	"github.com/andrescamacho/spacetraders-go/internal/domain/fleetgrowth"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// --- fakes -------------------------------------------------------------------

type fakeLanes struct {
	count    int
	readable bool
	err      error
}

func (f *fakeLanes) UnservedLaneCount(ctx context.Context, playerID int) (int, bool, error) {
	return f.count, f.readable, f.err
}

type fakeOutflow struct {
	total, largest int64
	err            error
	since          time.Time
	calls          int
}

func (f *fakeOutflow) CargoOutflowSince(ctx context.Context, playerID int, since time.Time) (int64, int64, error) {
	f.calls++
	f.since = since
	return f.total, f.largest, f.err
}

type fakeHighWater struct {
	value    int64
	readable bool
	err      error
	since    time.Time
	calls    int
}

func (f *fakeHighWater) TreasuryHighWaterSince(ctx context.Context, playerID shared.PlayerID, since time.Time) (int64, bool, error) {
	f.calls++
	f.since = since
	return f.value, f.readable, f.err
}

type fakeTradeHulls struct {
	count int
	err   error
}

func (f *fakeTradeHulls) TradeHulls(ctx context.Context, playerID int) (int, error) {
	return f.count, f.err
}

type fakeGrowthTreasury struct {
	credits  int64
	readable bool
}

func (f *fakeGrowthTreasury) Treasury(ctx context.Context, playerID int) (int64, bool, error) {
	return f.credits, f.readable, nil
}

type fakeGrowthAPIUtil struct{ pct float64 }

func (f *fakeGrowthAPIUtil) UtilizationPct(ctx context.Context) (float64, bool, error) {
	return f.pct, true, nil
}

type fakeGrowthCensus struct {
	owned int
	err   error
}

func (f *fakeGrowthCensus) HeaviesOwned(ctx context.Context, playerID int) (int, error) {
	return f.owned, f.err
}

type fakeGrowthYard struct{ ask int64 }

func (f *fakeGrowthYard) HeavyTarget(ctx context.Context, playerID int) (HeavyTargetYard, error) {
	return HeavyTargetYard{
		CapabilityOpen: true,
		Priced:         f.ask > 0,
		WaypointSymbol: "X1-AA-YARD",
		PurchasePrice:  f.ask,
	}, nil
}

type growthYardPriceCounter struct {
	ask   int64
	calls int
}

func (c *growthYardPriceCounter) PriceFor(ctx context.Context, playerID int, class HullClass, shipType string, preferProximal bool) (int64, int64, string, bool, error) {
	c.calls++
	if c.ask <= 0 {
		return 0, 0, "", false, nil
	}
	return c.ask, c.ask, "X1-AA-YARD", true, nil
}

type growthPurchaseRecorder struct {
	calls     int
	lastOrder BuyOrder
}

func (r *growthPurchaseRecorder) BuyAndDedicate(ctx context.Context, order BuyOrder) (BuyResult, error) {
	r.calls++
	r.lastOrder = order
	return BuyResult{ShipSymbol: "SHIP-1", Price: order.ExpectedPrice, Dedicated: true}, nil
}

// noopGrowthSink satisfies the whole sink so a test recorder can override ONE method without the
// rest panicking. Embedding the bare interface would compile and then panic on the first
// un-overridden call, which reads as a coordinator bug rather than a fixture gap.
type noopGrowthSink struct{}

func (noopGrowthSink) RecordWave(string, common.Wave, common.WaveProbeReason) {}
func (noopGrowthSink) RecordGrowthEnabled(string, bool)                       {}
func (noopGrowthSink) RecordHeavyReserve(string, int64, int64, int, int)      {}
func (noopGrowthSink) RecordWorkingCapital(string, int64)                     {}
func (noopGrowthSink) RecordDemand(HullClass, int, int)                       {}
func (noopGrowthSink) RecordPurchase(HullClass)                               {}
func (noopGrowthSink) RecordBlocked(HullClass, GuardName)                     {}
func (noopGrowthSink) RecordZeroEffectAlarm()                                 {}
func (noopGrowthSink) ObserveHeavyPricePremium(string, int64, int64)          {}

type recordingWaveSink struct {
	noopGrowthSink
	waves   []common.Wave
	reasons []common.WaveProbeReason
}

func (r *recordingWaveSink) RecordWave(playerID string, w common.Wave, reason common.WaveProbeReason) {
	r.waves = append(r.waves, w)
	r.reasons = append(r.reasons, reason)
}

type fixedGrowthClock struct{ now time.Time }

func (c fixedGrowthClock) Now() time.Time      { return c.now }
func (c fixedGrowthClock) Sleep(time.Duration) {}

// stubGrowthConfig is the live-knob snapshot source. It exists so a test can express the master
// switch OFF, which has no other expression: an unwired reader means ON.
type stubGrowthConfig map[string]interface{}

func (s stubGrowthConfig) Snapshot(ctx context.Context, containerID string, playerID int) (liveconfig.Snapshot, error) {
	return liveconfig.Snapshot(s), nil
}

// --- fixture -----------------------------------------------------------------

func boolPtr(v bool) *bool { return &v }

// growthFixture is one tick's world. Every *bool field defaults to TRUE when nil, because the
// coordinator's own defaults are ON and readable and a fixture that silently defaulted them off
// would make most of these tests pass for the wrong reason.
type growthFixture struct {
	lanes   *fakeLanes
	outflow *fakeOutflow

	growthEnabled *bool

	treasury  int64
	highWater int64
	// highWaterReadable defaults TRUE; false is the unobservable window.
	highWaterReadable *bool

	yardAsk      int64
	heaviesOwned int
	tradeHulls   int
	streak       int
}

func growthCmd() *RunFleetGrowthCoordinatorCommand {
	return &RunFleetGrowthCoordinatorCommand{PlayerID: 1, ContainerID: "growth-1"}
}

// newGrowthHandlerWith builds the coordinator through its setters, the way the composition root
// does. The high-water mark defaults to the live balance so a test that says nothing about the
// cycle describes a fleet whose peak IS where it stands.
func newGrowthHandlerWith(t *testing.T, f growthFixture) *RunFleetGrowthCoordinatorHandler {
	t.Helper()
	h := NewRunFleetGrowthCoordinatorHandler(fixedGrowthClock{now: time.Unix(1_700_000_000, 0)})

	if f.lanes != nil {
		h.SetUnservedLaneReader(f.lanes)
	}
	h.SetTreasuryReader(&fakeGrowthTreasury{credits: f.treasury, readable: true})
	h.SetAPIUtilizationReader(&fakeGrowthAPIUtil{pct: 10})
	h.SetYardPriceReader(&growthYardPriceCounter{ask: f.yardAsk})
	h.SetHeavyCensusReader(&fakeGrowthCensus{owned: f.heaviesOwned})
	h.SetHeavyYardReader(&fakeGrowthYard{ask: f.yardAsk})
	h.SetTradeHullCounter(&fakeTradeHulls{count: f.tradeHulls})

	highWater := f.highWater
	if highWater == 0 {
		highWater = f.treasury
	}
	readable := true
	if f.highWaterReadable != nil {
		readable = *f.highWaterReadable
	}
	h.SetTreasuryHighWaterReader(&fakeHighWater{value: highWater, readable: readable})

	outflow := f.outflow
	if outflow == nil {
		outflow = &fakeOutflow{}
	}
	h.SetCargoOutflowReader(outflow)

	if f.growthEnabled != nil && !*f.growthEnabled {
		h.SetGrowthConfigReader(stubGrowthConfig{growthEnabledKey: growthDisabled})
	}

	if f.streak > 0 {
		h.coordinatorState(growthCmd().ContainerID).heavyShortfallStreak = f.streak
	}
	return h
}

// --- tests -------------------------------------------------------------------

// THE WAVE IS PUBLISHED EVERY TICK ON EVERY PATH. A HEAVY wave and a stalled coordinator both
// look like "no probes bought", so the operator must be able to read the state from a series
// rather than infer it from the absence of one.
func TestGrowthReconcile_PublishesTheWaveEveryTick(t *testing.T) {
	sink := &recordingWaveSink{}
	h := newGrowthHandlerWith(t, growthFixture{lanes: &fakeLanes{count: 0, readable: true}})
	h.SetMetricsSink(sink)

	for i := 0; i < 3; i++ {
		if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	if len(sink.waves) != 3 {
		t.Fatalf("expected the wave published on all 3 ticks, got %d", len(sink.waves))
	}
	if sink.reasons[0] != common.WaveProbeReasonLanesServed {
		t.Fatalf("expected lanes_served, got %q", sink.reasons[0])
	}
}

// The master switch OFF must publish a PROBE wave, not go silent. A silent coordinator would
// pause probe buying for a buyer that is switched off — a deadlock no spender can clear.
func TestGrowthReconcile_DisabledPublishesProbeAndBuysNothing(t *testing.T) {
	sink := &recordingWaveSink{}
	h := newGrowthHandlerWith(t, growthFixture{lanes: &fakeLanes{count: 9, readable: true}, growthEnabled: boolPtr(false)})
	h.SetMetricsSink(sink)

	res, err := h.reconcileOnce(context.Background(), growthCmd())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Purchased != 0 {
		t.Fatalf("a disabled coordinator must buy nothing, bought %d", res.Purchased)
	}
	if len(sink.waves) != 1 || sink.waves[0] != common.WaveProbe {
		t.Fatalf("expected exactly one PROBE wave published, got %v", sink.waves)
	}
	if sink.reasons[0] != common.WaveProbeReasonGrowthDisabled {
		t.Fatalf("expected growth_disabled, got %q", sink.reasons[0])
	}
}

// OFF STOPS THE READS, not just the buying — that is the whole point of the switch, because
// the shipyard price walk runs BEFORE the guards can block it.
//
// THE FIXTURE MUST REACH THE WALK, or the assertion is vacuous: the price reader is consulted only
// once a HEAVY wave and a surviving shortfall carry a candidate into the request, so a thin
// treasury or an unpriced target would leave this passing with the switch deleted entirely.
func TestGrowthReconcile_DisabledReadsNoPrices(t *testing.T) {
	prices := &growthYardPriceCounter{ask: 1_000_000}
	h := newGrowthHandlerWith(t, growthFixture{
		lanes: &fakeLanes{count: 9, readable: true}, treasury: 12_000_000, yardAsk: 1_000_000, streak: 3,
		growthEnabled: boolPtr(false),
	})
	h.SetYardPriceReader(prices)

	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prices.calls != 0 {
		t.Fatalf("a disabled coordinator must walk no shipyards, got %d PriceFor calls", prices.calls)
	}
}

// A PROBE wave buys no heavy, whatever the demand says. The PROBE here comes from demonstrated
// capacity — a peak far below the entry threshold — not from where the live balance happens to
// sit, which is the whole point of the ruled measure.
//
// EVERY OTHER GUARD IS DELIBERATELY SATISFIED, which is what makes the wave gate the only thing
// refusing this buy. A peak below the live balance cannot occur in the world; it is constructed
// here because under physically-consistent inputs the affordability guard would refuse first and
// the gate would never be reached, so the test would pass without exercising it.
func TestGrowthReconcile_ProbeWaveBuysNoHeavy(t *testing.T) {
	buyer := &growthPurchaseRecorder{}
	h := newGrowthHandlerWith(t, growthFixture{
		lanes:     &fakeLanes{count: 9, readable: true},
		treasury:  12_000_000, // momentarily flush, and it must not matter
		highWater: 400_000,    // the peak across the cycle cannot reach the ask
		yardAsk:   1_000_000,
		streak:    3,
	})
	h.SetPurchaser(buyer)

	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buyer.calls != 0 {
		t.Fatalf("a PROBE wave must buy no heavy, got %d buys", buyer.calls)
	}
}

// A HEAVY wave with every guard satisfied buys exactly one hull, dedicated to the trade pool.
func TestGrowthReconcile_HeavyWaveBuysOne(t *testing.T) {
	buyer := &growthPurchaseRecorder{}
	h := newGrowthHandlerWith(t, growthFixture{
		lanes:    &fakeLanes{count: 9, readable: true},
		treasury: 12_000_000,
		yardAsk:  1_000_000,
		streak:   3, // the anti-thrash streak already satisfied
	})
	h.SetPurchaser(buyer)

	res, err := h.reconcileOnce(context.Background(), growthCmd())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Purchased != 1 || buyer.calls != 1 {
		t.Fatalf("expected exactly one heavy bought, got purchased=%d calls=%d", res.Purchased, buyer.calls)
	}
	if buyer.lastOrder.Class != HullClassHeavy {
		t.Fatalf("expected a heavy order, got %q", buyer.lastOrder.Class)
	}
}

// THE ANTI-THRASH STREAK LIVES ON THE BUY, NOT THE PREDICATE. A transient spike in the lane
// ranking must not trigger a seven-figure purchase; a transient spike in the WAVE costs only a
// paused probe tick, which is why the streak is deliberately asymmetric.
func TestGrowthReconcile_HeavyBuyWaitsForTheStreak(t *testing.T) {
	buyer := &growthPurchaseRecorder{}
	h := newGrowthHandlerWith(t, growthFixture{
		lanes: &fakeLanes{count: 9, readable: true}, treasury: 12_000_000, yardAsk: 1_000_000,
	})
	h.SetPurchaser(buyer)

	// Ticks 1 and 2 are inside the 3-tick streak: HEAVY wave, no purchase.
	for i := 0; i < 2; i++ {
		if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	if buyer.calls != 0 {
		t.Fatalf("no heavy may be bought before the streak is met, got %d", buyer.calls)
	}
	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("tick 3: %v", err)
	}
	if buyer.calls != 1 {
		t.Fatalf("the third consecutive tick must buy, got %d", buyer.calls)
	}
}

// The working-capital term reaches the guard. A treasury that clears price+margin+floor but
// not the observed cargo commitment must refuse the buy.
func TestGrowthReconcile_WorkingCapitalBlocksTheBuy(t *testing.T) {
	buyer := &growthPurchaseRecorder{}
	h := newGrowthHandlerWith(t, growthFixture{
		lanes: &fakeLanes{count: 9, readable: true}, treasury: 5_000_000, yardAsk: 1_000_000, streak: 3,
		tradeHulls: 4,
		outflow:    &fakeOutflow{total: 2_000_000, largest: 100_000},
	})
	h.SetPurchaser(buyer)

	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buyer.calls != 0 {
		t.Fatalf("the observed cargo commitment must block this buy, got %d", buyer.calls)
	}
}

// Fail-closed: an unreadable ledger is not a zero commitment.
func TestGrowthReconcile_UnreadableOutflowBuysNothing(t *testing.T) {
	buyer := &growthPurchaseRecorder{}
	h := newGrowthHandlerWith(t, growthFixture{
		lanes: &fakeLanes{count: 9, readable: true}, treasury: 12_000_000, yardAsk: 1_000_000, streak: 3,
		outflow: &fakeOutflow{err: errors.New("ledger down")},
	})
	h.SetPurchaser(buyer)

	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("an unreadable ledger must not fail the tick: %v", err)
	}
	if buyer.calls != 0 {
		t.Fatalf("an unreadable cargo outflow must buy nothing, got %d", buyer.calls)
	}
}

// A TRADING POOL THAT SPENT NOTHING ALL CYCLE IS AN UNOBSERVED COMMITMENT, NOT A ZERO ONE.
// Both working-capital terms read the same rows, so an empty window collapses them together —
// including the hold-fill term, which exists precisely because a fresh hull has no spend history.
// A pool of all-fresh hulls would otherwise reserve nothing at all against a seven-figure hull.
func TestGrowthReconcile_TradeHullsWithNoObservedSpendBuysNothing(t *testing.T) {
	buyer := &growthPurchaseRecorder{}
	h := newGrowthHandlerWith(t, growthFixture{
		lanes: &fakeLanes{count: 9, readable: true}, treasury: 12_000_000, yardAsk: 1_000_000, streak: 3,
		tradeHulls: 4,
		outflow:    &fakeOutflow{}, // the window held no cargo purchase at all
	})
	h.SetPurchaser(buyer)

	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buyer.calls != 0 {
		t.Fatalf("a spending-blind trade pool must buy nothing, got %d", buyer.calls)
	}
}

// THE COLD POOL IS THE ONE CASE WHERE AN EMPTY WINDOW IS A GENUINE ZERO: with no trade hull
// standing there is no cargo commitment to protect, and refusing here would make the FIRST trade
// hull unbuyable forever — a guard that rejects on absence rather than on evidence.
func TestGrowthReconcile_EmptyTradePoolBuysOnTheImmutableFloorAlone(t *testing.T) {
	buyer := &growthPurchaseRecorder{}
	h := newGrowthHandlerWith(t, growthFixture{
		lanes: &fakeLanes{count: 9, readable: true}, treasury: 12_000_000, yardAsk: 1_000_000, streak: 3,
		tradeHulls: 0,
		outflow:    &fakeOutflow{},
	})
	h.SetPurchaser(buyer)

	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buyer.calls != 1 {
		t.Fatalf("the first trade hull must be buyable against the immutable floor alone, got %d", buyer.calls)
	}
}

// BOTH LEDGER OBSERVATIONS ARE ASKED FOR A WINDOW, and the SAME window. Two differently-sized
// trailing windows over one fleet is two answers to one question; a point read is none.
func TestGrowthReconcile_LedgerReadsShareTheOneTradeCycleWindow(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	outflow := &fakeOutflow{}
	highWater := &fakeHighWater{value: 12_000_000, readable: true}
	h := newGrowthHandlerWith(t, growthFixture{lanes: &fakeLanes{count: 9, readable: true}, treasury: 12_000_000, yardAsk: 1_000_000})
	h.SetCargoOutflowReader(outflow)
	h.SetTreasuryHighWaterReader(highWater)

	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := now.Add(-fleetgrowth.TradeCycleWindow)
	if highWater.calls != 1 || !highWater.since.Equal(want) {
		t.Fatalf("the high-water read must span one trade cycle, got calls=%d since=%v want %v", highWater.calls, highWater.since, want)
	}
	if outflow.calls != 1 || !outflow.since.Equal(want) {
		t.Fatalf("the cargo-outflow read must span the SAME window, got calls=%d since=%v want %v", outflow.calls, outflow.since, want)
	}
}

// THE PROPERTY THE RULING BUYS, asserted end-to-end through the coordinator rather than only
// through the pure predicate: the regime does not change as the fleet moves through a trade
// cycle. The live balance sweeps the whole observed band while the high-water mark — the peak of
// that same band — stays put, and every tick must land on the same wave.
//
// A REACHABLE FLEET IS HEAVY AT EVERY POINT IN ITS CYCLE.
func TestGrowthReconcile_ReachableFleetStaysHeavyAcrossTheCycle(t *testing.T) {
	for live := int64(119_000); live <= 1_500_000; live += 37_313 {
		sink := &recordingWaveSink{}
		h := newGrowthHandlerWith(t, growthFixture{
			lanes:     &fakeLanes{count: 9, readable: true},
			treasury:  live,
			highWater: 1_500_000, // the band's peak clears floor + entry for this ask
			yardAsk:   1_916_613,
		})
		h.SetMetricsSink(sink)

		if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
			t.Fatalf("live balance %d: %v", live, err)
		}
		if sink.waves[0] != common.WaveHeavy {
			t.Fatalf("regime flipped to %q at live balance %d — the wave is reading trade-cycle phase", sink.waves[0], live)
		}
	}
}

// AN UNREACHABLE FLEET IS PROBE AT EVERY POINT IN ITS CYCLE — including at a peak that clears
// price+margin momentarily. This is the anti-deadlock regression at the coordinator level:
// a fleet that cannot plausibly reach the ask must never have its probe buying paused for it.
func TestGrowthReconcile_UnreachableFleetStaysProbeAcrossTheCycle(t *testing.T) {
	for live := int64(119_000); live <= 1_500_000; live += 37_313 {
		sink := &recordingWaveSink{}
		buyer := &growthPurchaseRecorder{}
		h := newGrowthHandlerWith(t, growthFixture{
			lanes:     &fakeLanes{count: 9, readable: true},
			treasury:  live,
			highWater: 400_000, // a peak far below floor + entry for this ask
			yardAsk:   1_916_613,
		})
		h.SetMetricsSink(sink)
		h.SetPurchaser(buyer)

		if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
			t.Fatalf("live balance %d: %v", live, err)
		}
		if sink.waves[0] != common.WaveProbe || sink.reasons[0] != common.WaveProbeReasonUnreachable {
			t.Fatalf("at live balance %d expected PROBE/unreachable, got %q/%q", live, sink.waves[0], sink.reasons[0])
		}
		if buyer.calls != 0 {
			t.Fatalf("an unreachable fleet bought a heavy at live balance %d", live)
		}
	}
}

// The measure has to be able to say NO, or the sweeps above prove only that a constant is
// constant. A fleet whose peak clears the bar and one whose peak does not must land on
// different regimes at the SAME live balance.
func TestGrowthReconcile_HighWaterDiscriminatesAtOneLiveBalance(t *testing.T) {
	const live = int64(479_798)
	rich := &recordingWaveSink{}
	poor := &recordingWaveSink{}

	hRich := newGrowthHandlerWith(t, growthFixture{
		lanes: &fakeLanes{count: 9, readable: true}, treasury: live, highWater: 1_500_000, yardAsk: 1_916_613,
	})
	hRich.SetMetricsSink(rich)
	hPoor := newGrowthHandlerWith(t, growthFixture{
		lanes: &fakeLanes{count: 9, readable: true}, treasury: live, highWater: 400_000, yardAsk: 1_916_613,
	})
	hPoor.SetMetricsSink(poor)

	for _, h := range []*RunFleetGrowthCoordinatorHandler{hRich, hPoor} {
		if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if rich.waves[0] != common.WaveHeavy || poor.waves[0] != common.WaveProbe {
		t.Fatalf("the high-water mark is not discriminating: rich=%q poor=%q at one live balance", rich.waves[0], poor.waves[0])
	}
}

// An unobservable window is PROBE, not a zero high-water. A quiet ledger must not read as a
// fleet that has never held money.
func TestGrowthReconcile_UnreadableHighWaterIsProbe(t *testing.T) {
	sink := &recordingWaveSink{}
	h := newGrowthHandlerWith(t, growthFixture{
		lanes: &fakeLanes{count: 9, readable: true}, treasury: 1_500_000,
		highWaterReadable: boolPtr(false), yardAsk: 1_000_000,
	})
	h.SetMetricsSink(sink)

	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sink.waves[0] != common.WaveProbe || sink.reasons[0] != common.WaveProbeReasonCapacityUnreadable {
		t.Fatalf("expected PROBE/capacity_unreadable, got %q/%q", sink.waves[0], sink.reasons[0])
	}
}

// An unwired high-water reader is the same blind read as an unreadable window: PROBE, never a
// silently-zeroed peak that reads as a verdict.
func TestGrowthReconcile_UnwiredHighWaterReaderIsProbe(t *testing.T) {
	sink := &recordingWaveSink{}
	h := newGrowthHandlerWith(t, growthFixture{
		lanes: &fakeLanes{count: 9, readable: true}, treasury: 1_500_000, yardAsk: 1_000_000,
	})
	h.SetTreasuryHighWaterReader(nil)
	h.SetMetricsSink(sink)

	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sink.waves[0] != common.WaveProbe || sink.reasons[0] != common.WaveProbeReasonCapacityUnreadable {
		t.Fatalf("expected PROBE/capacity_unreadable, got %q/%q", sink.waves[0], sink.reasons[0])
	}
}

func TestResolveFleetGrowthConfig_Defaults(t *testing.T) {
	cfg := resolveFleetGrowthConfig(&RunFleetGrowthCoordinatorCommand{})

	if cfg.Tick != defaultGrowthTickSeconds*time.Second {
		t.Errorf("tick default = %v, want %v", cfg.Tick, defaultGrowthTickSeconds*time.Second)
	}
	if cfg.HeavyCap != defaultGrowthHeavyCap {
		t.Errorf("heavy cap default = %d, want %d", cfg.HeavyCap, defaultGrowthHeavyCap)
	}
	if cfg.UnservedLanesMin != defaultGrowthUnservedLanesMin {
		t.Errorf("unserved-lanes-min default = %d, want %d", cfg.UnservedLanesMin, defaultGrowthUnservedLanesMin)
	}
	if cfg.RunwayMilliHours != defaultGrowthRunwayMilliHours {
		t.Errorf("runway default = %d, want %d", cfg.RunwayMilliHours, defaultGrowthRunwayMilliHours)
	}
	if cfg.ShipTypeHeavies != defaultGrowthShipTypeHeavies {
		t.Errorf("ship type default = %q, want %q", cfg.ShipTypeHeavies, defaultGrowthShipTypeHeavies)
	}
	if !cfg.PreferDemandProximalYard {
		t.Errorf("prefer_demand_proximal_yard default = false, want true")
	}
}

// An explicit 0 heavy cap is the operator's HOLD, not an unset knob — the pointer semantics the
// autosizer already carries, moved over unchanged so the two resolve identically.
func TestResolveFleetGrowthConfig_ExplicitZeroHeavyCapIsAHold(t *testing.T) {
	hold := 0
	cfg := resolveFleetGrowthConfig(&RunFleetGrowthCoordinatorCommand{HeavyCap: &hold})
	if cfg.HeavyCap != 0 {
		t.Fatalf("an explicit 0 heavy cap must survive as a hold, got %d", cfg.HeavyCap)
	}
}

// The tune registry and the coordinator must resolve the SAME defaults, or an operator's --show
// reports a number the coordinator does not use.
func TestFleetGrowthTunableDefaults_MatchTheCoordinator(t *testing.T) {
	defaults := FleetGrowthTunableDefaults()
	if defaults[heavyCapKey] != defaultGrowthHeavyCap {
		t.Fatalf("heavy_cap default = %d, want %d", defaults[heavyCapKey], defaultGrowthHeavyCap)
	}
	if defaults[growthEnabledKey] != defaultGrowthEnabled {
		t.Fatalf("growth_enabled default = %d, want %d", defaults[growthEnabledKey], defaultGrowthEnabled)
	}
	if defaults[growthRunwayKey] != defaultGrowthRunwayMilliHours {
		t.Fatalf("growth_runway_milli_hours default = %d, want %d", defaults[growthRunwayKey], defaultGrowthRunwayMilliHours)
	}
}
