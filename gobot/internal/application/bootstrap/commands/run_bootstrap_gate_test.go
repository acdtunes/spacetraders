package commands

import (
	"context"
	"errors"
	"testing"
)

// --- derivePhase: GATE stickiness + COMPLETE (Slice 3) ---

// The INCOME→GATE entry is realized $/hr ≥ income_bar (unchanged from Slice 2's stub-era derivation).
func TestBootstrap_DerivePhase_EntersGateAtIncomeBar(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd()) // income_bar 10000
	obs := Observation{MarketsTotal: 10, MarketsCovered: 10, IncomePerHour: 12000}
	if p := derivePhase(obs, cfg); p != PhaseGate {
		t.Fatalf("income over bar should derive GATE, got %s", p)
	}
}

// GATE stays STICKY once a construction pipeline exists, even at a low income read. Under Option B the
// whole contract fleet keeps earning through GATE (the gate-delivery fleet is BOUGHT from that income,
// never repurposed from it), so income no longer collapses at the boundary — the stickiness is now
// belt-and-suspenders against any transient dip, keeping derivePhase from regressing GATE→INCOME (which
// would re-buy contract haulers and thrash).
func TestBootstrap_DerivePhase_GateStickyOnceConstructionStarted(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd())
	obs := Observation{
		MarketsTotal: 10, MarketsCovered: 10,
		IncomePerHour:       500, // a transient dip well under the 10000 bar
		ConstructionStarted: true,
	}
	if p := derivePhase(obs, cfg); p != PhaseGate {
		t.Fatalf("construction started should keep GATE despite a low income read, got %s (would thrash to INCOME)", p)
	}
}

// A 100%-delivered gate derives COMPLETE — terminal and monotone, so a restart post-completion resumes COMPLETE.
func TestBootstrap_DerivePhase_CompleteWhenConstructionComplete(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd())
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

// Coverage still gates everything: a cold agent (no market data) stays DATA even if some stray
// construction flag is set — the arc never skips the data phase.
func TestBootstrap_DerivePhase_DataDominatesConstructionFlags(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd())
	obs := Observation{MarketsTotal: 0, ConstructionStarted: true, ConstructionComplete: true}
	if p := derivePhase(obs, cfg); p != PhaseData {
		t.Fatalf("uncovered world should derive DATA regardless of construction flags, got %s", p)
	}
}

// --- config defaults: the shared GATE-fleet solvency floor resolves LIVE (Option B) ---

// The shared working-capital floor knobs resolve to their documented defaults: the fleet's ~1M reserve
// and the 40% counter-cyclical treasury-percent, so the gate fleet shares the material engine's floor.
func TestBootstrap_ResolvesGateReserveDefaults(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd())
	if cfg.Reserve != defaultBootstrapReserve {
		t.Fatalf("reserve default = %d, got %d", defaultBootstrapReserve, cfg.Reserve)
	}
	if cfg.ReservePct != 40 {
		t.Fatalf("reserve_pct default = 40 (common.DefaultReserveTreasuryPct), got %d", cfg.ReservePct)
	}
}

// --- planGateWorkers: deterministic all-bought sizing (Option B, Slice 3) ---

// Option B: the ENTIRE gate-delivery fleet is BOUGHT from contract income — contract haulers are NEVER
// repurposed — so a tick with idle contract haulers and an uncovered pipeline shape still stages a buy
// (desired > GateWorkers); it does not release haulers to cover it.
func TestBootstrap_PlanGateWorkers_BuysEntireFleet_NoRepurpose(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd())
	obs := Observation{
		Haulers:            []HaulerSnapshot{{Symbol: "H1"}, {Symbol: "H2"}, {Symbol: "H3"}, {Symbol: "H4"}},
		GateWorkers:        0,
		GateMaterialChains: 2, // desired = min(2+1, 6) = 3; 3 > 0 workers ⇒ buy the whole fleet, one/tick
	}
	plan := planGateWorkers(obs, cfg)
	if plan.Buy != 1 {
		t.Fatalf("Option B buys the whole gate fleet from income (contract haulers not repurposed): desired 3 > 0 workers ⇒ buy=1, got %d", plan.Buy)
	}
}

// No buy before the pipeline reveals its chains: with chains still 0 the sizing target is 0, so the buy
// holds until the shape is known (there is no seed release to run ahead of it any more).
func TestBootstrap_PlanGateWorkers_NoBuyBeforeChainsKnown(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd())
	obs := Observation{
		Haulers:            []HaulerSnapshot{{Symbol: "H1"}, {Symbol: "H2"}, {Symbol: "H3"}},
		GateWorkers:        0,
		GateMaterialChains: 0, // pipeline shape not yet known
	}
	plan := planGateWorkers(obs, cfg)
	if plan.DesiredWorkers != 0 {
		t.Fatalf("desired must be 0 before the pipeline reveals its chains, got %d", plan.DesiredWorkers)
	}
	if plan.Buy != 0 {
		t.Fatalf("no buy before the pipeline reveals its chains, got buy=%d", plan.Buy)
	}
}

// The pipeline reveals 3 chains and no workers exist yet, so the staged delta buys ONE hull (the whole
// gate-delivery fleet is bought from income, one per tick — never a blind buy-all).
func TestBootstrap_PlanGateWorkers_BuysWhenShort(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd()) // target 6, delivery +1 ⇒ desired = min(3+1,6) = 4
	obs := Observation{
		Haulers:            []HaulerSnapshot{{Symbol: "H1"}}, // a contract earner, untouched
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
	cfg := resolveBootstrapConfig(baseCmd()) // target 6
	obs := Observation{Haulers: []HaulerSnapshot{{Symbol: "H1"}}, GateWorkers: 0, GateMaterialChains: 5}
	plan := planGateWorkers(obs, cfg) // desired = min(5+1,6) = 6, workers 0 ⇒ deficit 6
	if plan.Buy != 1 {
		t.Fatalf("deficit of 6 must still stage exactly one buy, got %d", plan.Buy)
	}
}

// No buy when the executor already has enough workers (idempotency + no over-buy: a restart mid-GATE
// re-observes GateWorkers and never re-buys or overshoots the pipeline's shape).
func TestBootstrap_PlanGateWorkers_NoBuyWhenWorkersSuffice(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd())
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
	cfg := resolveBootstrapConfig(cmd)
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
// There is NO worker-repurposer any more: Option B buys the whole gate-delivery fleet from contract income.
func gateHandler(obs Observation, con ConstructionManager, mfg ManufacturingController, acq GateWorkerAcquirer, ho HandoffLauncher) *RunBootstrapCoordinatorHandler {
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(&fakeObserver{obs: obs})
	if con != nil {
		h.SetConstructionManager(con)
	}
	if mfg != nil {
		h.SetManufacturingController(mfg)
	}
	if acq != nil {
		h.SetGateWorkerAcquirer(acq)
	}
	if ho != nil {
		h.SetHandoffLauncher(ho)
	}
	return h
}

// gateObs is a covered, GATE-phase observation (construction started + adopted by default, treasury ample).
// GATE stays sticky on ConstructionStarted regardless of income; tests tweak individual fields for the
// guard under test.
func gateObs() Observation {
	return Observation{
		HomeSystem: "X1-HQ", MarketsTotal: 10, MarketsCovered: 10, Treasury: 1000000,
		IncomePerHour:        12000, // whole contract fleet keeps earning through GATE (Option B — no repurpose)
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
	h := gateHandler(obs, con, &fakeManufacturing{}, &fakeGateAcquirer{}, &fakeHandoff{})
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
	h := gateHandler(obs, con, mfg, &fakeGateAcquirer{}, &fakeHandoff{})
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
	h := gateHandler(gateObs(), con, &fakeManufacturing{}, &fakeGateAcquirer{}, &fakeHandoff{})
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
	h := gateHandler(obs, &fakeConstruction{}, mfg, &fakeGateAcquirer{}, &fakeHandoff{})
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
	h := gateHandler(obs, &fakeConstruction{}, mfg, &fakeGateAcquirer{}, &fakeHandoff{})
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
	h := gateHandler(gateObs(), &fakeConstruction{}, mfg, &fakeGateAcquirer{}, &fakeHandoff{})
	h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if mfg.ensures != 0 || mfg.bounces != 0 {
		t.Fatalf("a running+adopted executor needs no action, got ensures=%d bounces=%d", mfg.ensures, mfg.bounces)
	}
}

// Option B at the handler: with the pipeline short of workers, the coordinator BUYS a gate-delivery hull
// from contract income — and with contract haulers present it STILL buys (never repurposes them), so the
// contract fleet stays intact and earning through GATE.
func TestBootstrap_Gate_BuysGateFleet_LeavesContractHaulersIntact(t *testing.T) {
	obs := gateObs()
	obs.Haulers = []HaulerSnapshot{{Symbol: "H1"}, {Symbol: "H2"}, {Symbol: "H3"}} // contract earners — must NOT be touched
	obs.GateWorkers = 0
	obs.GateMaterialChains = 3 // desired = min(3+1,6) = 4 > 0 ⇒ buy
	acq := &fakeGateAcquirer{price: 200000, yard: "Y1", readable: true}
	h := gateHandler(obs, &fakeConstruction{}, &fakeManufacturing{}, acq, &fakeHandoff{})
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if acq.buys != 1 {
		t.Fatalf("expected one staged gate-delivery buy from income, got %d", acq.buys)
	}
	if res.GateWorkersBought != 1 {
		t.Fatalf("expected GateWorkersBought=1, got %d", res.GateWorkersBought)
	}
}

// The SHARED solvency floor blocks a gate-delivery buy that would breach the working-capital reserve — the
// same max(50k, min(reserve, reserve_pct%×treasury)) primitive the material engine enforces. At mid
// treasury the counter-cyclical PROPORTIONAL term (40%×treasury) binds ABOVE the immutable 50k floor, so a
// buy that would clear a flat 50k is still blocked: proof the gate fleet shares the material engine's floor,
// not a looser cap. It retries as contract income refills the treasury.
func TestBootstrap_Gate_SolvencyFloorBlocksWorkerBuy(t *testing.T) {
	obs := gateObs()
	obs.Haulers = []HaulerSnapshot{{Symbol: "H1"}}
	obs.GateWorkers = 0
	obs.GateMaterialChains = 3
	obs.Treasury = 500000 // floor = max(50k, min(1M, 40%×500k=200k)) = 200k; treasury−price = 500k−350k = 150k < 200k ⇒ blocked
	acq := &fakeGateAcquirer{price: 350000, yard: "Y1", readable: true}
	h := gateHandler(obs, &fakeConstruction{}, &fakeManufacturing{}, acq, &fakeHandoff{})
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if acq.buys != 0 {
		t.Fatalf("shared proportional floor (40%%×treasury=200k) must block the buy (150k left < 200k), got %d buys", acq.buys)
	}
	if res.Blocker != "gate_worker_capital_gate" {
		t.Fatalf("expected blocker gate_worker_capital_gate, got %q", res.Blocker)
	}
}

// A settled GATE tick (started, adopted, workers suffice) takes NO action — quiet reconcile.
func TestBootstrap_Gate_SettledTickIsQuiet(t *testing.T) {
	obs := gateObs()
	obs.Haulers = []HaulerSnapshot{{Symbol: "H1"}} // a contract earner, untouched
	obs.GateWorkers = 4
	obs.GateMaterialChains = 3 // desired 4, have 4
	con := &fakeConstruction{}
	mfg := &fakeManufacturing{}
	acq := &fakeGateAcquirer{price: 1, yard: "Y", readable: true}
	h := gateHandler(obs, con, mfg, acq, &fakeHandoff{})
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if con.starts+mfg.ensures+mfg.bounces+acq.buys != 0 {
		t.Fatalf("settled GATE tick must be quiet, got starts=%d ensures=%d bounces=%d buys=%d",
			con.starts, mfg.ensures, mfg.bounces, acq.buys)
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
	h := gateHandler(obs, &fakeConstruction{}, &fakeManufacturing{}, &fakeGateAcquirer{}, ho)
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
	h := gateHandler(obs, &fakeConstruction{}, &fakeManufacturing{}, &fakeGateAcquirer{}, ho)
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
	h := gateHandler(obs, &fakeConstruction{}, &fakeManufacturing{}, &fakeGateAcquirer{}, ho)
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
