package commands

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	mfgServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-r5a6 (Admiral order: automate the input-poison anti-cycle). These pins drive the
// coordinator's input-layer detection, the pause/resume episode state machine, and the recovery
// (194min half-life) clock directly — decoupled from the DB and, for the unit checks, from the
// full Handle().

// inputPauseSupplyRepo drives EligibleSourceMedianAsk per input good: supplyByGood maps a good to
// the supply level at its single in-system EXPORT source ("" = the good has NO market at all,
// which also yields count==0 → ineligible). findErr, when set, makes FindAllMarketsInSystem fail
// (the read-failure fail-toward-production path). It counts market reads so the disabled-path
// "reads nothing" contract can be pinned.
type inputPauseSupplyRepo struct {
	market.MarketRepository
	supplyByGood map[string]string
	findErr      error
	reads        int
}

func (r *inputPauseSupplyRepo) FindAllMarketsInSystem(ctx context.Context, systemSymbol string, playerID int) ([]string, error) {
	r.reads++
	if r.findErr != nil {
		return nil, r.findErr
	}
	var wps []string
	for good, supply := range r.supplyByGood {
		if supply != "" {
			wps = append(wps, "WP-"+good)
		}
	}
	return wps, nil
}

func (r *inputPauseSupplyRepo) GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*market.Market, error) {
	r.reads++
	good := strings.TrimPrefix(waypointSymbol, "WP-")
	supply := r.supplyByGood[good]
	if supply == "" {
		return nil, nil
	}
	activity := "GROWING"
	tg, err := market.NewTradeGood(good, &supply, &activity, 90, 100, 20, market.TradeTypeExport)
	if err != nil {
		return nil, err
	}
	return market.NewMarket(waypointSymbol, []market.TradeGood{*tg}, time.Now())
}

// newInputPauseHandler wires a coordinator to a supply-controllable market repo and a MockClock,
// so evaluateInputLayerPause / the state machine can be driven directly.
func newInputPauseHandler(t *testing.T, clock shared.Clock, repo market.MarketRepository) *RunFactoryCoordinatorHandler {
	t.Helper()
	resolver := mfgServices.NewSupplyChainResolver(map[string][]string{testOutputGood: {testInputGood}}, repo)
	marketLocator := mfgServices.NewMarketLocator(repo, nil, nil, nil)
	return NewRunFactoryCoordinatorHandler(
		&factoryFakeMediator{},
		&factoryFakeShipRepo{ships: map[string]*navigation.Ship{}},
		repo,
		resolver,
		marketLocator,
		clock,
		nil,
	)
}

// buyLeaf / fabRoot build a minimal flattened dependency-tree node list for detection tests.
func buyLeaf(good string) *goods.SupplyChainNode {
	return &goods.SupplyChainNode{Good: good, AcquisitionMethod: goods.AcquisitionBuy}
}
func fabRoot(good string, children ...*goods.SupplyChainNode) *goods.SupplyChainNode {
	return &goods.SupplyChainNode{Good: good, AcquisitionMethod: goods.AcquisitionFabricate, Children: children}
}

func inputPauseCmd() *RunFactoryCoordinatorCommand {
	return &RunFactoryCoordinatorCommand{
		PlayerID:     1,
		TargetGood:   testOutputGood,
		SystemSymbol: testSystem,
		ContainerID:  testContainerID,
	}
}

// A chain whose required BUY input has NO MODERATE+ in-system source (SCARCE) PAUSES, naming the
// blocking input and resolving the default 194min recovery half-life. This is the D39-shape
// captain-run case: home input went wholesale SCARCE.
func TestInputPause_IneligibleLayer_Pauses(t *testing.T) {
	repo := &inputPauseSupplyRepo{supplyByGood: map[string]string{testInputGood: "SCARCE"}}
	handler := newInputPauseHandler(t, &shared.MockClock{CurrentTime: time.Now()}, repo)
	nodes := []*goods.SupplyChainNode{fabRoot(testOutputGood, buyLeaf(testInputGood)), buyLeaf(testInputGood)}

	v := handler.evaluateInputLayerPause(context.Background(), inputPauseCmd(), nodes)

	if !v.Paused {
		t.Fatalf("expected a pause for a chain whose input %s is SCARCE, got %+v", testInputGood, v)
	}
	if v.Reason != inputLayerIneligible {
		t.Errorf("reason = %q, want %q", v.Reason, inputLayerIneligible)
	}
	if len(v.BlockingInputs) != 1 || v.BlockingInputs[0] != testInputGood {
		t.Errorf("blocking inputs = %v, want [%s]", v.BlockingInputs, testInputGood)
	}
	if v.ReattemptMinutes != defaultInputRecoveryReattemptMinutes {
		t.Errorf("reattempt minutes = %d, want default %d", v.ReattemptMinutes, defaultInputRecoveryReattemptMinutes)
	}
}

// A chain whose input has a MODERATE+ source PROCEEDS (no pause) — the recovered / healthy path.
func TestInputPause_EligibleLayer_Proceeds(t *testing.T) {
	repo := &inputPauseSupplyRepo{supplyByGood: map[string]string{testInputGood: "MODERATE"}}
	handler := newInputPauseHandler(t, &shared.MockClock{CurrentTime: time.Now()}, repo)
	nodes := []*goods.SupplyChainNode{fabRoot(testOutputGood, buyLeaf(testInputGood)), buyLeaf(testInputGood)}

	v := handler.evaluateInputLayerPause(context.Background(), inputPauseCmd(), nodes)

	if v.Paused {
		t.Fatalf("expected no pause for a chain whose input is MODERATE, got %+v", v)
	}
	if v.Reason != inputPauseProceed {
		t.Errorf("reason = %q, want %q", v.Reason, inputPauseProceed)
	}
}

// A partially-ineligible layer (one input MODERATE, one SCARCE) PAUSES: one blocked required
// input blocks the whole chain, and only the blocked one is named.
func TestInputPause_PartiallyIneligible_Pauses(t *testing.T) {
	repo := &inputPauseSupplyRepo{supplyByGood: map[string]string{"ELECTRONICS": "MODERATE", "MICROPROCESSORS": "SCARCE"}}
	handler := newInputPauseHandler(t, &shared.MockClock{CurrentTime: time.Now()}, repo)
	nodes := []*goods.SupplyChainNode{
		fabRoot(testOutputGood, buyLeaf("ELECTRONICS"), buyLeaf("MICROPROCESSORS")),
		buyLeaf("ELECTRONICS"), buyLeaf("MICROPROCESSORS"),
	}

	v := handler.evaluateInputLayerPause(context.Background(), inputPauseCmd(), nodes)

	if !v.Paused {
		t.Fatalf("expected a pause when any required input is SCARCE, got %+v", v)
	}
	if len(v.BlockingInputs) != 1 || v.BlockingInputs[0] != "MICROPROCESSORS" {
		t.Errorf("blocking inputs = %v, want [MICROPROCESSORS] (only the SCARCE one)", v.BlockingInputs)
	}
}

// A market-READ failure must NOT pause (fail toward production) — a long pause on a transient
// blip is the expensive error; the margin guard's fail-closed park covers a truly unpriceable
// chain one step downstream.
func TestInputPause_ReadFailure_FailsTowardProduction(t *testing.T) {
	repo := &inputPauseSupplyRepo{supplyByGood: map[string]string{testInputGood: "SCARCE"}, findErr: errors.New("market list unreadable")}
	handler := newInputPauseHandler(t, &shared.MockClock{CurrentTime: time.Now()}, repo)
	nodes := []*goods.SupplyChainNode{fabRoot(testOutputGood, buyLeaf(testInputGood)), buyLeaf(testInputGood)}

	v := handler.evaluateInputLayerPause(context.Background(), inputPauseCmd(), nodes)

	if v.Paused {
		t.Fatalf("a market-read failure must NOT pause (fail toward production), got %+v", v)
	}
	if v.Reason != inputPauseProceed {
		t.Errorf("reason = %q, want %q (read failure defers to the downstream margin guard)", v.Reason, inputPauseProceed)
	}
}

// A required input with NO readable in-system EXPORT source at all (cold cache, or a good with
// no local source) must NOT arm the recovery pause — that is not a depleted-market-that-
// regenerates. It falls through to the selector's ordinary production-time park (a sourceless
// input needs a re-site, and a transient read miss must not idle a healthy chain for hours).
func TestInputPause_NoReadableSource_DoesNotPause(t *testing.T) {
	repo := &inputPauseSupplyRepo{supplyByGood: map[string]string{testInputGood: ""}} // no market at all
	handler := newInputPauseHandler(t, &shared.MockClock{CurrentTime: time.Now()}, repo)
	nodes := []*goods.SupplyChainNode{fabRoot(testOutputGood, buyLeaf(testInputGood)), buyLeaf(testInputGood)}

	v := handler.evaluateInputLayerPause(context.Background(), inputPauseCmd(), nodes)

	if v.Paused {
		t.Fatalf("a required input with no readable in-system source must NOT arm the recovery pause (re-site, not wait), got %+v", v)
	}
	if v.Reason != inputPauseProceed {
		t.Errorf("reason = %q, want %q", v.Reason, inputPauseProceed)
	}
}

// The emergency disable flag skips detection entirely and reads NO markets (RULINGS #5).
func TestInputPause_Disabled_SkipsDetectionAndReads(t *testing.T) {
	repo := &inputPauseSupplyRepo{supplyByGood: map[string]string{testInputGood: "SCARCE"}}
	handler := newInputPauseHandler(t, &shared.MockClock{CurrentTime: time.Now()}, repo)
	cmd := inputPauseCmd()
	cmd.AntiCycleDisabled = true
	nodes := []*goods.SupplyChainNode{fabRoot(testOutputGood, buyLeaf(testInputGood)), buyLeaf(testInputGood)}

	v := handler.evaluateInputLayerPause(context.Background(), cmd, nodes)

	if v.Paused {
		t.Fatalf("disabled anti-cycle must never pause, got %+v", v)
	}
	if v.Reason != inputPauseDisabled {
		t.Errorf("reason = %q, want %q", v.Reason, inputPauseDisabled)
	}
	if repo.reads != 0 {
		t.Errorf("disabled anti-cycle must not read any market, got %d reads", repo.reads)
	}
}

// A tree with no market-sourced (BUY-leaf) inputs has no input layer to gate — proceed.
func TestInputPause_NoBuyInputs_Proceeds(t *testing.T) {
	repo := &inputPauseSupplyRepo{supplyByGood: map[string]string{}}
	handler := newInputPauseHandler(t, &shared.MockClock{CurrentTime: time.Now()}, repo)
	// A pure fabricate root with no children, plus the target excluded even if a buy-leaf.
	nodes := []*goods.SupplyChainNode{fabRoot(testOutputGood), buyLeaf(testOutputGood)}

	v := handler.evaluateInputLayerPause(context.Background(), inputPauseCmd(), nodes)

	if v.Paused {
		t.Fatalf("a tree with no BUY inputs must not pause, got %+v", v)
	}
	if v.Reason != inputPauseNoInputs {
		t.Errorf("reason = %q, want %q", v.Reason, inputPauseNoInputs)
	}
}

// The recovery half-life is analyst-owned config: a set value overrides the 194min default.
func TestInputPause_ConfiguredReattemptMinutes(t *testing.T) {
	repo := &inputPauseSupplyRepo{supplyByGood: map[string]string{testInputGood: "SCARCE"}}
	handler := newInputPauseHandler(t, &shared.MockClock{CurrentTime: time.Now()}, repo)
	cmd := inputPauseCmd()
	cmd.InputRecoveryReattemptMinutes = 60
	nodes := []*goods.SupplyChainNode{fabRoot(testOutputGood, buyLeaf(testInputGood)), buyLeaf(testInputGood)}

	v := handler.evaluateInputLayerPause(context.Background(), cmd, nodes)

	if v.ReattemptMinutes != 60 {
		t.Errorf("reattempt minutes = %d, want configured 60", v.ReattemptMinutes)
	}
}

// Episode semantics + RESUME + the metric: the pause counter increments once per episode
// (running→paused), not on every re-check; when the layer recovers the chain resumes and a later
// re-poison is a fresh episode. Drives the real counter to prove once-per-episode.
func TestInputPause_EpisodeDedupResumeAndMetric(t *testing.T) {
	prev := metrics.Registry
	prevGlobal := metrics.GetGlobalChainInputPauseCollector()
	t.Cleanup(func() {
		metrics.Registry = prev
		metrics.SetGlobalChainInputPauseCollector(prevGlobal)
	})
	metrics.Registry = prometheus.NewRegistry()
	collector := metrics.NewChainInputPauseMetricsCollector()
	if err := collector.Register(); err != nil {
		t.Fatalf("register: %v", err)
	}
	metrics.SetGlobalChainInputPauseCollector(collector)

	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)}
	repo := &inputPauseSupplyRepo{supplyByGood: map[string]string{testInputGood: "SCARCE"}}
	handler := newInputPauseHandler(t, clock, repo)
	cmd := inputPauseCmd()
	logger := &capturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	pauseVerdict := inputPauseVerdict{Paused: true, Reason: inputLayerIneligible, Good: testOutputGood, BlockingInputs: []string{testInputGood}, ReattemptMinutes: defaultInputRecoveryReattemptMinutes}

	// Enter the paused episode: first record emits (transition), second re-arms but is deduped.
	if !handler.recordInputLayerPause(ctx, cmd, pauseVerdict) {
		t.Errorf("first pause must be a state transition (emit)")
	}
	if handler.recordInputLayerPause(ctx, cmd, pauseVerdict) {
		t.Errorf("second consecutive pause must be deduped (re-armed, no re-emit)")
	}
	// Recover: clearing a paused chain is a transition (resume logged).
	if !handler.clearInputLayerPause(ctx, cmd) {
		t.Errorf("clearing a paused chain must be a state transition (resume)")
	}
	if handler.clearInputLayerPause(ctx, cmd) {
		t.Errorf("clearing an already-running chain must be a no-op")
	}
	// A later re-poison is a fresh episode.
	if !handler.recordInputLayerPause(ctx, cmd, pauseVerdict) {
		t.Errorf("a re-poison after recovery must be a new pause episode (emit)")
	}

	got, ok := gatherCounterValue(t, metrics.Registry, "spacetraders_daemon_chain_input_pause_total", testOutputGood)
	if !ok {
		t.Fatalf("input-pause counter series not found")
	}
	if got != 2 {
		t.Errorf("input-pause counter = %v, want 2 (two episodes, re-checks deduped)", got)
	}
}

// The recovery CLOCK: within the half-life the chain is held OFF the market (inputPauseWithinWindow
// true → zero-poll short-circuit), and the backoff sleeps the remaining half-life, NOT the 45s
// no-work poll. Once the clock elapses, the window opens (false) so the one-iteration re-attempt
// runs.
func TestInputPause_RecoveryClock_NoReattemptBeforeClock(t *testing.T) {
	start := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	clock := &shared.MockClock{CurrentTime: start}
	repo := &inputPauseSupplyRepo{supplyByGood: map[string]string{testInputGood: "SCARCE"}}
	handler := newInputPauseHandler(t, clock, repo)
	cmd := inputPauseCmd()
	ctx := common.WithLogger(context.Background(), &capturingLogger{})

	pauseVerdict := inputPauseVerdict{Paused: true, Reason: inputLayerIneligible, Good: testOutputGood, BlockingInputs: []string{testInputGood}, ReattemptMinutes: defaultInputRecoveryReattemptMinutes}
	handler.recordInputLayerPause(ctx, cmd, pauseVerdict)

	// Immediately after pausing: within the window, and the backoff sleeps ~the full half-life.
	if _, within := handler.inputPauseWithinWindow(cmd.ContainerID); !within {
		t.Fatalf("chain must be within the recovery window immediately after pausing")
	}
	delay, paused := handler.inputPauseReattemptDelay(cmd.ContainerID)
	if !paused {
		t.Fatalf("a paused chain must report a reattempt backoff delay")
	}
	if want := defaultInputRecoveryReattemptMinutes * time.Minute; delay > want || delay < want-time.Second {
		t.Errorf("backoff delay = %v, want ~%v (the recovery half-life, not the 45s poll)", delay, want)
	}
	if delay <= noWorkIterationDelay {
		t.Fatalf("recovery backoff (%v) must be far longer than the 45s no-work poll (%v)", delay, noWorkIterationDelay)
	}

	// Part-way through recovery: still within the window (no re-attempt), shorter remaining delay.
	clock.Advance(100 * time.Minute)
	if _, within := handler.inputPauseWithinWindow(cmd.ContainerID); !within {
		t.Fatalf("chain must still be within the window 100min into a 194min recovery")
	}
	if delay, _ := handler.inputPauseReattemptDelay(cmd.ContainerID); delay > 94*time.Minute+time.Second || delay < 94*time.Minute-time.Second {
		t.Errorf("remaining delay 100min in = %v, want ~94min", delay)
	}

	// After the clock elapses: the window opens (re-attempt is due), and the backoff reverts.
	clock.Advance(95 * time.Minute) // now 195min in, past the 194min half-life
	if _, within := handler.inputPauseWithinWindow(cmd.ContainerID); within {
		t.Fatalf("after the recovery clock elapses the window must open for the re-attempt")
	}
	if _, paused := handler.inputPauseReattemptDelay(cmd.ContainerID); paused {
		t.Errorf("after the clock elapses the reattempt backoff must revert to the normal no-work poll")
	}
}

// Re-attempt that finds the layer STILL poisoned re-arms the clock for another half-life but does
// NOT re-emit (same episode) — the "re-park at zero cost" the spec calls for.
func TestInputPause_ReAttemptStillPoisoned_ReArmsSameEpisode(t *testing.T) {
	start := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	clock := &shared.MockClock{CurrentTime: start}
	repo := &inputPauseSupplyRepo{supplyByGood: map[string]string{testInputGood: "SCARCE"}}
	handler := newInputPauseHandler(t, clock, repo)
	cmd := inputPauseCmd()
	ctx := common.WithLogger(context.Background(), &capturingLogger{})
	pauseVerdict := inputPauseVerdict{Paused: true, Reason: inputLayerIneligible, Good: testOutputGood, BlockingInputs: []string{testInputGood}, ReattemptMinutes: defaultInputRecoveryReattemptMinutes}

	handler.recordInputLayerPause(ctx, cmd, pauseVerdict) // episode 1

	clock.Advance(defaultInputRecoveryReattemptMinutes*time.Minute + time.Minute) // clock elapsed
	if emitted := handler.recordInputLayerPause(ctx, cmd, pauseVerdict); emitted {
		t.Fatalf("a still-poisoned re-attempt must re-arm the SAME episode, not re-emit")
	}
	// The clock is re-armed: a fresh full half-life from the re-attempt instant.
	delay, paused := handler.inputPauseReattemptDelay(cmd.ContainerID)
	if !paused {
		t.Fatalf("re-armed pause must report a backoff delay")
	}
	if want := defaultInputRecoveryReattemptMinutes * time.Minute; delay > want || delay < want-time.Second {
		t.Errorf("re-armed delay = %v, want a fresh ~%v half-life", delay, want)
	}
}

// ---- Full Handle() integration: detect → pause end-to-end ----

// scarceIronRepo is the productive FAB_PLATE<-IRON fixture repo with IRON's in-system EXPORT
// source degraded to SCARCE, so the input layer is ineligible while the tree still builds.
type scarceIronRepo struct {
	factoryFakeMarketRepo
}

func (r *scarceIronRepo) GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*market.Market, error) {
	if waypointSymbol == testIronWaypoint {
		supply := "SCARCE"
		activity := "RESTRICTED"
		input, err := market.NewTradeGood(testInputGood, &supply, &activity, 8, 10, 10, market.TradeTypeExport)
		if err != nil {
			return nil, err
		}
		return market.NewMarket(waypointSymbol, []market.TradeGood{*input}, time.Now())
	}
	return r.factoryFakeMarketRepo.GetMarketData(ctx, waypointSymbol, playerID)
}

func newScarceInputFixture(t *testing.T, clock shared.Clock) *factoryFixture {
	t.Helper()
	shipA := newTestHauler(t, "CRAFTY-2", nil)
	shipB := newTestHauler(t, "CRAFTY-3", nil)
	shipRepo := &factoryFakeShipRepo{
		ships: map[string]*navigation.Ship{shipA.ShipSymbol(): shipA, shipB.ShipSymbol(): shipB},
		order: []string{shipA.ShipSymbol(), shipB.ShipSymbol()},
	}
	repo := &scarceIronRepo{}
	fakeMediator := &factoryFakeMediator{}
	resolver := mfgServices.NewSupplyChainResolver(map[string][]string{testOutputGood: {testInputGood}}, repo)
	marketLocator := mfgServices.NewMarketLocator(repo, nil, nil, nil)
	handler := NewRunFactoryCoordinatorHandler(fakeMediator, shipRepo, repo, resolver, marketLocator, clock, nil)
	cmd := &RunFactoryCoordinatorCommand{PlayerID: 1, TargetGood: testOutputGood, SystemSymbol: testSystem, ContainerID: testContainerID, MaxIterations: -1}
	return &factoryFixture{handler: handler, shipRepo: shipRepo, mediator: fakeMediator, cmd: cmd}
}

// End-to-end: a -1 factory whose input layer is SCARCE PAUSES pre-spend (no buys), sets the
// input-layer NoWorkReason, and — the anti-cycle's core — backs off for the recovery half-life
// (not the 45s no-work poll) so it stops polling and pressing the market during early recovery.
func TestInputPause_Handle_ScarceInput_PausesAndSleepsHalfLife(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)}
	f := newScarceInputFixture(t, clock)
	ctx := common.WithLogger(context.Background(), &capturingLogger{})

	before := clock.CurrentTime
	resp, err := f.handler.Handle(ctx, f.cmd)
	if err != nil {
		t.Fatalf("expected a clean paused response, got error: %v", err)
	}
	coordResp := resp.(*RunFactoryCoordinatorResponse)
	if !strings.Contains(coordResp.NoWorkReason, "input layer ineligible") {
		t.Fatalf("expected the input-layer pause reason, got %q", coordResp.NoWorkReason)
	}
	if len(f.mediator.purchases) != 0 {
		t.Fatalf("a paused chain must spend ZERO — got %d purchases", len(f.mediator.purchases))
	}
	if elapsed := clock.CurrentTime.Sub(before); elapsed < defaultInputRecoveryReattemptMinutes*time.Minute {
		t.Fatalf("expected Handle to back off ~the %dmin recovery half-life, clock only advanced %v", defaultInputRecoveryReattemptMinutes, elapsed)
	}
}

// Composition with the C2 kill-switch: when a chain would trip BOTH the input-pause (SCARCE
// inputs) and the C2 realized-P&L kill on the same tick, the input-pause WINS — it runs first
// (cheaper, upstream cause), so the C2 kill never evaluates. Proven by the NoWorkReason being the
// input-pause line and the C2 kill counter staying at zero.
func TestInputPause_Handle_WinsPrecedenceOverC2Kill(t *testing.T) {
	prev := metrics.Registry
	prevInput := metrics.GetGlobalChainInputPauseCollector()
	prevPnL := metrics.GetGlobalChainPnLCollector()
	t.Cleanup(func() {
		metrics.Registry = prev
		metrics.SetGlobalChainInputPauseCollector(prevInput)
		metrics.SetGlobalChainPnLCollector(prevPnL)
	})
	metrics.Registry = prometheus.NewRegistry()
	inputCollector := metrics.NewChainInputPauseMetricsCollector()
	pnlCollector := metrics.NewChainPnLMetricsCollector()
	if err := inputCollector.Register(); err != nil {
		t.Fatalf("register input: %v", err)
	}
	if err := pnlCollector.Register(); err != nil {
		t.Fatalf("register pnl: %v", err)
	}
	metrics.SetGlobalChainInputPauseCollector(inputCollector)
	metrics.SetGlobalChainPnLCollector(pnlCollector)

	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)}
	f := newScarceInputFixture(t, clock)
	// Wire a C2 reader that WOULD kill (deeply underwater realized P&L) so both guards would fire.
	f.handler.SetChainPnLReader(&fakeChainPnLReader{raw: singleGoodRaw(-300000, 50000, 0, 0)})
	ctx := common.WithLogger(context.Background(), &capturingLogger{})

	resp, err := f.handler.Handle(ctx, f.cmd)
	if err != nil {
		t.Fatalf("Handle errored: %v", err)
	}
	coordResp := resp.(*RunFactoryCoordinatorResponse)
	if !strings.Contains(coordResp.NoWorkReason, "input layer ineligible") {
		t.Fatalf("input-pause must win precedence — reason should be the input-layer pause, got %q", coordResp.NoWorkReason)
	}
	if got, ok := gatherCounterValue(t, metrics.Registry, "spacetraders_daemon_chain_input_pause_total", testOutputGood); !ok || got != 1 {
		t.Errorf("input-pause counter = %v (ok=%v), want 1 (the pause fired)", got, ok)
	}
	if got, ok := gatherCounterValue(t, metrics.Registry, "spacetraders_daemon_chain_pnl_kills_total", testOutputGood); ok && got != 0 {
		t.Errorf("C2 kill counter = %v, want 0 (input-pause ran first, C2 never evaluated)", got)
	}
}
