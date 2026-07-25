package commands

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// --- derivePhase: GATE stickiness + EXPANSION ---

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

// A 100%-delivered gate derives EXPANSION (sp-feiy7, Admiral 2026-07-24: the gate is built, steady-state
// growth begins — THE phase where probe-buying starts). Terminal and monotone, so a restart
// post-completion resumes EXPANSION.
func TestBootstrap_DerivePhase_ExpansionWhenConstructionComplete(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd(), nil)
	obs := Observation{
		MarketsTotal: 10, MarketsCovered: 10,
		IncomePerHour:        200, // income is irrelevant once the gate is built
		ConstructionStarted:  true,
		ConstructionComplete: true,
	}
	if p := derivePhase(obs, cfg); p != PhaseExpansion {
		t.Fatalf("completed construction should derive EXPANSION, got %s", p)
	}
}

// STICKINESS (sp-feiy7): a built gate dominates EVERY pre-100%% signal — even an observation whose other
// fields all point at the coldest possible DATA/INCOME world (zero probes, zero coverage, −inf sustained
// income, no scaler target, empty treasury) derives EXPANSION on ConstructionComplete alone. This is the
// same world-signal stickiness GATE rides (ConstructionStarted): no post-gate income dip, fleet churn, or
// restart-dropped in-memory window can pull the arc back to a buying phase and thrash (the GATE→INCOME
// re-buy lesson). A built gate stays built, so the derivation is monotone tick after tick.
func TestBootstrap_DerivePhase_ExpansionSticky_DominatesAllPreGateSignals(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd(), nil)
	obs := Observation{
		// Every non-construction signal at its coldest: this would derive DATA if the gate were unbuilt.
		ProbeCount: 0, MarketsTotal: 0, MarketsCovered: 0,
		IncomePerHour: 0, Treasury: 0, ContractScalerTarget: 0,
		ConstructionStarted:  false, // even the pipeline signal gone (post-completion the pipeline may be reaped)
		ConstructionComplete: true,
	}
	for tick := 0; tick < 3; tick++ {
		if p := derivePhase(obs, cfg); p != PhaseExpansion {
			t.Fatalf("tick %d: a built gate must derive EXPANSION regardless of every other signal, got %s (thrash)", tick, p)
		}
	}
}

// PARALLEL MODEL (sp-t39j): coverage NO LONGER gates the economic phase. The construction/income
// signals are evaluated regardless of scan coverage — a built gate is EXPANSION (terminal, monotone)
// even on a cold, uncovered world; a cold world with NO economic signal yet stays DATA (still scanning),
// and the contract workstream runs in parallel with that DATA label (see the tick dispatch). This
// replaces the old "coverage-gate-beats-everything" serial rule.
func TestBootstrap_DerivePhase_EconomicSignalsIgnoreCoverage(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd(), nil)
	// A completed gate is EXPANSION even uncovered (terminal + monotone — a built gate stays built).
	if p := derivePhase(Observation{MarketsTotal: 0, ConstructionStarted: true, ConstructionComplete: true}, cfg); p != PhaseExpansion {
		t.Fatalf("completed construction should derive EXPANSION regardless of coverage, got %s", p)
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

// --- planGateWorkers: the contract fleet is EXCLUSIVE (keep-all) → buy-only workforce sizing ---

// sp-cdxy2: the contract fleet is EXCLUSIVE (sp-9le3x) — GATE NEVER repurposes it to construction. For ANY
// contract delivery fleet size, planGateWorkers releases NOTHING and keeps the whole fleet on contracts, so
// the scaler's ContractHullCount never drops and the observed buy→repurpose→buy churn cannot start. The
// workforce is sourced by BUYING instead, at every fleet size — the gate never reaches for a contract hull.
func TestBootstrap_PlanGateWorkers_KeepsWholeContractFleet_ReleasesNone(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd(), nil)
	for _, n := range []int{0, 1, 2, 4, 10} {
		obs := Observation{Haulers: nHaulers(n), GateWorkers: 0}
		plan := planGateWorkers(obs, cfg)
		if len(plan.ReleaseShips) != 0 {
			t.Fatalf("n=%d: the exclusive contract fleet is never repurposed — expected 0 released, got %d (%v)", n, len(plan.ReleaseShips), plan.ReleaseShips)
		}
		if plan.KeptOnContract != n {
			t.Fatalf("n=%d: the whole contract fleet stays on contracts — expected KeptOnContract=%d, got %d", n, n, plan.KeptOnContract)
		}
		if plan.Buy != 1 {
			t.Fatalf("n=%d: the gate buys its own worker rather than drawing on the contract fleet, got buy=%d", n, plan.Buy)
		}
	}
}

// Ramp buy: no repurposable haulers and no workers yet, so the staged delta buys ONE hull (never a
// blind buy-all) toward the workforce target.
func TestBootstrap_PlanGateWorkers_TopsUpWhenShort(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd(), nil) // gate_worker_target 4
	obs := Observation{
		Haulers:            []HaulerSnapshot{{Symbol: "H1"}}, // only the kept earner, nothing to repurpose
		GateWorkers:        0,
		GateMaterialChains: 3,
	}
	plan := planGateWorkers(obs, cfg)
	if plan.DesiredWorkers != 4 {
		t.Fatalf("desired = gate_worker_target = 4, got %d", plan.DesiredWorkers)
	}
	if plan.Buy != 1 {
		t.Fatalf("short pool should stage ONE buy, got %d", plan.Buy)
	}
}

// One buy per tick: even a large deficit stages exactly one buy (never a blind buy-all).
func TestBootstrap_PlanGateWorkers_StagesOneBuyPerTick(t *testing.T) {
	cmd := baseCmd()
	cmd.GateWorkerTarget = 6 // a tuned-up workforce ⇒ deficit 6 from an empty pool
	cfg := resolveBootstrapConfig(cmd, nil)
	obs := Observation{Haulers: []HaulerSnapshot{{Symbol: "H1"}}, GateWorkers: 0}
	plan := planGateWorkers(obs, cfg)
	if plan.Buy != 1 {
		t.Fatalf("deficit of 6 must still stage exactly one buy, got %d", plan.Buy)
	}
}

// No buy when the executor already has enough workers (idempotency: a restart mid-GATE re-observes
// GateWorkers and never re-buys or re-overshoots). The pool is the executor's OWN workers alone now — the
// contract fleet is never released into it (sp-cdxy2), so a large contract fleet next to it never masks a
// genuine worker deficit.
func TestBootstrap_PlanGateWorkers_NoBuyWhenWorkersSuffice(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd(), nil)
	obs := Observation{Haulers: nHaulers(8), GateWorkers: 4, GateMaterialChains: 3}
	plan := planGateWorkers(obs, cfg) // desired = 4, have 4 workers (the 8 contract haulers are NOT the pool)
	if plan.Buy != 0 {
		t.Fatalf("workers already cover desired — no buy, got %d", plan.Buy)
	}
	if len(plan.ReleaseShips) != 0 {
		t.Fatalf("the 8 contract haulers are exclusive — none released, got %v", plan.ReleaseShips)
	}
}

// An explicitly-tuned gate_worker_target overrides the default and is the whole sizing decision.
func TestBootstrap_PlanGateWorkers_HonoursTunedTarget(t *testing.T) {
	cmd := baseCmd()
	cmd.GateWorkerTarget = 7
	cfg := resolveBootstrapConfig(cmd, nil)
	obs := Observation{Haulers: []HaulerSnapshot{{Symbol: "H1"}}, GateWorkers: 0}
	plan := planGateWorkers(obs, cfg)
	if plan.DesiredWorkers != 7 {
		t.Fatalf("desired must follow the tuned gate_worker_target=7, got %d", plan.DesiredWorkers)
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
	autosizer      int
	standing       int
	contractScaler int
	tradeCoord     int // sp-192k4: LaunchTradeFleetCoordinator calls
	autoErr        error
	standErr       error
	scalerErr      error
	tradeErr       error
}

func (f *fakeHandoff) LaunchAutosizer(ctx context.Context, playerID int, agentSymbol string) error {
	f.autosizer++
	return f.autoErr
}

func (f *fakeHandoff) LaunchStandingCoordinators(ctx context.Context, playerID int, agentSymbol string) error {
	f.standing++
	return f.standErr
}

func (f *fakeHandoff) LaunchContractScaler(ctx context.Context, playerID int, agentSymbol string) error {
	f.contractScaler++
	return f.scalerErr
}

func (f *fakeHandoff) LaunchTradeFleetCoordinator(ctx context.Context, playerID int, agentSymbol string) error {
	f.tradeCoord++
	return f.tradeErr
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

// primeGateIncomeWindow pre-fills a container's GATE-entry income smoother so the NEXT reconcile tick sees a
// SUSTAINED $/hr over gate_income_bar — the (unconditionally-on) scaled gate then enters GATE on a scaled op
// without waiting the full rolling window to fill. Pair with Haulers ≥ gate_min_haulers on the observation.
func primeGateIncomeWindow(h *RunBootstrapCoordinatorHandler, containerID string, perHour float64) {
	w := h.incomeWindowFor(containerID)
	for i := 0; i < gateIncomeWindowTicks; i++ {
		w.sustained(perHour)
	}
}

// --- actGate ---

// No gate site discovered ⇒ GATE is BLOCKED and never starts construction on an unknown target. Reaches
// GATE via the sticky ConstructionStarted latch (gateObs's default), so the site guard is exercised directly.
func TestBootstrap_Gate_NoSite_Blocks(t *testing.T) {
	obs := gateObs() // ConstructionStarted=true → sticky GATE
	obs.GateSite = ""
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
// observation still reads !started; adoption waits for the pipeline to be real next tick). Enters GATE via
// the (unconditionally-on) scaled gate: 2 haulers + a primed sustained-income window, ConstructionStarted=false.
func TestBootstrap_Gate_StartsConstructionOnce_NoAdoptSameTick(t *testing.T) {
	obs := gateObs()
	obs.ConstructionStarted = false
	obs.Haulers = []HaulerSnapshot{{Symbol: "H1"}, {Symbol: "H2"}} // full fleet = 2
	obs.ContractScalerTarget = 2                                   // == full fleet ⇒ scaler target reached
	obs.IncomePerHour = 60000                                      // ≥ gate_income_bar 50000 (sustained); gateObs treasury clears the surplus floor
	con := &fakeConstruction{}
	mfg := &fakeManufacturing{}
	h := gateHandler(obs, con, mfg, &fakeRepurposer{}, &fakeGateAcquirer{}, &fakeHandoff{})
	primeGateIncomeWindow(h, baseCmd().ContainerID, 60000) // full window ⇒ the scaled gate enters GATE this tick
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

// sp-cdxy2 (the churn fix, through the driving port): a GATE tick with a full contract delivery fleet NEVER
// repurposes a contract hauler to construction — the repurposer spy records ZERO calls — so no
// "contract"→"manufacturing" re-tag drops the scaler's ContractHullCount and triggers the observed
// buy→repurpose→buy churn. The gate workforce is sourced by BUYING the delta instead, proving the fix does
// not merely disable worker sizing.
func TestBootstrap_Gate_NeverRepurposesContractFleet(t *testing.T) {
	obs := gateObs()
	obs.Haulers = nHaulers(10) // a fully-ramped exclusive contract fleet
	obs.GateWorkers = 0
	// desired = gate_worker_target (4) > 0 workers ⇒ the buy path sizes the workforce
	rep := &fakeRepurposer{}
	acq := &fakeGateAcquirer{price: 200000, yard: "Y1", readable: true}
	h := gateHandler(obs, &fakeConstruction{}, &fakeManufacturing{}, rep, acq, &fakeHandoff{})
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if rep.calls != 0 {
		t.Fatalf("the exclusive contract fleet must NEVER be repurposed in GATE, got %d repurpose call(s) (%v)", rep.calls, rep.ships)
	}
	if res.WorkersReleased != 0 {
		t.Fatalf("no contract hauler is released to construction, got WorkersReleased=%d", res.WorkersReleased)
	}
	if acq.buys != 1 {
		t.Fatalf("the gate workforce is sourced by BUYING the delta (not cannibalizing contracts), expected 1 buy, got %d", acq.buys)
	}
}

// Ramp buy: the executor's workers fall short of the workforce target ⇒ ONE staged buy.
func TestBootstrap_Gate_BuysTopUpWorkerWhenShort(t *testing.T) {
	obs := gateObs()
	obs.Haulers = []HaulerSnapshot{{Symbol: "H1"}} // only the kept earner; nothing to repurpose
	obs.GateWorkers = 0
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

// The capital gate blocks a clearly-unaffordable worker buy (sp-bpdf: the ABSOLUTE working-capital
// floor, not the old reserve_margin×treasury cap — here the price exceeds the whole treasury so the
// cushion is negative, well below the floor).
func TestBootstrap_Gate_CapitalGateBlocksWorkerBuy(t *testing.T) {
	obs := gateObs()
	obs.Haulers = []HaulerSnapshot{{Symbol: "H1"}}
	obs.GateWorkers = 0
	obs.GateMaterialChains = 3
	obs.Treasury = 100000 // cushion = 100k−200k = −100k, far below the 50k floor
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

// --- sp-bpdf: the gate-worker buy (bootstrap's sole GATE-phase construction spend) now gates on the
// SAME absolute contract working-capital floor as the hauler buy (treasury−price ≥ floor), NOT the old
// proportional reserve_margin×treasury cap. Gate construction can no longer drive the treasury below the
// working-capital line, and a worker is bought as soon as the buy still clears the floor. ---

// gateFloorObs is a GATE observation shaped so planGateWorkers calls for exactly ONE staged worker buy
// (3 material chains ⇒ desired 4, no existing workers, only the kept earner so nothing to repurpose):
// the capital gate is the only thing between it and the buy, so treasury/price isolate the floor.
func gateFloorObs(treasury int64) Observation {
	obs := gateObs()
	obs.Treasury = treasury
	obs.Haulers = []HaulerSnapshot{{Symbol: "H1"}} // exactly min_contract_earners → no surplus to repurpose
	obs.GateWorkers = 0
	// desired = gate_worker_target (4) > pool(0) ⇒ plan.Buy = 1
	return obs
}

// BUYS where the OLD proportional cap would have BLOCKED: the win of the floor. price=300000 with
// treasury = price+floor+1 leaves a cushion of exactly floor+1 (clears), yet the old cap
// (0.5×350001 = 175000 < price) would have refused the worker — the same premature-hold the floor removes.
func TestBootstrap_Gate_WorkingCapitalFloor_BuysWhereProportionalCapWouldBlock(t *testing.T) {
	const price = int64(300000)
	obs := gateFloorObs(price + defaultContractWorkingCapitalFloor + 1)
	acq := &fakeGateAcquirer{price: price, yard: "Y1", readable: true}
	h := gateHandler(obs, &fakeConstruction{}, &fakeManufacturing{}, &fakeRepurposer{}, acq, &fakeHandoff{})
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if acq.buys != 1 || res.GateWorkersBought != 1 {
		t.Fatalf("cushion clears the floor (treasury=%d price=%d floor=%d): must buy 1 worker, got buys=%d bought=%d blocker=%q",
			obs.Treasury, price, defaultContractWorkingCapitalFloor, acq.buys, res.GateWorkersBought, res.Blocker)
	}
}

// BLOCKS where the OLD proportional cap would have ALLOWED: the two-buyer safety of the floor.
// treasury=90000, price=44000 → cushion 46000 is below the 50k floor (blocked), but the old cap
// (0.5×90000 = 45000 ≥ price) would have permitted the buy and drained working capital below the line.
func TestBootstrap_Gate_WorkingCapitalFloor_BlocksWhereProportionalCapWouldAllow(t *testing.T) {
	obs := gateFloorObs(90000)
	acq := &fakeGateAcquirer{price: 44000, yard: "Y1", readable: true}
	h := gateHandler(obs, &fakeConstruction{}, &fakeManufacturing{}, &fakeRepurposer{}, acq, &fakeHandoff{})
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if acq.buys != 0 {
		t.Fatalf("cushion (90000−44000=46000) is below the 50k floor: must NOT buy, got %d buys", acq.buys)
	}
	if res.Blocker != "capital_gate" {
		t.Fatalf("expected capital_gate blocker on a short cushion, got %q", res.Blocker)
	}
}

// The floor is a strict lower bound: a cushion one credit short blocks, RULINGS #4 fail-closed, and the
// decision line emits the floor guardrail arithmetic (floor/cushion, not reserve_margin/cap).
func TestBootstrap_Gate_WorkingCapitalFloor_BlocksAtBoundaryAndLogsFloor(t *testing.T) {
	const price = int64(300000)
	obs := gateFloorObs(price + defaultContractWorkingCapitalFloor - 1) // cushion = floor − 1
	acq := &fakeGateAcquirer{price: price, yard: "Y1", readable: true}
	log := &capturingLogger{}
	h := gateHandler(obs, &fakeConstruction{}, &fakeManufacturing{}, &fakeRepurposer{}, acq, &fakeHandoff{})
	res, _ := h.reconcileOnce(ctxWithLogger(log), baseCmd())
	if acq.buys != 0 || res.Blocker != "capital_gate" {
		t.Fatalf("cushion 1 below the floor (treasury=%d price=%d floor=%d): must block, got buys=%d blocker=%q",
			obs.Treasury, price, defaultContractWorkingCapitalFloor, acq.buys, res.Blocker)
	}
	dl, ok := log.find("bootstrap_gate_worker_buy_decision")
	if !ok {
		t.Fatalf("expected a gate-worker buy-decision line with the floor guardrail arithmetic")
	}
	for _, want := range []string{"price=300000", "floor=", "cushion="} {
		if !strings.Contains(dl.msg, want) {
			t.Fatalf("gate-worker decision line missing %q: %s", want, dl.msg)
		}
	}
}

// The bootstrap contract-op cushion and the immutable anti-stall bound are now DISTINCT (Admiral RULINGS
// #5 2026-07-18 amendment split): the gate-worker buy uses the SAME contract cushion as the hauler buy —
// the 150k contract-operating floor — which is RAISED above (never below) the immutable 50k anti-stall
// bound, so bootstrap spend stays stricter than the line the autosizer/reconciler reserve. This pins the
// split so a future edit cannot silently re-unify them or drop the contract cushion below the bound.
func TestBootstrap_ContractCushion_IsDistinctFromAndAboveTheImmutableBound(t *testing.T) {
	if defaultContractWorkingCapitalFloor != 150_000 {
		t.Fatalf("bootstrap contract working-capital cushion (%d) must be the 150k contract-operating floor",
			defaultContractWorkingCapitalFloor)
	}
	if common.ImmutableReserveFloor != 50_000 {
		t.Fatalf("the immutable anti-stall bound (%d) must remain 50k — unchanged by the cushion raise",
			common.ImmutableReserveFloor)
	}
	if defaultContractWorkingCapitalFloor < common.ImmutableReserveFloor {
		t.Fatalf("the contract cushion (%d) must never drop below the immutable anti-stall bound (%d)",
			defaultContractWorkingCapitalFloor, common.ImmutableReserveFloor)
	}
}

// A settled GATE tick (started, adopted, workers suffice, no surplus) takes NO action — quiet reconcile.
func TestBootstrap_Gate_SettledTickIsQuiet(t *testing.T) {
	obs := gateObs()
	obs.Haulers = []HaulerSnapshot{{Symbol: "H1"}} // exactly the kept earner
	obs.GateWorkers = 4
	obs.GateWorkers = 4
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

// --- actExpansion (hand-off) ---

// On EXPANSION with the autosizer not yet running, launch the autosizer + standing coordinators once and
// signal the loop to exit (Done) — the gate is built, the standing economy owns steady-state growth.
func TestBootstrap_Expansion_LaunchesHandoffAndExits(t *testing.T) {
	obs := gateObs()
	obs.ConstructionComplete = true // derives EXPANSION
	obs.AutosizerRunning = false
	ho := &fakeHandoff{}
	h := gateHandler(obs, &fakeConstruction{}, &fakeManufacturing{}, &fakeRepurposer{}, &fakeGateAcquirer{}, ho)
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if res.Phase != PhaseExpansion {
		t.Fatalf("expected EXPANSION phase, got %s", res.Phase)
	}
	if ho.autosizer != 1 || ho.standing != 1 {
		t.Fatalf("expected one autosizer + one standing launch, got autosizer=%d standing=%d", ho.autosizer, ho.standing)
	}
	if !res.HandoffLaunched || !res.Done {
		t.Fatalf("expected HandoffLaunched=true and Done=true, got %+v", res)
	}
}

// A hand-off whose autosizer launch FAILS does not exit — it holds and retries next tick (never leaves
// the fleet un-handed-off).
func TestBootstrap_Expansion_HoldsWhenHandoffFails(t *testing.T) {
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

// STICKY across live ticks + full observability threading (sp-feiy7): a held EXPANSION (hand-off blocked,
// so the loop keeps ticking — the one state where bootstrap genuinely ticks repeatedly post-gate) stays
// EXPANSION every tick, the phase GAUGE records "EXPANSION" every tick, the heartbeat prints it, and the
// nextAction switch handles it (no "unhandled" fallthrough). Meanwhile NO pre-gate action fires — the
// probe acquirer and hauler machinery are never touched — so the phase can never thrash back into a
// buying phase (RULINGS #2).
func TestBootstrap_Expansion_StickyAcrossTicks_GaugeHeartbeatAndNoPreGateActions(t *testing.T) {
	obs := gateObs()
	obs.ConstructionComplete = true
	obs.AutosizerRunning = false
	ho := &fakeHandoff{autoErr: errors.New("launcher down")} // hold: Done stays false, ticks continue
	acq := &fakeAcquirer{price: 1, yard: "Y", readable: true}
	metrics := &fakeMetrics{}
	log := &capturingLogger{}
	h := gateHandler(obs, &fakeConstruction{}, &fakeManufacturing{}, &fakeRepurposer{}, &fakeGateAcquirer{}, ho)
	h.SetProbeAcquirer(acq)
	h.SetMetricsSink(metrics)

	for tick := 0; tick < 3; tick++ {
		res, err := h.reconcileOnce(ctxWithLogger(log), baseCmd())
		if err != nil {
			t.Fatalf("tick %d: reconcileOnce: %v", tick, err)
		}
		if res.Phase != PhaseExpansion {
			t.Fatalf("tick %d: EXPANSION must be sticky across ticks, got %s", tick, res.Phase)
		}
	}
	if len(metrics.phases) != 3 {
		t.Fatalf("the phase gauge must be recorded every tick, got %d records (%v)", len(metrics.phases), metrics.phases)
	}
	for i, p := range metrics.phases {
		if p != "EXPANSION" {
			t.Fatalf("gauge record %d: expected EXPANSION, got %q (%v)", i, p, metrics.phases)
		}
	}
	hb, ok := log.find("bootstrap_heartbeat")
	if !ok {
		t.Fatalf("expected a heartbeat line")
	}
	if !strings.Contains(hb.msg, "phase=EXPANSION") {
		t.Fatalf("heartbeat must print the EXPANSION phase, got: %s", hb.msg)
	}
	if strings.Contains(hb.msg, "unhandled") {
		t.Fatalf("nextAction must handle EXPANSION (no unhandled fallthrough), got: %s", hb.msg)
	}
	if acq.buys != 0 || acq.priceChks != 0 {
		t.Fatalf("EXPANSION must never run the DATA probe-buy machinery, got buys=%d priceChecks=%d", acq.buys, acq.priceChks)
	}
}

// --- sp-mxflh: release the gate's OWN surplus idle manufacturing hulls to the undedicated pool ---

// idleWorkers builds GateWorkerSnapshots (all idle) from symbols — the common over-provisioned-worker shape.
func idleWorkers(symbols ...string) []GateWorkerSnapshot {
	out := make([]GateWorkerSnapshot, 0, len(symbols))
	for _, s := range symbols {
		out = append(out, GateWorkerSnapshot{Symbol: s, Idle: true})
	}
	return out
}

// planGateWorkers releases the gate's OWN surplus IDLE manufacturing hulls (GateWorkers over the pipeline's
// revealed shape) to the undedicated pool — deterministic (lowest-symbol first), exactly (GateWorkers−desired)
// of them, NEVER a hull mid-task, and NEVER a contract hauler (obs.Haulers is untouched). It releases NOTHING
// (byte-identical) at/below the shape or before the shape is revealed. Distinct from the dormant ReleaseShips
// repurpose seam, which stays empty throughout.
func TestBootstrap_PlanGateWorkers_ReleasesIdleManufacturingSurplus(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd(), nil)

	cases := []struct {
		name string
		obs  Observation
		want []string
	}{
		{
			// desired = gate_worker_target (4); 6 workers ⇒ 2 surplus. Symbols deliberately shuffled to prove sorting.
			"over-provisioned → the 2 lowest-symbol idle surplus, deterministic",
			Observation{Haulers: nHaulers(10), GateMaterialChains: 3, GateWorkers: 6,
				GateWorkerHulls: idleWorkers("M5", "M4", "M3", "M2", "M1", "M6")},
			[]string{"M1", "M2"},
		},
		{
			"mid-task hulls are never selected — the surplus is drawn from the idle ones only",
			Observation{GateMaterialChains: 3, GateWorkers: 6, GateWorkerHulls: []GateWorkerSnapshot{
				{Symbol: "M1", Idle: false}, {Symbol: "M2", Idle: false}, // mid-construction — excluded
				{Symbol: "M3", Idle: true}, {Symbol: "M4", Idle: true},
				{Symbol: "M5", Idle: true}, {Symbol: "M6", Idle: true}}},
			[]string{"M3", "M4"},
		},
		{
			"surplus exceeds the idle count → release only the idle ones (fail-safe, retry next tick)",
			Observation{GateMaterialChains: 3, GateWorkers: 8, GateWorkerHulls: []GateWorkerSnapshot{ // surplus 4
				{Symbol: "M1", Idle: true}, {Symbol: "M2", Idle: true}, // only 2 idle
				{Symbol: "M3", Idle: false}, {Symbol: "M4", Idle: false},
				{Symbol: "M5", Idle: false}, {Symbol: "M6", Idle: false},
				{Symbol: "M7", Idle: false}, {Symbol: "M8", Idle: false}}},
			[]string{"M1", "M2"},
		},
		{
			"at the shape → releases nothing (byte-identical)",
			Observation{GateMaterialChains: 3, GateWorkers: 4, GateWorkerHulls: idleWorkers("M1", "M2", "M3", "M4")},
			nil,
		},
		{
			"below the shape → releases nothing (the buy path sizes up instead)",
			Observation{GateMaterialChains: 3, GateWorkers: 2, GateWorkerHulls: idleWorkers("M1", "M2")},
			nil,
		},
		{
			// The target is live from GATE entry, so an over-provisioned executor sheds its surplus whether or
			// not the pipeline has revealed a chain shape — release no longer waits on revelation either.
			"chains unrevealed → still releases the surplus over the workforce target",
			Observation{GateMaterialChains: 0, GateWorkers: 6, GateWorkerHulls: idleWorkers("M1", "M2", "M3", "M4", "M5", "M6")},
			[]string{"M1", "M2"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := planGateWorkers(tc.obs, cfg)
			if !reflect.DeepEqual(plan.SurplusToUndedicate, tc.want) {
				t.Fatalf("SurplusToUndedicate = %v, want %v", plan.SurplusToUndedicate, tc.want)
			}
			// the release path and the buy path are mutually exclusive (a clean bang-bang around desired).
			if len(tc.want) > 0 && plan.Buy != 0 {
				t.Fatalf("must never buy while releasing surplus, got Buy=%d", plan.Buy)
			}
			// the exclusive contract fleet is never released, and the dormant repurpose seam stays empty.
			if len(plan.ReleaseShips) != 0 {
				t.Fatalf("ReleaseShips must stay empty (dormant repurpose seam), got %v", plan.ReleaseShips)
			}
		})
	}
}

// fakeGateReleaser is the GateSurplusReleaser double (sp-mxflh): it records each release call's symbols and
// reports every requested hull released (the happy path), so the test can assert exactly which hulls the
// coordinator handed to the un-dedicate action.
type fakeGateReleaser struct {
	calls [][]string
	err   error
}

func (f *fakeGateReleaser) ReleaseSurplusGateWorkers(ctx context.Context, playerID int, shipSymbols []string) (int, error) {
	f.calls = append(f.calls, shipSymbols)
	if f.err != nil {
		return 0, f.err
	}
	return len(shipSymbols), nil
}

// sp-mxflh (through the driving port): a GATE tick where the executor holds MORE manufacturing hulls than the
// pipeline's shape needs un-dedicates the surplus IDLE ones to the undedicated pool (so the contract scaler
// adopts them, zero buys) — while NEVER touching the exclusive contract delivery fleet and NEVER buying a
// gate worker (it is over, not under, the shape).
func TestBootstrap_Gate_ReleasesSurplusManufacturingHulls(t *testing.T) {
	obs := gateObs()
	obs.Haulers = nHaulers(10) // the exclusive contract fleet — must be untouched
	obs.GateWorkers = 6        // 2 over the workforce target
	obs.GateWorkerHulls = []GateWorkerSnapshot{
		{Symbol: "M1", Idle: true}, {Symbol: "M2", Idle: true}, {Symbol: "M3", Idle: true},
		{Symbol: "M4", Idle: true}, {Symbol: "M5", Idle: true}, {Symbol: "M6", Idle: false}, // one mid-task
	}
	rel := &fakeGateReleaser{}
	acq := &fakeGateAcquirer{price: 200000, yard: "Y1", readable: true}
	h := gateHandler(obs, &fakeConstruction{}, &fakeManufacturing{}, &fakeRepurposer{}, acq, &fakeHandoff{})
	h.SetGateSurplusReleaser(rel)
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())

	if len(rel.calls) != 1 {
		t.Fatalf("expected exactly one surplus-release call, got %d (%v)", len(rel.calls), rel.calls)
	}
	if !reflect.DeepEqual(rel.calls[0], []string{"M1", "M2"}) {
		t.Fatalf("expected the 2 idle manufacturing surplus [M1 M2] released, got %v", rel.calls[0])
	}
	if res.WorkersReleased != 2 {
		t.Fatalf("expected WorkersReleased=2, got %d", res.WorkersReleased)
	}
	if acq.buys != 0 {
		t.Fatalf("an over-provisioned gate must NOT buy a worker, got %d buys", acq.buys)
	}
	for _, s := range rel.calls[0] {
		if strings.HasPrefix(s, "H") {
			t.Fatalf("a contract hauler was released to the idle pool: %s", s)
		}
	}
}

// --- The era-5 cold-start retune: a SMALLER construction workforce that ramps from GATE ENTRY ---

// The construction-worker cap is 4: the Admiral's era-5 cold-start target, reached by the tunable
// gate_worker_target and by the resolved config when a launch leaves it unset.
func TestBootstrap_GateWorkerTarget_DefaultsToFour(t *testing.T) {
	if defaultGateWorkerTarget != 4 {
		t.Fatalf("defaultGateWorkerTarget = %d, want 4 (the era-5 cold-start construction workforce)", defaultGateWorkerTarget)
	}
	if cfg := resolveBootstrapConfig(baseCmd(), nil); cfg.GateWorkerTarget != 4 {
		t.Fatalf("an unset launch config must resolve gate_worker_target to 4, got %d", cfg.GateWorkerTarget)
	}
}

// THE RAMP STARTS ON GATE ENTRY, NOT ON CHAIN REVELATION. A fresh GATE has revealed no material chains
// yet (GateMaterialChains 0) — the pipeline has only just been created. The workforce target is the full
// gate_worker_target immediately and the first hull is staged the same tick, so the construction fleet is
// bought while the pipeline warms instead of idling until the chain shape appears.
func TestBootstrap_PlanGateWorkers_RampsFromGateEntryWithNoChainsRevealed(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd(), nil)
	obs := Observation{Haulers: nHaulers(3), GateWorkers: 0, GateMaterialChains: 0}

	plan := planGateWorkers(obs, cfg)

	if plan.DesiredWorkers != cfg.GateWorkerTarget {
		t.Fatalf("an unrevealed pipeline must still target the full workforce %d, got %d", cfg.GateWorkerTarget, plan.DesiredWorkers)
	}
	if plan.Buy != 1 {
		t.Fatalf("the ramp must stage its first buy on GATE entry, got Buy=%d", plan.Buy)
	}
}

// The revealed chain shape never SHRINKS the target below the workforce cap: a one-chain pipeline still
// targets 4. This is what keeps the ramp monotone inside GATE — a target that tracked a fluctuating chain
// count downward would make the hulls just bought read as surplus and release them, the buy/release
// oscillation the bang-bang around `desired` exists to prevent.
func TestBootstrap_PlanGateWorkers_RevealedChainsNeverShrinkTheTarget(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd(), nil)
	for _, chains := range []int{0, 1, 2, 3, 9} {
		obs := Observation{Haulers: nHaulers(3), GateWorkers: 0, GateMaterialChains: chains}
		if got := planGateWorkers(obs, cfg).DesiredWorkers; got != cfg.GateWorkerTarget {
			t.Fatalf("chains=%d: desired = %d, want the workforce cap %d (the shape never shrinks the target)", chains, got, cfg.GateWorkerTarget)
		}
	}
}

// ONE HULL PER TICK UP TO THE CAP, THEN STOP. Walking the observed pool 0→4 stages exactly one buy each
// tick (never a blind buy-all) and stops dead at the cap — and never releases while ramping, so the buy and
// release paths stay mutually exclusive the whole way up.
func TestBootstrap_PlanGateWorkers_BuysOnePerTickThenStopsAtTheCap(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd(), nil)
	for have := 0; have <= cfg.GateWorkerTarget+2; have++ {
		obs := Observation{
			Haulers:         nHaulers(3),
			GateWorkers:     have,
			GateWorkerHulls: idleWorkers(workerSymbols(have)...),
		}
		plan := planGateWorkers(obs, cfg)

		wantBuy := 0
		if have < cfg.GateWorkerTarget {
			wantBuy = 1
		}
		if plan.Buy != wantBuy {
			t.Fatalf("have=%d: Buy = %d, want %d (one per tick below the cap, none at or above it)", have, plan.Buy, wantBuy)
		}
		if plan.Buy > 0 && len(plan.SurplusToUndedicate) > 0 {
			t.Fatalf("have=%d: must never buy and release in the same tick, got Buy=%d release=%v", have, plan.Buy, plan.SurplusToUndedicate)
		}
	}
}

// PURE AND IDEMPOTENT ACROSS A RESTART MID-RAMP. Bootstrap holds no ramp cursor: re-deriving the plan from
// the same re-observed pool yields the same single staged buy, and a pool that has grown by the hull just
// bought stages the NEXT one — so a restart part-way up the ramp never double-buys and never re-overshoots.
func TestBootstrap_PlanGateWorkers_RestartMidRampDoesNotDoubleBuy(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd(), nil)
	obs := Observation{Haulers: nHaulers(3), GateWorkers: 2, GateWorkerHulls: idleWorkers("M1", "M2")}

	first := planGateWorkers(obs, cfg)
	afterRestart := planGateWorkers(obs, cfg) // same observation, fresh process — same plan
	if !reflect.DeepEqual(first, afterRestart) {
		t.Fatalf("the plan must be a pure function of the observation: %+v vs %+v", first, afterRestart)
	}
	if first.Buy != 1 {
		t.Fatalf("a pool of 2 under the cap stages exactly one buy, got %d", first.Buy)
	}

	// The bought hull is observed as a GateWorker next tick, so the deficit shrinks by one — never re-bought.
	obs.GateWorkers = 3
	obs.GateWorkerHulls = idleWorkers("M1", "M2", "M3")
	if next := planGateWorkers(obs, cfg); next.Buy != 1 {
		t.Fatalf("the tick after a buy stages the next single hull, got %d", next.Buy)
	}
	obs.GateWorkers = cfg.GateWorkerTarget
	obs.GateWorkerHulls = idleWorkers(workerSymbols(cfg.GateWorkerTarget)...)
	if atCap := planGateWorkers(obs, cfg); atCap.Buy != 0 {
		t.Fatalf("the ramp stops at the cap and never re-buys, got %d", atCap.Buy)
	}
}

// RULINGS #4 — THE MONEY GUARD IS EXACTLY AS STRICT AT THE BOUNDARY. The ramp now reaches the spend path
// from GATE entry, so the working-capital cushion is the only thing standing between an unrevealed pipeline
// and a buy. One credit short of the floor REFUSES; exactly at the floor buys. A weakened comparison
// (cushion > floor, or any proportional relaxation) fails one side or the other.
func TestBootstrap_Gate_RampBuyRefusedWhenCushionIsOneShortOfTheFloor(t *testing.T) {
	const price = int64(120_000)
	obs := gateObs()
	obs.Haulers = nHaulers(3)
	obs.GateWorkers = 0
	obs.GateMaterialChains = 0 // the ramp is live from GATE entry — only the cushion can stop it
	obs.Treasury = price + defaultContractWorkingCapitalFloor - 1

	acq := &fakeGateAcquirer{price: price, yard: "Y1", readable: true}
	h := gateHandler(obs, &fakeConstruction{}, &fakeManufacturing{}, &fakeRepurposer{}, acq, &fakeHandoff{})
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())

	if acq.buys != 0 || res.GateWorkersBought != 0 {
		t.Fatalf("cushion=%d is one credit under the floor %d — the buy must be REFUSED, got buys=%d bought=%d",
			obs.Treasury-price, defaultContractWorkingCapitalFloor, acq.buys, res.GateWorkersBought)
	}
	if res.Blocker != "capital_gate" {
		t.Fatalf("expected blocker capital_gate, got %q", res.Blocker)
	}

	// Exactly at the floor the same ramp buys — proving the refusal above is the floor, not a dead ramp.
	obs.Treasury = price + defaultContractWorkingCapitalFloor
	acqAtFloor := &fakeGateAcquirer{price: price, yard: "Y1", readable: true}
	hAtFloor := gateHandler(obs, &fakeConstruction{}, &fakeManufacturing{}, &fakeRepurposer{}, acqAtFloor, &fakeHandoff{})
	if _, _ = hAtFloor.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); acqAtFloor.buys != 1 {
		t.Fatalf("cushion exactly at the floor %d must buy, got %d buys", defaultContractWorkingCapitalFloor, acqAtFloor.buys)
	}
}

// workerSymbols builds n stable manufacturing-hull symbols (M1..Mn) for pool-size table cases.
func workerSymbols(n int) []string {
	out := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, fmt.Sprintf("M%d", i))
	}
	return out
}

// RULINGS #4, ADVERSARIALLY: an unreadable price FAILS CLOSED even though the acquirer hands back a
// perfectly affordable price alongside the miss flag. The ramp reaches this spend path from GATE entry
// now, so a guard that trusted the returned value over the flag would buy on an unknown price every tick.
func TestBootstrap_Gate_RampBuyFailsClosedOnUnreadablePriceDespiteAffordableValue(t *testing.T) {
	obs := gateObs()
	obs.Haulers = nHaulers(3)
	obs.GateWorkers = 0
	obs.Treasury = 10_000_000 // richly affordable — only the unreadable flag may stop the buy

	for _, tc := range []struct {
		name string
		acq  *fakeGateAcquirer
	}{
		{"miss flag with an affordable price", &fakeGateAcquirer{price: 1_000, yard: "Y1", readable: false}},
		{"error with an affordable price", &fakeGateAcquirer{price: 1_000, yard: "Y1", readable: true, priceErr: errors.New("yard read failed")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := gateHandler(obs, &fakeConstruction{}, &fakeManufacturing{}, &fakeRepurposer{}, tc.acq, &fakeHandoff{})
			res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
			if tc.acq.buys != 0 {
				t.Fatalf("an unreadable price must fail CLOSED regardless of the value returned, got %d buys", tc.acq.buys)
			}
			if res.Blocker != "price_unreadable" {
				t.Fatalf("expected blocker price_unreadable, got %q", res.Blocker)
			}
			if tc.acq.priceChks != 1 {
				t.Fatalf("the ramp must actually reach the price check (proving the refusal is the guard, not an earlier hold), got %d checks", tc.acq.priceChks)
			}
		})
	}
}
