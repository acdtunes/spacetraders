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

// fakeScanner is the sp-hh0h shipyard-readability positioner port. dispatched/err are what it returns;
// readyAcq (optional) is flipped readable when it "dispatches", modeling the hull arriving at the yard
// so the NEXT tick's live price read succeeds.
type fakeScanner struct {
	dispatched  bool
	err         error
	calls       int
	homeSystems []string
	readyAcq    *fakeAcquirer // if set, its readable is flipped true on a dispatch
}

func (f *fakeScanner) EnsureHomeShipyardReadable(ctx context.Context, playerID int, homeSystem string) (bool, error) {
	f.calls++
	f.homeSystems = append(f.homeSystems, homeSystem)
	if f.err != nil {
		return false, f.err
	}
	if f.dispatched && f.readyAcq != nil {
		f.readyAcq.readable = true // the hull reaches the yard → the live price becomes readable
	}
	return f.dispatched, nil
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

// sp-hh0h: buy-to-target in ONE tick (not one probe per 5-min tick). A cold agent with 1 probe and
// target 3 buys the 2-probe remainder this tick, capital permitting.
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
	if res.Purchased != 2 {
		t.Fatalf("live-by-default cold agent (1/3 probes) should buy the 2-probe remainder to target this tick, got %d (blocker=%q)", res.Purchased, res.Blocker)
	}
	if acq.buys != 2 {
		t.Fatalf("acquirer should have executed 2 buys to reach target, got %d", acq.buys)
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
	cfg := resolveBootstrapConfig(baseCmd(), nil)
	if p := derivePhase(Observation{MarketsTotal: 10, MarketsCovered: 0}, cfg); p != PhaseData {
		t.Fatalf("uncovered world should derive DATA, got %s", p)
	}
	// cold agent: nothing known yet (total 0) stays DATA, never reads empty world as fully covered
	if p := derivePhase(Observation{MarketsTotal: 0, MarketsCovered: 0}, cfg); p != PhaseData {
		t.Fatalf("cold agent (total 0) should derive DATA, got %s", p)
	}
}

func TestBootstrap_DerivePhase_BeyondDataAtCoverageBar(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd(), nil) // bar 0.9
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
	// treasury 150k, cap 75k/decrementing, probe 40k → both remaining buys affordable (1→3, need 2).
	obs := Observation{HomeSystem: "X1-HQ", ProbeCount: 1, ProbesScouting: 1, HasIdlePurchaser: true, Treasury: 150000, Readable: true}
	acq := &fakeAcquirer{price: 40000, yard: "Y", readable: true}
	h := newWiredHandler(obs, acq, &fakeScouter{})
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if acq.buys != 2 || res.Purchased != 2 {
		t.Fatalf("affordable probes should buy to target (2 remaining): buys=%d purchased=%d", acq.buys, res.Purchased)
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

// --- sp-hh0h: buy to target in ONE tick (not one probe per tick) ---

func TestBootstrap_BuysToTargetInOneTick(t *testing.T) {
	// 0/3 probes, ample treasury → buy all 3 THIS tick (the old behavior was exactly 1).
	obs := Observation{HomeSystem: "X1-HQ", ProbeCount: 0, HasIdlePurchaser: true, Treasury: 500000, Readable: true}
	acq := &fakeAcquirer{price: 40000, yard: "Y", readable: true}
	h := newWiredHandler(obs, acq, &fakeScouter{})
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if res.Purchased != 3 || acq.buys != 3 {
		t.Fatalf("short by 3 must buy to target (3) in one tick: purchased=%d buys=%d", res.Purchased, acq.buys)
	}
}

// The buy loop honors the reserve_margin capital gate against the DECREMENTING treasury: it buys what
// fits this tick and stops (the rest next tick as treasury grows), never overspending on a stale snapshot.
func TestBootstrap_BuyLoop_CapitalGateStopsPartway(t *testing.T) {
	// treasury 100k, reserve_margin 0.5, price 40k. iter1: cap on 100k = 50k ≥ 40k → buy (spent 40k).
	// iter2: cap on remaining 60k = 30k < 40k → BLOCKED. So exactly 1 buys this tick, blocker capital_gate.
	obs := Observation{HomeSystem: "X1-HQ", ProbeCount: 0, HasIdlePurchaser: true, Treasury: 100000, Readable: true}
	acq := &fakeAcquirer{price: 40000, yard: "Y", readable: true}
	h := newWiredHandler(obs, acq, &fakeScouter{})
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if res.Purchased != 1 || acq.buys != 1 {
		t.Fatalf("decrementing capital gate should allow exactly 1 buy from 100k: purchased=%d buys=%d", res.Purchased, acq.buys)
	}
	if res.Blocker != "capital_gate" {
		t.Fatalf("expected capital_gate to stop the loop partway, got blocker=%q", res.Blocker)
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
	// buy-to-target dry-run reports the whole remainder it WOULD buy (0/3 → 3), still spending nothing.
	if res.WouldBuy != 3 {
		t.Fatalf("dry-run should report would_buy=3 (buy-to-target), got %d", res.WouldBuy)
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
	// buy-to-target records one metric per probe bought (0/3 → 3).
	if m.purchase != 3 {
		t.Fatalf("expected 3 probe-purchase metrics (buy-to-target), got %d", m.purchase)
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

// --- DATA-phase acceptance (sp-hh0h): from a cold fixture, reaches 3 probes scouting FAST — the probe
// fleet fills to target in ONE tick, then scouting is assigned — with no overshoot. ---

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

	// Tick 0 buys the whole 3-probe remainder to target; tick 1 assigns scouting on the now-observed
	// probes. A few ticks reach steady state (contrast the old ~4-tick one-probe-per-tick staging).
	firstTickBuys := 0
	for i := 0; i < 5; i++ {
		res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
		if err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
		if i == 0 {
			firstTickBuys = res.Purchased
		}
	}
	if firstTickBuys != 3 {
		t.Fatalf("DATA acceptance: probe fleet must reach target in the FIRST tick (buy-to-target), bought %d on tick 0", firstTickBuys)
	}
	final := world.snapshot()
	if final.ProbeCount != 3 {
		t.Fatalf("DATA acceptance: expected 3 probes, got %d", final.ProbeCount)
	}
	if final.ProbesScouting != 3 {
		t.Fatalf("DATA acceptance: expected 3 probes scouting, got %d", final.ProbesScouting)
	}
	if acq.buys != 3 {
		t.Fatalf("DATA acceptance: expected exactly 3 buys total (no overshoot), got %d", acq.buys)
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

// --- sp-hh0h: cold-start shipyard readability. An unreadable price positions a hull at the home yard
// (does NOT weaken the guard — no buy this tick), then buys to target once the live price reads. ---

// Price unreadable + scanner wired → the coordinator dispatches an idle hull to the yard (positioning),
// surfaces it on the heartbeat, and buys nothing this tick.
func TestBootstrap_PriceUnreadable_PositionsHullAtShipyard(t *testing.T) {
	obs := Observation{HomeSystem: "X1-HQ", ProbeCount: 1, ProbesScouting: 1, HasIdlePurchaser: true, Treasury: 150000, Readable: true}
	acq := &fakeAcquirer{price: 0, yard: "", readable: false} // cold shipyard: no priced listing yet
	scanner := &fakeScanner{dispatched: true}
	h := newWiredHandler(obs, acq, &fakeScouter{})
	h.SetShipyardScanner(scanner)

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if acq.buys != 0 {
		t.Fatalf("unreadable price must buy nothing this tick, got %d buys", acq.buys)
	}
	if scanner.calls != 1 || len(scanner.homeSystems) != 1 || scanner.homeSystems[0] != "X1-HQ" {
		t.Fatalf("unreadable price must dispatch the positioner once for the home system, got calls=%d systems=%v", scanner.calls, scanner.homeSystems)
	}
	if res.Blocker != "positioning_purchaser_at_shipyard" {
		t.Fatalf("the positioning must be surfaced on the heartbeat, got blocker=%q", res.Blocker)
	}
}

// Price unreadable but the scanner reports NOT dispatched (a hull is already there / en route, or none
// free) → the coordinator keeps waiting (price_unreadable), still buys nothing, no re-navigation churn.
func TestBootstrap_PriceUnreadable_ScannerAlreadyPositioned_Waits(t *testing.T) {
	obs := Observation{HomeSystem: "X1-HQ", ProbeCount: 1, ProbesScouting: 1, HasIdlePurchaser: true, Treasury: 150000, Readable: true}
	acq := &fakeAcquirer{readable: false}
	scanner := &fakeScanner{dispatched: false}
	h := newWiredHandler(obs, acq, &fakeScouter{})
	h.SetShipyardScanner(scanner)

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if acq.buys != 0 || scanner.calls != 1 {
		t.Fatalf("already-positioned: no buy, one scanner consult; got buys=%d calls=%d", acq.buys, scanner.calls)
	}
	if res.Blocker != "price_unreadable" {
		t.Fatalf("awaiting a readable price should surface price_unreadable, got %q", res.Blocker)
	}
}

// Acceptance (defect 1): a cold home shipyard SELF-CLEARS — tick 0 positions a hull (no buy), tick 1
// finds the price readable and buys the whole fleet to target. Zero captain intervention.
func TestBootstrap_ColdShipyard_PositionsThenBuysToTarget(t *testing.T) {
	obs := Observation{HomeSystem: "X1-HQ", ProbeCount: 0, HasIdlePurchaser: true, Treasury: 500000, Readable: true}
	acq := &fakeAcquirer{price: 40000, yard: "X1-HQ-YARD", readable: false} // starts cold
	scanner := &fakeScanner{dispatched: true, readyAcq: acq}                // dispatch → price reads next tick
	h := newWiredHandler(obs, acq, &fakeScouter{})
	h.SetShipyardScanner(scanner)

	res0, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if res0.Purchased != 0 || scanner.calls != 1 {
		t.Fatalf("tick0 (cold yard): must position, not buy; got purchased=%d scanner.calls=%d", res0.Purchased, scanner.calls)
	}
	res1, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if res1.Purchased != 3 || acq.buys != 3 {
		t.Fatalf("tick1 (price now readable): must buy to target 3; got purchased=%d buys=%d", res1.Purchased, acq.buys)
	}
}

// --- sp-t39j: DATA (scanning) and INCOME (contracts) run in PARALLEL from hour-0. Coverage no longer
// gates income — contracts are the RULINGS #1 funding floor, started while probes are still scanning. ---

// The critical parallel pin: a cold, uncovered world (still DATA/scanning) STILL launches the contract
// engine this tick AND buys probes to target — both workstreams act in one reconcile.
func TestBootstrap_ParallelDataIncome_ContractsStartAtHour0WhileScanning(t *testing.T) {
	obs := Observation{
		HomeSystem: "X1-HQ", ProbeCount: 1, ProbesScouting: 1, HasIdlePurchaser: true,
		MarketsTotal: 10, MarketsCovered: 0, // coverage 0 → still DATA (scanning)
		Treasury: 500000, CommandFrigateID: "FRIGATE-1", BatchContractRunning: false, Readable: true,
	}
	acq := &fakeAcquirer{price: 40000, yard: "X1-HQ-YARD", readable: true}
	run := &fakeContractRunner{}
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(&fakeObserver{obs: obs})
	h.SetProbeAcquirer(acq)
	h.SetScoutAssigner(&fakeScouter{})
	h.SetFrigateRetirer(&fakeRetirer{})
	h.SetHaulerAcquirer(&fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true})
	h.SetContractRunner(run)

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if res.Phase != PhaseData {
		t.Fatalf("uncovered world is still in the DATA (scanning) label, got %s", res.Phase)
	}
	if acq.buys != 2 { // 1/3 probes → buy the 2-probe remainder (scanning workstream)
		t.Fatalf("scanning must run in parallel: expected 2 probe buys to target, got %d", acq.buys)
	}
	if run.calls != 1 || !res.ContractRun {
		t.Fatalf("contracts must start at HOUR-0 in parallel with scanning: batch-contract calls=%d ran=%v", run.calls, res.ContractRun)
	}
}

// GATE triggers on funding regardless of coverage (t39j point 4): a fleet that clears income_bar while
// still scanning enters GATE, not held in DATA by the coverage bar.
func TestBootstrap_DerivePhase_IncomeBarBeatsCoverage_Gate(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd(), nil) // income_bar 10000, coverage_bar 0.90
	obs := Observation{MarketsTotal: 10, MarketsCovered: 3, IncomePerHour: 12000}
	if p := derivePhase(obs, cfg); p != PhaseGate {
		t.Fatalf("income over the bar while still scanning (coverage 30%%) should derive GATE, got %s", p)
	}
}
