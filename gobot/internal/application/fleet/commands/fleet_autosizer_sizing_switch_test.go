package commands

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/health"
	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
)

// sp-k4wdd — the autosizer's sizing_enabled master switch.
//
// WHAT THESE TESTS ARE DEFENDING. The autosizer costs almost nothing in credits and everything in
// API requests: measured live it made 25 buy-decisions, bought NOTHING (14 blocked by demand, 11 by
// heavy_cap) and spent 14,053 Get Shipyard calls doing it — 96.7% of all Get Shipyard traffic and
// ~21% of the account's 2.00 req/s ceiling. The reads happen because sizeClass prices a hull
// (buildPurchaseRequest → resolveHullPrice → PriceFor, walking every system × every shipyard, once
// per hull type in TradeHullPreferenceOrder) BEFORE EvaluateGuards can block. So the ONLY switch
// worth having is one that stops the READS. A switch that stops the buying is worth nothing here,
// and TestSizingSwitch_Off_StopsTheReads_NotJustTheBuy is the test that says so.

// --- counting ports: every seam the autosizer can touch, counted ---

// countingPorts counts every port read the coordinator can make in a tick. The paused-tick tests
// assert the TOTAL is zero, so a future read added to the tick path is covered by construction
// rather than needing its own assertion here.
type countingPorts struct {
	treasury  int
	apiUtil   int
	census    int
	heavyYard int
	yardPrice int
	catalogue int
	errand    int
}

func (c *countingPorts) total() int {
	return c.treasury + c.apiUtil + c.census + c.heavyYard + c.yardPrice + c.catalogue + c.errand
}

type countingTreasury struct{ c *countingPorts }

func (f *countingTreasury) Treasury(context.Context, int) (int64, bool, error) {
	f.c.treasury++
	return 5_000_000, true, nil
}

type countingAPIUtil struct{ c *countingPorts }

func (f *countingAPIUtil) UtilizationPct(context.Context) (float64, bool, error) {
	f.c.apiUtil++
	return 40, true, nil
}

type countingCensus struct{ c *countingPorts }

func (f *countingCensus) HeaviesOwned(context.Context, int) (int, error) {
	f.c.census++
	return 0, nil
}

type countingHeavyYard struct{ c *countingPorts }

func (f *countingHeavyYard) HeavyTarget(context.Context, int) (HeavyTargetYard, error) {
	f.c.heavyYard++
	return HeavyTargetYard{}, nil
}

// countingYardPrice stands in for the shipyard price walk — the seam that issues the Get Shipyard
// calls this whole bead is about. Its counter IS the acceptance criterion.
type countingYardPrice struct{ c *countingPorts }

func (f *countingYardPrice) PriceFor(context.Context, int, HullClass, string, bool) (int64, int64, string, bool, error) {
	f.c.yardPrice++
	return 437_000, 400_000, "KA42-A2", true, nil
}

type countingCatalogue struct{ c *countingPorts }

func (f *countingCatalogue) KnownHeavyYards(context.Context, int) ([]KnownHeavyYard, error) {
	f.c.catalogue++
	return nil, nil
}

type countingErrand struct{ c *countingPorts }

func (f *countingErrand) ErrandHulls(context.Context, int) ([]PricingErrandHull, error) {
	f.c.errand++
	return nil, nil
}
func (f *countingErrand) SendToYard(context.Context, int, string, string) error { return nil }

// mutableLiveConfig is a live-config reader whose snapshot can be changed BETWEEN ticks, which is
// how the "re-read live, no restart" property is tested: one handler, one command, two ticks.
type mutableLiveConfig struct {
	mu    sync.Mutex
	snap  liveconfig.Snapshot
	reads int
}

func (m *mutableLiveConfig) Snapshot(context.Context, string, int) (liveconfig.Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reads++
	return m.snap, nil
}

func (m *mutableLiveConfig) set(key string, value int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snap = liveconfig.Snapshot{key: value}
}

func (m *mutableLiveConfig) readCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reads
}

// recordingStall captures the stall verdicts a tick reports.
type recordingStall struct{ outcomes []health.TickOutcome }

func (r *recordingStall) Observe(_ context.Context, _ health.StallKey, o health.TickOutcome) {
	r.outcomes = append(r.outcomes, o)
}

// capturingLogger collects log lines so the paused tick's operator-facing message can be asserted.
type capturingLogger struct {
	mu      sync.Mutex
	lines   []string
	actions []string
}

func (l *capturingLogger) Log(_, message string, metadata map[string]interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, message)
	if metadata != nil {
		if a, ok := metadata["action"].(string); ok {
			l.actions = append(l.actions, a)
		}
	}
}

func (l *capturingLogger) sawAction(action string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, a := range l.actions {
		if a == action {
			return true
		}
	}
	return false
}

func (l *capturingLogger) joined() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// switchHandler wires a coordinator whose every port counts its reads, with a light class in
// shortfall so that WITH THE SWITCH ON the tick genuinely reaches the shipyard price walk and the
// purchaser. That saturation is what makes the switched-OFF assertions mean anything: a fixture
// that never reached the reads would report zero reads under every mutation.
func switchHandler(cfg liveconfig.Reader) (*RunFleetAutosizerCoordinatorHandler, *countingPorts, *recordingPurchaser, *recordingMetrics, *recordingStall) {
	ports := &countingPorts{}
	h := NewRunFleetAutosizerCoordinatorHandler(nil)
	h.AddDemandProvider(lightShortfall())
	h.SetTreasuryReader(&countingTreasury{c: ports})
	h.SetAPIUtilizationReader(&countingAPIUtil{c: ports})
	h.SetHeavyCensusReader(&countingCensus{c: ports})
	h.SetHeavyYardReader(&countingHeavyYard{c: ports})
	h.SetYardPriceReader(&countingYardPrice{c: ports})
	h.SetHeavyYardCatalogReader(&countingCatalogue{c: ports})
	h.SetHeavyPricingErrandPort(&countingErrand{c: ports})
	purchaser := &recordingPurchaser{}
	metrics := &recordingMetrics{}
	stall := &recordingStall{}
	h.SetPurchaser(purchaser)
	h.SetMetricsSink(metrics)
	h.SetStallObserver(stall)
	if cfg != nil {
		h.SetHeavyCapReader(cfg)
	}
	return h, ports, purchaser, metrics, stall
}

func switchCmd() *RunFleetAutosizerCoordinatorCommand {
	return &RunFleetAutosizerCoordinatorCommand{PlayerID: 1, ContainerID: "c1"}
}

// SATURATION GUARD. Everything below asserts that some read count is ZERO when the switch is off;
// each of those assertions is worthless unless the same fixture makes the count NON-ZERO when the
// switch is on. This test pins that precondition on its own, so a fixture that silently stops
// reaching the price walk fails HERE rather than making every other test vacuously green.
func TestSizingSwitch_On_FixtureActuallyReachesTheReadsAndTheBuy(t *testing.T) {
	cfg := &mutableLiveConfig{snap: liveconfig.Snapshot{sizingEnabledKey: 1}}
	h, ports, purchaser, metrics, _ := switchHandler(cfg)

	if _, err := h.reconcileOnce(context.Background(), switchCmd()); err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}

	if ports.yardPrice == 0 {
		t.Fatal("FIXTURE IS NOT SATURATED: the shipyard price walk was never reached with the switch ON, " +
			"so every 'zero reads when off' assertion in this file would pass without the switch existing")
	}
	if ports.treasury == 0 || ports.apiUtil == 0 || ports.census == 0 || ports.catalogue == 0 {
		t.Fatalf("FIXTURE IS NOT SATURATED: tick inputs/errand not all reached with the switch ON: %+v", *ports)
	}
	if len(purchaser.orders) != 1 {
		t.Fatalf("expected the armed fixture to buy once with the switch ON, got %d", len(purchaser.orders))
	}
	if len(metrics.sizingStates) != 1 || !metrics.sizingStates[0] {
		t.Fatalf("switch ON must publish enabled=true every tick, got %v", metrics.sizingStates)
	}
}

// MUTATION: delete the gate in reconcileOnce (the knob is parsed but never consulted).
//
// The whole tick must be skipped: no port read at all, and nothing bought.
func TestSizingSwitch_Off_SkipsTheEntireTick(t *testing.T) {
	cfg := &mutableLiveConfig{snap: liveconfig.Snapshot{sizingEnabledKey: sizingDisabled}}
	h, ports, purchaser, _, _ := switchHandler(cfg)

	res, err := h.reconcileOnce(context.Background(), switchCmd())
	if err != nil {
		t.Fatalf("a paused tick must not error: %v", err)
	}

	if ports.total() != 0 {
		t.Fatalf("sizing_enabled=2 must read NOTHING, but ports were read: %+v", *ports)
	}
	if len(purchaser.orders) != 0 {
		t.Fatalf("sizing_enabled=2 must buy nothing, got %d orders", len(purchaser.orders))
	}
	if res.ClassesEvaluated != 0 || res.Purchased != 0 {
		t.Fatalf("a paused tick must evaluate and purchase nothing, got %+v", res)
	}
}

// MUTATION: THE TRAP — gate the PURCHASE instead of the tick (i.e. copy expansion_enabled's shape,
// letting the "free" work continue and only refusing to spend).
//
// This is the mutation that must fail loudly, because it is the one that looks correct, passes a
// "nothing was bought" assertion, and delivers NONE of the benefit: the 14,053 shipyard scans all
// happen before any purchase decision is reached. Buying nothing is not the goal — the autosizer
// already bought nothing. Reading nothing is the goal.
func TestSizingSwitch_Off_StopsTheReads_NotJustTheBuy(t *testing.T) {
	cfg := &mutableLiveConfig{snap: liveconfig.Snapshot{sizingEnabledKey: sizingDisabled}}
	h, ports, purchaser, _, _ := switchHandler(cfg)

	if _, err := h.reconcileOnce(context.Background(), switchCmd()); err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}

	// The acceptance criterion, stated as the code sees it: the shipyard price walk is the seam
	// that issues Get Shipyard, and it must not be entered even once.
	if ports.yardPrice != 0 {
		t.Fatalf("SWITCH GATES ONLY THE BUY: the shipyard price walk ran %d time(s) with sizing_enabled=2. "+
			"This is the trap this bead exists to avoid — resolveHullPrice runs BEFORE EvaluateGuards, "+
			"so gating the purchase leaves every Get Shipyard call in place and reclaims no API at all", ports.yardPrice)
	}
	// The demand reads and the pricing errand are API traffic too, and are equally not-a-purchase.
	if ports.catalogue != 0 || ports.errand != 0 {
		t.Fatalf("the heavy pricing errand ran with sizing_enabled=2 (catalogue=%d errand=%d) — "+
			"it spends no credits, so a spend-only gate would leave it running", ports.catalogue, ports.errand)
	}
	if ports.treasury != 0 || ports.apiUtil != 0 || ports.census != 0 || ports.heavyYard != 0 {
		t.Fatalf("the shared tick inputs were read with sizing_enabled=2: %+v", *ports)
	}
	if len(purchaser.orders) != 0 {
		t.Fatalf("expected no purchase, got %d", len(purchaser.orders))
	}
}

// MUTATION: encode the knob 0=off / 1=on.
//
// `tune <key> 0` DELETES the key fleet-wide and means revert-to-default, so under a 0/1 encoding
// "off" is unexpressible and a 0 would instead silently disable the coordinator. 0 must therefore
// read as the DEFAULT, which is ON.
func TestSizingSwitch_Zero_IsRevertToDefault_NotOff(t *testing.T) {
	cfg := &mutableLiveConfig{snap: liveconfig.Snapshot{sizingEnabledKey: 0}}
	h, ports, purchaser, _, _ := switchHandler(cfg)

	if _, err := h.reconcileOnce(context.Background(), switchCmd()); err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}

	if ports.yardPrice == 0 || len(purchaser.orders) != 1 {
		t.Fatalf("sizing_enabled=0 is REVERT-TO-DEFAULT (default ON), not off — the tick must run "+
			"normally, but reads=%+v orders=%d", *ports, len(purchaser.orders))
	}
}

// MUTATION: ship the knob default-OFF, or treat an absent key as off.
//
// A bare deploy that nobody has tuned must size the fleet exactly as it did before this knob
// existed; anything else is a silent behaviour change for everyone else running this code.
func TestSizingSwitch_DefaultsOn_WhenUntunedOrUnwired(t *testing.T) {
	t.Run("key absent from a live config that exists", func(t *testing.T) {
		cfg := &mutableLiveConfig{snap: liveconfig.Snapshot{heavyCapKey: 12}}
		h, ports, purchaser, _, _ := switchHandler(cfg)
		if _, err := h.reconcileOnce(context.Background(), switchCmd()); err != nil {
			t.Fatalf("reconcileOnce: %v", err)
		}
		if ports.yardPrice == 0 || len(purchaser.orders) != 1 {
			t.Fatalf("an untuned autosizer must size normally (default ON): reads=%+v orders=%d", *ports, len(purchaser.orders))
		}
	})

	t.Run("no live-config reader wired at all", func(t *testing.T) {
		h, ports, purchaser, _, _ := switchHandler(nil)
		if _, err := h.reconcileOnce(context.Background(), switchCmd()); err != nil {
			t.Fatalf("reconcileOnce: %v", err)
		}
		if ports.yardPrice == 0 || len(purchaser.orders) != 1 {
			t.Fatalf("an unwired live config must fall back to ON: reads=%+v orders=%d", *ports, len(purchaser.orders))
		}
	})

	t.Run("snapshot read fails", func(t *testing.T) {
		h, ports, purchaser, _, _ := switchHandler(&fakeLiveConfig{err: context.DeadlineExceeded})
		if _, err := h.reconcileOnce(context.Background(), switchCmd()); err != nil {
			t.Fatalf("reconcileOnce: %v", err)
		}
		if ports.yardPrice == 0 || len(purchaser.orders) != 1 {
			t.Fatalf("a transient config read failure must fall back to the LAUNCH behaviour (ON), "+
				"not disable the autosizer: reads=%+v orders=%d", *ports, len(purchaser.orders))
		}
	})
}

// MUTATION: read the knob once at launch (from the frozen command) instead of per tick, or cache
// the first snapshot.
//
// Also the "flipping it back on restores the behaviour" half of the acceptance criteria: ONE
// handler and ONE command run three ticks across a tune in each direction, with no restart and no
// container rebuild in between.
func TestSizingSwitch_IsRereadLive_AndFlippingBackOnRestores(t *testing.T) {
	cfg := &mutableLiveConfig{snap: liveconfig.Snapshot{sizingEnabledKey: 1}}
	h, ports, purchaser, metrics, _ := switchHandler(cfg)
	cmd := switchCmd()
	ctx := context.Background()

	// Tick 1: ON.
	if _, err := h.reconcileOnce(ctx, cmd); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	onReads, onOrders := ports.yardPrice, len(purchaser.orders)
	if onReads == 0 || onOrders == 0 {
		t.Fatalf("tick 1 (ON) must read and buy: reads=%d orders=%d", onReads, onOrders)
	}

	// Tune it OFF. No restart, no rebuild — the same handler and the same command.
	cfg.set(sizingEnabledKey, sizingDisabled)
	if _, err := h.reconcileOnce(ctx, cmd); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if ports.yardPrice != onReads {
		t.Fatalf("the tune did not apply on the NEXT tick: the price walk ran again (%d → %d). "+
			"The knob must be re-read live, not frozen at launch", onReads, ports.yardPrice)
	}
	if len(purchaser.orders) != onOrders {
		t.Fatalf("tick 2 (OFF) bought something: %d → %d", onOrders, len(purchaser.orders))
	}

	// Tune it back ON. The behaviour must RESTORE, not stay latched off.
	cfg.set(sizingEnabledKey, 1)
	if _, err := h.reconcileOnce(ctx, cmd); err != nil {
		t.Fatalf("tick 3: %v", err)
	}
	if ports.yardPrice <= onReads {
		t.Fatalf("flipping sizing_enabled back to 1 did not restore sizing: the price walk stayed at %d", ports.yardPrice)
	}
	if len(purchaser.orders) <= onOrders {
		t.Fatalf("flipping sizing_enabled back to 1 did not restore buying: orders stayed at %d", len(purchaser.orders))
	}

	// The gauge must be continuous across the pause: on, off, on — never a gap.
	want := []bool{true, false, true}
	if len(metrics.sizingStates) != len(want) {
		t.Fatalf("expected one sizing_enabled emission per tick, got %v", metrics.sizingStates)
	}
	for i, w := range want {
		if metrics.sizingStates[i] != w {
			t.Fatalf("sizing_enabled series %v does not match the tunes applied %v", metrics.sizingStates, want)
		}
	}
}

// MUTATION: skip the work by `continue`-ing the loop without its sleep (or otherwise returning
// early in a way that tightens the cadence).
//
// A paused coordinator that spins is worse than the problem being fixed: it would trade API burn
// for CPU burn and hammer the config table. Handle sleeps the full tick after EVERY reconcileOnce,
// and this pins that the paused path is no exception.
func TestSizingSwitch_Off_DoesNotSpinHot(t *testing.T) {
	cfg := &mutableLiveConfig{snap: liveconfig.Snapshot{sizingEnabledKey: sizingDisabled}}
	h, ports, _, _, _ := switchHandler(cfg)

	const window = 250 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), window)
	defer cancel()
	// TickIntervalSecs unset ⇒ the 900s default tick.
	_, _ = h.Handle(ctx, &RunFleetAutosizerCoordinatorCommand{PlayerID: 1, ContainerID: "c1"})

	// Handle's tick comes from the launch config; with TickIntervalSecs unset it is the 900s
	// default, so a correctly-sleeping loop reconciles exactly ONCE inside a 250ms window.
	// A hot spin would reconcile thousands of times.
	reads := cfg.readCount()
	if reads > 5 {
		t.Fatalf("the paused coordinator is SPINNING: %d config snapshots in %s with a 900s tick. "+
			"Skipping the work must not skip the sleep", reads, window)
	}
	if reads == 0 {
		t.Fatal("the coordinator never ticked at all — this test would pass vacuously")
	}
	if ports.total() != 0 {
		t.Fatalf("a paused Handle loop read ports: %+v", *ports)
	}
}

// MUTATION: pause silently (drop the log line and/or the gauge), or report the skipped classes as
// BLOCKED.
//
// The autosizer's liveness signal is its per-tick log line. A paused coordinator that simply went
// quiet is indistinguishable from a dead one, and a paused coordinator reported as BLOCKED would
// escalate a stall alarm for doing exactly what it was told.
func TestSizingSwitch_Off_IsVisibleAsPaused_NotSilentAndNotBlocked(t *testing.T) {
	cfg := &mutableLiveConfig{snap: liveconfig.Snapshot{sizingEnabledKey: sizingDisabled}}
	h, _, _, metrics, stall := switchHandler(cfg)

	logger := &capturingLogger{}
	ctx := logging.WithLogger(context.Background(), logger)
	if _, err := h.reconcileOnce(ctx, switchCmd()); err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}

	if !logger.sawAction("autosizer_paused") {
		t.Fatalf("a paused tick emitted no autosizer_paused line — it is indistinguishable from a dead "+
			"coordinator. Lines seen:\n%s", logger.joined())
	}
	if !strings.Contains(logger.joined(), sizingEnabledKey) {
		t.Fatalf("the paused line must NAME the knob so an operator can find it:\n%s", logger.joined())
	}
	if len(metrics.sizingStates) != 1 || metrics.sizingStates[0] {
		t.Fatalf("a paused tick must still publish sizing_enabled=false, got %v", metrics.sizingStates)
	}

	if len(stall.outcomes) == 0 {
		t.Fatal("a paused tick reported no stall verdict, so a stale BLOCKED streak from before the pause never clears")
	}
	for _, o := range stall.outcomes {
		if o.Outcome != health.StallIdle {
			t.Fatalf("a paused class must report IDLE (idle by instruction), not %s — paused is not stalled", o.Outcome)
		}
	}
}

// The registered tune surface must carry the switch with the 1/2 encoding documented, and the
// coordinator's own default must be ON. The registry↔coordinator anti-drift check lives in
// container_ops_tune_test.go; this pins the value the registry copies.
func TestSizingSwitch_TunableDefault_IsOn(t *testing.T) {
	defaults := FleetAutosizerTunableDefaults()
	got, ok := defaults[sizingEnabledKey]
	if !ok {
		t.Fatalf("%s must be exposed as a live-tunable knob, got %v", sizingEnabledKey, defaults)
	}
	if got != defaultSizingEnabled {
		t.Fatalf("the exported default for %s (%d) has drifted from the coordinator's own const (%d)",
			sizingEnabledKey, got, defaultSizingEnabled)
	}
	if defaultSizingEnabled != 1 {
		t.Fatalf("%s must ship ARMED — default 1 (ON), got %d. A default-off knob is a silent "+
			"behaviour change for anyone else running this code", sizingEnabledKey, defaultSizingEnabled)
	}
	if sizingDisabled != 2 {
		t.Fatalf("the off sentinel must be 2, not %d: `tune <key> 0` means revert-to-default, "+
			"so a 0/1 encoding makes 'off' unexpressible", sizingDisabled)
	}
}
