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

// sp-xdk6 (analyst redesign C4, from sp-hzz5): mechanize the export-ask-subsidy REST signal (the
// 8w40 finding — tours paying a premium at OUR OWN markets = the chain over-lifted and needs to
// rest). These pins drive the coordinator's own-market-ask detection against the a5j7 eligible
// cross-source median, the rest/resume episode state machine, and the recovery-window clock —
// decoupled from the DB and, for the unit checks, from the full Handle().

const (
	testReExport1 = "X1-TEST-REEXPORT1"
	testReExport2 = "X1-TEST-REEXPORT2"
)

// restSource models one in-system market carrying the chain's OUTPUT good. importsInput marks a
// producing FACTORY (it imports the chain's input, so FindFactoryForProduction identifies it as
// OUR own market even when its ask has laddered above every healthy source); importsInput=false is
// a re-export market — an eligible-median baseline source, but never OUR factory.
type restSource struct {
	ask          int
	supply       string
	importsInput bool
}

// restSignalRepo drives both FindFactoryForProduction (own-market ask, via the imports-input
// identity) and EligibleSourceMedianAsk (the cross-source MODERATE+ median) off one controllable
// set of markets. findErr makes FindAllMarketsInSystem fail (the read-failure fail-toward-
// production path); reads counts market reads so the disabled-path "reads nothing" contract holds.
type restSignalRepo struct {
	market.MarketRepository
	outputGood string
	inputGood  string
	sources    map[string]restSource
	findErr    error
	reads      int
}

func (r *restSignalRepo) FindAllMarketsInSystem(ctx context.Context, systemSymbol string, playerID int) ([]string, error) {
	r.reads++
	if r.findErr != nil {
		return nil, r.findErr
	}
	wps := make([]string, 0, len(r.sources))
	for wp := range r.sources {
		wps = append(wps, wp)
	}
	return wps, nil
}

func (r *restSignalRepo) GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*market.Market, error) {
	r.reads++
	src, ok := r.sources[waypointSymbol]
	if !ok {
		return nil, nil
	}
	activity := "GROWING"
	supply := src.supply
	out, err := market.NewTradeGood(r.outputGood, &supply, &activity, src.ask, src.ask, 20, market.TradeTypeExport)
	if err != nil {
		return nil, err
	}
	tgoods := []market.TradeGood{*out}
	if src.importsInput {
		imp := "MODERATE"
		in, err := market.NewTradeGood(r.inputGood, &imp, &activity, 30, 32, 20, market.TradeTypeImport)
		if err != nil {
			return nil, err
		}
		tgoods = append(tgoods, *in)
	}
	return market.NewMarket(waypointSymbol, tgoods, time.Now())
}

// newRestSignalHandler wires a coordinator to a rest-controllable market repo and a clock so
// evaluateExportRest and the rest state machine can be driven directly.
func newRestSignalHandler(t *testing.T, clock shared.Clock, repo market.MarketRepository) *RunFactoryCoordinatorHandler {
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

func restSignalCmd() *RunFactoryCoordinatorCommand {
	return &RunFactoryCoordinatorCommand{
		PlayerID:     1,
		TargetGood:   testOutputGood,
		SystemSymbol: testSystem,
		ContainerID:  testContainerID,
	}
}

// restTreeRoot is the FAB_PLATE<-IRON root node (the target fabricate node with its one buy-leaf
// input), so directInputGoods yields [IRON] and FindFactoryForProduction identifies the own factory.
func restTreeRoot() *goods.SupplyChainNode {
	return fabRoot(testOutputGood, buyLeaf(testInputGood))
}

// A chain whose OWN factory ask has laddered ABOVE the cross-source eligible median RESTS, naming
// the own ask, the eligible median, and resolving the default rest window. This is the 8w40 case:
// our own market subsidized above healthy sources by our own over-lifting.
func TestExportRest_OwnAskAboveMedian_Rests(t *testing.T) {
	repo := &restSignalRepo{
		outputGood: testOutputGood, inputGood: testInputGood,
		sources: map[string]restSource{
			testFactoryWaypoint: {ask: 6000, supply: "MODERATE", importsInput: true}, // OUR factory (laddered)
			testReExport1:       {ask: 3000, supply: "MODERATE"},                      // healthy baseline sources
			testReExport2:       {ask: 3200, supply: "MODERATE"},
		},
	}
	handler := newRestSignalHandler(t, &shared.MockClock{CurrentTime: time.Now()}, repo)

	v := handler.evaluateExportRest(context.Background(), restSignalCmd(), restTreeRoot())

	if !v.Rested {
		t.Fatalf("expected a rest when own ask (6000) exceeds the eligible median, got %+v", v)
	}
	if v.Reason != exportRestSubsidized {
		t.Errorf("reason = %q, want %q", v.Reason, exportRestSubsidized)
	}
	if v.OwnAsk != 6000 {
		t.Errorf("own ask = %d, want 6000 (OUR factory, not the cheapest source)", v.OwnAsk)
	}
	if v.EligibleMedian != 3200 {
		t.Errorf("eligible median = %d, want 3200 (median of 3000,3200,6000)", v.EligibleMedian)
	}
	if v.WindowMinutes != defaultRestWindowMinutes {
		t.Errorf("window minutes = %d, want default %d", v.WindowMinutes, defaultRestWindowMinutes)
	}
}

// A chain whose own factory ask is at or below the eligible median PROCEEDS — the market is not
// subsidized, nothing to rest.
func TestExportRest_OwnAskAtOrBelowMedian_Proceeds(t *testing.T) {
	repo := &restSignalRepo{
		outputGood: testOutputGood, inputGood: testInputGood,
		sources: map[string]restSource{
			testFactoryWaypoint: {ask: 3000, supply: "MODERATE", importsInput: true},
			testReExport1:       {ask: 3200, supply: "MODERATE"},
			testReExport2:       {ask: 6000, supply: "MODERATE"},
		},
	}
	handler := newRestSignalHandler(t, &shared.MockClock{CurrentTime: time.Now()}, repo)

	v := handler.evaluateExportRest(context.Background(), restSignalCmd(), restTreeRoot())

	if v.Rested {
		t.Fatalf("own ask (3000) <= eligible median (3200) must not rest, got %+v", v)
	}
	if v.Reason != exportRestProceed {
		t.Errorf("reason = %q, want %q", v.Reason, exportRestProceed)
	}
}

// With NO eligible (MODERATE+) cross-source baseline — our factory is the only source and it has
// laddered out of MODERATE+ into SCARCE — there is no median to judge the ladder against, so the
// chain PROCEEDS. The signal is defined relative to a cross-source median; with none, no signal.
func TestExportRest_NoEligibleBaseline_Proceeds(t *testing.T) {
	repo := &restSignalRepo{
		outputGood: testOutputGood, inputGood: testInputGood,
		sources: map[string]restSource{
			testFactoryWaypoint: {ask: 6000, supply: "SCARCE", importsInput: true}, // laddered out of MODERATE+
		},
	}
	handler := newRestSignalHandler(t, &shared.MockClock{CurrentTime: time.Now()}, repo)

	v := handler.evaluateExportRest(context.Background(), restSignalCmd(), restTreeRoot())

	if v.Rested {
		t.Fatalf("no eligible cross-source baseline (count==0) must not rest, got %+v", v)
	}
	if v.Reason != exportRestNoBaseline {
		t.Errorf("reason = %q, want %q", v.Reason, exportRestNoBaseline)
	}
}

// The emergency disable flag skips detection entirely and reads NO markets (RULINGS #5).
func TestExportRest_Disabled_SkipsDetectionAndReads(t *testing.T) {
	repo := &restSignalRepo{
		outputGood: testOutputGood, inputGood: testInputGood,
		sources: map[string]restSource{
			testFactoryWaypoint: {ask: 6000, supply: "MODERATE", importsInput: true},
			testReExport1:       {ask: 3000, supply: "MODERATE"},
		},
	}
	handler := newRestSignalHandler(t, &shared.MockClock{CurrentTime: time.Now()}, repo)
	cmd := restSignalCmd()
	cmd.RestSignalDisabled = true

	v := handler.evaluateExportRest(context.Background(), cmd, restTreeRoot())

	if v.Rested {
		t.Fatalf("disabled rest signal must never rest, got %+v", v)
	}
	if v.Reason != exportRestDisabled {
		t.Errorf("reason = %q, want %q", v.Reason, exportRestDisabled)
	}
	if repo.reads != 0 {
		t.Errorf("disabled rest signal must not read any market, got %d reads", repo.reads)
	}
}

// A market-READ failure must NOT rest (fail toward production) — a rest on a transient blip is the
// expensive error, exactly as the input-pause treats a read failure.
func TestExportRest_ReadFailure_FailsTowardProduction(t *testing.T) {
	repo := &restSignalRepo{
		outputGood: testOutputGood, inputGood: testInputGood,
		sources: map[string]restSource{
			testFactoryWaypoint: {ask: 6000, supply: "MODERATE", importsInput: true},
			testReExport1:       {ask: 3000, supply: "MODERATE"},
		},
		findErr: errors.New("market list unreadable"),
	}
	handler := newRestSignalHandler(t, &shared.MockClock{CurrentTime: time.Now()}, repo)

	v := handler.evaluateExportRest(context.Background(), restSignalCmd(), restTreeRoot())

	if v.Rested {
		t.Fatalf("a market-read failure must NOT rest (fail toward production), got %+v", v)
	}
}

// The rest window is captain/analyst-owned config: a set value overrides the default.
func TestExportRest_ConfiguredWindowMinutes(t *testing.T) {
	repo := &restSignalRepo{
		outputGood: testOutputGood, inputGood: testInputGood,
		sources: map[string]restSource{
			testFactoryWaypoint: {ask: 6000, supply: "MODERATE", importsInput: true},
			testReExport1:       {ask: 3000, supply: "MODERATE"},
			testReExport2:       {ask: 3200, supply: "MODERATE"},
		},
	}
	handler := newRestSignalHandler(t, &shared.MockClock{CurrentTime: time.Now()}, repo)
	cmd := restSignalCmd()
	cmd.RestWindowMinutes = 45

	v := handler.evaluateExportRest(context.Background(), cmd, restTreeRoot())

	if v.WindowMinutes != 45 {
		t.Errorf("window minutes = %d, want configured 45", v.WindowMinutes)
	}
}

// Episode semantics + RESUME + the metric: the rest counter increments once per episode
// (running->resting), not on every re-check; when the market recovers the chain resumes and a later
// re-subsidy is a fresh episode. Drives the real counter to prove once-per-episode.
func TestExportRest_EpisodeDedupResumeAndMetric(t *testing.T) {
	prev := metrics.Registry
	prevGlobal := metrics.GetGlobalChainExportRestCollector()
	t.Cleanup(func() {
		metrics.Registry = prev
		metrics.SetGlobalChainExportRestCollector(prevGlobal)
	})
	metrics.Registry = prometheus.NewRegistry()
	collector := metrics.NewChainExportRestMetricsCollector()
	if err := collector.Register(); err != nil {
		t.Fatalf("register: %v", err)
	}
	metrics.SetGlobalChainExportRestCollector(collector)

	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)}
	repo := &restSignalRepo{outputGood: testOutputGood, inputGood: testInputGood}
	handler := newRestSignalHandler(t, clock, repo)
	cmd := restSignalCmd()
	logger := &capturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	restVerdict := exportRestVerdict{Rested: true, Reason: exportRestSubsidized, Good: testOutputGood, OwnAsk: 6000, EligibleMedian: 3200, EligibleCount: 3, OwnWaypoint: testFactoryWaypoint, WindowMinutes: defaultRestWindowMinutes}

	// Enter the resting episode: first record emits (transition), second re-arms but is deduped.
	if !handler.recordExportRest(ctx, cmd, restVerdict) {
		t.Errorf("first rest must be a state transition (emit)")
	}
	if handler.recordExportRest(ctx, cmd, restVerdict) {
		t.Errorf("second consecutive rest must be deduped (re-armed, no re-emit)")
	}
	// Recover: clearing a resting chain is a transition (resume logged).
	if !handler.clearExportRest(ctx, cmd) {
		t.Errorf("clearing a resting chain must be a state transition (resume)")
	}
	if handler.clearExportRest(ctx, cmd) {
		t.Errorf("clearing an already-lifting chain must be a no-op")
	}
	// A later re-subsidy is a fresh episode.
	if !handler.recordExportRest(ctx, cmd, restVerdict) {
		t.Errorf("a re-subsidy after recovery must be a new rest episode (emit)")
	}

	got, ok := gatherCounterValue(t, metrics.Registry, "spacetraders_daemon_chain_export_rest_total", testOutputGood)
	if !ok {
		t.Fatalf("export-rest counter series not found")
	}
	if got != 2 {
		t.Errorf("export-rest counter = %v, want 2 (two episodes, re-checks deduped)", got)
	}
}

// The recovery CLOCK: within the window the chain is held OFF the lift (exportRestWithinWindow
// true -> zero-poll short-circuit), and the backoff sleeps the remaining window, NOT the 45s
// no-work poll. Once the window elapses, it opens (false) so the next lift is re-attempted.
func TestExportRest_RecoveryClock_NoReattemptBeforeWindow(t *testing.T) {
	start := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	clock := &shared.MockClock{CurrentTime: start}
	repo := &restSignalRepo{outputGood: testOutputGood, inputGood: testInputGood}
	handler := newRestSignalHandler(t, clock, repo)
	cmd := restSignalCmd()
	ctx := common.WithLogger(context.Background(), &capturingLogger{})

	restVerdict := exportRestVerdict{Rested: true, Reason: exportRestSubsidized, Good: testOutputGood, OwnAsk: 6000, EligibleMedian: 3200, EligibleCount: 3, WindowMinutes: defaultRestWindowMinutes}
	handler.recordExportRest(ctx, cmd, restVerdict)

	// Immediately after resting: within the window, and the backoff sleeps ~the full window.
	if _, within := handler.exportRestWithinWindow(cmd.ContainerID); !within {
		t.Fatalf("chain must be within the rest window immediately after resting")
	}
	delay, resting := handler.exportRestReattemptDelay(cmd.ContainerID)
	if !resting {
		t.Fatalf("a resting chain must report a reattempt backoff delay")
	}
	if want := defaultRestWindowMinutes * time.Minute; delay > want || delay < want-time.Second {
		t.Errorf("backoff delay = %v, want ~%v (the rest window, not the 45s poll)", delay, want)
	}
	if delay <= noWorkIterationDelay {
		t.Fatalf("rest backoff (%v) must be far longer than the 45s no-work poll (%v)", delay, noWorkIterationDelay)
	}

	// Part-way through the window: still resting (no re-attempt), shorter remaining delay.
	clock.Advance(50 * time.Minute)
	if _, within := handler.exportRestWithinWindow(cmd.ContainerID); !within {
		t.Fatalf("chain must still be within the window 50min into a 90min rest")
	}

	// After the window elapses: it opens (re-attempt due), and the backoff reverts.
	clock.Advance(41 * time.Minute) // now 91min in, past the 90min window
	if _, within := handler.exportRestWithinWindow(cmd.ContainerID); within {
		t.Fatalf("after the rest window elapses it must open for the next lift")
	}
	if _, resting := handler.exportRestReattemptDelay(cmd.ContainerID); resting {
		t.Errorf("after the window elapses the reattempt backoff must revert to the normal no-work poll")
	}
}

// A re-attempt that finds the market STILL subsidized re-arms the clock for another window but does
// NOT re-emit (same episode).
func TestExportRest_ReAttemptStillSubsidized_ReArmsSameEpisode(t *testing.T) {
	start := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	clock := &shared.MockClock{CurrentTime: start}
	repo := &restSignalRepo{outputGood: testOutputGood, inputGood: testInputGood}
	handler := newRestSignalHandler(t, clock, repo)
	cmd := restSignalCmd()
	ctx := common.WithLogger(context.Background(), &capturingLogger{})
	restVerdict := exportRestVerdict{Rested: true, Reason: exportRestSubsidized, Good: testOutputGood, OwnAsk: 6000, EligibleMedian: 3200, EligibleCount: 3, WindowMinutes: defaultRestWindowMinutes}

	handler.recordExportRest(ctx, cmd, restVerdict) // episode 1

	clock.Advance(defaultRestWindowMinutes*time.Minute + time.Minute) // window elapsed
	if emitted := handler.recordExportRest(ctx, cmd, restVerdict); emitted {
		t.Fatalf("a still-subsidized re-attempt must re-arm the SAME episode, not re-emit")
	}
	delay, resting := handler.exportRestReattemptDelay(cmd.ContainerID)
	if !resting {
		t.Fatalf("re-armed rest must report a backoff delay")
	}
	if want := defaultRestWindowMinutes * time.Minute; delay > want || delay < want-time.Second {
		t.Errorf("re-armed delay = %v, want a fresh ~%v window", delay, want)
	}
}

// ---- Full Handle() integration ----

// subsidizedExportRepo is the productive FAB_PLATE<-IRON fixture with the OWN factory's FAB_PLATE
// ask laddered to 6000 above two healthy re-export sources (3000, 3200), so the export-ask-subsidy
// signal fires. ironSupply defaults to MODERATE (input layer eligible so the input-pause does NOT
// fire) but can be set SCARCE to trip both guards for the precedence pin.
type subsidizedExportRepo struct {
	factoryFakeMarketRepo
	ironSupply string
}

func (r *subsidizedExportRepo) FindAllMarketsInSystem(ctx context.Context, systemSymbol string, playerID int) ([]string, error) {
	return []string{testFactoryWaypoint, testIronWaypoint, testReExport1, testReExport2}, nil
}

func (r *subsidizedExportRepo) GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*market.Market, error) {
	activity := "GROWING"
	switch waypointSymbol {
	case testFactoryWaypoint:
		// OUR factory: FAB_PLATE EXPORT laddered to 6000 (still MODERATE), IRON IMPORT (the
		// imports-input identity FindFactoryForProduction keys on).
		supply := "MODERATE"
		out, err := market.NewTradeGood(testOutputGood, &supply, &activity, 6000, 6000, 20, market.TradeTypeExport)
		if err != nil {
			return nil, err
		}
		imp := "MODERATE"
		in, err := market.NewTradeGood(testInputGood, &imp, &activity, 30, 32, 20, market.TradeTypeImport)
		if err != nil {
			return nil, err
		}
		return market.NewMarket(waypointSymbol, []market.TradeGood{*out, *in}, time.Now())
	case testReExport1, testReExport2:
		supply := "MODERATE"
		ask := 3000
		if waypointSymbol == testReExport2 {
			ask = 3200
		}
		out, err := market.NewTradeGood(testOutputGood, &supply, &activity, ask, ask, 20, market.TradeTypeExport)
		if err != nil {
			return nil, err
		}
		return market.NewMarket(waypointSymbol, []market.TradeGood{*out}, time.Now())
	case testIronWaypoint:
		supply := r.ironSupply
		if supply == "" {
			supply = "MODERATE"
		}
		in, err := market.NewTradeGood(testInputGood, &supply, &activity, 8, 10, 10, market.TradeTypeExport)
		if err != nil {
			return nil, err
		}
		return market.NewMarket(waypointSymbol, []market.TradeGood{*in}, time.Now())
	}
	return nil, nil
}

func newSubsidizedExportFixture(t *testing.T, clock shared.Clock, ironSupply string) *factoryFixture {
	t.Helper()
	shipA := newTestHauler(t, "CRAFTY-2", nil)
	shipB := newTestHauler(t, "CRAFTY-3", nil)
	shipRepo := &factoryFakeShipRepo{
		ships: map[string]*navigation.Ship{shipA.ShipSymbol(): shipA, shipB.ShipSymbol(): shipB},
		order: []string{shipA.ShipSymbol(), shipB.ShipSymbol()},
	}
	repo := &subsidizedExportRepo{ironSupply: ironSupply}
	fakeMediator := &factoryFakeMediator{}
	resolver := mfgServices.NewSupplyChainResolver(map[string][]string{testOutputGood: {testInputGood}}, repo)
	marketLocator := mfgServices.NewMarketLocator(repo, nil, nil, nil)
	handler := NewRunFactoryCoordinatorHandler(fakeMediator, shipRepo, repo, resolver, marketLocator, clock, nil)
	cmd := &RunFactoryCoordinatorCommand{PlayerID: 1, TargetGood: testOutputGood, SystemSymbol: testSystem, ContainerID: testContainerID, MaxIterations: -1}
	return &factoryFixture{handler: handler, shipRepo: shipRepo, mediator: fakeMediator, cmd: cmd}
}

// End-to-end: a -1 factory whose OWN market ask is subsidized above the eligible median RESTS
// pre-spend (no buys), sets the rest NoWorkReason, and backs off for the rest window (not the 45s
// no-work poll) so it stops lifting and lets the market recover.
func TestExportRest_Handle_SubsidizedOwnMarket_RestsAndSleepsWindow(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)}
	f := newSubsidizedExportFixture(t, clock, "MODERATE")
	ctx := common.WithLogger(context.Background(), &capturingLogger{})

	before := clock.CurrentTime
	resp, err := f.handler.Handle(ctx, f.cmd)
	if err != nil {
		t.Fatalf("expected a clean resting response, got error: %v", err)
	}
	coordResp := resp.(*RunFactoryCoordinatorResponse)
	if !strings.Contains(coordResp.NoWorkReason, "export ask") {
		t.Fatalf("expected the export-ask-subsidy rest reason, got %q", coordResp.NoWorkReason)
	}
	if len(f.mediator.purchases) != 0 {
		t.Fatalf("a resting chain must spend ZERO — got %d purchases", len(f.mediator.purchases))
	}
	if elapsed := clock.CurrentTime.Sub(before); elapsed < defaultRestWindowMinutes*time.Minute {
		t.Fatalf("expected Handle to back off ~the %dmin rest window, clock only advanced %v", defaultRestWindowMinutes, elapsed)
	}
}

// Composition with the input-pause: when a chain would trip BOTH the input-pause (SCARCE inputs)
// AND the export-ask-subsidy rest on the same tick, the input-pause WINS — it runs first (cheaper,
// upstream cause), so the rest signal never evaluates. Proven by the NoWorkReason being the
// input-pause line and the export-rest counter staying at zero.
func TestExportRest_Handle_InputPauseWinsPrecedence(t *testing.T) {
	prev := metrics.Registry
	prevRest := metrics.GetGlobalChainExportRestCollector()
	prevInput := metrics.GetGlobalChainInputPauseCollector()
	t.Cleanup(func() {
		metrics.Registry = prev
		metrics.SetGlobalChainExportRestCollector(prevRest)
		metrics.SetGlobalChainInputPauseCollector(prevInput)
	})
	metrics.Registry = prometheus.NewRegistry()
	restCollector := metrics.NewChainExportRestMetricsCollector()
	inputCollector := metrics.NewChainInputPauseMetricsCollector()
	if err := restCollector.Register(); err != nil {
		t.Fatalf("register rest: %v", err)
	}
	if err := inputCollector.Register(); err != nil {
		t.Fatalf("register input: %v", err)
	}
	metrics.SetGlobalChainExportRestCollector(restCollector)
	metrics.SetGlobalChainInputPauseCollector(inputCollector)

	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)}
	// IRON SCARCE trips the input-pause; the own FAB_PLATE market is also subsidized (would rest).
	f := newSubsidizedExportFixture(t, clock, "SCARCE")
	ctx := common.WithLogger(context.Background(), &capturingLogger{})

	resp, err := f.handler.Handle(ctx, f.cmd)
	if err != nil {
		t.Fatalf("Handle errored: %v", err)
	}
	coordResp := resp.(*RunFactoryCoordinatorResponse)
	if !strings.Contains(coordResp.NoWorkReason, "input layer ineligible") {
		t.Fatalf("input-pause must win precedence — reason should be the input-layer pause, got %q", coordResp.NoWorkReason)
	}
	if got, ok := gatherCounterValue(t, metrics.Registry, "spacetraders_daemon_chain_export_rest_total", testOutputGood); ok && got != 0 {
		t.Errorf("export-rest counter = %v, want 0 (input-pause ran first, rest never evaluated)", got)
	}
}
