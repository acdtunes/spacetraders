package commands

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	gasCmd "github.com/andrescamacho/spacetraders-go/internal/application/gas/commands"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	storageApp "github.com/andrescamacho/spacetraders-go/internal/application/storage"
	tradingsvc "github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// stkFixture is the shared mutable world the stocker fakes read/write: the hull's live
// cargo + location (advanced by the fake mediator on buy/navigate/transfer) and the
// per-waypoint market prices + data age (read for the freshness gate and, honoring the
// per-tranche ceiling, to price + fail-close a laddered buy).
type stkFixture struct {
	mu        sync.Mutex
	cargo     map[string]int
	location  string
	cargoCap  int
	ask       map[string]map[string]int     // waypoint -> good -> ask (SellPrice, buy cost)
	marketAge map[string]time.Duration      // waypoint -> how old the cached data is (0 = fresh/now)
	buys      int
	buyUnits  int
	transfers int
	navs      []string
}

func (fx *stkFixture) buildShip(t *testing.T, symbol string) *navigation.Ship {
	t.Helper()
	fx.mu.Lock()
	defer fx.mu.Unlock()
	var inv []*shared.CargoItem
	total := 0
	for good, units := range fx.cargo {
		if units > 0 {
			inv = append(inv, &shared.CargoItem{Symbol: good, Units: units})
			total += units
		}
	}
	cargo, err := shared.NewCargo(fx.cargoCap, total, inv)
	if err != nil {
		t.Fatalf("cargo: %v", err)
	}
	fuel, err := shared.NewFuel(1000, 1000)
	if err != nil {
		t.Fatalf("fuel: %v", err)
	}
	wp, err := shared.NewWaypoint(fx.location, 0, 0)
	if err != nil {
		t.Fatalf("waypoint: %v", err)
	}
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(1), wp, fuel, 1000, fx.cargoCap, cargo, 30,
		"FRAME_LIGHT_FREIGHTER", "HAULER", nil, navigation.NavStatusDocked)
	if err != nil {
		t.Fatalf("ship: %v", err)
	}
	return ship
}

// stkFakeMediator advances the world on the stocker's dispatches. Unlike the tour fake it
// HONORS the per-tranche buy ceiling (MaxAskPerUnit): a live ask above the ceiling aborts
// the purchase with CeilingAborted and zero units — the sp-9mkf live-verify seam.
type stkFakeMediator struct {
	fx *stkFixture
}

func (m *stkFakeMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	switch cmd := request.(type) {
	case *navCmd.NavigateRouteCommand:
		m.fx.mu.Lock()
		m.fx.location = cmd.Destination
		m.fx.navs = append(m.fx.navs, cmd.Destination)
		m.fx.mu.Unlock()
		return nil, nil
	case *shipCargo.PurchaseCargoCommand:
		m.fx.mu.Lock()
		defer m.fx.mu.Unlock()
		liveAsk := m.fx.ask[m.fx.location][cmd.GoodSymbol]
		if cmd.MaxAskPerUnit > 0 && liveAsk > cmd.MaxAskPerUnit {
			// Live-verify ceiling tripped: nothing bought, remainder aborted fail-closed.
			return &shipCargo.PurchaseCargoResponse{UnitsAdded: 0, TotalCost: 0, CeilingAborted: true, CeilingObservedAsk: liveAsk}, nil
		}
		units := cmd.Units
		m.fx.cargo[cmd.GoodSymbol] += units
		m.fx.buys++
		m.fx.buyUnits += units
		return &shipCargo.PurchaseCargoResponse{UnitsAdded: units, TotalCost: units * liveAsk, TransactionCount: 1}, nil
	case *gasCmd.TransferCargoCommand:
		// A deposit transfer: the good LEAVES the hull into the warehouse hull.
		m.fx.mu.Lock()
		m.fx.cargo[cmd.GoodSymbol] -= cmd.Units
		m.fx.transfers++
		m.fx.mu.Unlock()
		return &gasCmd.TransferCargoResponse{UnitsTransferred: cmd.Units}, nil
	default:
		return nil, nil // dock, orbit, etc. succeed silently
	}
}

func (m *stkFakeMediator) Register(requestType reflect.Type, handler common.RequestHandler) error {
	return nil
}
func (m *stkFakeMediator) RegisterMiddleware(middleware common.Middleware) {}

type stkFakeMarketRepo struct {
	market.MarketRepository
	fx *stkFixture
	t  *testing.T
}

func (r *stkFakeMarketRepo) GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*market.Market, error) {
	r.fx.mu.Lock()
	defer r.fx.mu.Unlock()
	goods, ok := r.fx.ask[waypointSymbol]
	if !ok {
		return nil, nil
	}
	supply, activity := "MODERATE", "STRONG"
	var tgs []market.TradeGood
	for good, ask := range goods {
		g, err := market.NewTradeGood(good, &supply, &activity, ask-10, ask, 1000, market.TradeTypeExport)
		if err != nil {
			r.t.Fatalf("trade good: %v", err)
		}
		tgs = append(tgs, *g)
	}
	observed := time.Now().Add(-r.fx.marketAge[waypointSymbol])
	m, err := market.NewMarket(waypointSymbol, tgs, observed)
	if err != nil {
		r.t.Fatalf("market: %v", err)
	}
	return m, nil
}

type stkFakeShipRepo struct {
	navigation.ShipRepository
	fx *stkFixture
	t  *testing.T
}

func (r *stkFakeShipRepo) FindBySymbol(ctx context.Context, symbol string, playerID shared.PlayerID) (*navigation.Ship, error) {
	return r.fx.buildShip(r.t, symbol), nil
}
func (r *stkFakeShipRepo) Save(ctx context.Context, ship *navigation.Ship) error { return nil }
func (r *stkFakeShipRepo) SyncShipFromAPI(ctx context.Context, symbol string, playerID shared.PlayerID) (*navigation.Ship, error) {
	return r.fx.buildShip(r.t, symbol), nil
}

// stkFakeMiner returns canned demand candidates (the Lane A miner port the pick ranks from).
type stkFakeMiner struct {
	rows []persistence.DemandCandidate
	err  error
}

func (m *stkFakeMiner) Mine(ctx context.Context, homeSystem string, playerID int, eraID *int, opts persistence.DemandMinerOptions) ([]persistence.DemandCandidate, error) {
	return m.rows, m.err
}

// eligible builds a stock-eligible candidate row (both asks known, home > foreign).
func eligible(good, foreignMkt string, foreignAsk, homeAsk, demandUnits int) persistence.DemandCandidate {
	return persistence.DemandCandidate{
		Good: good, DemandUnits: demandUnits,
		ForeignMarket: foreignMkt, ForeignSystem: shared.ExtractSystemSymbol(foreignMkt), ForeignAsk: foreignAsk,
		HomeAsk: homeAsk, HomeAskKnown: true,
		ProjectedSavingsPerUnit: homeAsk - foreignAsk, StockEligible: homeAsk-foreignAsk > 0,
	}
}

// stkWireWarehouse builds a REAL in-memory storage coordinator + running warehouse op at
// waypoint holding a capacity storage hull that buffers goods (the Lane B harness).
func stkWireWarehouse(t *testing.T, opID, waypoint string, capacity int, goods []string) (*storageApp.InMemoryStorageCoordinator, *storage.StorageOperation) {
	t.Helper()
	coord := storageApp.NewInMemoryStorageCoordinator()
	whShip, err := storage.NewStorageShip(opID+"-WH", waypoint, opID, capacity, nil)
	if err != nil {
		t.Fatalf("storage ship: %v", err)
	}
	if err := coord.RegisterStorageShip(whShip); err != nil {
		t.Fatalf("register: %v", err)
	}
	op, err := storage.NewWarehouseOperation(opID, 1, waypoint, []string{opID + "-WH"}, goods, shared.NewRealClock())
	if err != nil {
		t.Fatalf("warehouse op: %v", err)
	}
	if err := op.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	return coord, op
}

func newStockerHandler(t *testing.T, fx *stkFixture, coord storage.StorageCoordinator, op *storage.StorageOperation, miner tradingsvc.DepositDemandMiner, apiClient domainPorts.APIClient, cfg tradingsvc.DepositCandidateConfig, ceilingPct int) *RunStockerCoordinatorHandler {
	finder := &fakeRunningFinder{ops: []*storage.StorageOperation{op}}
	return NewRunStockerCoordinatorHandler(
		&stkFakeMediator{fx: fx},
		&stkFakeShipRepo{fx: fx, t: t},
		&stkFakeMarketRepo{fx: fx, t: t},
		nil, // marketRefresher: unused (the fake mediator honors the ceiling directly)
		&trFakeClock{},
		apiClient,
		coord, finder, miner, cfg, ceilingPct,
	)
}

// stkWarehouseOpAt builds a RUNNING warehouse operation with id at waypoint, created at
// createdAt (via a MockClock pinned to that instant, so CreatedAt() is fully
// controllable) — used to pin the sp-3lj5 zombie-row collision shape directly at the
// stocker's warehouseAt call site.
func stkWarehouseOpAt(t *testing.T, id, waypoint string, createdAt time.Time) *storage.StorageOperation {
	t.Helper()
	op, err := storage.NewWarehouseOperation(id, 1, waypoint, []string{id + "-WH"}, []string{"FOOD"}, &shared.MockClock{CurrentTime: createdAt})
	if err != nil {
		t.Fatalf("warehouse op %s: %v", id, err)
	}
	if err := op.Start(); err != nil {
		t.Fatalf("start %s: %v", id, err)
	}
	return op
}

// TestStockerWarehouseAt_ResolvesToNewestOnZombieCollision is the regression pin for
// sp-3lj5 at the stocker's own warehouseAt call site: warehouse-TORWIND-12-bad719ff
// was STOPPED at 15:24Z but its storage_operations row was never terminalized (fixed
// separately in daemon_server.go), so it still surfaced as RUNNING alongside its live
// replacement warehouse-TORWIND-12-3477282e at the same waypoint — and the finder
// returned the zombie FIRST, matching the actual incident order. The old
// first-match-wins loop picked the dead operation (which always reads back zero free
// space), logging "warehouse full (0 free space)" three times and exiting, while the
// live warehouse sat at 80/80 free. warehouseAt must resolve to the live operation.
func TestStockerWarehouseAt_ResolvesToNewestOnZombieCollision(t *testing.T) {
	t0 := time.Date(2026, 7, 1, 15, 24, 0, 0, time.UTC)
	zombie := stkWarehouseOpAt(t, "warehouse-TORWIND-12-bad719ff", "X1-TORWIND-12", t0)
	live := stkWarehouseOpAt(t, "warehouse-TORWIND-12-3477282e", "X1-TORWIND-12", t0.Add(2*time.Hour))
	finder := &fakeRunningFinder{ops: []*storage.StorageOperation{zombie, live}}
	h := NewRunStockerCoordinatorHandler(nil, nil, nil, nil, nil, nil, nil, finder, nil, tradingsvc.DepositCandidateConfig{}, 0)

	got := h.warehouseAt(context.Background(), 1, "X1-TORWIND-12")

	if got == nil || got.ID() != live.ID() {
		t.Fatalf("want live op %s, got %v", live.ID(), got)
	}
}

// TestStockerWarehouseAt_NoRunningWarehouseReturnsNil is the fail-closed sanity check:
// with nothing RUNNING at the waypoint (e.g. the only warehouse there is now properly
// terminalized-stopped, so FindRunning no longer returns it), warehouseAt returns nil
// rather than guessing — the caller then treats the pass as empty (RULINGS #6, never
// speculative stocking).
func TestStockerWarehouseAt_NoRunningWarehouseReturnsNil(t *testing.T) {
	finder := &fakeRunningFinder{ops: nil}
	h := NewRunStockerCoordinatorHandler(nil, nil, nil, nil, nil, nil, nil, finder, nil, tradingsvc.DepositCandidateConfig{}, 0)

	got := h.warehouseAt(context.Background(), 1, "X1-TORWIND-12")

	if got != nil {
		t.Fatalf("want nil, got %v", got)
	}
}

func stkResponse(t *testing.T, resp interface{}) *RunStockerCoordinatorResponse {
	t.Helper()
	r, ok := resp.(*RunStockerCoordinatorResponse)
	if !ok || r == nil {
		t.Fatalf("unexpected response %T", resp)
	}
	return r
}

// ---- pick: need-ranking + the fail-closed cap matrix (each cap excludes) ----

// The pick chooses the highest (savings/u × units-short), not the highest total demand:
// MEDICINE (savings 700 × 40 short = 28000) beats FOOD (savings 100 × 200 short = 20000).
func TestStocker_Pick_MostNeededByValue(t *testing.T) {
	fx := &stkFixture{cargo: map[string]int{}, location: "X1-S1-H", cargoCap: 100,
		ask:       map[string]map[string]int{"X1-S1-M": {"MEDICINE": 2100}, "X1-S1-F": {"FOOD": 20}},
		marketAge: map[string]time.Duration{}}
	coord, op := stkWireWarehouse(t, "wh", "X1-S1-H", 5000, []string{"MEDICINE", "FOOD"})
	miner := &stkFakeMiner{rows: []persistence.DemandCandidate{
		eligible("FOOD", "X1-S1-F", 20, 120, 200),
		eligible("MEDICINE", "X1-S1-M", 2100, 2800, 40),
	}}
	h := newStockerHandler(t, fx, coord, op, miner, &sfFakeAPIClient{credits: 100000000}, tradingsvc.DepositCandidateConfig{}, 10)

	ctx := auth.WithPlayerToken(context.Background(), "TOK")
	pick, ok := h.pick(ctx, &RunStockerCoordinatorCommand{ShipSymbol: "S", PlayerID: 1, WarehouseWaypoint: "X1-S1-H"}, op, int64(defaultWorkingCapitalReserve), maxListingAge)
	if !ok {
		t.Fatalf("expected a pick")
	}
	if pick.Good != "MEDICINE" {
		t.Fatalf("expected MEDICINE (highest savings×units-short), got %s", pick.Good)
	}
	if pick.Units != 40 {
		t.Fatalf("expected to haul all 40 short units, got %d", pick.Units)
	}
}

// A candidate below the min-savings floor is excluded even if it is the only one.
func TestStocker_Pick_MinSavingsExcludes(t *testing.T) {
	fx := &stkFixture{cargo: map[string]int{}, location: "X1-S1-H", cargoCap: 100,
		ask: map[string]map[string]int{"X1-S1-M": {"MEDICINE": 2100}}, marketAge: map[string]time.Duration{}}
	coord, op := stkWireWarehouse(t, "wh", "X1-S1-H", 5000, []string{"MEDICINE"})
	// savings 50/u; min-savings floor 100 → excluded.
	miner := &stkFakeMiner{rows: []persistence.DemandCandidate{eligible("MEDICINE", "X1-S1-M", 2100, 2150, 40)}}
	h := newStockerHandler(t, fx, coord, op, miner, &sfFakeAPIClient{credits: 100000000}, tradingsvc.DepositCandidateConfig{MinSavingsPerUnit: 100}, 10)

	ctx := auth.WithPlayerToken(context.Background(), "TOK")
	if _, ok := h.pick(ctx, &RunStockerCoordinatorCommand{ShipSymbol: "S", PlayerID: 1, WarehouseWaypoint: "X1-S1-H"}, op, int64(defaultWorkingCapitalReserve), maxListingAge); ok {
		t.Fatalf("a below-min-savings candidate must be excluded")
	}
}

// The capital ceiling (10% of live treasury, junior to the reserve) caps the haul below
// units-short. Treasury 700000 → ceiling 70000; at ask 2100 that is 33 units, under the
// 40 short.
func TestStocker_Pick_CeilingCapsUnits(t *testing.T) {
	fx := &stkFixture{cargo: map[string]int{}, location: "X1-S1-H", cargoCap: 100,
		ask: map[string]map[string]int{"X1-S1-M": {"MEDICINE": 2100}}, marketAge: map[string]time.Duration{}}
	coord, op := stkWireWarehouse(t, "wh", "X1-S1-H", 5000, []string{"MEDICINE"})
	miner := &stkFakeMiner{rows: []persistence.DemandCandidate{eligible("MEDICINE", "X1-S1-M", 2100, 2800, 40)}}
	h := newStockerHandler(t, fx, coord, op, miner, &sfFakeAPIClient{credits: 700000}, tradingsvc.DepositCandidateConfig{}, 10)

	ctx := auth.WithPlayerToken(context.Background(), "TOK")
	pick, ok := h.pick(ctx, &RunStockerCoordinatorCommand{ShipSymbol: "S", PlayerID: 1, WarehouseWaypoint: "X1-S1-H"}, op, int64(defaultWorkingCapitalReserve), maxListingAge)
	if !ok {
		t.Fatalf("expected a pick within the ceiling")
	}
	if pick.Units != 33 { // 70000 / 2100 = 33
		t.Fatalf("expected the capital ceiling to cap the haul to 33 units, got %d", pick.Units)
	}
}

// --budget-per-leg caps the haul below units-short: budget 42000 at ask 2100 = 20 units.
func TestStocker_Pick_BudgetPerLegCapsUnits(t *testing.T) {
	fx := &stkFixture{cargo: map[string]int{}, location: "X1-S1-H", cargoCap: 100,
		ask: map[string]map[string]int{"X1-S1-M": {"MEDICINE": 2100}}, marketAge: map[string]time.Duration{}}
	coord, op := stkWireWarehouse(t, "wh", "X1-S1-H", 5000, []string{"MEDICINE"})
	miner := &stkFakeMiner{rows: []persistence.DemandCandidate{eligible("MEDICINE", "X1-S1-M", 2100, 2800, 40)}}
	h := newStockerHandler(t, fx, coord, op, miner, &sfFakeAPIClient{credits: 100000000}, tradingsvc.DepositCandidateConfig{}, 10)

	ctx := auth.WithPlayerToken(context.Background(), "TOK")
	pick, ok := h.pick(ctx, &RunStockerCoordinatorCommand{ShipSymbol: "S", PlayerID: 1, WarehouseWaypoint: "X1-S1-H", BudgetPerLeg: 42000}, op, int64(defaultWorkingCapitalReserve), maxListingAge)
	if !ok {
		t.Fatalf("expected a pick within the per-leg budget")
	}
	if pick.Units != 20 { // 42000 / 2100 = 20
		t.Fatalf("expected the per-leg budget to cap the haul to 20 units, got %d", pick.Units)
	}
}

// Warehouse free space caps the haul: a warehouse with only 15 free units caps the 40-short
// haul to 15.
func TestStocker_Pick_WarehouseSpaceCapsUnits(t *testing.T) {
	fx := &stkFixture{cargo: map[string]int{}, location: "X1-S1-H", cargoCap: 100,
		ask: map[string]map[string]int{"X1-S1-M": {"MEDICINE": 2100}}, marketAge: map[string]time.Duration{}}
	coord, op := stkWireWarehouse(t, "wh", "X1-S1-H", 15, []string{"MEDICINE"})
	miner := &stkFakeMiner{rows: []persistence.DemandCandidate{eligible("MEDICINE", "X1-S1-M", 2100, 2800, 40)}}
	h := newStockerHandler(t, fx, coord, op, miner, &sfFakeAPIClient{credits: 100000000}, tradingsvc.DepositCandidateConfig{}, 10)

	ctx := auth.WithPlayerToken(context.Background(), "TOK")
	pick, ok := h.pick(ctx, &RunStockerCoordinatorCommand{ShipSymbol: "S", PlayerID: 1, WarehouseWaypoint: "X1-S1-H"}, op, int64(defaultWorkingCapitalReserve), maxListingAge)
	if !ok {
		t.Fatalf("expected a pick within warehouse space")
	}
	if pick.Units != 15 {
		t.Fatalf("expected warehouse free space to cap the haul to 15 units, got %d", pick.Units)
	}
}

// A good already stocked to its target is excluded (units-short <= 0).
func TestStocker_Pick_AtTargetExcludes(t *testing.T) {
	fx := &stkFixture{cargo: map[string]int{}, location: "X1-S1-H", cargoCap: 100,
		ask: map[string]map[string]int{"X1-S1-M": {"MEDICINE": 2100}}, marketAge: map[string]time.Duration{}}
	coord, op := stkWireWarehouse(t, "wh", "X1-S1-H", 5000, []string{"MEDICINE"})
	// Pre-stock the warehouse to the target (40): deposit 40 first.
	ship, reserved, ok := coord.ReserveSpaceForDeposit("wh", 40)
	if !ok {
		t.Fatalf("pre-stock reserve failed")
	}
	coord.ConfirmDeposit(ship.ShipSymbol(), "MEDICINE", reserved)
	miner := &stkFakeMiner{rows: []persistence.DemandCandidate{eligible("MEDICINE", "X1-S1-M", 2100, 2800, 40)}}
	h := newStockerHandler(t, fx, coord, op, miner, &sfFakeAPIClient{credits: 100000000}, tradingsvc.DepositCandidateConfig{}, 10)

	ctx := auth.WithPlayerToken(context.Background(), "TOK")
	if _, ok := h.pick(ctx, &RunStockerCoordinatorCommand{ShipSymbol: "S", PlayerID: 1, WarehouseWaypoint: "X1-S1-H"}, op, int64(defaultWorkingCapitalReserve), maxListingAge); ok {
		t.Fatalf("a good already at target must be excluded")
	}
}

// An UNREADABLE live balance stocks nothing (fail closed, RULINGS #4): the ceiling is
// unknown, so pick returns false even with an otherwise-perfect candidate.
func TestStocker_Pick_UnreadableBalanceFailsClosed(t *testing.T) {
	fx := &stkFixture{cargo: map[string]int{}, location: "X1-S1-H", cargoCap: 100,
		ask: map[string]map[string]int{"X1-S1-M": {"MEDICINE": 2100}}, marketAge: map[string]time.Duration{}}
	coord, op := stkWireWarehouse(t, "wh", "X1-S1-H", 5000, []string{"MEDICINE"})
	miner := &stkFakeMiner{rows: []persistence.DemandCandidate{eligible("MEDICINE", "X1-S1-M", 2100, 2800, 40)}}
	h := newStockerHandler(t, fx, coord, op, miner, &sfFakeAPIClient{err: errors.New("agent API unavailable")}, tradingsvc.DepositCandidateConfig{}, 10)

	ctx := auth.WithPlayerToken(context.Background(), "TOK")
	if _, ok := h.pick(ctx, &RunStockerCoordinatorCommand{ShipSymbol: "S", PlayerID: 1, WarehouseWaypoint: "X1-S1-H"}, op, int64(defaultWorkingCapitalReserve), maxListingAge); ok {
		t.Fatalf("an unreadable balance must stock nothing (fail closed)")
	}
}

// A good the warehouse does not BUFFER is excluded (it would strand — no worker could
// withdraw it).
func TestStocker_Pick_UnsupportedGoodExcludes(t *testing.T) {
	fx := &stkFixture{cargo: map[string]int{}, location: "X1-S1-H", cargoCap: 100,
		ask: map[string]map[string]int{"X1-S1-M": {"MEDICINE": 2100}}, marketAge: map[string]time.Duration{}}
	coord, op := stkWireWarehouse(t, "wh", "X1-S1-H", 5000, []string{"FOOD"}) // supports FOOD, not MEDICINE
	miner := &stkFakeMiner{rows: []persistence.DemandCandidate{eligible("MEDICINE", "X1-S1-M", 2100, 2800, 40)}}
	h := newStockerHandler(t, fx, coord, op, miner, &sfFakeAPIClient{credits: 100000000}, tradingsvc.DepositCandidateConfig{}, 10)

	ctx := auth.WithPlayerToken(context.Background(), "TOK")
	if _, ok := h.pick(ctx, &RunStockerCoordinatorCommand{ShipSymbol: "S", PlayerID: 1, WarehouseWaypoint: "X1-S1-H"}, op, int64(defaultWorkingCapitalReserve), maxListingAge); ok {
		t.Fatalf("a good the warehouse does not support must be excluded")
	}
}

// A candidate whose foreign market data is older than the freshness cap is excluded (do
// not haul to a stale price).
func TestStocker_Pick_StaleForeignMarketExcludes(t *testing.T) {
	fx := &stkFixture{cargo: map[string]int{}, location: "X1-S1-H", cargoCap: 100,
		ask:       map[string]map[string]int{"X1-S1-M": {"MEDICINE": 2100}},
		marketAge: map[string]time.Duration{"X1-S1-M": 76 * time.Minute}} // > 75-min cap
	coord, op := stkWireWarehouse(t, "wh", "X1-S1-H", 5000, []string{"MEDICINE"})
	miner := &stkFakeMiner{rows: []persistence.DemandCandidate{eligible("MEDICINE", "X1-S1-M", 2100, 2800, 40)}}
	h := newStockerHandler(t, fx, coord, op, miner, &sfFakeAPIClient{credits: 100000000}, tradingsvc.DepositCandidateConfig{}, 10)

	ctx := auth.WithPlayerToken(context.Background(), "TOK")
	if _, ok := h.pick(ctx, &RunStockerCoordinatorCommand{ShipSymbol: "S", PlayerID: 1, WarehouseWaypoint: "X1-S1-H"}, op, int64(defaultWorkingCapitalReserve), maxListingAge); ok {
		t.Fatalf("a stale foreign market must be excluded (freshness discipline)")
	}
}

// ---- buy: live-verify aborts on a laddered ask; the reserve floor blocks pre-spend ----

// The live ask at the dock has laddered above the ceiling (foreign ask + tolerance): the
// buy aborts pre-spend with zero units bought.
func TestStocker_Buy_LiveAskCeilingAbortsPreSpend(t *testing.T) {
	// Miner's foreign ask 2000 → ceiling 2000×1.15 = 2300; the LIVE ask at the dock is 5000.
	fx := &stkFixture{cargo: map[string]int{}, location: "X1-S1-H", cargoCap: 100,
		ask: map[string]map[string]int{"X1-S1-M": {"MEDICINE": 5000}}, marketAge: map[string]time.Duration{}}
	coord, op := stkWireWarehouse(t, "wh", "X1-S1-H", 5000, []string{"MEDICINE"})
	h := newStockerHandler(t, fx, coord, op, &stkFakeMiner{}, &sfFakeAPIClient{credits: 100000000}, tradingsvc.DepositCandidateConfig{}, 10)

	ctx := auth.WithPlayerToken(context.Background(), "TOK")
	resp := &RunStockerCoordinatorResponse{}
	bought, err := h.buy(ctx, &RunStockerCoordinatorCommand{ShipSymbol: "S", PlayerID: 1, WarehouseWaypoint: "X1-S1-H"},
		stockerPick{Good: "MEDICINE", ForeignMarket: "X1-S1-M", ForeignAsk: 2000, Units: 40}, resp, int64(defaultWorkingCapitalReserve))
	if err != nil {
		t.Fatalf("buy returned error: %v", err)
	}
	if bought != 0 {
		t.Fatalf("a laddered live ask must abort pre-spend, got %d bought", bought)
	}
	if resp.TotalSpent != 0 || fx.buyUnits != 0 {
		t.Fatalf("aborted buy must spend nothing, got spent=%d units=%d", resp.TotalSpent, fx.buyUnits)
	}
}

// The working-capital reserve floor blocks a buy that would drop live treasury below it —
// no purchase is even dispatched.
func TestStocker_Buy_ReserveFloorBlocks(t *testing.T) {
	// Treasury 60000, reserve 50000: a 40×2100=84000 buy would drop to -24000 → blocked.
	fx := &stkFixture{cargo: map[string]int{}, location: "X1-S1-H", cargoCap: 100,
		ask: map[string]map[string]int{"X1-S1-M": {"MEDICINE": 2100}}, marketAge: map[string]time.Duration{}}
	coord, op := stkWireWarehouse(t, "wh", "X1-S1-H", 5000, []string{"MEDICINE"})
	h := newStockerHandler(t, fx, coord, op, &stkFakeMiner{}, &sfFakeAPIClient{credits: 60000}, tradingsvc.DepositCandidateConfig{}, 10)

	ctx := auth.WithPlayerToken(context.Background(), "TOK")
	resp := &RunStockerCoordinatorResponse{}
	bought, err := h.buy(ctx, &RunStockerCoordinatorCommand{ShipSymbol: "S", PlayerID: 1, WarehouseWaypoint: "X1-S1-H"},
		stockerPick{Good: "MEDICINE", ForeignMarket: "X1-S1-M", ForeignAsk: 2100, Units: 40}, resp, int64(defaultWorkingCapitalReserve))
	if err != nil {
		t.Fatalf("buy returned error: %v", err)
	}
	if bought != 0 {
		t.Fatalf("the reserve floor must block the buy, got %d bought", bought)
	}
	if fx.buys != 0 {
		t.Fatalf("no purchase must be dispatched when the reserve blocks it, got %d buys", fx.buys)
	}
}

// ---- loop: full round-trip, starvation, stranded veto, restart-resume ----

// One round-trip picks, buys live-verified, hauls home, and deposits — the warehouse gains
// the stocked units, no revenue is booked, and the run completes honestly.
func TestStocker_FullRoundTrip_StocksWarehouse(t *testing.T) {
	fx := &stkFixture{cargo: map[string]int{}, location: "X1-S1-H", cargoCap: 100,
		ask: map[string]map[string]int{"X1-S1-M": {"MEDICINE": 100}}, marketAge: map[string]time.Duration{}}
	coord, op := stkWireWarehouse(t, "wh", "X1-S1-H", 1000, []string{"MEDICINE"})
	miner := &stkFakeMiner{rows: []persistence.DemandCandidate{eligible("MEDICINE", "X1-S1-M", 100, 800, 40)}}
	h := newStockerHandler(t, fx, coord, op, miner, &sfFakeAPIClient{credits: 5000000}, tradingsvc.DepositCandidateConfig{}, 10)

	ctx := auth.WithPlayerToken(context.Background(), "TOK")
	resp, err := h.Handle(ctx, &RunStockerCoordinatorCommand{ShipSymbol: "STOCKER-1", PlayerID: 1, ContainerID: "ctr-1", WarehouseWaypoint: "X1-S1-H"})
	if err != nil {
		t.Fatalf("stocker returned error: %v", err)
	}
	r := stkResponse(t, resp)

	if got := coord.GetTotalCargoAvailable("wh", "MEDICINE"); got != 40 {
		t.Fatalf("warehouse should hold 40 deposited MEDICINE, got %d", got)
	}
	if r.RoundTripsCompleted != 1 || r.UnitsDeposited != 40 {
		t.Fatalf("expected 1 round-trip / 40 deposited, got %d / %d", r.RoundTripsCompleted, r.UnitsDeposited)
	}
	if r.TotalSpent != 40*100 {
		t.Fatalf("expected the foreign buy cost 4000, got spent %d", r.TotalSpent)
	}
	if r.CargoStranded {
		t.Fatalf("a clean round-trip must not strand: %s", r.CargoStrandedReason)
	}
	if !r.Completed {
		t.Fatalf("expected honest completion, got %+v", r)
	}
	if ok, reason := r.CompletionOutcome(); !ok {
		t.Fatalf("expected honest completion, got veto: %s", reason)
	}
}

// Nothing to stock (an empty demand table) exits HONESTLY after the starvation streak: no
// buys, a completed container, no strand.
func TestStocker_StarvationExit_Honest(t *testing.T) {
	fx := &stkFixture{cargo: map[string]int{}, location: "X1-S1-H", cargoCap: 100,
		ask: map[string]map[string]int{}, marketAge: map[string]time.Duration{}}
	coord, op := stkWireWarehouse(t, "wh", "X1-S1-H", 1000, []string{"MEDICINE"})
	h := newStockerHandler(t, fx, coord, op, &stkFakeMiner{}, &sfFakeAPIClient{credits: 5000000}, tradingsvc.DepositCandidateConfig{}, 10)

	ctx := auth.WithPlayerToken(context.Background(), "TOK")
	resp, err := h.Handle(ctx, &RunStockerCoordinatorCommand{ShipSymbol: "STOCKER-2", PlayerID: 1, ContainerID: "ctr-2", WarehouseWaypoint: "X1-S1-H", Iterations: -1})
	if err != nil {
		t.Fatalf("stocker returned error: %v", err)
	}
	r := stkResponse(t, resp)

	if r.ExitReason != stockerExitStarvation {
		t.Fatalf("expected starvation exit, got %q", r.ExitReason)
	}
	if r.RoundTripsCompleted != 0 || fx.buys != 0 {
		t.Fatalf("nothing to stock must run no round-trips / no buys, got %d / %d", r.RoundTripsCompleted, fx.buys)
	}
	if !r.Completed || r.CargoStranded {
		t.Fatalf("starvation is an HONEST completion, got %+v", r)
	}
}

// A hull that ends the run laden with undeposited cargo — a restart into a FULL warehouse:
// the resume path cannot deposit, so after the starvation streak the run vetoes FAILED.
func TestStocker_StrandedVeto_WhenWarehouseFull(t *testing.T) {
	fx := &stkFixture{cargo: map[string]int{"MEDICINE": 40}, location: "X1-S1-H", cargoCap: 100,
		ask: map[string]map[string]int{}, marketAge: map[string]time.Duration{}}
	coord, op := stkWireWarehouse(t, "wh", "X1-S1-H", 0, []string{"MEDICINE"}) // 0 capacity → full
	h := newStockerHandler(t, fx, coord, op, &stkFakeMiner{}, &sfFakeAPIClient{credits: 5000000}, tradingsvc.DepositCandidateConfig{}, 10)

	ctx := auth.WithPlayerToken(context.Background(), "TOK")
	resp, err := h.Handle(ctx, &RunStockerCoordinatorCommand{ShipSymbol: "STOCKER-3", PlayerID: 1, ContainerID: "ctr-3", WarehouseWaypoint: "X1-S1-H", Iterations: -1})
	if err != nil {
		t.Fatalf("a stranded stocker vetoes via CompletionOutcome, not a Go error; got %v", err)
	}
	r := stkResponse(t, resp)

	ok, reason := r.CompletionOutcome()
	if ok {
		t.Fatalf("expected a stranded-cargo veto, got clean completion: %+v", r)
	}
	if !r.CargoStranded {
		t.Fatalf("expected CargoStranded set, got %+v", r)
	}
	if reason == "" || fx.transfers != 0 {
		t.Fatalf("full warehouse must deposit nothing and name the strand, reason=%q transfers=%d", reason, fx.transfers)
	}
}

// A restart resumes DEPOSIT-FIRST: a hull that boots laden (bought in a prior interrupted
// round-trip) deposits the held cargo before buying more — never a blind re-buy (RULINGS #2).
func TestStocker_RestartResume_DepositsHeldCargoFirst(t *testing.T) {
	fx := &stkFixture{cargo: map[string]int{"MEDICINE": 40}, location: "X1-S1-H", cargoCap: 100,
		ask: map[string]map[string]int{"X1-S1-M": {"MEDICINE": 100}}, marketAge: map[string]time.Duration{}}
	coord, op := stkWireWarehouse(t, "wh", "X1-S1-H", 1000, []string{"MEDICINE"})
	// A miner that WOULD offer a buy — the resume path must deposit first and not re-buy.
	miner := &stkFakeMiner{rows: []persistence.DemandCandidate{eligible("MEDICINE", "X1-S1-M", 100, 800, 40)}}
	h := newStockerHandler(t, fx, coord, op, miner, &sfFakeAPIClient{credits: 5000000}, tradingsvc.DepositCandidateConfig{}, 1)

	ctx := auth.WithPlayerToken(context.Background(), "TOK")
	resp, err := h.Handle(ctx, &RunStockerCoordinatorCommand{ShipSymbol: "STOCKER-4", PlayerID: 1, ContainerID: "ctr-4", WarehouseWaypoint: "X1-S1-H", Iterations: 1})
	if err != nil {
		t.Fatalf("stocker returned error: %v", err)
	}
	r := stkResponse(t, resp)

	if got := coord.GetTotalCargoAvailable("wh", "MEDICINE"); got != 40 {
		t.Fatalf("the resume path must deposit the held 40 MEDICINE, warehouse holds %d", got)
	}
	if fx.buys != 0 {
		t.Fatalf("resume must deposit the held cargo, NOT re-buy — got %d buys", fx.buys)
	}
	if r.CargoStranded {
		t.Fatalf("the held cargo was deposited — must not strand: %s", r.CargoStrandedReason)
	}
	if r.UnitsDeposited != 40 {
		t.Fatalf("expected 40 deposited on the resume, got %d", r.UnitsDeposited)
	}
}
