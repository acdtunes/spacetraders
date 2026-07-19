package commands

import (
	"context"
	"errors"
	"testing"
)

// --- derivePhase: GATE stickiness + COMPLETE ---

// The INCOME→GATE entry is realized $/hr ≥ income_bar.
func TestBootstrap_DerivePhase_EntersGateAtIncomeBar(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd(), nil) // income_bar 10000
	obs := Observation{MarketsTotal: 10, MarketsCovered: 10, IncomePerHour: 12000}
	if p := derivePhase(obs, cfg); p != PhaseGate {
		t.Fatalf("income over bar should derive GATE, got %s", p)
	}
}

// THE key correctness pin: once a construction pipeline exists, GATE is STICKY even though contract
// income has fallen back under the bar (haulers repurposed to construction). Without this, derivePhase
// regresses GATE→INCOME and re-buys haulers — a thrash loop.
func TestBootstrap_DerivePhase_GateStickyOnceConstructionStarted(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd(), nil)
	obs := Observation{
		MarketsTotal: 10, MarketsCovered: 10,
		IncomePerHour:       500, // well under the 10000 bar — haulers repurposed
		ConstructionStarted: true,
	}
	if p := derivePhase(obs, cfg); p != PhaseGate {
		t.Fatalf("construction started should keep GATE despite low income, got %s (would thrash to INCOME)", p)
	}
}

// A 100%-delivered gate derives COMPLETE — terminal and monotone, so a restart post-completion resumes COMPLETE.
func TestBootstrap_DerivePhase_CompleteWhenConstructionComplete(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd(), nil)
	obs := Observation{
		MarketsTotal: 10, MarketsCovered: 10,
		IncomePerHour:        200, // income is irrelevant once the gate is built
		ConstructionStarted:  true,
		ConstructionComplete: true,
	}
	if p := derivePhase(obs, cfg); p != PhaseComplete {
		t.Fatalf("completed construction should derive COMPLETE, got %s", p)
	}
}

// PARALLEL MODEL (sp-t39j): coverage NO LONGER gates the economic phase. The construction/income
// signals are evaluated regardless of scan coverage — a built gate is COMPLETE (terminal, monotone)
// even on a cold, uncovered world; a cold world with NO economic signal yet stays DATA (still scanning),
// and the contract workstream runs in parallel with that DATA label (see the tick dispatch). This
// replaces the old "coverage-gate-beats-everything" serial rule.
func TestBootstrap_DerivePhase_EconomicSignalsIgnoreCoverage(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd(), nil)
	// A completed gate is COMPLETE even uncovered (terminal + monotone — a built gate stays built).
	if p := derivePhase(Observation{MarketsTotal: 0, ConstructionStarted: true, ConstructionComplete: true}, cfg); p != PhaseComplete {
		t.Fatalf("completed construction should derive COMPLETE regardless of coverage, got %s", p)
	}
	// A started (not complete) pipeline is GATE even uncovered (sticky).
	if p := derivePhase(Observation{MarketsTotal: 0, ConstructionStarted: true}, cfg); p != PhaseGate {
		t.Fatalf("started construction should derive GATE regardless of coverage, got %s", p)
	}
	// A cold world with NO economic signal is DATA (still scanning) — contracts run in parallel there.
	if p := derivePhase(Observation{MarketsTotal: 0}, cfg); p != PhaseData {
		t.Fatalf("cold world with no economic signal should derive DATA, got %s", p)
	}
}

// --- planGateWorkers: deterministic repurpose-first → top-up sizing ---

// Repurpose-first: when GATE begins, all contract haulers beyond min_contract_earners are released to
// the executor as the seed workforce — BEFORE the pipeline reveals its shape (chains still 0), so no buy.
func TestBootstrap_PlanGateWorkers_RepurposesSurplusFirst(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd(), nil) // min_contract_earners 1, gate_worker_target 6
	obs := Observation{
		Haulers:            []HaulerSnapshot{{Symbol: "H1"}, {Symbol: "H2"}, {Symbol: "H3"}, {Symbol: "H4"}},
		GateMaterialChains: 0, // pipeline shape not yet known
	}
	plan := planGateWorkers(obs, cfg)
	if got := len(plan.ReleaseShips); got != 3 {
		t.Fatalf("expected 3 surplus haulers released (4 on contract − 1 kept), got %d (%v)", got, plan.ReleaseShips)
	}
	if plan.KeptOnContract != 1 {
		t.Fatalf("expected 1 hauler kept on contract, got %d", plan.KeptOnContract)
	}
	if plan.Buy != 0 {
		t.Fatalf("no buy before the pipeline reveals its chains, got buy=%d", plan.Buy)
	}
	// The kept earner (first) is NOT released; the surplus (H2..H4) is.
	for _, s := range plan.ReleaseShips {
		if s == "H1" {
			t.Fatalf("the kept contract earner H1 must not be released, got %v", plan.ReleaseShips)
		}
	}
}

// The keep guard holds: at exactly min_contract_earners on contract, nothing is released.
func TestBootstrap_PlanGateWorkers_KeepsMinContractEarners(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd(), nil) // keep 1
	obs := Observation{Haulers: []HaulerSnapshot{{Symbol: "H1"}}, GateMaterialChains: 3}
	plan := planGateWorkers(obs, cfg)
	if len(plan.ReleaseShips) != 0 {
		t.Fatalf("must keep min_contract_earners on contract, released %v", plan.ReleaseShips)
	}
}

// Top-up buy: the pipeline reveals 3 chains, no repurposable haulers cover it and no workers yet, so
// the staged delta buys ONE hull (never a blind buy-all).
func TestBootstrap_PlanGateWorkers_TopsUpWhenShort(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd(), nil) // target 6, delivery +1 ⇒ desired = min(3+1,6) = 4
	obs := Observation{
		Haulers:            []HaulerSnapshot{{Symbol: "H1"}}, // only the kept earner, nothing to repurpose
		GateWorkers:        0,
		GateMaterialChains: 3,
	}
	plan := planGateWorkers(obs, cfg)
	if plan.DesiredWorkers != 4 {
		t.Fatalf("desired = min(chains+delivery, target) = 4, got %d", plan.DesiredWorkers)
	}
	if plan.Buy != 1 {
		t.Fatalf("short pool should stage ONE buy, got %d", plan.Buy)
	}
}

// One buy per tick: even a large deficit stages exactly one buy (never a blind buy-all).
func TestBootstrap_PlanGateWorkers_StagesOneBuyPerTick(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd(), nil) // target 6
	obs := Observation{Haulers: []HaulerSnapshot{{Symbol: "H1"}}, GateWorkers: 0, GateMaterialChains: 5}
	plan := planGateWorkers(obs, cfg) // desired = min(5+1,6) = 6, pool 0 ⇒ deficit 6
	if plan.Buy != 1 {
		t.Fatalf("deficit of 6 must still stage exactly one buy, got %d", plan.Buy)
	}
}

// No buy when the repurposed seed + existing workers already cover the pipeline's shape.
func TestBootstrap_PlanGateWorkers_NoBuyWhenPoolCovers(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd(), nil) // target 6, desired = min(2+1,6) = 3
	obs := Observation{
		Haulers:            []HaulerSnapshot{{Symbol: "H1"}, {Symbol: "H2"}, {Symbol: "H3"}, {Symbol: "H4"}}, // release 3
		GateWorkers:        0,
		GateMaterialChains: 2,
	}
	plan := planGateWorkers(obs, cfg)
	if plan.DesiredWorkers != 3 {
		t.Fatalf("desired = 3, got %d", plan.DesiredWorkers)
	}
	if len(plan.ReleaseShips) != 3 {
		t.Fatalf("expected 3 released, got %d", len(plan.ReleaseShips))
	}
	if plan.Buy != 0 {
		t.Fatalf("pool after release (0 workers + 3 released) covers desired 3 — no buy, got %d", plan.Buy)
	}
}

// No buy when the executor already has enough workers (idempotency: a restart mid-GATE re-observes
// GateWorkers and never re-buys or re-overshoots).
func TestBootstrap_PlanGateWorkers_NoBuyWhenWorkersSuffice(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd(), nil)
	obs := Observation{Haulers: []HaulerSnapshot{{Symbol: "H1"}}, GateWorkers: 4, GateMaterialChains: 3}
	plan := planGateWorkers(obs, cfg) // desired = 4, have 4
	if plan.Buy != 0 {
		t.Fatalf("workers already cover desired — no buy, got %d", plan.Buy)
	}
}

// The worker target caps the desired count so a wide pipeline can't drive an unbounded ramp.
func TestBootstrap_PlanGateWorkers_CapsAtTarget(t *testing.T) {
	cmd := baseCmd()
	cmd.GateWorkerTarget = 4
	cfg := resolveBootstrapConfig(cmd, nil)
	obs := Observation{Haulers: []HaulerSnapshot{{Symbol: "H1"}}, GateWorkers: 0, GateMaterialChains: 10}
	plan := planGateWorkers(obs, cfg)
	if plan.DesiredWorkers != 4 {
		t.Fatalf("desired must cap at gate_worker_target=4, got %d", plan.DesiredWorkers)
	}
}

// --- GATE fakes (black-box: the reconciler is driven through its ports only) ---

type fakeConstruction struct {
	starts int
	sites  []string
	err    error
}

func (f *fakeConstruction) Start(ctx context.Context, playerID int, site string) error {
	f.starts++
	f.sites = append(f.sites, site)
	return f.err
}

type fakeManufacturing struct {
	ensures   int
	bounces   int
	ensureErr error
	bounceErr error
}

func (f *fakeManufacturing) EnsureRunning(ctx context.Context, playerID int) error {
	f.ensures++
	return f.ensureErr
}

func (f *fakeManufacturing) BounceForAdoption(ctx context.Context, playerID int) error {
	f.bounces++
	return f.bounceErr
}

type fakeRepurposer struct {
	calls int
	ships []string
	err   error
}

func (f *fakeRepurposer) RepurposeToConstruction(ctx context.Context, playerID int, shipSymbol string) error {
	f.calls++
	f.ships = append(f.ships, shipSymbol)
	return f.err
}

type fakeGateAcquirer struct {
	price     int64
	yard      string
	readable  bool
	priceErr  error
	buyErr    error
	buys      int
	priceChks int
}

func (f *fakeGateAcquirer) PriceCheck(ctx context.Context, playerID int, shipType string) (int64, string, bool, error) {
	f.priceChks++
	return f.price, f.yard, f.readable, f.priceErr
}

func (f *fakeGateAcquirer) BuyForConstruction(ctx context.Context, playerID int, shipType, yard string) (BuyResult, error) {
	if f.buyErr != nil {
		return BuyResult{}, f.buyErr
	}
	f.buys++
	return BuyResult{ShipSymbol: "GATEWORKER-NEW", Price: f.price}, nil
}

type fakeHandoff struct {
	autosizer int
	standing  int
	autoErr   error
	standErr  error
}

func (f *fakeHandoff) LaunchAutosizer(ctx context.Context, playerID int, agentSymbol string) error {
	f.autosizer++
	return f.autoErr
}

func (f *fakeHandoff) LaunchStandingCoordinators(ctx context.Context, playerID int, agentSymbol string) error {
	f.standing++
	return f.standErr
}

// gateHandler wires a handler with the given GATE collaborators plus the always-needed refresher/observer
// (a fixed observation per tick — GATE guards are all observation-driven, so one snapshot exercises them).
func gateHandler(obs Observation, con ConstructionManager, mfg ManufacturingController, rep WorkerRepurposer, acq GateWorkerAcquirer, ho HandoffLauncher) *RunBootstrapCoordinatorHandler {
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(&fakeObserver{obs: obs})
	if con != nil {
		h.SetConstructionManager(con)
	}
	if mfg != nil {
		h.SetManufacturingController(mfg)
	}
	if rep != nil {
		h.SetWorkerRepurposer(rep)
	}
	if acq != nil {
		h.SetGateWorkerAcquirer(acq)
	}
	if ho != nil {
		h.SetHandoffLauncher(ho)
	}
	return h
}

// gateObs is a covered, GATE-phase observation (construction started + adopted by default, income low
// because haulers are repurposed). Tests tweak individual fields for the guard under test.
func gateObs() Observation {
	return Observation{
		HomeSystem: "X1-HQ", MarketsTotal: 10, MarketsCovered: 10, Treasury: 1000000,
		IncomePerHour:        500, // repurposed — under the bar; GATE stays sticky on ConstructionStarted
		GateSite:             "X1-HQ-GATE",
		ConstructionStarted:  true,
		ManufacturingRunning: true,
		ManufacturingAdopted: true,
		HasIdlePurchaser:     true,
		Readable:             true,
	}
}

// --- actGate ---

// No gate site discovered ⇒ GATE is BLOCKED and never starts construction on an unknown target.
func TestBootstrap_Gate_NoSite_Blocks(t *testing.T) {
	obs := gateObs()
	obs.GateSite = ""
	obs.ConstructionStarted = false
	obs.IncomePerHour = 12000 // over the bar so the phase is GATE even without a pipeline
	con := &fakeConstruction{}
	h := gateHandler(obs, con, &fakeManufacturing{}, &fakeRepurposer{}, &fakeGateAcquirer{}, &fakeHandoff{})
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if res.Phase != PhaseGate {
		t.Fatalf("expected GATE phase, got %s", res.Phase)
	}
	if con.starts != 0 {
		t.Fatalf("must not start construction without a site, got %d starts", con.starts)
	}
	if res.Blocker != "no_gate_site" {
		t.Fatalf("expected blocker no_gate_site, got %q", res.Blocker)
	}
}

// Entering GATE with no pipeline yet ⇒ start construction, and DON'T ensure/bounce this tick (the
// observation still reads !started; adoption waits for the pipeline to be real next tick).
func TestBootstrap_Gate_StartsConstructionOnce_NoAdoptSameTick(t *testing.T) {
	obs := gateObs()
	obs.ConstructionStarted = false
	obs.IncomePerHour = 12000 // GATE entry via the income bar
	con := &fakeConstruction{}
	mfg := &fakeManufacturing{}
	h := gateHandler(obs, con, mfg, &fakeRepurposer{}, &fakeGateAcquirer{}, &fakeHandoff{})
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if con.starts != 1 || con.sites[0] != "X1-HQ-GATE" {
		t.Fatalf("expected one construction start on X1-HQ-GATE, got starts=%d sites=%v", con.starts, con.sites)
	}
	if mfg.ensures != 0 || mfg.bounces != 0 {
		t.Fatalf("must not ensure/bounce the executor on the pipeline-creating tick, got ensures=%d bounces=%d", mfg.ensures, mfg.bounces)
	}
	if !res.ConstructionStartRan {
		t.Fatalf("expected ConstructionStartRan=true")
	}
}

// Construction already started (idempotency) ⇒ never a second start.
func TestBootstrap_Gate_DoesNotRestartConstruction(t *testing.T) {
	con := &fakeConstruction{}
	h := gateHandler(gateObs(), con, &fakeManufacturing{}, &fakeRepurposer{}, &fakeGateAcquirer{}, &fakeHandoff{})
	h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if con.starts != 0 {
		t.Fatalf("must not re-start an already-started pipeline, got %d starts", con.starts)
	}
}

// Pipeline exists but the executor is DOWN ⇒ ensure it running (a fresh start adopts), NOT a bounce.
func TestBootstrap_Gate_EnsuresExecutorWhenDown(t *testing.T) {
	obs := gateObs()
	obs.ManufacturingRunning = false
	obs.ManufacturingAdopted = false
	mfg := &fakeManufacturing{}
	h := gateHandler(obs, &fakeConstruction{}, mfg, &fakeRepurposer{}, &fakeGateAcquirer{}, &fakeHandoff{})
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if mfg.ensures != 1 {
		t.Fatalf("expected 1 EnsureRunning, got %d", mfg.ensures)
	}
	if mfg.bounces != 0 {
		t.Fatalf("must not bounce a down executor (fresh start adopts), got %d bounces", mfg.bounces)
	}
	if !res.MfgEnsured {
		t.Fatalf("expected MfgEnsured=true")
	}
}

// Pipeline exists, executor UP but has NOT adopted it ⇒ the L57 bounce (restart to adopt), NOT ensure.
func TestBootstrap_Gate_BouncesExecutorForAdoption(t *testing.T) {
	obs := gateObs()
	obs.ManufacturingRunning = true
	obs.ManufacturingAdopted = false
	mfg := &fakeManufacturing{}
	h := gateHandler(obs, &fakeConstruction{}, mfg, &fakeRepurposer{}, &fakeGateAcquirer{}, &fakeHandoff{})
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if mfg.bounces != 1 {
		t.Fatalf("expected 1 BounceForAdoption (L57), got %d", mfg.bounces)
	}
	if mfg.ensures != 0 {
		t.Fatalf("must not ensure a running executor, got %d ensures", mfg.ensures)
	}
	if !res.MfgBounced {
		t.Fatalf("expected MfgBounced=true")
	}
}

// Executor up AND adopted ⇒ neither ensure nor bounce (idempotent settled adoption).
func TestBootstrap_Gate_NoBounceWhenAdopted(t *testing.T) {
	mfg := &fakeManufacturing{}
	h := gateHandler(gateObs(), &fakeConstruction{}, mfg, &fakeRepurposer{}, &fakeGateAcquirer{}, &fakeHandoff{})
	h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if mfg.ensures != 0 || mfg.bounces != 0 {
		t.Fatalf("a running+adopted executor needs no action, got ensures=%d bounces=%d", mfg.ensures, mfg.bounces)
	}
}

// Repurpose-first: surplus contract haulers beyond min_contract_earners are released to the executor.
func TestBootstrap_Gate_RepurposesSurplusHaulers(t *testing.T) {
	obs := gateObs()
	obs.Haulers = []HaulerSnapshot{{Symbol: "H1"}, {Symbol: "H2"}, {Symbol: "H3"}}
	rep := &fakeRepurposer{}
	h := gateHandler(obs, &fakeConstruction{}, &fakeManufacturing{}, rep, &fakeGateAcquirer{}, &fakeHandoff{})
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if rep.calls != 2 { // keep 1, release 2
		t.Fatalf("expected 2 haulers repurposed (3 − 1 kept), got %d (%v)", rep.calls, rep.ships)
	}
	if res.WorkersReleased != 2 {
		t.Fatalf("expected WorkersReleased=2, got %d", res.WorkersReleased)
	}
}

// Top-up buy: the pipeline reveals chains the repurposed pool + workers don't cover ⇒ ONE staged buy.
func TestBootstrap_Gate_BuysTopUpWorkerWhenShort(t *testing.T) {
	obs := gateObs()
	obs.Haulers = []HaulerSnapshot{{Symbol: "H1"}} // only the kept earner; nothing to repurpose
	obs.GateWorkers = 0
	obs.GateMaterialChains = 3 // desired = min(3+1,6) = 4
	acq := &fakeGateAcquirer{price: 200000, yard: "Y1", readable: true}
	h := gateHandler(obs, &fakeConstruction{}, &fakeManufacturing{}, &fakeRepurposer{}, acq, &fakeHandoff{})
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if acq.buys != 1 {
		t.Fatalf("expected one staged gate-worker buy, got %d", acq.buys)
	}
	if res.GateWorkersBought != 1 {
		t.Fatalf("expected GateWorkersBought=1, got %d", res.GateWorkersBought)
	}
}

// The capital gate blocks a needed worker buy that would exceed reserve_margin × treasury.
func TestBootstrap_Gate_CapitalGateBlocksWorkerBuy(t *testing.T) {
	obs := gateObs()
	obs.Haulers = []HaulerSnapshot{{Symbol: "H1"}}
	obs.GateWorkers = 0
	obs.GateMaterialChains = 3
	obs.Treasury = 100000 // cap = 50k; price 200k unaffordable
	acq := &fakeGateAcquirer{price: 200000, yard: "Y1", readable: true}
	h := gateHandler(obs, &fakeConstruction{}, &fakeManufacturing{}, &fakeRepurposer{}, acq, &fakeHandoff{})
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if acq.buys != 0 {
		t.Fatalf("capital gate must block the buy, got %d buys", acq.buys)
	}
	if res.Blocker != "capital_gate" {
		t.Fatalf("expected blocker capital_gate, got %q", res.Blocker)
	}
}

// A settled GATE tick (started, adopted, workers suffice, no surplus) takes NO action — quiet reconcile.
func TestBootstrap_Gate_SettledTickIsQuiet(t *testing.T) {
	obs := gateObs()
	obs.Haulers = []HaulerSnapshot{{Symbol: "H1"}} // exactly the kept earner
	obs.GateWorkers = 4
	obs.GateMaterialChains = 3 // desired 4, have 4
	con := &fakeConstruction{}
	mfg := &fakeManufacturing{}
	rep := &fakeRepurposer{}
	acq := &fakeGateAcquirer{price: 1, yard: "Y", readable: true}
	h := gateHandler(obs, con, mfg, rep, acq, &fakeHandoff{})
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if con.starts+mfg.ensures+mfg.bounces+rep.calls+acq.buys != 0 {
		t.Fatalf("settled GATE tick must be quiet, got starts=%d ensures=%d bounces=%d repurpose=%d buys=%d",
			con.starts, mfg.ensures, mfg.bounces, rep.calls, acq.buys)
	}
	if res.Blocker != "" {
		t.Fatalf("settled tick should have no blocker, got %q", res.Blocker)
	}
}

// --- actComplete (hand-off) ---

// On COMPLETE with the autosizer not yet running, launch the autosizer + standing coordinators once and
// signal the loop to exit (Done).
func TestBootstrap_Complete_LaunchesHandoffAndExits(t *testing.T) {
	obs := gateObs()
	obs.ConstructionComplete = true // derives COMPLETE
	obs.AutosizerRunning = false
	ho := &fakeHandoff{}
	h := gateHandler(obs, &fakeConstruction{}, &fakeManufacturing{}, &fakeRepurposer{}, &fakeGateAcquirer{}, ho)
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if res.Phase != PhaseComplete {
		t.Fatalf("expected COMPLETE phase, got %s", res.Phase)
	}
	if ho.autosizer != 1 || ho.standing != 1 {
		t.Fatalf("expected one autosizer + one standing launch, got autosizer=%d standing=%d", ho.autosizer, ho.standing)
	}
	if !res.HandoffLaunched || !res.Done {
		t.Fatalf("expected HandoffLaunched=true and Done=true, got %+v", res)
	}
}

// Restart post-COMPLETE: the autosizer is already running ⇒ never re-launch, just exit (terminal idempotency).
func TestBootstrap_Complete_NoRelaunchWhenAutosizerRunning(t *testing.T) {
	obs := gateObs()
	obs.ConstructionComplete = true
	obs.AutosizerRunning = true
	ho := &fakeHandoff{}
	h := gateHandler(obs, &fakeConstruction{}, &fakeManufacturing{}, &fakeRepurposer{}, &fakeGateAcquirer{}, ho)
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if ho.autosizer != 0 || ho.standing != 0 {
		t.Fatalf("must not relaunch the hand-off when the autosizer already runs, got autosizer=%d standing=%d", ho.autosizer, ho.standing)
	}
	if !res.Done {
		t.Fatalf("post-COMPLETE with autosizer running should still exit (Done=true), got Done=%v", res.Done)
	}
}

// A hand-off whose autosizer launch FAILS does not exit — it holds and retries next tick (never leaves
// the fleet un-handed-off).
func TestBootstrap_Complete_HoldsWhenHandoffFails(t *testing.T) {
	obs := gateObs()
	obs.ConstructionComplete = true
	obs.AutosizerRunning = false
	ho := &fakeHandoff{autoErr: errors.New("boom")}
	h := gateHandler(obs, &fakeConstruction{}, &fakeManufacturing{}, &fakeRepurposer{}, &fakeGateAcquirer{}, ho)
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if res.Done {
		t.Fatalf("a failed hand-off must NOT exit (Done must stay false), got Done=true")
	}
	if ho.standing != 0 {
		t.Fatalf("standing coordinators must not launch after the autosizer launch failed, got %d", ho.standing)
	}
	if res.Blocker == "" {
		t.Fatalf("expected a blocker on the failed hand-off")
	}
}
