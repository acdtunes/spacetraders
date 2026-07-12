package commands

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// --- fakes (black-box: the reconciler is driven through its ports only) ---

type fakeRefresher struct {
	calls int
	err   error
}

func (f *fakeRefresher) RefreshFleet(ctx context.Context, playerID int) error {
	f.calls++
	return f.err
}

// fakeObserver returns a fixed Observation. When world is non-nil it snapshots the live world
// instead, so a multi-tick test sees the effect of buys/assignments.
type fakeObserver struct {
	obs   Observation
	err   error
	world *scriptedWorld
	calls int
}

func (f *fakeObserver) Observe(ctx context.Context, playerID int) (Observation, error) {
	f.calls++
	if f.err != nil {
		return Observation{}, f.err
	}
	if f.world != nil {
		return f.world.snapshot(), nil
	}
	return f.obs, nil
}

type fakeAcquirer struct {
	price     int64
	yard      string
	readable  bool
	priceErr  error
	buyErr    error
	buys      int
	priceChks int
	world     *scriptedWorld // mutated on a successful buy
}

func (f *fakeAcquirer) PriceCheck(ctx context.Context, playerID int, shipType string) (int64, string, bool, error) {
	f.priceChks++
	return f.price, f.yard, f.readable, f.priceErr
}

func (f *fakeAcquirer) Buy(ctx context.Context, playerID int, shipType, yard string) (BuyResult, error) {
	if f.buyErr != nil {
		return BuyResult{}, f.buyErr
	}
	f.buys++
	if f.world != nil {
		f.world.addProbe()
	}
	return BuyResult{ShipSymbol: "PROBE-NEW", Price: f.price}, nil
}

type fakeScouter struct {
	calls   int
	systems []string
	err     error
	world   *scriptedWorld // mutated on a successful assignment (all probes now scout)
}

func (f *fakeScouter) AssignAllMarkets(ctx context.Context, playerID int, system string) error {
	f.calls++
	f.systems = append(f.systems, system)
	if f.err != nil {
		return f.err
	}
	if f.world != nil {
		f.world.scoutAll()
	}
	return nil
}

type fakeMetrics struct {
	phases          []string
	purchase        int
	haulers         int
	constructionPct float64
	pctRecorded     bool
}

func (m *fakeMetrics) RecordPhase(phase string) { m.phases = append(m.phases, phase) }
func (m *fakeMetrics) RecordProbePurchased()    { m.purchase++ }
func (m *fakeMetrics) RecordHaulerPurchased()   { m.haulers++ }
func (m *fakeMetrics) RecordConstructionPct(pct float64) {
	m.constructionPct = pct
	m.pctRecorded = true
}

// scriptedWorld is a tiny stateful model so a multi-tick acceptance test can observe the effect of
// buys and scout assignments (the DATA arc reaching 3 probes scouting).
type scriptedWorld struct {
	mu             sync.Mutex
	probeCount     int
	probesScouting int
	treasury       int64
	homeSystem     string
	marketsCovered int
	marketsTotal   int
	hasPurchaser   bool
}

func (w *scriptedWorld) snapshot() Observation {
	w.mu.Lock()
	defer w.mu.Unlock()
	return Observation{
		HomeSystem:       w.homeSystem,
		ProbeCount:       w.probeCount,
		ProbesScouting:   w.probesScouting,
		HasIdlePurchaser: w.hasPurchaser,
		MarketsCovered:   w.marketsCovered,
		MarketsTotal:     w.marketsTotal,
		Treasury:         w.treasury,
		Readable:         true,
	}
}

func (w *scriptedWorld) addProbe() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.probeCount++
}

func (w *scriptedWorld) scoutAll() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.probesScouting = w.probeCount
}

// capturingLogger records every log line so tests can pin the heartbeat + decision-line
// observability requirements (captain L61 — never a silent stall).
type capturingLogger struct {
	mu    sync.Mutex
	lines []logLine
}

type logLine struct {
	level  string
	msg    string
	action string
}

func (l *capturingLogger) Log(level, message string, metadata map[string]interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	action := ""
	if metadata != nil {
		if a, ok := metadata["action"].(string); ok {
			action = a
		}
	}
	l.lines = append(l.lines, logLine{level: level, msg: message, action: action})
}

func (l *capturingLogger) has(action string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, ln := range l.lines {
		if ln.action == action {
			return true
		}
	}
	return false
}

func (l *capturingLogger) find(action string) (logLine, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, ln := range l.lines {
		if ln.action == action {
			return ln, true
		}
	}
	return logLine{}, false
}

func ctxWithLogger(l common.ContainerLogger) context.Context {
	return common.WithLogger(context.Background(), l)
}

func baseCmd() *RunBootstrapCoordinatorCommand {
	// All-zero knobs on purpose: pins that the resolved defaults arm the coordinator LIVE.
	return &RunBootstrapCoordinatorCommand{PlayerID: 1, ContainerID: "boot-1", AgentSymbol: "TEST"}
}

// --- live-by-default: a fresh, all-zero-config launch acts (no enablement flip) ---

func TestBootstrap_LiveByDefault_BuysProbeOnColdAgent(t *testing.T) {
	obs := Observation{HomeSystem: "X1-HQ", ProbeCount: 1, ProbesScouting: 1, HasIdlePurchaser: true, Treasury: 150000, Readable: true}
	acq := &fakeAcquirer{price: 40000, yard: "X1-HQ-YARD", readable: true}
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(&fakeObserver{obs: obs})
	h.SetProbeAcquirer(acq)
	h.SetScoutAssigner(&fakeScouter{})

	log := &capturingLogger{}
	res, err := h.reconcileOnce(ctxWithLogger(log), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if res.Purchased != 1 {
		t.Fatalf("live-by-default cold agent should buy 1 probe, got %d (blocker=%q)", res.Purchased, res.Blocker)
	}
	if acq.buys != 1 {
		t.Fatalf("acquirer should have executed 1 buy, got %d", acq.buys)
	}
}

// --- disabled boot-gate: takes no action but stays resident (returns cleanly) ---

func TestBootstrap_Disabled_TakesNoAction(t *testing.T) {
	acq := &fakeAcquirer{price: 40000, yard: "Y", readable: true}
	scout := &fakeScouter{}
	ref := &fakeRefresher{}
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(ref)
	h.SetWorldObserver(&fakeObserver{obs: Observation{ProbeCount: 0, HasIdlePurchaser: true, Treasury: 999999, Readable: true}})
	h.SetProbeAcquirer(acq)
	h.SetScoutAssigner(scout)

	cmd := baseCmd()
	cmd.Disabled = true
	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), cmd)
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if acq.buys != 0 || scout.calls != 0 || ref.calls != 0 {
		t.Fatalf("disabled coordinator must not act: buys=%d scouts=%d refresh=%d", acq.buys, scout.calls, ref.calls)
	}
	if res.Purchased != 0 {
		t.Fatalf("disabled: expected 0 purchases, got %d", res.Purchased)
	}
}

// --- phase derivation is from observation, never a stored cursor ---

func TestBootstrap_DerivePhase_DataWhenUncovered(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd())
	if p := derivePhase(Observation{MarketsTotal: 10, MarketsCovered: 0}, cfg); p != PhaseData {
		t.Fatalf("uncovered world should derive DATA, got %s", p)
	}
	// cold agent: nothing known yet (total 0) stays DATA, never reads empty world as fully covered
	if p := derivePhase(Observation{MarketsTotal: 0, MarketsCovered: 0}, cfg); p != PhaseData {
		t.Fatalf("cold agent (total 0) should derive DATA, got %s", p)
	}
}

func TestBootstrap_DerivePhase_BeyondDataAtCoverageBar(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd()) // bar 0.9
	if p := derivePhase(Observation{MarketsTotal: 10, MarketsCovered: 9}, cfg); p != PhaseIncome {
		t.Fatalf("coverage 90%% should derive past DATA (INCOME), got %s", p)
	}
}

// At/over the coverage bar the arc enters INCOME (Slice 2): the DATA act (probe buy, scout assign)
// must NOT run — only INCOME acts from here. (Pre-Slice-2 this held at DATA-complete; INCOME is now
// live, so the assertion is the phase crossover + DATA-act silence, not a "not implemented" hold.)
func TestBootstrap_CoverageMet_EntersIncome_NoDataAct(t *testing.T) {
	obs := Observation{HomeSystem: "X1-HQ", ProbeCount: 3, ProbesScouting: 3, HasIdlePurchaser: true, Treasury: 500000, MarketsTotal: 10, MarketsCovered: 10, Readable: true}
	acq := &fakeAcquirer{price: 40000, yard: "Y", readable: true}
	scout := &fakeScouter{}
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(&fakeObserver{obs: obs})
	h.SetProbeAcquirer(acq)
	h.SetScoutAssigner(scout)

	log := &capturingLogger{}
	res, err := h.reconcileOnce(ctxWithLogger(log), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if res.Phase != PhaseIncome {
		t.Fatalf("expected derived phase INCOME, got %s", res.Phase)
	}
	if acq.buys != 0 || scout.calls != 0 {
		t.Fatalf("coverage met: DATA act must not run; buys=%d scouts=%d", acq.buys, scout.calls)
	}
	// INCOME is implemented now — no "phase not yet implemented" hold at INCOME (that line is reserved
	// for GATE, past the income bar).
	if log.has("bootstrap_phase_not_implemented") {
		t.Fatalf("INCOME is live: must not log a 'phase not yet implemented' hold")
	}
}

// --- phantom-cache guard (captain L47): refresh before observe; refresh failure fails closed ---

func TestBootstrap_RefreshesBeforeObserving(t *testing.T) {
	ref := &fakeRefresher{}
	obsvr := &fakeObserver{obs: Observation{HasIdlePurchaser: true, Treasury: 100000, Readable: true}}
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(ref)
	h.SetWorldObserver(obsvr)
	h.SetProbeAcquirer(&fakeAcquirer{price: 40000, yard: "Y", readable: true})
	h.SetScoutAssigner(&fakeScouter{})

	if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if ref.calls != 1 {
		t.Fatalf("expected exactly 1 fleet refresh, got %d", ref.calls)
	}
	if obsvr.calls != 1 {
		t.Fatalf("expected observe after refresh, got %d observe calls", obsvr.calls)
	}
}

func TestBootstrap_RefreshFailure_FailsClosed(t *testing.T) {
	ref := &fakeRefresher{err: errors.New("refresh boom")}
	obsvr := &fakeObserver{obs: Observation{ProbeCount: 0, HasIdlePurchaser: true, Treasury: 100000, Readable: true}}
	acq := &fakeAcquirer{price: 40000, yard: "Y", readable: true}
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(ref)
	h.SetWorldObserver(obsvr)
	h.SetProbeAcquirer(acq)
	h.SetScoutAssigner(&fakeScouter{})

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce should swallow refresh failure, got err %v", err)
	}
	if obsvr.calls != 0 {
		t.Fatalf("refresh failure must fail closed BEFORE observing; observe calls=%d", obsvr.calls)
	}
	if acq.buys != 0 || res.Purchased != 0 {
		t.Fatalf("refresh failure must take no action; buys=%d", acq.buys)
	}
}

// --- capital gate: price ≤ reserve_margin × treasury, fail closed, decision line emitted ---

func TestBootstrap_CapitalGate_BlocksUnaffordableProbe(t *testing.T) {
	// treasury 150k, reserve_margin 0.5 → cap 75k. A 300k probe must be blocked.
	obs := Observation{HomeSystem: "X1-HQ", ProbeCount: 1, ProbesScouting: 1, HasIdlePurchaser: true, Treasury: 150000, Readable: true}
	acq := &fakeAcquirer{price: 300000, yard: "Y", readable: true}
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(&fakeObserver{obs: obs})
	h.SetProbeAcquirer(acq)
	h.SetScoutAssigner(&fakeScouter{})

	log := &capturingLogger{}
	res, err := h.reconcileOnce(ctxWithLogger(log), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if acq.buys != 0 {
		t.Fatalf("unaffordable probe must NOT buy, got %d buys", acq.buys)
	}
	if res.Blocker != "capital_gate" {
		t.Fatalf("expected capital_gate blocker, got %q", res.Blocker)
	}
	// decision line must carry the arithmetic (price + treasury + cap)
	dl, ok := log.find("bootstrap_buy_decision")
	if !ok {
		t.Fatalf("expected a buy-decision line with the guardrail arithmetic")
	}
	for _, want := range []string{"price=300000", "treasury=150000", "cap="} {
		if !strings.Contains(dl.msg, want) {
			t.Fatalf("decision line missing %q: %s", want, dl.msg)
		}
	}
}

func TestBootstrap_CapitalGate_AllowsAffordableProbe(t *testing.T) {
	// treasury 150k, cap 75k, probe 40k → affordable.
	obs := Observation{HomeSystem: "X1-HQ", ProbeCount: 1, ProbesScouting: 1, HasIdlePurchaser: true, Treasury: 150000, Readable: true}
	acq := &fakeAcquirer{price: 40000, yard: "Y", readable: true}
	h := newWiredHandler(obs, acq, &fakeScouter{})
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if acq.buys != 1 || res.Purchased != 1 {
		t.Fatalf("affordable probe should buy exactly 1: buys=%d purchased=%d", acq.buys, res.Purchased)
	}
}

// --- readiness gate: no idle purchaser blocks (not fails) ---

func TestBootstrap_NoPurchaser_Blocks(t *testing.T) {
	obs := Observation{HomeSystem: "X1-HQ", ProbeCount: 1, ProbesScouting: 1, HasIdlePurchaser: false, Treasury: 150000, Readable: true}
	acq := &fakeAcquirer{price: 40000, yard: "Y", readable: true}
	h := newWiredHandler(obs, acq, &fakeScouter{})
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if acq.buys != 0 || acq.priceChks != 0 {
		t.Fatalf("no purchaser: must not price-check or buy; priceChks=%d buys=%d", acq.priceChks, acq.buys)
	}
	if res.Blocker != "no_purchaser" {
		t.Fatalf("expected no_purchaser blocker, got %q", res.Blocker)
	}
}

// --- price unreadable → fail closed ---

func TestBootstrap_PriceUnreadable_FailsClosed(t *testing.T) {
	obs := Observation{HomeSystem: "X1-HQ", ProbeCount: 1, HasIdlePurchaser: true, Treasury: 150000, Readable: true}
	acq := &fakeAcquirer{price: 0, yard: "", readable: false}
	h := newWiredHandler(obs, acq, &fakeScouter{})
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if acq.buys != 0 {
		t.Fatalf("unreadable price must fail closed (no buy), got %d buys", acq.buys)
	}
	if res.Blocker != "price_unreadable" {
		t.Fatalf("expected price_unreadable blocker, got %q", res.Blocker)
	}
}

// --- at most ONE buy per tick, even when short by more than one ---

func TestBootstrap_OneBuyPerTick(t *testing.T) {
	obs := Observation{HomeSystem: "X1-HQ", ProbeCount: 0, HasIdlePurchaser: true, Treasury: 500000, Readable: true}
	acq := &fakeAcquirer{price: 40000, yard: "Y", readable: true}
	h := newWiredHandler(obs, acq, &fakeScouter{})
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if res.Purchased != 1 || acq.buys != 1 {
		t.Fatalf("short by 3 but must buy exactly 1 per tick: purchased=%d buys=%d", res.Purchased, acq.buys)
	}
}

// --- scout assignment is idempotent: skip when every probe already scouts ---

func TestBootstrap_ScoutAssign_SkippedWhenAllScouting(t *testing.T) {
	obs := Observation{HomeSystem: "X1-HQ", ProbeCount: 3, ProbesScouting: 3, HasIdlePurchaser: true, Treasury: 500000, Readable: true}
	scout := &fakeScouter{}
	h := newWiredHandler(obs, &fakeAcquirer{price: 40000, yard: "Y", readable: true}, scout)
	h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if scout.calls != 0 {
		t.Fatalf("all probes scouting: assignment must be skipped, got %d calls", scout.calls)
	}
}

func TestBootstrap_ScoutAssign_RunsWhenProbeNotScouting(t *testing.T) {
	obs := Observation{HomeSystem: "X1-HQ", ProbeCount: 3, ProbesScouting: 1, HasIdlePurchaser: true, Treasury: 500000, Readable: true}
	scout := &fakeScouter{}
	h := newWiredHandler(obs, &fakeAcquirer{price: 40000, yard: "Y", readable: true}, scout)
	h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if scout.calls != 1 || len(scout.systems) != 1 || scout.systems[0] != "X1-HQ" {
		t.Fatalf("probe not scouting: expected 1 assignment in X1-HQ, got calls=%d systems=%v", scout.calls, scout.systems)
	}
}

// --- dry-run: observes + logs would-buy but takes NO action ---

func TestBootstrap_DryRun_TakesNoAction(t *testing.T) {
	obs := Observation{HomeSystem: "X1-HQ", ProbeCount: 0, ProbesScouting: 0, HasIdlePurchaser: true, Treasury: 500000, Readable: true}
	acq := &fakeAcquirer{price: 40000, yard: "Y", readable: true}
	scout := &fakeScouter{}
	h := newWiredHandler(obs, acq, scout)
	cmd := baseCmd()
	cmd.DryRun = true
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), cmd)
	if acq.buys != 0 || scout.calls != 0 {
		t.Fatalf("dry-run must take no action: buys=%d scouts=%d", acq.buys, scout.calls)
	}
	if res.WouldBuy != 1 {
		t.Fatalf("dry-run should report would_buy=1, got %d", res.WouldBuy)
	}
}

// --- heartbeat emitted every tick (captain L61: never a silent stall) ---

func TestBootstrap_HeartbeatEmittedEveryTick(t *testing.T) {
	obs := Observation{HomeSystem: "X1-HQ", ProbeCount: 2, ProbesScouting: 2, HasIdlePurchaser: true, Treasury: 500000, MarketsTotal: 10, MarketsCovered: 4, Readable: true}
	h := newWiredHandler(obs, &fakeAcquirer{price: 40000, yard: "Y", readable: true}, &fakeScouter{})
	log := &capturingLogger{}
	h.reconcileOnce(ctxWithLogger(log), baseCmd())
	hb, ok := log.find("bootstrap_heartbeat")
	if !ok {
		t.Fatalf("every tick must emit a heartbeat")
	}
	for _, want := range []string{"phase=DATA", "probes=2/3", "coverage=4/10"} {
		if !strings.Contains(hb.msg, want) {
			t.Fatalf("heartbeat missing %q: %s", want, hb.msg)
		}
	}
}

// --- metrics: phase gauge + probe counter recorded ---

func TestBootstrap_RecordsMetrics(t *testing.T) {
	obs := Observation{HomeSystem: "X1-HQ", ProbeCount: 0, HasIdlePurchaser: true, Treasury: 500000, Readable: true}
	m := &fakeMetrics{}
	h := newWiredHandler(obs, &fakeAcquirer{price: 40000, yard: "Y", readable: true}, &fakeScouter{})
	h.SetMetricsSink(m)
	h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(m.phases) != 1 || m.phases[0] != "DATA" {
		t.Fatalf("expected phase DATA recorded, got %v", m.phases)
	}
	if m.purchase != 1 {
		t.Fatalf("expected 1 probe-purchase metric, got %d", m.purchase)
	}
}

// --- world unreadable → fail closed, but heartbeat still fires (no silent stall) ---

func TestBootstrap_UnreadableWorld_FailsClosedButHeartbeats(t *testing.T) {
	acq := &fakeAcquirer{price: 40000, yard: "Y", readable: true}
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(&fakeObserver{obs: Observation{Readable: false, Reason: "treasury read failed"}})
	h.SetProbeAcquirer(acq)
	h.SetScoutAssigner(&fakeScouter{})
	log := &capturingLogger{}
	res, _ := h.reconcileOnce(ctxWithLogger(log), baseCmd())
	if acq.buys != 0 {
		t.Fatalf("unreadable world must take no action, got %d buys", acq.buys)
	}
	if res.Blocker != "world_unreadable" {
		t.Fatalf("expected world_unreadable blocker, got %q", res.Blocker)
	}
	if !log.has("bootstrap_heartbeat") {
		t.Fatalf("unreadable world must still emit a heartbeat (no silent stall)")
	}
}

// --- recovery / idempotency: a restart at/after target never double-buys ---

func TestBootstrap_Recovery_NoBuyWhenTargetMet(t *testing.T) {
	// Simulate a restart that re-observes the count already at target (a mid-purchase crash that
	// had completed the buy): the fresh handler must NOT buy again.
	obs := Observation{HomeSystem: "X1-HQ", ProbeCount: 3, ProbesScouting: 3, HasIdlePurchaser: true, Treasury: 500000, MarketsTotal: 10, MarketsCovered: 5, Readable: true}
	acq := &fakeAcquirer{price: 40000, yard: "Y", readable: true}
	h := newWiredHandler(obs, acq, &fakeScouter{})
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if acq.buys != 0 || res.Purchased != 0 {
		t.Fatalf("target met on restart: must not double-buy; buys=%d", acq.buys)
	}
}

// --- DATA-phase acceptance (Slice 1): from a cold fixture, reaches 3 probes scouting, staged ---

func TestBootstrap_DataAcceptance_ReachesThreeProbesScouting(t *testing.T) {
	world := &scriptedWorld{probeCount: 0, probesScouting: 0, treasury: 500000, homeSystem: "X1-HQ", hasPurchaser: true, marketsTotal: 10, marketsCovered: 0}
	acq := &fakeAcquirer{price: 40000, yard: "X1-HQ-YARD", readable: true, world: world}
	scout := &fakeScouter{world: world}
	obsvr := &fakeObserver{world: world}
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(obsvr)
	h.SetProbeAcquirer(acq)
	h.SetScoutAssigner(scout)

	// Drive ticks until steady state; assert staging (one buy per tick).
	for i := 0; i < 10; i++ {
		res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
		if err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
		if res.Purchased > 1 {
			t.Fatalf("tick %d bought %d probes — staging violated (one per tick)", i, res.Purchased)
		}
	}
	final := world.snapshot()
	if final.ProbeCount != 3 {
		t.Fatalf("DATA acceptance: expected 3 probes, got %d", final.ProbeCount)
	}
	if final.ProbesScouting != 3 {
		t.Fatalf("DATA acceptance: expected 3 probes scouting, got %d", final.ProbesScouting)
	}
	if acq.buys != 3 {
		t.Fatalf("DATA acceptance: expected exactly 3 buys total (staged), got %d", acq.buys)
	}
}

// newWiredHandler builds a handler with a fixed observation and the standard refresher, for the
// single-tick guard pins.
func newWiredHandler(obs Observation, acq ProbeAcquirer, scout ScoutAssigner) *RunBootstrapCoordinatorHandler {
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(&fakeObserver{obs: obs})
	h.SetProbeAcquirer(acq)
	h.SetScoutAssigner(scout)
	return h
}
