package commands

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// --- INCOME fakes (black-box: the reconciler is driven through its ports only) ---

type fakeRetirer struct {
	calls int
	ships []string
	err   error
	world *incomeWorld // mutated on a successful retire (frigate untagged)
}

func (f *fakeRetirer) RetireFromContract(ctx context.Context, playerID int, shipSymbol string) error {
	f.calls++
	f.ships = append(f.ships, shipSymbol)
	if f.err != nil {
		return f.err
	}
	if f.world != nil {
		f.world.retireFrigate()
	}
	return nil
}

type fakeHaulerAcquirer struct {
	price     int64
	yard      string
	readable  bool
	priceErr  error
	buyErr    error
	buys      int
	priceChks int
	placedOn  []string // the hub each BuyAndPlace was told to place on (order = buy order)
	world     *incomeWorld
}

func (f *fakeHaulerAcquirer) PriceCheck(ctx context.Context, playerID int, shipType string) (int64, string, bool, error) {
	f.priceChks++
	return f.price, f.yard, f.readable, f.priceErr
}

func (f *fakeHaulerAcquirer) BuyAndPlace(ctx context.Context, playerID int, shipType, yard, hubWaypoint string) (BuyResult, error) {
	if f.buyErr != nil {
		return BuyResult{}, f.buyErr
	}
	f.buys++
	f.placedOn = append(f.placedOn, hubWaypoint)
	if f.world != nil {
		f.world.addHauler(hubWaypoint)
	}
	return BuyResult{ShipSymbol: "HAULER-NEW", Price: f.price}, nil
}

type fakeContractRunner struct {
	calls int
	err   error
	world *incomeWorld
}

func (f *fakeContractRunner) StartBatchContract(ctx context.Context, playerID int) error {
	f.calls++
	if f.err != nil {
		return f.err
	}
	if f.world != nil {
		f.world.startBatch()
	}
	return nil
}

// fakeFrigateLoop is the sp-rype frigate sole-earner contract-loop starter port. It records the ships
// it was asked to loop; when world is set it flips the world's frigateLoopRunning so a multi-tick test
// proves the loop is started exactly once (never re-started while running).
type fakeFrigateLoop struct {
	calls int
	ships []string
	err   error
	world *incomeWorld
}

func (f *fakeFrigateLoop) StartLoop(ctx context.Context, playerID int, frigateSymbol string) error {
	f.calls++
	f.ships = append(f.ships, frigateSymbol)
	if f.err != nil {
		return f.err
	}
	if f.world != nil {
		f.world.startFrigateLoop()
	}
	return nil
}

// incomeWorld is a stateful model so a multi-tick INCOME acceptance test can observe the effect of the
// retire / batch-contract launch / staged hauler buys.
type incomeWorld struct {
	mu                 sync.Mutex
	treasury           int64
	homeSystem         string
	marketsTotal       int
	marketsCovered     int
	frigateID          string
	frigateOnContract  bool
	batchRunning       bool
	haulers            []HaulerSnapshot
	markets            []MarketSnapshot
	contractGoods      []string
	incomePerHour      float64
	hasPurchaser       bool
	probeCount         int  // sp-rype: provisioning progress — the frigate-loop start gates on probes≥target
	frigateLoopRunning bool // sp-rype: the frigate's own contract loop is running (earner-signal)
}

func (w *incomeWorld) snapshot() Observation {
	w.mu.Lock()
	defer w.mu.Unlock()
	return Observation{
		HomeSystem:                 w.homeSystem,
		Treasury:                   w.treasury,
		MarketsTotal:               w.marketsTotal,
		MarketsCovered:             w.marketsCovered,
		ProbeCount:                 w.probeCount,
		CommandFrigateID:           w.frigateID,
		CommandFrigateOnContract:   w.frigateOnContract,
		BatchContractRunning:       w.batchRunning,
		FrigateContractLoopRunning: w.frigateLoopRunning,
		Haulers:                    append([]HaulerSnapshot(nil), w.haulers...),
		Markets:                    w.markets,
		ContractGoods:              w.contractGoods,
		IncomePerHour:              w.incomePerHour,
		HasIdlePurchaser:           w.hasPurchaser,
		Readable:                   true,
	}
}

func (w *incomeWorld) startFrigateLoop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.frigateLoopRunning = true
}

func (w *incomeWorld) retireFrigate() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.frigateOnContract = false
}

func (w *incomeWorld) startBatch() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.batchRunning = true
}

func (w *incomeWorld) addHauler(hub string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.haulers = append(w.haulers, HaulerSnapshot{Symbol: "HAULER-NEW", Waypoint: hub})
}

type fakeIncomeObserver struct {
	world *incomeWorld
	calls int
}

func (f *fakeIncomeObserver) Observe(ctx context.Context, playerID int) (Observation, error) {
	f.calls++
	return f.world.snapshot(), nil
}

// incomeHubs is the standard 3-hub fixture (A cheapest/densest, then B, then C by coverage/cost).
func incomeHubs() []MarketSnapshot {
	return []MarketSnapshot{
		mkt("X1-HUBA", "X1", map[string]int64{"IRON": 100, "ALUMINUM": 100, "COPPER": 100}),
		mkt("X1-HUBB", "X1", map[string]int64{"IRON": 200, "ALUMINUM": 200}),
		mkt("X1-HUBC", "X1", map[string]int64{"IRON": 300}),
	}
}

// newIncomeHandler wires a handler with a fixed observation + all INCOME collaborators, for the
// single-tick INCOME guard pins.
func newIncomeHandler(obs Observation, ret *fakeRetirer, acq *fakeHaulerAcquirer, run *fakeContractRunner) *RunBootstrapCoordinatorHandler {
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(&fakeObserver{obs: obs})
	h.SetProbeAcquirer(&fakeAcquirer{price: 40000, yard: "Y", readable: true}) // present but unused in INCOME
	h.SetScoutAssigner(&fakeScouter{})
	h.SetFrigateRetirer(ret)
	h.SetHaulerAcquirer(acq)
	h.SetContractRunner(run)
	return h
}

// incomeObs is a coverage-met observation in the INCOME band (income below the default 10k bar).
func incomeObs() Observation {
	return Observation{
		HomeSystem: "X1", MarketsTotal: 10, MarketsCovered: 10, // coverage met → past DATA
		Treasury: 2000000, HasIdlePurchaser: true, IncomePerHour: 0,
		Markets: incomeHubs(), ContractGoods: []string{"IRON", "ALUMINUM"},
		Readable: true,
	}
}

// --- phase derivation: past coverage, INCOME below the bar, GATE at/above it ---

func TestBootstrap_DerivePhase_IncomeBelowBar(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd(), nil) // income_bar default 10000
	obs := Observation{MarketsTotal: 10, MarketsCovered: 10, IncomePerHour: 5000}
	if p := derivePhase(obs, cfg); p != PhaseIncome {
		t.Fatalf("coverage met + income below bar should derive INCOME, got %s", p)
	}
}

func TestBootstrap_DerivePhase_GateAtIncomeBar(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd(), nil)
	obs := Observation{MarketsTotal: 10, MarketsCovered: 10, IncomePerHour: 10000}
	if p := derivePhase(obs, cfg); p != PhaseGate {
		t.Fatalf("realized $/hr ≥ bar should derive GATE, got %s", p)
	}
}

// --- config defaults resolve LIVE for the INCOME knobs ---

func TestBootstrap_ResolvesIncomeDefaults(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd(), nil)
	if cfg.HaulerTarget != defaultHaulerTarget {
		t.Fatalf("hauler_target default = %d, got %d", defaultHaulerTarget, cfg.HaulerTarget)
	}
	if cfg.IncomeBar != defaultIncomeBar {
		t.Fatalf("income_bar default = %.0f, got %.0f", defaultIncomeBar, cfg.IncomeBar)
	}
	if cfg.MinContractEarners != defaultMinContractEarners {
		t.Fatalf("min_contract_earners default = %d, got %d", defaultMinContractEarners, cfg.MinContractEarners)
	}
	if cfg.HaulerShipType != defaultHaulerShipType {
		t.Fatalf("hauler_ship_type default = %q, got %q", defaultHaulerShipType, cfg.HaulerShipType)
	}
}

// --- frigate retirement: retire when tagged, skip when already retired ---

func TestBootstrap_Income_RetiresTaggedFrigate(t *testing.T) {
	obs := incomeObs()
	obs.CommandFrigateID = "FRIGATE-1"
	obs.CommandFrigateOnContract = true
	obs.BatchContractRunning = true         // isolate: don't also launch batch-contract
	obs.Haulers = make([]HaulerSnapshot, 4) // isolate: cap met, no buy
	ret := &fakeRetirer{}
	h := newIncomeHandler(obs, ret, &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}, &fakeContractRunner{})
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if ret.calls != 1 || len(ret.ships) != 1 || ret.ships[0] != "FRIGATE-1" {
		t.Fatalf("tagged frigate must be retired once by symbol, got calls=%d ships=%v", ret.calls, ret.ships)
	}
	if !res.FrigateRetired {
		t.Fatalf("res.FrigateRetired should be true")
	}
}

func TestBootstrap_Income_SkipsUntaggedFrigate(t *testing.T) {
	obs := incomeObs()
	obs.CommandFrigateID = "FRIGATE-1"
	obs.CommandFrigateOnContract = false // already retired
	obs.BatchContractRunning = true
	obs.Haulers = make([]HaulerSnapshot, 4)
	ret := &fakeRetirer{}
	h := newIncomeHandler(obs, ret, &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}, &fakeContractRunner{})
	h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if ret.calls != 0 {
		t.Fatalf("untagged frigate must not be retired again, got %d calls", ret.calls)
	}
}

// --- batch-contract idempotency: launch when not running, skip when running ---

func TestBootstrap_Income_LaunchesBatchContractWhenNotRunning(t *testing.T) {
	obs := incomeObs()
	obs.BatchContractRunning = false
	obs.Haulers = make([]HaulerSnapshot, 4) // isolate: cap met, no buy
	run := &fakeContractRunner{}
	h := newIncomeHandler(obs, &fakeRetirer{}, &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}, run)
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if run.calls != 1 {
		t.Fatalf("batch-contract must be launched once when not running, got %d", run.calls)
	}
	if !res.ContractRun {
		t.Fatalf("res.ContractRun should be true")
	}
}

func TestBootstrap_Income_SkipsBatchContractWhenRunning(t *testing.T) {
	obs := incomeObs()
	obs.BatchContractRunning = true
	obs.Haulers = make([]HaulerSnapshot, 4)
	run := &fakeContractRunner{}
	h := newIncomeHandler(obs, &fakeRetirer{}, &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}, run)
	h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if run.calls != 0 {
		t.Fatalf("running batch-contract must not be relaunched, got %d", run.calls)
	}
}

// --- staged hauler buy: affordable → buy 1, placed on the top viable hub, metric recorded ---

func TestBootstrap_Income_BuysHaulerOnTopHub(t *testing.T) {
	obs := incomeObs()
	obs.BatchContractRunning = true // isolate the buy
	acq := &fakeHaulerAcquirer{price: 300000, yard: "X1-YARD", readable: true}
	m := &fakeMetrics{}
	h := newIncomeHandler(obs, &fakeRetirer{}, acq, &fakeContractRunner{})
	h.SetMetricsSink(m)
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if acq.buys != 1 || res.HaulersBought != 1 {
		t.Fatalf("affordable hauler should buy exactly 1: buys=%d bought=%d (blocker=%q)", acq.buys, res.HaulersBought, res.Blocker)
	}
	if len(acq.placedOn) != 1 || acq.placedOn[0] != "X1-HUBA" {
		t.Fatalf("hauler must be placed on the top-ranked hub X1-HUBA, got %v", acq.placedOn)
	}
	if m.haulers != 1 {
		t.Fatalf("expected 1 hauler-purchase metric, got %d", m.haulers)
	}
}

// --- capital gate blocks an unaffordable hauler; decision line carries the arithmetic ---

func TestBootstrap_Income_CapitalGateBlocksHauler(t *testing.T) {
	obs := incomeObs()
	obs.Treasury = 150000 // cushion = 150000−300000 = −150000, below the 50k working-capital floor → blocked (sp-acv5)
	obs.BatchContractRunning = true
	acq := &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}
	h := newIncomeHandler(obs, &fakeRetirer{}, acq, &fakeContractRunner{})
	log := &capturingLogger{}
	res, _ := h.reconcileOnce(ctxWithLogger(log), baseCmd())
	if acq.buys != 0 {
		t.Fatalf("unaffordable hauler must NOT buy, got %d", acq.buys)
	}
	if res.Blocker != "capital_gate" {
		t.Fatalf("expected capital_gate blocker, got %q", res.Blocker)
	}
	dl, ok := log.find("bootstrap_hauler_buy_decision")
	if !ok {
		t.Fatalf("expected a hauler buy-decision line with the guardrail arithmetic")
	}
	for _, want := range []string{"price=300000", "treasury=150000", "floor=", "cushion=", "hub=X1-HUBA"} {
		if !strings.Contains(dl.msg, want) {
			t.Fatalf("hauler decision line missing %q: %s", want, dl.msg)
		}
	}
}

// --- sp-acv5: the hauler affordability gate is an ABSOLUTE contract working-capital floor
// (treasury−price ≥ floor), NOT a proportional reserve_margin×treasury cap. The first light hauler is
// bought as soon as the buy still leaves the goods+fuel operating cushion — it no longer waits for
// treasury to grow past ~2× the hauler price (PLAYBOOK §3). ---

func TestBootstrap_Income_WorkingCapitalFloor_BuysAsSoonAsCushionClears(t *testing.T) {
	const price = int64(300000)
	obs := incomeObs()
	obs.BatchContractRunning = true // isolate the buy
	// treasury = price + floor + 1 → the cushion clears the floor by 1 credit, so the buy IS made. This
	// treasury is far below 2×price (600000), so the OLD proportional gate (cap = reserve_margin×treasury
	// = 0.5×350001 = 175000 < price) would have BLOCKED it — the exact ~2×price delay sp-acv5 removes.
	obs.Treasury = price + defaultContractWorkingCapitalFloor + 1
	acq := &fakeHaulerAcquirer{price: price, yard: "X1-YARD", readable: true}
	h := newIncomeHandler(obs, &fakeRetirer{}, acq, &fakeContractRunner{})
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if acq.buys != 1 || res.HaulersBought != 1 {
		t.Fatalf("cushion clears the working-capital floor (treasury=%d price=%d floor=%d): must buy 1, got buys=%d bought=%d blocker=%q",
			obs.Treasury, price, defaultContractWorkingCapitalFloor, acq.buys, res.HaulersBought, res.Blocker)
	}
}

func TestBootstrap_Income_WorkingCapitalFloor_BlocksWhenCushionShort(t *testing.T) {
	const price = int64(300000)
	obs := incomeObs()
	obs.BatchContractRunning = true
	// treasury = price + floor − 1 → the buy would leave 1 credit LESS than the floor. RULINGS #4
	// fail-closed: do NOT buy (the contract operation must retain its working-capital cushion).
	obs.Treasury = price + defaultContractWorkingCapitalFloor - 1
	acq := &fakeHaulerAcquirer{price: price, yard: "X1-YARD", readable: true}
	h := newIncomeHandler(obs, &fakeRetirer{}, acq, &fakeContractRunner{})
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if acq.buys != 0 {
		t.Fatalf("cushion is 1 below the working-capital floor (treasury=%d price=%d floor=%d): must NOT buy, got %d",
			obs.Treasury, price, defaultContractWorkingCapitalFloor, acq.buys)
	}
	if res.Blocker != "capital_gate" {
		t.Fatalf("expected capital_gate blocker on a short cushion, got %q", res.Blocker)
	}
}

// --- no idle purchaser blocks the hauler buy (and never price-checks) ---

func TestBootstrap_Income_NoPurchaserBlocksHauler(t *testing.T) {
	obs := incomeObs()
	obs.HasIdlePurchaser = false
	obs.BatchContractRunning = true
	acq := &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}
	h := newIncomeHandler(obs, &fakeRetirer{}, acq, &fakeContractRunner{})
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if acq.buys != 0 || acq.priceChks != 0 {
		t.Fatalf("no purchaser: must not price-check or buy; priceChks=%d buys=%d", acq.priceChks, acq.buys)
	}
	if res.Blocker != "no_purchaser" {
		t.Fatalf("expected no_purchaser blocker, got %q", res.Blocker)
	}
}

// --- at most ONE hauler per tick, even when short by more than one ---

func TestBootstrap_Income_OneHaulerPerTick(t *testing.T) {
	obs := incomeObs() // 3 viable hubs, 0 haulers, target 4 → desired 3
	obs.BatchContractRunning = true
	acq := &fakeHaulerAcquirer{price: 100000, yard: "Y", readable: true}
	h := newIncomeHandler(obs, &fakeRetirer{}, acq, &fakeContractRunner{})
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if res.HaulersBought != 1 || acq.buys != 1 {
		t.Fatalf("short by 3 but must buy exactly 1 per tick: bought=%d buys=%d", res.HaulersBought, acq.buys)
	}
}

// --- placement skips a hub already served by a hauler (buys the next-ranked unserved hub) ---

func TestBootstrap_Income_PlacementSkipsServedHub(t *testing.T) {
	obs := incomeObs()
	obs.BatchContractRunning = true
	obs.Haulers = []HaulerSnapshot{{Symbol: "H1", Waypoint: "X1-HUBA"}} // top hub already served
	acq := &fakeHaulerAcquirer{price: 100000, yard: "Y", readable: true}
	h := newIncomeHandler(obs, &fakeRetirer{}, acq, &fakeContractRunner{})
	h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(acq.placedOn) != 1 || acq.placedOn[0] != "X1-HUBB" {
		t.Fatalf("served top hub must be skipped; expected placement on X1-HUBB, got %v", acq.placedOn)
	}
}

// --- cap: one hauler per viable hub, capped at min(hubs, hauler_target) → no buy when met ---

func TestBootstrap_Income_NoBuyWhenPerHubCapMet(t *testing.T) {
	obs := incomeObs() // 3 viable hubs
	obs.BatchContractRunning = true
	obs.Haulers = []HaulerSnapshot{ // 3 haulers, one per hub → desired 3 met
		{Waypoint: "X1-HUBA"}, {Waypoint: "X1-HUBB"}, {Waypoint: "X1-HUBC"},
	}
	acq := &fakeHaulerAcquirer{price: 100000, yard: "Y", readable: true}
	h := newIncomeHandler(obs, &fakeRetirer{}, acq, &fakeContractRunner{})
	h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if acq.buys != 0 {
		t.Fatalf("per-hub cap met (3 haulers, 3 hubs): must not buy, got %d", acq.buys)
	}
}

// --- recovery: a restart with haulers already at the cap never double-buys ---

func TestBootstrap_Income_Recovery_NoDoubleBuy(t *testing.T) {
	cmd := baseCmd()
	cmd.HaulerTarget = 2 // cap 2
	obs := incomeObs()   // 3 viable hubs → desired min(3,2)=2
	obs.BatchContractRunning = true
	obs.Haulers = []HaulerSnapshot{{Waypoint: "X1-HUBA"}, {Waypoint: "X1-HUBB"}}
	acq := &fakeHaulerAcquirer{price: 100000, yard: "Y", readable: true}
	h := newIncomeHandler(obs, &fakeRetirer{}, acq, &fakeContractRunner{})
	h.reconcileOnce(ctxWithLogger(&capturingLogger{}), cmd)
	if acq.buys != 0 {
		t.Fatalf("cap met on restart: must not double-buy, got %d", acq.buys)
	}
}

// --- no market data → no hubs → no hauler buy (fail closed) ---

func TestBootstrap_Income_NoMarketsNoHaulerBuy(t *testing.T) {
	obs := incomeObs()
	obs.Markets = nil
	obs.BatchContractRunning = true
	acq := &fakeHaulerAcquirer{price: 100000, yard: "Y", readable: true}
	h := newIncomeHandler(obs, &fakeRetirer{}, acq, &fakeContractRunner{})
	h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if acq.buys != 0 {
		t.Fatalf("no market data → no viable hubs → no buy, got %d", acq.buys)
	}
}

// --- dry-run: INCOME evaluates + logs but takes NO retire/launch/buy action ---

func TestBootstrap_Income_DryRunTakesNoAction(t *testing.T) {
	obs := incomeObs()
	obs.CommandFrigateID = "FRIGATE-1"
	obs.CommandFrigateOnContract = true
	obs.BatchContractRunning = false
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: 100000, yard: "Y", readable: true}
	run := &fakeContractRunner{}
	h := newIncomeHandler(obs, ret, acq, run)
	cmd := baseCmd()
	cmd.DryRun = true
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), cmd)
	if ret.calls != 0 || run.calls != 0 || acq.buys != 0 {
		t.Fatalf("dry-run must take no action: retire=%d launch=%d buy=%d", ret.calls, run.calls, acq.buys)
	}
	if res.WouldBuy != 1 {
		t.Fatalf("dry-run should report would_buy=1 hauler, got %d", res.WouldBuy)
	}
}

// --- INCOME→GATE crossover: income ≥ bar derives GATE, and the INCOME acts stop running.
// From the INCOME fixture (no gate site discovered, no GATE collaborators wired) GATE blocks on the
// undiscovered site rather than doing any INCOME work — the phase crossover is clean. ---

func TestBootstrap_IncomeToGate_Crossover_NoIncomeAct(t *testing.T) {
	obs := incomeObs()
	obs.IncomePerHour = 50000 // ≥ 10k bar → GATE
	obs.BatchContractRunning = false
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: 100000, yard: "Y", readable: true}
	run := &fakeContractRunner{}
	h := newIncomeHandler(obs, ret, acq, run) // no GATE collaborators wired
	log := &capturingLogger{}
	res, _ := h.reconcileOnce(ctxWithLogger(log), baseCmd())
	if res.Phase != PhaseGate {
		t.Fatalf("expected derived phase GATE, got %s", res.Phase)
	}
	if ret.calls != 0 || run.calls != 0 || acq.buys != 0 {
		t.Fatalf("in GATE the INCOME act must not run; retire=%d launch=%d buy=%d", ret.calls, run.calls, acq.buys)
	}
	// The INCOME fixture has no gate site, so GATE fails closed on discovery — never a "not implemented" hold.
	if res.Blocker != "no_gate_site" {
		t.Fatalf("expected GATE to block on the undiscovered site, got blocker %q", res.Blocker)
	}
	if _, ok := log.find("bootstrap_phase_not_implemented"); ok {
		t.Fatalf("the GATE 'not implemented' stub must be gone now that GATE is live")
	}
}

// --- frigate sole-earner contract loop (sp-rype): once provisioning is done, the frigate is put on its
// own continuous contract loop so it EARNS instead of parking idle at the shipyard after the probe buy.
// Guarded on provisioning-done + not-already-looping (the earner-signal), nil-safe, dry-run-silent. ---

// frigateLoopObs is a provisioned INCOME observation with the frigate present and no loop yet running —
// the state in which the frigate must be put on its earning loop. Batch-contract "running" and the
// hauler cap "met" isolate the assertion to the frigate-loop action.
func frigateLoopObs() Observation {
	obs := incomeObs()
	obs.ProbeCount = 3 // provisioning done (default probe_target 3)
	obs.CommandFrigateID = "FRIGATE-1"
	obs.FrigateContractLoopRunning = false
	obs.BatchContractRunning = true         // isolate: don't also launch the coordinator
	obs.Haulers = make([]HaulerSnapshot, 4) // isolate: hauler cap met, no buy
	return obs
}

func TestBootstrap_Income_StartsFrigateLoopWhenProvisioned(t *testing.T) {
	loop := &fakeFrigateLoop{}
	h := newIncomeHandler(frigateLoopObs(), &fakeRetirer{}, &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}, &fakeContractRunner{})
	h.SetFrigateContractLoopStarter(loop)
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if loop.calls != 1 || len(loop.ships) != 1 || loop.ships[0] != "FRIGATE-1" {
		t.Fatalf("a provisioned frigate must be put on its contract loop once, by symbol; got calls=%d ships=%v (blocker=%q)", loop.calls, loop.ships, res.Blocker)
	}
	if !res.FrigateLoopStarted {
		t.Fatalf("res.FrigateLoopStarted should be true")
	}
}

// earner-signal recognition: a frigate loop already running must NOT be re-started (no double-start,
// no double-claim). This is exactly the obs.BatchContractRunning-blind-spot the sp-rype signal closes.
func TestBootstrap_Income_SkipsFrigateLoopWhenAlreadyRunning(t *testing.T) {
	obs := frigateLoopObs()
	obs.FrigateContractLoopRunning = true // the earner-signal: the loop already runs
	loop := &fakeFrigateLoop{}
	h := newIncomeHandler(obs, &fakeRetirer{}, &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}, &fakeContractRunner{})
	h.SetFrigateContractLoopStarter(loop)
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if loop.calls != 0 {
		t.Fatalf("a running frigate loop must NOT be re-started, got %d calls", loop.calls)
	}
	if res.FrigateLoopStarted {
		t.Fatalf("res.FrigateLoopStarted should be false when the loop is already running")
	}
}

// juggle order (sp-t39j): buy the initial probes FIRST, THEN earn — the loop must not start while the
// frigate is still needed as the probe buyer (probes below target).
func TestBootstrap_Income_SkipsFrigateLoopBeforeProvisioned(t *testing.T) {
	obs := frigateLoopObs()
	obs.ProbeCount = 1 // provisioning NOT done — the frigate is still the probe purchaser
	loop := &fakeFrigateLoop{}
	h := newIncomeHandler(obs, &fakeRetirer{}, &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}, &fakeContractRunner{})
	h.SetFrigateContractLoopStarter(loop)
	h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if loop.calls != 0 {
		t.Fatalf("the frigate must finish provisioning (probes<target) before earning; got %d loop starts", loop.calls)
	}
}

// no frigate ID resolved ⇒ cannot start a loop (fail-closed, no guess).
func TestBootstrap_Income_SkipsFrigateLoopWithoutFrigateID(t *testing.T) {
	obs := frigateLoopObs()
	obs.CommandFrigateID = "" // frigate unresolved
	loop := &fakeFrigateLoop{}
	h := newIncomeHandler(obs, &fakeRetirer{}, &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}, &fakeContractRunner{})
	h.SetFrigateContractLoopStarter(loop)
	h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if loop.calls != 0 {
		t.Fatalf("no resolved frigate ID: must not start a loop, got %d calls", loop.calls)
	}
}

// nil-safe: no starter wired ⇒ a logged skip surfaced as a blocker, never a panic (matches the other
// INCOME collaborators' nil contract).
func TestBootstrap_Income_FrigateLoopNilSafe(t *testing.T) {
	h := newIncomeHandler(frigateLoopObs(), &fakeRetirer{}, &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}, &fakeContractRunner{})
	// deliberately NO SetFrigateContractLoopStarter
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()) // must not panic
	if res.FrigateLoopStarted {
		t.Fatalf("no starter wired: FrigateLoopStarted must be false")
	}
	if res.Blocker != "no_frigate_loop_starter" {
		t.Fatalf("nil frigate-loop starter should surface blocker no_frigate_loop_starter, got %q", res.Blocker)
	}
}

// the sp-rype stall reproduction, in the DATA parallel window: cold start still below the coverage bar,
// but the frigate has finished its hour-0 shipyard run + probe buy (probes at target, scouting). Under
// the parallel model (sp-t39j) actIncome runs in DATA, so the frigate must start EARNING rather than
// park idle at the yard — the fix for "sole earner dead, income never flows".
func TestBootstrap_Data_StartsFrigateLoopInParallelAfterProvisioning(t *testing.T) {
	obs := Observation{
		HomeSystem: "X1-HQ", ProbeCount: 3, ProbesScouting: 3, HasIdlePurchaser: true,
		Treasury: 120000, MarketsTotal: 10, MarketsCovered: 2, // 20% < 90% bar → DATA
		CommandFrigateID: "FRIGATE-1", FrigateContractLoopRunning: false, Readable: true,
	}
	loop := &fakeFrigateLoop{}
	h := newIncomeHandler(obs, &fakeRetirer{}, &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}, &fakeContractRunner{})
	h.SetFrigateContractLoopStarter(loop)
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if res.Phase != PhaseData {
		t.Fatalf("coverage below bar must derive DATA, got %s", res.Phase)
	}
	if loop.calls != 1 || loop.ships[0] != "FRIGATE-1" {
		t.Fatalf("in the DATA parallel window a provisioned frigate must start earning (contract loop), got calls=%d ships=%v", loop.calls, loop.ships)
	}
	if !res.FrigateLoopStarted {
		t.Fatalf("res.FrigateLoopStarted should be true in the DATA parallel window")
	}
}

// dry-run: evaluated + logged but NO loop started.
func TestBootstrap_Income_DryRunNoFrigateLoop(t *testing.T) {
	loop := &fakeFrigateLoop{}
	h := newIncomeHandler(frigateLoopObs(), &fakeRetirer{}, &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}, &fakeContractRunner{})
	h.SetFrigateContractLoopStarter(loop)
	cmd := baseCmd()
	cmd.DryRun = true
	h.reconcileOnce(ctxWithLogger(&capturingLogger{}), cmd)
	if loop.calls != 0 {
		t.Fatalf("dry-run must not start the frigate loop, got %d calls", loop.calls)
	}
}

// idempotent across ticks: the loop is started exactly once — after it starts, the observed
// earner-signal (FrigateContractLoopRunning) keeps every later tick from re-starting it.
func TestBootstrap_Income_FrigateLoopStartedExactlyOnce(t *testing.T) {
	world := &incomeWorld{
		treasury: 3000000, homeSystem: "X1", marketsTotal: 10, marketsCovered: 10,
		frigateID: "FRIGATE-1", frigateOnContract: false, batchRunning: true,
		probeCount: 3, // provisioned
		markets:    incomeHubs(), contractGoods: []string{"IRON"},
		incomePerHour: 0, hasPurchaser: true,
		haulers: make([]HaulerSnapshot, 4), // cap met: isolate to the frigate-loop lifecycle
	}
	loop := &fakeFrigateLoop{world: world}
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(&fakeIncomeObserver{world: world})
	h.SetProbeAcquirer(&fakeAcquirer{price: 40000, yard: "Y", readable: true})
	h.SetScoutAssigner(&fakeScouter{})
	h.SetFrigateRetirer(&fakeRetirer{})
	h.SetHaulerAcquirer(&fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true})
	h.SetContractRunner(&fakeContractRunner{})
	h.SetFrigateContractLoopStarter(loop)
	for i := 0; i < 5; i++ {
		if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	if loop.calls != 1 {
		t.Fatalf("frigate loop must be started exactly once across ticks (idempotent on the earner-signal), got %d", loop.calls)
	}
	if !world.snapshot().FrigateContractLoopRunning {
		t.Fatalf("the frigate loop should be observed running after start")
	}
}

// --- INCOME acceptance (Slice 2): from a coverage-met fixture, the arc retires the frigate, launches
// batch-contract, and stages one hauler per viable hub (capped), one per tick, on distinct hubs. ---

func TestBootstrap_IncomeAcceptance_RetiresLaunchesRampsHaulers(t *testing.T) {
	world := &incomeWorld{
		treasury: 3000000, homeSystem: "X1", marketsTotal: 10, marketsCovered: 10,
		frigateID: "FRIGATE-1", frigateOnContract: true, batchRunning: false,
		markets: incomeHubs(), contractGoods: []string{"IRON", "ALUMINUM"},
		incomePerHour: 0, hasPurchaser: true,
	}
	ret := &fakeRetirer{world: world}
	acq := &fakeHaulerAcquirer{price: 200000, yard: "X1-YARD", readable: true, world: world}
	run := &fakeContractRunner{world: world}
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(&fakeIncomeObserver{world: world})
	h.SetProbeAcquirer(&fakeAcquirer{price: 40000, yard: "Y", readable: true})
	h.SetScoutAssigner(&fakeScouter{})
	h.SetFrigateRetirer(ret)
	h.SetHaulerAcquirer(acq)
	h.SetContractRunner(run)

	for i := 0; i < 10; i++ {
		res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
		if err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
		if res.HaulersBought > 1 {
			t.Fatalf("tick %d bought %d haulers — staging violated (one per tick)", i, res.HaulersBought)
		}
	}
	final := world.snapshot()
	if final.CommandFrigateOnContract {
		t.Fatalf("acceptance: frigate should be retired from contracts")
	}
	if !final.BatchContractRunning {
		t.Fatalf("acceptance: batch-contract should be running")
	}
	// 3 viable hubs, target 4 → desired 3 haulers, one per distinct hub.
	if len(final.Haulers) != 3 {
		t.Fatalf("acceptance: expected 3 haulers (one per viable hub), got %d", len(final.Haulers))
	}
	if ret.calls != 1 {
		t.Fatalf("acceptance: frigate retired exactly once, got %d", ret.calls)
	}
	if run.calls != 1 {
		t.Fatalf("acceptance: batch-contract launched exactly once, got %d", run.calls)
	}
	seen := map[string]bool{}
	for _, hp := range acq.placedOn {
		if seen[hp] {
			t.Fatalf("acceptance: two haulers placed on the same hub %s (placements=%v)", hp, acq.placedOn)
		}
		seen[hp] = true
	}
	if acq.buys != 3 {
		t.Fatalf("acceptance: expected exactly 3 staged buys, got %d", acq.buys)
	}
}
