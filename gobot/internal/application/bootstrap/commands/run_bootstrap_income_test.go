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

// incomeWorld is a stateful model so a multi-tick INCOME acceptance test can observe the effect of the
// retire / batch-contract launch / staged hauler buys.
type incomeWorld struct {
	mu                sync.Mutex
	treasury          int64
	homeSystem        string
	marketsTotal      int
	marketsCovered    int
	frigateID         string
	frigateOnContract bool
	batchRunning      bool
	haulers           []HaulerSnapshot
	markets           []MarketSnapshot
	contractGoods     []string
	incomePerHour     float64
	hasPurchaser      bool
}

func (w *incomeWorld) snapshot() Observation {
	w.mu.Lock()
	defer w.mu.Unlock()
	return Observation{
		HomeSystem:               w.homeSystem,
		Treasury:                 w.treasury,
		MarketsTotal:             w.marketsTotal,
		MarketsCovered:           w.marketsCovered,
		CommandFrigateID:         w.frigateID,
		CommandFrigateOnContract: w.frigateOnContract,
		BatchContractRunning:     w.batchRunning,
		Haulers:                  append([]HaulerSnapshot(nil), w.haulers...),
		Markets:                  w.markets,
		ContractGoods:            w.contractGoods,
		IncomePerHour:            w.incomePerHour,
		HasIdlePurchaser:         w.hasPurchaser,
		Readable:                 true,
	}
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
	cfg := resolveBootstrapConfig(baseCmd()) // income_bar default 10000
	obs := Observation{MarketsTotal: 10, MarketsCovered: 10, IncomePerHour: 5000}
	if p := derivePhase(obs, cfg); p != PhaseIncome {
		t.Fatalf("coverage met + income below bar should derive INCOME, got %s", p)
	}
}

func TestBootstrap_DerivePhase_GateAtIncomeBar(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd())
	obs := Observation{MarketsTotal: 10, MarketsCovered: 10, IncomePerHour: 10000}
	if p := derivePhase(obs, cfg); p != PhaseGate {
		t.Fatalf("realized $/hr ≥ bar should derive GATE, got %s", p)
	}
}

// --- config defaults resolve LIVE for the INCOME knobs ---

func TestBootstrap_ResolvesIncomeDefaults(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd())
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
	obs.Treasury = 150000 // cap 75k
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
	for _, want := range []string{"price=300000", "treasury=150000", "cap=", "hub=X1-HUBA"} {
		if !strings.Contains(dl.msg, want) {
			t.Fatalf("hauler decision line missing %q: %s", want, dl.msg)
		}
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

// --- GATE stub: income ≥ bar → holds at INCOME-complete, INCOME act does NOT run ---

func TestBootstrap_GateStub_HoldsAtIncomeComplete(t *testing.T) {
	obs := incomeObs()
	obs.IncomePerHour = 50000 // ≥ 10k bar → GATE
	obs.BatchContractRunning = false
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: 100000, yard: "Y", readable: true}
	run := &fakeContractRunner{}
	h := newIncomeHandler(obs, ret, acq, run)
	log := &capturingLogger{}
	res, _ := h.reconcileOnce(ctxWithLogger(log), baseCmd())
	if res.Phase != PhaseGate {
		t.Fatalf("expected derived phase GATE, got %s", res.Phase)
	}
	if ret.calls != 0 || run.calls != 0 || acq.buys != 0 {
		t.Fatalf("GATE stub: INCOME act must not run; retire=%d launch=%d buy=%d", ret.calls, run.calls, acq.buys)
	}
	hold, ok := log.find("bootstrap_phase_not_implemented")
	if !ok || !strings.Contains(hold.msg, "INCOME-complete") {
		t.Fatalf("expected a 'holding at INCOME-complete' hold line, got ok=%v msg=%q", ok, hold.msg)
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
