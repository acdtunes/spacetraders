package commands

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	"github.com/andrescamacho/spacetraders-go/internal/domain/fleetgrowth"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// --- fakes -------------------------------------------------------------------

type fakeLanes struct {
	count    int
	readable bool
	// saturated is the DEPTH verdict, debounced and published by the shared reader beside the
	// count. Its zero value is "not saturated" — the state every fixture written before the
	// switch-back term existed was implicitly asserting, so those cases stay HEAVY unchanged.
	saturated bool
	err       error
}

func (f *fakeLanes) LaneDemand(ctx context.Context, playerID int) (fleetgrowth.LaneDemand, bool, error) {
	return fleetgrowth.LaneDemand{UnservedLanes: f.count, Saturated: f.saturated}, f.readable, f.err
}

type fakeOutflow struct {
	obs   fleetgrowth.CargoOutflow
	err   error
	since time.Time
	calls int
}

func (f *fakeOutflow) CargoOutflowSince(ctx context.Context, playerID int, since time.Time) (fleetgrowth.CargoOutflow, error) {
	f.calls++
	f.since = since
	return f.obs, f.err
}

// observedOutflow builds a WHOLE-window observation — the ordinary case, and the one where netting
// the recovery is honest. Tests that need the truncated window build the struct themselves.
func observedOutflow(spent, recovered, largest int64) fleetgrowth.CargoOutflow {
	return fleetgrowth.CargoOutflow{Spent: spent, Recovered: recovered, Largest: largest, Complete: true}
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

func (noopGrowthSink) RecordWave(string, common.Wave, common.WaveProbeReason)       {}
func (noopGrowthSink) RecordGrowthEnabled(string, bool)                             {}
func (noopGrowthSink) RecordHeavyReserve(string, int64, int64, int, int)            {}
func (noopGrowthSink) RecordWorkingCapital(string, fleetgrowth.WorkingCapitalTerms) {}
func (noopGrowthSink) RecordDemand(HullClass, int, int)                             {}
func (noopGrowthSink) RecordPurchase(HullClass)                                     {}
func (noopGrowthSink) RecordBlocked(HullClass, GuardName)                           {}
func (noopGrowthSink) RecordZeroEffectAlarm()                                       {}
func (noopGrowthSink) ObserveHeavyPricePremium(string, int64, int64)                {}

type recordingWaveSink struct {
	noopGrowthSink
	waves   []common.Wave
	reasons []common.WaveProbeReason
}

// recordingCapitalSink captures the working-capital terms as the coordinator publishes them, which
// is the only way to prove the ARMS reach the gauges rather than being collapsed to a total on the
// way out.
type recordingCapitalSink struct {
	noopGrowthSink
	terms fleetgrowth.WorkingCapitalTerms
	calls int
}

func (r *recordingCapitalSink) RecordWorkingCapital(_ string, terms fleetgrowth.WorkingCapitalTerms) {
	r.calls++
	r.terms = terms
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
	// shortfallHeld pre-loads the anti-thrash WINDOW: how long the shortfall has already stood when
	// the fixture's first tick runs. It replaced a tick count (sp-739gf) — the window is wall clock.
	shortfallHeld time.Duration
}

// growthSettledWindow is "the anti-thrash window is already satisfied", the state most of these
// fixtures want so that the guard each test is about is the one that decides.
const growthSettledWindow = 2 * time.Hour

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

	if f.shortfallHeld > 0 {
		h.coordinatorState(growthCmd().ContainerID).heavyShortfallSince = h.clock.Now().Add(-f.shortfallHeld)
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
		lanes: &fakeLanes{count: 9, readable: true}, treasury: 12_000_000, yardAsk: 1_000_000, shortfallHeld: growthSettledWindow,
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
		lanes:         &fakeLanes{count: 9, readable: true},
		treasury:      12_000_000, // momentarily flush, and it must not matter
		highWater:     400_000,    // the peak across the cycle cannot reach the ask
		yardAsk:       1_000_000,
		shortfallHeld: growthSettledWindow,
	})
	h.SetPurchaser(buyer)

	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buyer.calls != 0 {
		t.Fatalf("a PROBE wave must buy no heavy, got %d buys", buyer.calls)
	}
}

// THE BUY CARRIES THE COORDINATOR'S OWN CONTAINER, and that is not decoration: the purchase claims a
// borrowed signer under it, and the column carries a foreign key, so an order that arrived with an
// empty id could hold no claim and would silently refuse every borrowed hull — fail-closed, correct,
// and invisible. Nothing else in the coordinator would look wrong.
func TestGrowthReconcile_HeavyBuyOrderCarriesTheCoordinatorsContainer(t *testing.T) {
	buyer := &growthPurchaseRecorder{}
	h := newGrowthHandlerWith(t, growthFixture{
		lanes:         &fakeLanes{count: 9, readable: true},
		treasury:      12_000_000,
		yardAsk:       1_000_000,
		shortfallHeld: growthSettledWindow,
	})
	h.SetPurchaser(buyer)

	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buyer.calls != 1 {
		t.Fatalf("expected one purchase, got %d", buyer.calls)
	}
	if buyer.lastOrder.ContainerID != "growth-1" {
		t.Fatalf("the buy must own its claim through the coordinator's container, got %q", buyer.lastOrder.ContainerID)
	}
}

// A HEAVY wave with every guard satisfied buys exactly one hull, dedicated to the trade pool.
func TestGrowthReconcile_HeavyWaveBuysOne(t *testing.T) {
	buyer := &growthPurchaseRecorder{}
	h := newGrowthHandlerWith(t, growthFixture{
		lanes:         &fakeLanes{count: 9, readable: true},
		treasury:      12_000_000,
		yardAsk:       1_000_000,
		shortfallHeld: growthSettledWindow, // the anti-thrash streak already satisfied
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

// THE ANTI-THRASH WINDOW LIVES ON THE BUY, NOT THE PREDICATE. A transient spike in the lane ranking
// must not trigger a seven-figure purchase; a transient spike in the WAVE costs only a paused probe
// tick, which is why the window is deliberately asymmetric.
//
// The fixture is the MARGINAL regime — 9 unserved lanes against a pool of 9 already flying them —
// because that is the only regime the window governs since sp-739gf: a shortfall larger than the pool
// serving it survives halving the surface, so waiting on it buys no information. The clock STEPS, so
// what elapses here is wall time rather than a tick count.
func TestGrowthReconcile_HeavyBuyWaitsForTheDwell(t *testing.T) {
	buyer := &growthPurchaseRecorder{}
	h := newGrowthHandlerWith(t, growthFixture{
		lanes: &fakeLanes{count: 9, readable: true}, tradeHulls: 9, treasury: 12_000_000, yardAsk: 1_000_000,
		// A pool of nine that has traded: the cold-pool exception in readWorkingCapital does not
		// apply once hulls exist, so the ledger has to have something in it or the working-capital
		// rung refuses before the demand guard is ever consulted.
		outflow: &fakeOutflow{obs: observedOutflow(100_000, 100_000, 50_000)},
	})
	clock := &steppingGrowthClock{now: time.Unix(1_700_000_000, 0)}
	h.clock = clock
	h.SetPurchaser(buyer)

	// Two ticks a minute apart, well inside the dwell: HEAVY wave, no purchase.
	for i := 0; i < 2; i++ {
		if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
		clock.step(time.Minute)
	}
	if buyer.calls != 0 {
		t.Fatalf("no heavy may be bought before a MARGINAL shortfall settles, got %d", buyer.calls)
	}

	// Past the dwell, the same unchanged shortfall buys.
	clock.step(defaultGrowthShortfallDwell)
	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("settled tick: %v", err)
	}
	if buyer.calls != 1 {
		t.Fatalf("a shortfall that has stood the whole dwell must buy, got %d", buyer.calls)
	}
}

// steppingGrowthClock is fixedGrowthClock that can be moved forward, which is what a WALL-CLOCK
// window needs a test to be able to do.
type steppingGrowthClock struct{ now time.Time }

func (c *steppingGrowthClock) Now() time.Time       { return c.now }
func (c *steppingGrowthClock) Sleep(time.Duration)  {}
func (c *steppingGrowthClock) step(d time.Duration) { c.now = c.now.Add(d) }

// The working-capital term reaches the guard. A treasury that clears price+margin+floor but
// not the observed cargo commitment must refuse the buy.
func TestGrowthReconcile_WorkingCapitalBlocksTheBuy(t *testing.T) {
	buyer := &growthPurchaseRecorder{}
	h := newGrowthHandlerWith(t, growthFixture{
		lanes: &fakeLanes{count: 9, readable: true}, treasury: 5_000_000, yardAsk: 1_000_000, shortfallHeld: growthSettledWindow,
		tradeHulls: 4,
		outflow:    &fakeOutflow{obs: observedOutflow(2_000_000, 0, 100_000)},
	})
	h.SetPurchaser(buyer)

	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buyer.calls != 0 {
		t.Fatalf("the observed cargo commitment must block this buy, got %d", buyer.calls)
	}
}

// THE LIVE FRAME, staging 2026-08-08 07:02. Nine trade hulls recycling 4,319,078 credits of cargo
// an hour and recovering more than that back through sales; the heavy asks 1,742,500. Measured as
// GROSS spend the reserve was 8,638,156 and the heavy's effective bar was 10,630,656 of treasury —
// a bar that RISES with every trading win, because gross outflow counts the same credits again on
// every recycle. Measured as the UNRECOVERED position the reserve is the hold-bounded float,
// 9 × 182,000 = 1,638,000, and the bar is 3,630,500.
//
// The fixture stands exactly ON the new bar and the credit BELOW it is asserted too, so this pins
// the arithmetic rather than its direction: a reserve that merely got smaller passes the first
// assertion, and only the exact figure passes both.
func TestGrowthReconcile_HighVelocityFleetIsHeldToItsFloatNotItsTurnover(t *testing.T) {
	atTreasury := func(treasury int64) *growthPurchaseRecorder {
		buyer := &growthPurchaseRecorder{}
		h := newGrowthHandlerWith(t, growthFixture{
			lanes: &fakeLanes{count: 73, readable: true}, treasury: treasury, yardAsk: 1_742_500, shortfallHeld: growthSettledWindow,
			tradeHulls: 9,
			outflow:    &fakeOutflow{obs: observedOutflow(4_319_078, 4_750_000, 182_000)},
		})
		h.SetPurchaser(buyer)
		if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return buyer
	}

	if got := atTreasury(3_630_500).calls; got != 1 {
		t.Fatalf("a fleet holding 1638000 of float must clear a 3630500 bar, got %d buys", got)
	}
	if got := atTreasury(3_630_499).calls; got != 0 {
		t.Fatalf("one credit under the bar must still refuse — the reserve is 1638000 exactly, got %d buys", got)
	}
}

// THE ANTI-VACUITY CONTROL FOR THE FRAME ABOVE. The identical tick with the recovery removed — the
// same hulls, the same spend, the same treasury, nothing sold back — reserves the full 8,638,156
// and refuses. The two tests differ in ONE field, so a change that stopped netting at all cannot
// leave both of them passing.
func TestGrowthReconcile_UnrecoveredSpendOnTheSameFrameStillRefuses(t *testing.T) {
	buyer := &growthPurchaseRecorder{}
	h := newGrowthHandlerWith(t, growthFixture{
		lanes: &fakeLanes{count: 73, readable: true}, treasury: 3_630_500, yardAsk: 1_742_500, shortfallHeld: growthSettledWindow,
		tradeHulls: 9,
		outflow:    &fakeOutflow{obs: observedOutflow(4_319_078, 0, 182_000)},
	})
	h.SetPurchaser(buyer)

	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buyer.calls != 0 {
		t.Fatalf("cargo bought and NOT sold back is capital in flight and must still refuse the hull, got %d buys", buyer.calls)
	}
}

// THE COLD-START REGIME THE RUNWAY ARM WAS WRITTEN FOR, and the half of this guard a fix must not
// disarm. Two hulls, 500,000 spent, nothing recovered: the runway arm reserves 2h × 500,000 and
// blocks a treasury that the hold-fill arm alone (2 × 80,000 = 160,000) would have let through.
//
// The pair below is what makes the block attributable to the RUNWAY arm rather than to the frame:
// the same tick with the position fully recovered buys, and nothing else about it changes.
func TestGrowthReconcile_ColdStartRunwayArmStillBinds(t *testing.T) {
	coldStart := func(recovered int64) *growthPurchaseRecorder {
		buyer := &growthPurchaseRecorder{}
		h := newGrowthHandlerWith(t, growthFixture{
			lanes: &fakeLanes{count: 5, readable: true}, treasury: 1_500_000, yardAsk: 1_000_000, shortfallHeld: growthSettledWindow,
			tradeHulls: 2,
			outflow:    &fakeOutflow{obs: observedOutflow(500_000, recovered, 80_000)},
		})
		h.SetPurchaser(buyer)
		if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return buyer
	}

	if got := coldStart(0).calls; got != 0 {
		t.Fatalf("an unrecovered 500000 position must still reserve 1000000 and refuse the hull, got %d buys", got)
	}
	if got := coldStart(500_000).calls; got != 1 {
		t.Fatalf("with the position recovered only the 160000 hold-fill arm remains and this treasury clears it, got %d buys", got)
	}
}

// THE ARMS SURVIVE THE TRIP TO THE DECISION LINE — the one an operator actually reads when a heavy
// is refused. The guard renders the clause and the coordinator fills the field, and only a test
// that drives BOTH can see the field going unfilled: a request that never carries the arms renders
// a line the guard tests would still pass, saying how much was held back and not by which measure.
func TestGrowthReconcile_DecisionLineNamesTheBindingWorkingCapitalArm(t *testing.T) {
	logger := &capturingLogger{}
	h := newGrowthHandlerWith(t, growthFixture{
		lanes: &fakeLanes{count: 73, readable: true}, treasury: 3_630_499, yardAsk: 1_742_500, shortfallHeld: growthSettledWindow,
		tradeHulls: 9,
		outflow:    &fakeOutflow{obs: observedOutflow(4_319_078, 4_750_000, 182_000)},
	})

	if _, err := h.reconcileOnce(logging.WithLogger(context.Background(), logger), growthCmd()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	joined := strings.Join(logger.lines, "\n")
	if !strings.Contains(joined, "hold_fill binds: runway 0, hold_fill 1638000") {
		t.Fatalf("the refusal an operator reads must name the arm that bound:\n%s", joined)
	}
}

// THE ARMS SURVIVE THE TRIP TO THE GAUGES. The coordinator derives both terms and the sink must
// receive both: publishing only the maximum is exactly the blindness that made the binding arm a
// source read in the field, and it is invisible from inside the money math.
func TestGrowthReconcile_BothWorkingCapitalArmsReachTheMetricsSink(t *testing.T) {
	sink := &recordingCapitalSink{}
	h := newGrowthHandlerWith(t, growthFixture{
		lanes: &fakeLanes{count: 73, readable: true}, treasury: 3_630_500, yardAsk: 1_742_500, shortfallHeld: growthSettledWindow,
		tradeHulls: 9,
		outflow:    &fakeOutflow{obs: observedOutflow(4_319_078, 4_750_000, 182_000)},
	})
	h.SetMetricsSink(sink)

	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sink.calls != 1 {
		t.Fatalf("the reserve must be published once per tick, got %d", sink.calls)
	}
	if sink.terms.Runway != 0 || sink.terms.HoldFill != 1_638_000 {
		t.Fatalf("both arms must reach the sink: got %+v", sink.terms)
	}
	if got := sink.terms.Binding(); got != fleetgrowth.BindingHoldFill {
		t.Fatalf("the published terms must name the binding arm %q, got %q", fleetgrowth.BindingHoldFill, got)
	}
}

// A WINDOW THE READER COULD NOT SEE WHOLE IS NOT NETTED. The recovery is real and the position IS
// recovered, but the read hit its row bound, so the reserve falls back to the gross measure and the
// buy is refused — the strict direction, on an observation that cannot be trusted to net.
func TestGrowthReconcile_TruncatedLedgerWindowFallsBackToGross(t *testing.T) {
	buyer := &growthPurchaseRecorder{}
	h := newGrowthHandlerWith(t, growthFixture{
		lanes: &fakeLanes{count: 5, readable: true}, treasury: 1_500_000, yardAsk: 1_000_000, shortfallHeld: growthSettledWindow,
		tradeHulls: 2,
		outflow: &fakeOutflow{obs: fleetgrowth.CargoOutflow{
			Spent: 500_000, Recovered: 500_000, Largest: 80_000, Complete: false,
		}},
	})
	h.SetPurchaser(buyer)

	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buyer.calls != 0 {
		t.Fatalf("a half-seen window must reserve the gross spend, not the netted one, got %d buys", buyer.calls)
	}
}

// Fail-closed: an unreadable ledger is not a zero commitment.
func TestGrowthReconcile_UnreadableOutflowBuysNothing(t *testing.T) {
	buyer := &growthPurchaseRecorder{}
	h := newGrowthHandlerWith(t, growthFixture{
		lanes: &fakeLanes{count: 9, readable: true}, treasury: 12_000_000, yardAsk: 1_000_000, shortfallHeld: growthSettledWindow,
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
		lanes: &fakeLanes{count: 9, readable: true}, treasury: 12_000_000, yardAsk: 1_000_000, shortfallHeld: growthSettledWindow,
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
		lanes: &fakeLanes{count: 9, readable: true}, treasury: 12_000_000, yardAsk: 1_000_000, shortfallHeld: growthSettledWindow,
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
	if cfg.ShortfallDwell != defaultGrowthShortfallDwell {
		t.Errorf("shortfall dwell default = %v, want %v", cfg.ShortfallDwell, defaultGrowthShortfallDwell)
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

// THE TRAP THE KNOB USED TO CARRY. A "<= 0 ⇒ default" resolve made an explicit 0 REASSERT the
// percentage-of-treasury ceiling, so an operator revoking it by config would believe they had and
// would not have — and `tune <key> 0` deletes a key rather than setting it, which left the ceiling
// as the only reachable state. Unapplied is now the shipped state; a positive value is the only
// thing that applies the rule, and it still does.
func TestResolveFleetGrowthConfig_TreasuryPctCeilingIsOffAndZeroDoesNotReassertIt(t *testing.T) {
	if pct := resolveFleetGrowthConfig(&RunFleetGrowthCoordinatorCommand{}).TreasuryPctPerPurchase; pct != 0 {
		t.Errorf("an unset knob must leave the ceiling unapplied, got %d%%", pct)
	}
	if pct := resolveFleetGrowthConfig(&RunFleetGrowthCoordinatorCommand{TreasuryPctPerPurchase: 0}).TreasuryPctPerPurchase; pct != 0 {
		t.Errorf("an explicit 0 must stay unapplied, not resolve back to a ceiling, got %d%%", pct)
	}
	if pct := resolveFleetGrowthConfig(&RunFleetGrowthCoordinatorCommand{TreasuryPctPerPurchase: 25}).TreasuryPctPerPurchase; pct != 25 {
		t.Errorf("a configured ceiling must survive the resolve — restorable without a merge, got %d%%", pct)
	}
}

// FAIL-CLOSED SURVIVES THE REVOCATION (RULINGS #4), and this is the one that matters most: the
// percentage term also refused an unreadable balance, so removing it must not leave the guard
// permitting a buy it cannot price against a treasury it cannot read. Fed the RESOLVED knobs rather
// than hand-written ones, so this always exercises the configuration the coordinator actually runs.
func TestGrowthResolvedDefaults_UnreadableTreasuryStillRefusesWithTheCeilingOff(t *testing.T) {
	cfg := resolveFleetGrowthConfig(&RunFleetGrowthCoordinatorCommand{})
	if cfg.TreasuryPctPerPurchase != 0 {
		t.Fatalf("this test is aimed at the ceiling-OFF configuration; the resolve gave %d%%", cfg.TreasuryPctPerPurchase)
	}
	// The balance is one the floor term AFFORDS, so readability is the only thing left that can
	// refuse: an unreadable request the floor term would have blocked anyway proves nothing, and the
	// first assertion is what fails if this fixture ever drifts to that side of the floor.
	req := PurchaseRequest{
		Class:             HullClassHeavy,
		LiveTreasury:      5_000_000,
		TreasuryReadable:  true,
		Price:             1_400_000,
		MarginOverFloor:   cfg.PurchaseMarginOverFloor,
		TreasuryPctPerBuy: cfg.TreasuryPctPerPurchase,
	}
	if v := guardAffordability(req); !v.Passed {
		t.Fatalf("the fixture must PASS while the balance is readable, or the fail-closed arm is never reached: %s", v.Detail)
	}

	req.TreasuryReadable = false
	v := guardAffordability(req)
	if v.Passed {
		t.Fatalf("an unreadable treasury must refuse whatever the percentage rule is set to: %s", v.Detail)
	}
	if !strings.Contains(v.Detail, "UNREADABLE") {
		t.Fatalf("the refusal must name the unreadable balance, not refuse for some other reason: %s", v.Detail)
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

// THE SHORTFALL IS A THRESHOLD ON THE SPEND PATH, NOT A QUANTITY. The unserved-lane count is the
// heavy class's whole shortfall, so a census correction that raises it ~100x raises the shortfall
// ~100x too. Nothing downstream multiplies by it: the demand guard asks only shortfall > 0 and the
// per-tick cap is 1, so a fleet 900 lanes short buys at exactly the pace a fleet 1 lane short does.
// Pinned because the pacing is the whole answer to whether a corrected census can be armed.
func TestGrowthReconcile_ALargeShortfallBuysNoFasterThanOnePerTick(t *testing.T) {
	buyer := &growthPurchaseRecorder{}
	h := newGrowthHandlerWith(t, growthFixture{
		lanes:         &fakeLanes{count: 900, readable: true},
		treasury:      1_000_000_000,
		yardAsk:       1_000_000,
		shortfallHeld: growthSettledWindow,
	})
	h.SetPurchaser(buyer)

	const ticks = 4
	for i := 0; i < ticks; i++ {
		res, err := h.reconcileOnce(context.Background(), growthCmd())
		if err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
		if res.Shortfall != 900 {
			t.Fatalf("tick %d: shortfall = %d, want the full unserved count 900", i, res.Shortfall)
		}
		if res.Purchased != 1 {
			t.Fatalf("tick %d: purchased %d hulls, want exactly 1 whatever the shortfall", i, res.Purchased)
		}
	}
	if buyer.calls != ticks {
		t.Fatalf("%d ticks against a 900-lane shortfall bought %d hulls, want %d", ticks, buyer.calls, ticks)
	}
}
