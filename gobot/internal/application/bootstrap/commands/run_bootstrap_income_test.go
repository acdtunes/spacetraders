package commands

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// --- contract fakes (black-box: the reconciler is driven through its ports only) ---

type fakeRetirer struct {
	calls       int
	ships       []string
	err         error
	dedications []string // Ships dedicated as the exclusive purchasing ship
	dedicateErr error
	world       *incomeWorld // mutated on a successful retire (frigate untagged)
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

func (f *fakeRetirer) DedicateAsPurchaser(ctx context.Context, playerID int, shipSymbol string) error {
	f.dedications = append(f.dedications, shipSymbol)
	if f.dedicateErr != nil {
		return f.dedicateErr
	}
	if f.world != nil {
		f.world.dedicatePurchasing()
	}
	return nil
}

type fakeHaulerAcquirer struct {
	price      int64
	yard       string
	readable   bool
	priceErr   error
	buyErr     error
	buys       int
	priceChks  int
	lastAsk    int64    // what a cold yard reports: the last price it gave, 0 until one is read
	placedOn   []string // the hub each BuyAndPlace was told to place on (order = buy order)
	purchasers []string // The purchaser symbol each BuyAndPlace was told to use ("" = scan)
	world      *incomeWorld

	// The trade-seed buy (BuyAndDedicate). Records the fleet tag + purchaser each call used and a
	// separate count, so a test can prove acquisition #2 was dedicated to "trade" (NOT placed on a contract hub).
	dedicateBuys    int      // count of BuyAndDedicate calls that bought
	dedicatedFleets []string // the fleet tag each BuyAndDedicate was told to dedicate to (order = call order)
	dedicatePurch   []string // the purchaser symbol each BuyAndDedicate was told to use
}

// PriceCheck models the presence-gated yard: it prices only while readable, and a cold read carries the
// last ask it gave (0 when it never has) so the pivot has evidence but no price to spend against.
func (f *fakeHaulerAcquirer) PriceCheck(ctx context.Context, playerID int, shipType string) (int64, string, bool, error) {
	f.priceChks++
	if f.priceErr != nil || !f.readable {
		return f.lastAsk, "", false, f.priceErr
	}
	f.lastAsk = f.price
	return f.price, f.yard, true, nil
}

func (f *fakeHaulerAcquirer) BuyAndPlace(ctx context.Context, playerID int, shipType, yard, hubWaypoint, purchaserSymbol string) (BuyResult, error) {
	if f.buyErr != nil {
		return BuyResult{}, f.buyErr
	}
	f.buys++
	f.placedOn = append(f.placedOn, hubWaypoint)
	f.purchasers = append(f.purchasers, purchaserSymbol)
	if f.world != nil {
		f.world.addHauler(hubWaypoint)
	}
	return BuyResult{ShipSymbol: "HAULER-NEW", Price: f.price}, nil
}

// BuyAndDedicate is the trade-seed buy: it records the fleet tag + purchaser it was told to use and
// (when a world is set) grows the world's trade-hull count so a multi-tick test sees the seeded signal appear.
func (f *fakeHaulerAcquirer) BuyAndDedicate(ctx context.Context, playerID int, shipType, yard, fleet, purchaserSymbol string) (BuyResult, error) {
	if f.buyErr != nil {
		return BuyResult{}, f.buyErr
	}
	f.dedicateBuys++
	f.dedicatedFleets = append(f.dedicatedFleets, fleet)
	f.dedicatePurch = append(f.dedicatePurch, purchaserSymbol)
	if f.world != nil {
		f.world.addTradeHull()
	}
	return BuyResult{ShipSymbol: "TRADE-NEW", Price: f.price}, nil
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
	calls     int
	ships     []string
	err       error
	stopCalls int      // StopLoop invocations (the first-hauler pivot)
	stopped   []string // ships whose loop StopLoop was asked to stop
	stopErr   error
	world     *incomeWorld
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

func (f *fakeFrigateLoop) StopLoop(ctx context.Context, playerID int, frigateSymbol string) error {
	f.stopCalls++
	f.stopped = append(f.stopped, frigateSymbol)
	if f.stopErr != nil {
		return f.stopErr
	}
	if f.world != nil {
		f.world.stopFrigateLoop()
	}
	return nil
}

// incomeWorld is a stateful model so a multi-tick Contract acceptance test can observe the effect of the
// retire / batch-contract launch / staged hauler buys.
type incomeWorld struct {
	mu                       sync.Mutex
	treasury                 int64
	homeSystem               string
	marketsTotal             int
	marketsCovered           int
	frigateID                string
	frigateOnContract        bool
	batchRunning             bool
	haulers                  []HaulerSnapshot
	placementSlots           []string
	incomePerHour            float64
	hasPurchaser             bool
	probeCount               int  // sp-rype: provisioning progress — the frigate-loop start gates on probes≥target
	frigateLoopRunning       bool // sp-rype: the frigate's own contract loop is running (earner-signal)
	frigateCargoEmpty        bool // The frigate carries no contract cargo (the pivot safe point)
	commandFrigatePurchasing bool // The frigate is the exclusive purchasing ship (post-pivot)
	tradeHullCount           int  // sp-192k4: 'trade'-dedicated hulls now — the observable trade-seeded signal
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
		ProbesScouting:             w.probeCount, // provisioned probes are scouting in the contract world
		CommandFrigateID:           w.frigateID,
		CommandFrigateOnContract:   w.frigateOnContract,
		BatchContractRunning:       w.batchRunning,
		FrigateContractLoopRunning: w.frigateLoopRunning,
		FrigateCargoEmpty:          w.frigateCargoEmpty,
		CommandFrigatePurchasing:   w.commandFrigatePurchasing,
		TradeHullCount:             w.tradeHullCount,
		Haulers:                    append([]HaulerSnapshot(nil), w.haulers...),
		ContractPlacementSlots:     append([]string(nil), w.placementSlots...),
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

func (w *incomeWorld) stopFrigateLoop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.frigateLoopRunning = false
}

// dedicatePurchasing models the first-hauler pivot's dedication: the frigate becomes the exclusive
// purchasing ship (off contract), so the loop-start gate reads it and never restarts the loop.
func (w *incomeWorld) dedicatePurchasing() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.commandFrigatePurchasing = true
	w.frigateOnContract = false
}

// retireFrigate models the fleet unassign behind RetireFromContract: it writes an EMPTY dedication, so
// it clears whichever tag the frigate carried — the contract one at the stale-tag retire, the purchasing
// one when a stranded buy ship is handed back to earning.
func (w *incomeWorld) retireFrigate() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.frigateOnContract = false
	w.commandFrigatePurchasing = false
}

// earn models the sole earner booking contract income between ticks — the treasury growth that a frigate
// held off its loop can never produce.
func (w *incomeWorld) earn(credits int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.treasury += credits
}

// purchaserAtYard models the freed+dedicated command frigate arriving at the home shipyard (sp-5nd2
// fault-2): it now stands idle at the yard as the purchaser, so the next tick reads a live price and
// sees an idle purchasing hull.
func (w *incomeWorld) purchaserAtYard() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.hasPurchaser = true
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

// addTradeHull models the trade-seed: the bought hull is dedicated 'trade', so the observable
// trade-hull count grows — the durable "seeded" signal a later tick reads to stop re-seeding.
func (w *incomeWorld) addTradeHull() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.tradeHullCount++
}

type fakeIncomeObserver struct {
	world *incomeWorld
	calls int
}

func (f *fakeIncomeObserver) Observe(ctx context.Context, playerID int) (Observation, error) {
	f.calls++
	return f.world.snapshot(), nil
}

// incomeSlots is the standard fixed delivery-slot fixture: four distinct central parks in placement
// order, so the ramp can spread one hull per slot all the way to the fixed hauler target.
func incomeSlots() []string {
	return []string{"X1-HUBA", "X1-HUBB", "X1-HUBC", "X1-HUBD"}
}

// newIncomeHandler wires a handler with a fixed observation + all contract collaborators, for the
// single-tick contract guard pins.
func newIncomeHandler(obs Observation, ret *fakeRetirer, acq *fakeHaulerAcquirer, run *fakeContractRunner) *RunBootstrapCoordinatorHandler {
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(&fakeObserver{obs: obs})
	h.SetProbeAcquirer(&fakeAcquirer{price: 40000, yard: "Y", readable: true}) // present but unused by the contract workstream
	h.SetFrigateRetirer(ret)
	h.SetHaulerAcquirer(acq)
	h.SetContractRunner(run)
	return h
}

// incomeObs is a provisioned observation in the cold-start band (income below the default 10k bar).
// Provisioning — probes at target + scouting — is what derives the phase label, not coverage.
func incomeObs() Observation {
	return Observation{
		HomeSystem: "X1", MarketsTotal: 10, MarketsCovered: 10,
		ProbeCount: 3, ProbesScouting: 3, // provisioned (probe_target 3) + scouting → COLDSTART-labeled
		Treasury: 2000000, HasIdlePurchaser: true, IncomePerHour: 0,
		ContractPlacementSlots: incomeSlots(),
		Readable:               true,
	}
}

// --- the cold-start contract shape is fixed in code ---

func TestBootstrap_ContractShape_IsFixed(t *testing.T) {
	if haulerTarget != 4 {
		t.Fatalf("hauler target = %d, want 4 (the fixed Phase-1 contract-hauler count)", haulerTarget)
	}
	if haulerShipType != "SHIP_LIGHT_HAULER" {
		t.Fatalf("contract hauler asset = %q, want SHIP_LIGHT_HAULER", haulerShipType)
	}
}

// --- frigate retirement: retire when tagged, skip when already retired ---

func TestBootstrap_Income_RetiresTaggedFrigate(t *testing.T) {
	obs := incomeObs()
	obs.CommandFrigateID = "FRIGATE-1"
	obs.CommandFrigateOnContract = true
	obs.BatchContractRunning = true         // isolate: don't also launch batch-contract
	obs.Haulers = make([]HaulerSnapshot, 4) // isolate: cap met, no buy
	obs.TradeHullCount = 1                  // Post-seed state — isolate from the trade-seed detour
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
	obs.TradeHullCount = 1 // Post-seed state — isolate from the trade-seed detour
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
	obs.TradeHullCount = 1                  // Post-seed state — isolate from the trade-seed detour
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
	obs.TradeHullCount = 1 // Post-seed state — isolate from the trade-seed detour
	run := &fakeContractRunner{}
	h := newIncomeHandler(obs, &fakeRetirer{}, &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}, run)
	h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if run.calls != 0 {
		t.Fatalf("running batch-contract must not be relaunched, got %d", run.calls)
	}
}

// --- staged hauler buy: affordable → buy 1, placed on the first fixed delivery slot, metric recorded ---

func TestBootstrap_Income_BuysHaulerOnFirstSlot(t *testing.T) {
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
		t.Fatalf("hauler must be placed on the first delivery slot X1-HUBA, got %v", acq.placedOn)
	}
	if m.haulers != 1 {
		t.Fatalf("expected 1 hauler-purchase metric, got %d", m.haulers)
	}
}

// --- capital gate blocks an unaffordable hauler; decision line carries the arithmetic ---

func TestBootstrap_Income_CapitalGateBlocksHauler(t *testing.T) {
	obs := incomeObs()
	obs.Treasury = 150000 // cushion = 150000−300000 = −150000, below the 50k working-capital floor → blocked
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
	for _, want := range []string{"price=300000", "treasury=150000", "floor=", "cushion=", "slot=X1-HUBA"} {
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
	obs.Treasury = price + contractWorkingCapitalFloor + 1
	acq := &fakeHaulerAcquirer{price: price, yard: "X1-YARD", readable: true}
	h := newIncomeHandler(obs, &fakeRetirer{}, acq, &fakeContractRunner{})
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if acq.buys != 1 || res.HaulersBought != 1 {
		t.Fatalf("cushion clears the working-capital floor (treasury=%d price=%d floor=%d): must buy 1, got buys=%d bought=%d blocker=%q",
			obs.Treasury, price, contractWorkingCapitalFloor, acq.buys, res.HaulersBought, res.Blocker)
	}
}

func TestBootstrap_Income_WorkingCapitalFloor_BlocksWhenCushionShort(t *testing.T) {
	const price = int64(300000)
	obs := incomeObs()
	obs.BatchContractRunning = true
	// treasury = price + floor − 1 → the buy would leave 1 credit LESS than the floor. RULINGS #4
	// fail-closed: do NOT buy (the contract operation must retain its working-capital cushion).
	obs.Treasury = price + contractWorkingCapitalFloor - 1
	acq := &fakeHaulerAcquirer{price: price, yard: "X1-YARD", readable: true}
	h := newIncomeHandler(obs, &fakeRetirer{}, acq, &fakeContractRunner{})
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if acq.buys != 0 {
		t.Fatalf("cushion is 1 below the working-capital floor (treasury=%d price=%d floor=%d): must NOT buy, got %d",
			obs.Treasury, price, contractWorkingCapitalFloor, acq.buys)
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
	obs := incomeObs() // 0 haulers against the fixed target of 4
	obs.BatchContractRunning = true
	acq := &fakeHaulerAcquirer{price: 100000, yard: "Y", readable: true}
	h := newIncomeHandler(obs, &fakeRetirer{}, acq, &fakeContractRunner{})
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if res.HaulersBought != 1 || acq.buys != 1 {
		t.Fatalf("short by 4 but must buy exactly 1 per tick: bought=%d buys=%d", res.HaulersBought, acq.buys)
	}
}

// --- placement skips a slot already served by a hauler (places on the next unserved slot) ---

func TestBootstrap_Income_PlacementSkipsServedSlot(t *testing.T) {
	obs := incomeObs()
	obs.BatchContractRunning = true
	obs.TradeHullCount = 1                                              // Post-seed — a 2nd+ contract hull buys (not the trade-seed)
	obs.Haulers = []HaulerSnapshot{{Symbol: "H1", Waypoint: "X1-HUBA"}} // first slot already served
	acq := &fakeHaulerAcquirer{price: 100000, yard: "Y", readable: true}
	h := newIncomeHandler(obs, &fakeRetirer{}, acq, &fakeContractRunner{})
	h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(acq.placedOn) != 1 || acq.placedOn[0] != "X1-HUBB" {
		t.Fatalf("served first slot must be skipped; expected placement on X1-HUBB, got %v", acq.placedOn)
	}
}

// --- the fixed hauler target is met → no buy ---

func TestBootstrap_Income_NoBuyWhenTargetMet(t *testing.T) {
	obs := incomeObs()
	obs.BatchContractRunning = true
	obs.TradeHullCount = 1          // Post-seed — isolate the target guard (no trade-seed detour)
	obs.Haulers = []HaulerSnapshot{ // haulerTarget haulers, one per slot → the fixed target is met
		{Waypoint: "X1-HUBA"}, {Waypoint: "X1-HUBB"}, {Waypoint: "X1-HUBC"}, {Waypoint: "X1-HUBD"},
	}
	acq := &fakeHaulerAcquirer{price: 100000, yard: "Y", readable: true}
	h := newIncomeHandler(obs, &fakeRetirer{}, acq, &fakeContractRunner{})
	h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if acq.buys != 0 {
		t.Fatalf("fixed target met (%d haulers): must not buy, got %d", haulerTarget, acq.buys)
	}
}

// --- recovery: a restart with haulers already at the cap never double-buys ---

func TestBootstrap_Income_Recovery_NoDoubleBuy(t *testing.T) {
	obs := incomeObs()
	obs.BatchContractRunning = true
	obs.TradeHullCount = 1 // Post-seed — isolate the recovery count guard (no trade-seed detour)
	obs.Haulers = []HaulerSnapshot{{Waypoint: "X1-HUBA"}, {Waypoint: "X1-HUBB"}, {Waypoint: "X1-HUBC"}, {Waypoint: "X1-HUBD"}}
	acq := &fakeHaulerAcquirer{price: 100000, yard: "Y", readable: true}
	h := newIncomeHandler(obs, &fakeRetirer{}, acq, &fakeContractRunner{})
	h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if acq.buys != 0 {
		t.Fatalf("target met on restart: must not double-buy, got %d", acq.buys)
	}
}

// --- an unresolved era (no delivery slots) → no placement target → no hauler buy (fail closed) ---

func TestBootstrap_Income_NoPlacementSlotsNoHaulerBuy(t *testing.T) {
	obs := incomeObs()
	obs.ContractPlacementSlots = nil
	obs.BatchContractRunning = true
	acq := &fakeHaulerAcquirer{price: 100000, yard: "Y", readable: true}
	h := newIncomeHandler(obs, &fakeRetirer{}, acq, &fakeContractRunner{})
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if acq.buys != 0 {
		t.Fatalf("no delivery slots → nowhere to place → no buy, got %d", acq.buys)
	}
	if res.Blocker != "no_placement_slot" {
		t.Fatalf("expected the no_placement_slot blocker, got %q", res.Blocker)
	}
}

// --- COLDSTART→GATE crossover: a scaled, funded op derives GATE, and the contract acts stop running.
// From the cold-start fixture (no gate site discovered, no GATE collaborators wired) GATE blocks on the
// undiscovered site rather than doing any contract work — the phase crossover is clean. ---

func TestBootstrap_ColdStartToGate_Crossover_NoContractAct(t *testing.T) {
	obs := incomeObs()
	obs.Haulers = []HaulerSnapshot{{Symbol: "H1"}, {Symbol: "H2"}} // full fleet = 2
	obs.ContractScalerTarget = 2                                   // == full fleet ⇒ scaler target reached (scaled op); incomeObs treasury clears the surplus floor
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
		t.Fatalf("in GATE the contract act must not run; retire=%d launch=%d buy=%d", ret.calls, run.calls, acq.buys)
	}
	// The cold-start fixture has no gate site, so GATE fails closed on discovery — never a "not implemented" hold.
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

// frigateLoopObs is a provisioned cold-start observation with the frigate present and no loop yet running —
// the state in which the frigate must be put on its earning loop. Batch-contract "running" and the
// hauler cap "met" isolate the assertion to the frigate-loop action.
func frigateLoopObs() Observation {
	obs := incomeObs()
	obs.ProbeCount = 3 // provisioning done (default probe_target 3)
	obs.CommandFrigateID = "FRIGATE-1"
	obs.FrigateContractLoopRunning = false
	obs.BatchContractRunning = true // isolate: don't also launch the coordinator
	// The frigate loop is the PRE-hauler earner — it starts only at 0 haulers. The staged buy
	// therefore also runs on this fixture; it is left fully affordable and placeable so it completes
	// without raising a blocker of its own, isolating these pins to the loop-start action.
	obs.Haulers = nil
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

// juggle order: buy the initial probes FIRST, THEN earn — the loop must not start while the
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
// contract collaborators' nil contract).
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

// the sp-rype stall reproduction: the frigate has finished its hour-0 shipyard run + probe buy (probes at
// target, scouting), so the arc is COLDSTART-labeled even though the home system is barely scanned (20%
// coverage — coverage no longer gates the label). The frigate must start EARNING rather than park idle at
// the yard — the fix for "sole earner dead, income never flows".
func TestBootstrap_Income_StartsFrigateLoopAtLowCoverage(t *testing.T) {
	obs := Observation{
		HomeSystem: "X1-HQ", ProbeCount: 3, ProbesScouting: 3, HasIdlePurchaser: true,
		Treasury: 120000, MarketsTotal: 10, MarketsCovered: 2, // 20% coverage — no economic signal → COLDSTART
		CommandFrigateID: "FRIGATE-1", FrigateContractLoopRunning: false, Readable: true,
	}
	loop := &fakeFrigateLoop{}
	h := newIncomeHandler(obs, &fakeRetirer{}, &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}, &fakeContractRunner{})
	h.SetFrigateContractLoopStarter(loop)
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if res.Phase != PhaseColdStart {
		t.Fatalf("provisioned probes must derive COLDSTART regardless of coverage (20%%), got %s", res.Phase)
	}
	if loop.calls != 1 || loop.ships[0] != "FRIGATE-1" {
		t.Fatalf("a provisioned frigate must start earning (contract loop), got calls=%d ships=%v", loop.calls, loop.ships)
	}
	if !res.FrigateLoopStarted {
		t.Fatalf("res.FrigateLoopStarted should be true for a provisioned frigate")
	}
}

// idempotent across ticks: the loop is started exactly once — after it starts, the observed
// earner-signal (FrigateContractLoopRunning) keeps every later tick from re-starting it.
func TestBootstrap_Income_FrigateLoopStartedExactlyOnce(t *testing.T) {
	world := &incomeWorld{
		treasury: 3000000, homeSystem: "X1", marketsTotal: 10, marketsCovered: 10,
		frigateID: "FRIGATE-1", frigateOnContract: false, batchRunning: true,
		probeCount:    3, // provisioned
		incomePerHour: 0, hasPurchaser: true,
		// The pre-hauler loop starts only at 0 haulers; leave NO viable hubs (markets/goods unset)
		// so the hauler-buy step is not "needed" — isolating the frigate-loop lifecycle across ticks.
	}
	loop := &fakeFrigateLoop{world: world}
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(&fakeIncomeObserver{world: world})
	h.SetProbeAcquirer(&fakeAcquirer{price: 40000, yard: "Y", readable: true})
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

// --- Contract acceptance (Slice 2): from a provisioned fixture, the arc retires the frigate, launches
// batch-contract, and stages the fixed hauler target one per tick, spread across distinct slots. ---

func TestBootstrap_IncomeAcceptance_RetiresLaunchesRampsHaulers(t *testing.T) {
	world := &incomeWorld{
		treasury: 3000000, homeSystem: "X1", marketsTotal: 10, marketsCovered: 10,
		frigateID: "FRIGATE-1", frigateOnContract: true, batchRunning: false,
		probeCount:     3, // provisioned (probe_target 3) → COLDSTART-labeled
		placementSlots: incomeSlots(),
		incomePerHour:  0, hasPurchaser: true,
		// Post-seed state (a trade hull already exists) so this test stays a PURE contract ramp;
		// the #1-contract → #2-trade routing is covered by TestBootstrap_TradeSeedAcceptance.
		tradeHullCount: 1,
	}
	ret := &fakeRetirer{world: world}
	acq := &fakeHaulerAcquirer{price: 200000, yard: "X1-YARD", readable: true, world: world}
	run := &fakeContractRunner{world: world}
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(&fakeIncomeObserver{world: world})
	h.SetProbeAcquirer(&fakeAcquirer{price: 40000, yard: "Y", readable: true})
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
	// The ramp climbs to the fixed hauler target, one hull per distinct slot.
	if len(final.Haulers) != haulerTarget {
		t.Fatalf("acceptance: expected %d haulers (the fixed target), got %d", haulerTarget, len(final.Haulers))
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
			t.Fatalf("acceptance: two haulers placed on the same slot %s (placements=%v)", hp, acq.placedOn)
		}
		seen[hp] = true
	}
	if acq.buys != haulerTarget {
		t.Fatalf("acceptance: expected exactly %d staged buys, got %d", haulerTarget, acq.buys)
	}
}

// --- REGRESSION: the ramp's hauler target is the FIXED Phase-1 constant, NOT the size of whatever set the
// tick happened to sense. A sparse read (here: two resolved parks, with both haulers out on deliveries
// rather than sitting on them) must not shrink the fleet the ramp is climbing to — sizing the target from
// the sensed set stalls the ramp at 2 forever, which is the stall this pins against. ---

func TestBootstrap_Income_HaulerTargetIsFixed_NotTheSensedSetSize(t *testing.T) {
	obs := incomeObs()
	obs.BatchContractRunning = true
	obs.TradeHullCount = 1 // post-seed: this is a contract buy, not the trade seed
	obs.ContractPlacementSlots = []string{"X1-HUBA", "X1-HUBB"}
	obs.Haulers = []HaulerSnapshot{{Symbol: "H1", Waypoint: "X1-SINK"}, {Symbol: "H2", Waypoint: "X1-SINK"}}
	acq := &fakeHaulerAcquirer{price: 100000, yard: "Y", readable: true}
	h := newIncomeHandler(obs, &fakeRetirer{}, acq, &fakeContractRunner{})
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if acq.buys != 1 || res.HaulersBought != 1 {
		t.Fatalf("2 haulers against the fixed target of %d must still buy (sensed slots=%d): buys=%d bought=%d blocker=%q",
			haulerTarget, len(obs.ContractPlacementSlots), acq.buys, res.HaulersBought, res.Blocker)
	}
}
