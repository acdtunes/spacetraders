package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	gasCmd "github.com/andrescamacho/spacetraders-go/internal/application/gas/commands"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipQueries "github.com/andrescamacho/spacetraders-go/internal/application/ship/queries"
	storageApp "github.com/andrescamacho/spacetraders-go/internal/application/storage"
	tradingsvc "github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// tourFixture is the shared mutable world the tour-coordinator fakes read/write: the
// hull's live cargo and location (advanced by the fake mediator on buy/sell/navigate)
// and the per-waypoint market prices (read by the market repo for both snapshot
// assembly and the live re-verify, and by the mediator to price trades). This models
// what production gets from persistence: after a buy the reloaded ship reflects it, so
// the executor's cargo-sized sells and the stranded check exercise the real code path.
type tourFixture struct {
	mu       sync.Mutex
	cargo    map[string]int
	location string
	cargoCap int

	markets map[string][]string       // system -> market waypoints
	bid     map[string]map[string]int // waypoint -> good -> bid (PurchasePrice, sell revenue)
	ask     map[string]map[string]int // waypoint -> good -> ask (SellPrice, buy cost)
	tv      map[string]map[string]int // waypoint -> good -> tradeVolume
	// staleMarkets marks waypoints whose cached market reads are >maxListingAge old (sp-z7ng): a
	// listed waypoint here gets an ObservedAt 2h in the past, so freshListings drops it and the
	// reposition/placement staleness gate excludes it. Absent (nil) → every market reads fresh
	// (ObservedAt=now), the pre-existing behavior every other test relies on.
	staleMarkets map[string]bool
	// tradeType maps waypoint -> good -> trade type (EXPORT/IMPORT/EXCHANGE). Absent →
	// EXPORT, the pre-existing default every non-look-back test relies on. sp-ed4i uses it
	// to model a real IMPORT sink so the look-back sink discipline (sp-9mkf: never sell into
	// an exporter's bid) is exercised, not bypassed.
	tradeType map[string]map[string]string

	// neighbors maps a system to the systems one jump-gate hop away (the fake's answer to
	// GetJumpGateConnectionsQuery). Drives both the tour graph's neighbor scan and the
	// sp-zhii reposition candidate set. Absent → no neighbors (home-only), the pre-sp-zhii
	// default every existing test relies on. jumps records each JumpShipCommand's
	// destination system so a reposition test can assert the hull actually jumped.
	neighbors map[string][]string
	jumps     []string
	// navDests records every NavigateRouteCommand destination in dispatch order (sp-trnp), so a
	// test can assert the cross-system departure hop (waypoint->source gate) fired BEFORE the
	// jump. Purely additive — no existing test reads it.
	navDests []string
	// jumpRequiresGate, when true, makes the fake JumpShipCommand enforce the live engine's
	// precondition — a driveless hull must be sitting ON a jump gate — and reject a jump from a
	// non-gate waypoint with the exact "not at a jump gate" error jump_ship.go returns (sp-trnp).
	// The fake models gates as "<sys>-GATE" waypoints. Default false so every existing jump test,
	// which relies on the fake jump always succeeding, is byte-for-byte unchanged.
	jumpRequiresGate bool
	// departureHopNavStall, when true, models the arrival-wait false-positive that caused the
	// live leg-1 crash (sp-trnp): a departure-hop navigate to a jump gate reports completion
	// (route "arrived") while the persisted position still lags at the origin (the nav-cache
	// race) — so the hull does NOT move here. Only an AUTHORITATIVE resync (SyncShipFromAPI)
	// later reveals its true position (the gate), recording it via stalledGateDest. Default
	// false so every existing navigate moves synchronously exactly as before.
	departureHopNavStall bool
	stalledGateDest      string

	sellCap  map[string]int // per-good cap on units a sell absorbs (stranded test); 0 = uncapped
	timeline []string       // ordered "BUY:good"/"SELL:good" for sell-before-buy assertions
	buys     int
	sells    int

	// Normalized operation_type carried on ctx at each buy/sell dispatch — the exact
	// value the real cargo-tx path stamps onto the ledger row (sp-lgnh). Captured at
	// the mediator seam so a test can prove the coordinator threads "tour".
	buyOpTypes  []string
	sellOpTypes []string
}

func (fx *tourFixture) buildShip(t *testing.T, symbol string) *navigation.Ship {
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
	// The fake models jump gates as "<sys>-GATE" waypoints (see the mediator's
	// FindNearestJumpGate/JumpShipCommand handlers); type them JUMP_GATE so
	// CurrentLocation().IsJumpGate() reflects the same convention the real
	// waypoint-typed code (and the sp-trnp departure-hop re-confirmation) reads.
	if strings.HasSuffix(fx.location, "-GATE") {
		wp.Type = "JUMP_GATE"
	}
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(1), wp, fuel, 1000, fx.cargoCap, cargo, 30,
		"FRAME_LIGHT_FREIGHTER", "HAULER", nil, navigation.NavStatusDocked)
	if err != nil {
		t.Fatalf("ship: %v", err)
	}
	return ship
}

type tourFakeMediator struct {
	fx *tourFixture
}

func (m *tourFakeMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	switch cmd := request.(type) {
	case *navCmd.NavigateRouteCommand:
		m.fx.mu.Lock()
		m.fx.navDests = append(m.fx.navDests, cmd.Destination)
		// sp-trnp: model the nav-cache false-positive on a departure-hop navigate to the
		// gate — the route reports "arrived" but the persisted position lags at the origin,
		// so the hull does NOT move here; only an authoritative resync reveals the truth.
		// Guarded, so every existing navigate moves synchronously exactly as before.
		if m.fx.departureHopNavStall && strings.HasSuffix(cmd.Destination, "-GATE") {
			m.fx.stalledGateDest = cmd.Destination
		} else {
			m.fx.location = cmd.Destination
		}
		m.fx.mu.Unlock()
		return nil, nil
	case *shipCargo.PurchaseCargoCommand:
		m.fx.mu.Lock()
		price := m.fx.ask[m.fx.location][cmd.GoodSymbol]
		// Honor the sp-9mkf per-tranche buy ceiling like the real PurchaseCargoCommand
		// handler: a live ask above MaxAskPerUnit aborts the buy (zero units) rather than
		// overpaying. MaxAskPerUnit==0 means no ceiling (the pre-ceiling callers/tests).
		if cmd.MaxAskPerUnit > 0 && price > cmd.MaxAskPerUnit {
			m.fx.mu.Unlock()
			return &shipCargo.PurchaseCargoResponse{UnitsAdded: 0, CeilingAborted: true, CeilingObservedAsk: price}, nil
		}
		units := cmd.Units
		m.fx.cargo[cmd.GoodSymbol] += units
		m.fx.timeline = append(m.fx.timeline, "BUY:"+cmd.GoodSymbol)
		m.fx.buys++
		m.fx.buyOpTypes = append(m.fx.buyOpTypes, shared.OperationContextFromContext(ctx).NormalizedOperationType())
		m.fx.mu.Unlock()
		return &shipCargo.PurchaseCargoResponse{TotalCost: units * price, UnitsAdded: units, TransactionCount: 1}, nil
	case *shipCargo.SellCargoCommand:
		m.fx.mu.Lock()
		price := m.fx.bid[m.fx.location][cmd.GoodSymbol]
		units := cmd.Units
		if capUnits, ok := m.fx.sellCap[cmd.GoodSymbol]; ok && capUnits < units {
			units = capUnits
		}
		m.fx.cargo[cmd.GoodSymbol] -= units
		m.fx.timeline = append(m.fx.timeline, "SELL:"+cmd.GoodSymbol)
		m.fx.sells++
		m.fx.sellOpTypes = append(m.fx.sellOpTypes, shared.OperationContextFromContext(ctx).NormalizedOperationType())
		m.fx.mu.Unlock()
		return &shipCargo.SellCargoResponse{TotalRevenue: units * price, UnitsSold: units, TransactionCount: 1}, nil
	case *gasCmd.TransferCargoCommand:
		// A haul-to-storage deposit transfer (sp-dchv): the good LEAVES the hull
		// into the warehouse — model it like a sell with no revenue.
		m.fx.mu.Lock()
		m.fx.cargo[cmd.GoodSymbol] -= cmd.Units
		m.fx.mu.Unlock()
		return &gasCmd.TransferCargoResponse{UnitsTransferred: cmd.Units}, nil
	case *shipQueries.GetJumpGateConnectionsQuery:
		// Jump-gate neighbor scan (tour graph + sp-zhii reposition candidate set).
		m.fx.mu.Lock()
		defer m.fx.mu.Unlock()
		return &shipQueries.GetJumpGateConnectionsResponse{ConnectedSystems: m.fx.neighbors[cmd.SystemSymbol]}, nil
	case *shipQueries.FindNearestJumpGateQuery:
		// The sp-5nqx departure hop resolves the source system's gate before a jump; the
		// fake returns a synthetic gate for whatever system the hull is currently in.
		m.fx.mu.Lock()
		sys := shared.ExtractSystemSymbol(m.fx.location)
		m.fx.mu.Unlock()
		wp, err := shared.NewWaypoint(sys+"-GATE", 0, 0)
		if err != nil {
			return nil, err
		}
		wp.Type = "JUMP_GATE"
		return &shipQueries.FindNearestJumpGateResponse{JumpGate: wp, SystemSymbol: sys}, nil
	case *navCmd.JumpShipCommand:
		// A cross-system jump (sp-zhii reposition rides the shared travel machinery): land
		// the hull on the destination system's gate and record the hop. CooldownSeconds=0
		// so the post-jump settle wait is a no-op under the instant fake clock.
		m.fx.mu.Lock()
		// sp-trnp: when a test opts in, model the live jump API's precondition — a driveless
		// hull must be sitting ON a jump gate (jump_ship.go rejects otherwise). The fake tracks
		// gates as "<sys>-GATE" waypoints, so a current location without that suffix is a market/
		// other waypoint and the jump must fail with the exact engine error — the crash the shared
		// travel() departure hop (navigate waypoint->gate first) exists to prevent.
		if m.fx.jumpRequiresGate && !strings.HasSuffix(m.fx.location, "-GATE") {
			at := m.fx.location
			m.fx.mu.Unlock()
			return nil, fmt.Errorf("cannot jump: no jump drive module and not at a jump gate (ship %s at %s)", cmd.ShipSymbol, at)
		}
		m.fx.jumps = append(m.fx.jumps, cmd.DestinationSystem)
		m.fx.location = cmd.DestinationSystem + "-GATE"
		m.fx.mu.Unlock()
		return &navCmd.JumpShipResponse{Success: true, DestinationSystem: cmd.DestinationSystem, CooldownSeconds: 0}, nil
	default:
		return nil, nil // dock, orbit, etc. succeed silently
	}
}

func (m *tourFakeMediator) Register(requestType reflect.Type, handler common.RequestHandler) error {
	return nil
}
func (m *tourFakeMediator) RegisterMiddleware(middleware common.Middleware) {}

type tourFakeMarketRepo struct {
	market.MarketRepository
	fx *tourFixture
	t  *testing.T
}

func (r *tourFakeMarketRepo) FindAllMarketsInSystem(ctx context.Context, systemSymbol string, playerID int) ([]string, error) {
	return r.fx.markets[systemSymbol], nil
}

func (r *tourFakeMarketRepo) GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*market.Market, error) {
	r.fx.mu.Lock()
	defer r.fx.mu.Unlock()
	goods, ok := r.fx.ask[waypointSymbol]
	if !ok {
		return nil, nil
	}
	supply, activity := "MODERATE", "STRONG"
	var tgs []market.TradeGood
	for good := range goods {
		// Per-good trade type (sp-ed4i): defaults to EXPORT (the pre-existing behavior) so a
		// good is a real IMPORT sink only where a test declares it, letting the look-back
		// sink discipline be exercised rather than bypassed.
		tt := market.TradeTypeExport
		if byGood, ok := r.fx.tradeType[waypointSymbol]; ok {
			switch byGood[good] {
			case "IMPORT":
				tt = market.TradeTypeImport
			case "EXCHANGE":
				tt = market.TradeTypeExchange
			}
		}
		g, err := market.NewTradeGood(good, &supply, &activity,
			r.fx.bid[waypointSymbol][good], r.fx.ask[waypointSymbol][good], r.fx.tv[waypointSymbol][good], tt)
		if err != nil {
			r.t.Fatalf("trade good: %v", err)
		}
		tgs = append(tgs, *g)
	}
	observedAt := time.Now()
	if r.fx.staleMarkets[waypointSymbol] {
		observedAt = observedAt.Add(-2 * time.Hour) // >maxListingAge (75m) → freshListings drops it
	}
	m, err := market.NewMarket(waypointSymbol, tgs, observedAt)
	if err != nil {
		r.t.Fatalf("market: %v", err)
	}
	return m, nil
}

type tourFakeWaypointRepo struct {
	system.WaypointRepository
	fx *tourFixture
}

func (r *tourFakeWaypointRepo) ListBySystem(ctx context.Context, systemSymbol string) ([]*shared.Waypoint, error) {
	var wps []*shared.Waypoint
	for _, wp := range r.fx.markets[systemSymbol] {
		w, _ := shared.NewWaypoint(wp, 1, 1)
		wps = append(wps, w)
	}
	return wps, nil
}

type tourFakeShipRepo struct {
	navigation.ShipRepository
	fx *tourFixture
	t  *testing.T
}

func (r *tourFakeShipRepo) FindBySymbol(ctx context.Context, symbol string, playerID shared.PlayerID) (*navigation.Ship, error) {
	return r.fx.buildShip(r.t, symbol), nil
}
func (r *tourFakeShipRepo) Save(ctx context.Context, ship *navigation.Ship) error { return nil }

// SyncShipFromAPI models the authoritative live resync the sp-trnp departure-hop
// re-confirmation performs after navigating to the source gate. When a departure-hop
// navigate stalled (departureHopNavStall — the persisted position lagged the truth), the
// authoritative read reveals the hull actually reached the gate: it resolves the stalled
// destination into the live position, which is what lets the re-confirmation RECOVER instead
// of firing a doomed jump. Absent a stall it simply reflects the hull's current position.
func (r *tourFakeShipRepo) SyncShipFromAPI(ctx context.Context, symbol string, playerID shared.PlayerID) (*navigation.Ship, error) {
	r.fx.mu.Lock()
	if r.fx.stalledGateDest != "" {
		r.fx.location = r.fx.stalledGateDest
		r.fx.stalledGateDest = ""
	}
	r.fx.mu.Unlock()
	return r.fx.buildShip(r.t, symbol), nil
}

type tourFakeRoutingClient struct {
	routing.RoutingClient
	plans []*routing.TourPlan
	err   error
	calls int
	// positions and cargos capture the hull state the coordinator planned FROM on
	// each call (sp-m5kv: proving a continuous tour re-plans from the NEW position and
	// carries held cargo forward as planner input). errAfter, when >0, makes the
	// planner start returning err only from that call onward (a mid-run planner blip /
	// margin-death simulation) while earlier calls return plans.
	positions []string
	cargos    []map[string]int
	errAfter  int
	// maxSpends captures cons.MaxSpend on each call (sp-7z7j: proving a dynamic-cap
	// continuous tour RE-RESOLVES the 25%-of-treasury budget per iteration — a fresh
	// positive value each pass, never a stale/0 budget). infeasibleOnZeroSpend makes
	// the fake mirror the real solver's spend_cap=max(0, max_spend-reserve) verdict:
	// a maxSpend of 0 (the unreadable-treasury signature) yields spend_cap 0, which the
	// Python planner reports as infeasible (tour_solver.py) — the exact production
	// mechanism that turned a transient treasury read failure into a loop-ending
	// "tour unavailable".
	maxSpends             []int64
	infeasibleOnZeroSpend bool
	// reserves captures cons.WorkingCapitalReserve on each call (sp-4hl5: proving the
	// dynamic-budget path hands the planner a reserve of 0 — the 25%-of-treasury budget
	// already embodies the capital guard — while an explicit --max-spend keeps the
	// original cash-contract subtraction).
	reserves []int64
	// cancel + cancelOnCall simulate a daemon stop: when the call count reaches
	// cancelOnCall the planner cancels the run's context (as interruptAllContainers
	// does), so a test can prove a continuous run exits RESUMABLE at the tour boundary
	// rather than COMPLETING via the starvation streak (sp-ovkn).
	cancel       context.CancelFunc
	cancelOnCall int
	// planFn, when set, fully computes the plan for a call from the (synthetic or live)
	// ship state — used by sp-zhii reposition tests that must return different plans per
	// SYSTEM (home goes dead, a candidate is alive) and per call, which the flat
	// index-based plans slice cannot express because planAtCandidate's pre-flight calls
	// interleave with the loop's own re-plans. Takes precedence over the plans slice.
	planFn func(ship routing.TourShipState) *routing.TourPlan
	// absorptions captures the absorption view the coordinator assembled and passed on
	// each call (sp-78ai L3): a netting integration test asserts the tour nets the ledger's
	// outstanding depth into its plan request.
	absorptions [][]routing.TourMarketAbsorption
	// maxTourSystems captures cons.MaxTourSystems on each call (sp-syaz): a coordinator-level
	// pin that the request-driven distinct-system cap rides cmd.MaxTourSystems onto the
	// TourConstraints the planner receives (unset → 0, the solver's default-2 hinge).
	maxTourSystems []int
	// closed + anchorSystems capture the sp-im74 closure fields on each call: the
	// cmd→cons hop that arms closed-tour mode on the planner request. Unset must reach
	// the planner as false/"" — the dormant proto3 default, byte-identical to today.
	closed        []bool
	anchorSystems []string
}

func (c *tourFakeRoutingClient) OptimizeTradeTour(ctx context.Context, snapshot []routing.TourGoodSnapshot, waypoints []routing.TourWaypoint, ship routing.TourShipState, cons routing.TourConstraints, deposits []routing.TourDepositCandidate, absorption []routing.TourMarketAbsorption) (*routing.TourPlan, error) {
	c.calls++
	c.positions = append(c.positions, ship.CurrentWaypoint)
	c.maxSpends = append(c.maxSpends, cons.MaxSpend)
	c.reserves = append(c.reserves, cons.WorkingCapitalReserve)
	c.maxTourSystems = append(c.maxTourSystems, cons.MaxTourSystems)
	c.closed = append(c.closed, cons.Closed)
	c.anchorSystems = append(c.anchorSystems, cons.AnchorSystem)
	c.absorptions = append(c.absorptions, absorption)
	held := map[string]int{}
	for g, u := range ship.Cargo {
		held[g] = u
	}
	c.cargos = append(c.cargos, held)
	if c.cancel != nil && c.calls == c.cancelOnCall {
		c.cancel()
	}
	if c.err != nil && (c.errAfter == 0 || c.calls >= c.errAfter) {
		return nil, c.err
	}
	if c.planFn != nil {
		return c.planFn(ship), nil
	}
	if c.infeasibleOnZeroSpend && cons.MaxSpend == 0 {
		return &routing.TourPlan{Feasible: false, InfeasibleReason: "spend_cap_zero"}, nil
	}
	idx := c.calls - 1
	if idx >= len(c.plans) {
		idx = len(c.plans) - 1
	}
	return c.plans[idx], nil
}

type tourFakeTelemetry struct {
	mu   sync.Mutex
	rows []trading.TourLegTelemetry
}

func (r *tourFakeTelemetry) RecordLeg(ctx context.Context, leg trading.TourLegTelemetry) error {
	r.mu.Lock()
	r.rows = append(r.rows, leg)
	r.mu.Unlock()
	return nil
}
func (r *tourFakeTelemetry) ListByPlayer(ctx context.Context, playerID int, since time.Time) ([]trading.TourLegTelemetry, error) {
	return r.rows, nil
}

func writeTourArtifact(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "market_model.json")
	if err := os.WriteFile(path, []byte(`{"fit_version":1,"era":"test-era"}`), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	return path
}

func newTourHandler(t *testing.T, fx *tourFixture, planner routing.RoutingClient, tel trading.TourTelemetryRepository) *RunTourCoordinatorHandler {
	return NewRunTourCoordinatorHandler(
		&tourFakeMediator{fx: fx},
		&tourFakeShipRepo{fx: fx, t: t},
		&tourFakeMarketRepo{fx: fx, t: t},
		&tourFakeWaypointRepo{fx: fx},
		tel,
		planner,
		nil,
		&trFakeClock{},
		nil,
	)
}

func tourResponse(t *testing.T, resp interface{}) *RunTourCoordinatorResponse {
	t.Helper()
	r, ok := resp.(*RunTourCoordinatorResponse)
	if !ok || r == nil {
		t.Fatalf("unexpected response %T", resp)
	}
	return r
}

func leg(wp, sys string, trades ...routing.TourTrade) routing.TourLeg {
	return routing.TourLeg{Waypoint: wp, System: sys, Trades: trades}
}
func buy(good string, units, price int) routing.TourTrade {
	return routing.TourTrade{Good: good, Units: units, ExpectedUnitPrice: price, IsBuy: true}
}
func sell(good string, units, price int) routing.TourTrade {
	return routing.TourTrade{Good: good, Units: units, ExpectedUnitPrice: price, IsBuy: false}
}

// deposit is a haul-to-storage DEPOSIT tranche (sp-dchv Lane C): a sell-side trade
// (IsBuy=false) flagged IsDeposit, priced at the synthetic bid (= home_ask).
func deposit(good string, units, syntheticBid int) routing.TourTrade {
	return routing.TourTrade{Good: good, Units: units, ExpectedUnitPrice: syntheticBid, IsBuy: false, IsDeposit: true}
}

// fakeRunningFinder returns canned running storage operations (the warehouse-op
// finder port the deposit path discovers the warehouse through).
type fakeRunningFinder struct{ ops []*storage.StorageOperation }

func (f *fakeRunningFinder) FindRunning(_ context.Context, _ int) ([]*storage.StorageOperation, error) {
	return f.ops, nil
}

// wireWarehouse registers a warehouse hull with a real in-memory coordinator and
// wires the tour handler's deposit subsystem to it (Lane B harness pattern). The
// assembler is left off (Enabled=false) — these tests drive the EXECUTOR through
// canned deposit legs, not the plan-time candidate assembly.
func wireWarehouse(t *testing.T, h *RunTourCoordinatorHandler, opID, waypoint string, capacity int, goods []string) *storageApp.InMemoryStorageCoordinator {
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
	h.SetPrePositioning(coord, &fakeRunningFinder{ops: []*storage.StorageOperation{op}}, nil,
		tradingsvc.DepositCandidateConfig{}, 10)
	return coord
}

// coLocatedSpec describes one warehouse hull in a co-located group for the multi-
// warehouse deposit harness (sp-5q2c).
type coLocatedSpec struct {
	id        string
	capacity  int
	createdAt time.Time
	goods     []string
}

// wireWarehouses registers several co-located warehouse hulls at waypoint into one
// coordinator and wires the tour handler's deposit subsystem to all of them, so a
// deposit leg can spill across the group.
func wireWarehouses(t *testing.T, h *RunTourCoordinatorHandler, waypoint string, specs []coLocatedSpec) *storageApp.InMemoryStorageCoordinator {
	t.Helper()
	coord := storageApp.NewInMemoryStorageCoordinator()
	var ops []*storage.StorageOperation
	for _, s := range specs {
		whShip, err := storage.NewStorageShip(s.id+"-WH", waypoint, s.id, s.capacity, nil)
		if err != nil {
			t.Fatalf("storage ship %s: %v", s.id, err)
		}
		if err := coord.RegisterStorageShip(whShip); err != nil {
			t.Fatalf("register %s: %v", s.id, err)
		}
		op, err := storage.NewWarehouseOperation(s.id, 1, waypoint, []string{s.id + "-WH"}, s.goods, &shared.MockClock{CurrentTime: s.createdAt})
		if err != nil {
			t.Fatalf("warehouse op %s: %v", s.id, err)
		}
		if err := op.Start(); err != nil {
			t.Fatalf("start %s: %v", s.id, err)
		}
		ops = append(ops, op)
	}
	h.SetPrePositioning(coord, &fakeRunningFinder{ops: ops}, nil, tradingsvc.DepositCandidateConfig{}, 10)
	return coord
}

// tourWarehouseOpAt builds a RUNNING warehouse operation with id at waypoint, created
// at createdAt (via a MockClock pinned to that instant, so CreatedAt() is fully
// controllable) — used to pin the sp-3lj5 zombie-row collision shape directly at the
// tour's warehouseAt call site.
func tourWarehouseOpAt(t *testing.T, id, waypoint string, createdAt time.Time) *storage.StorageOperation {
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

// TestTourWarehouseAt_ResolvesToNewestOnZombieCollision is the regression pin for
// sp-3lj5 at the tour's own warehouseAt call site: warehouse-TORWIND-12-bad719ff was
// STOPPED at 15:24Z but its storage_operations row was never terminalized (fixed
// separately in daemon_server.go), so it still surfaced as RUNNING alongside its live
// replacement warehouse-TORWIND-12-3477282e at the same waypoint — and the finder
// returned the zombie FIRST, matching the actual incident order. The old
// first-match-wins loop picked the dead operation. warehouseAt must resolve to the
// live operation instead. (No existing test exercised this call site directly before
// sp-3lj5 — the executor tests all go through wireWarehouse's single-op finder.)
func TestTourWarehouseAt_ResolvesToNewestOnZombieCollision(t *testing.T) {
	t0 := time.Date(2026, 7, 1, 15, 24, 0, 0, time.UTC)
	zombie := tourWarehouseOpAt(t, "warehouse-TORWIND-12-bad719ff", "X1-TORWIND-12", t0)
	live := tourWarehouseOpAt(t, "warehouse-TORWIND-12-3477282e", "X1-TORWIND-12", t0.Add(2*time.Hour))
	h := NewRunTourCoordinatorHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h.SetPrePositioning(nil, &fakeRunningFinder{ops: []*storage.StorageOperation{zombie, live}}, nil,
		tradingsvc.DepositCandidateConfig{}, 0)

	got := h.warehouseAt(context.Background(), 1, "X1-TORWIND-12")

	if got == nil || got.ID() != live.ID() {
		t.Fatalf("want live op %s, got %v", live.ID(), got)
	}
}

// TestTourWarehouseAt_NoRunningWarehouseReturnsNil is the fail-closed sanity check:
// with nothing RUNNING at the waypoint (e.g. the only warehouse there is now properly
// terminalized-stopped, so FindRunning no longer returns it), warehouseAt returns nil
// rather than guessing — the caller then degrades to pure arb for that leg.
func TestTourWarehouseAt_NoRunningWarehouseReturnsNil(t *testing.T) {
	h := NewRunTourCoordinatorHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h.SetPrePositioning(nil, &fakeRunningFinder{ops: nil}, nil, tradingsvc.DepositCandidateConfig{}, 0)

	got := h.warehouseAt(context.Background(), 1, "X1-TORWIND-12")

	if got != nil {
		t.Fatalf("want nil, got %v", got)
	}
}

// A 3-leg tour that fills the hold both ways executes every buy and sell, records one
// telemetry row per trade, orders the mixed leg sells-before-buys, and completes clean.
func TestTour_ExecutesLegsAndRecordsTelemetry(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B", "X1-S1-C"}},
		bid:     map[string]map[string]int{"X1-S1-B": {"G1": 200}, "X1-S1-C": {"G2": 120}},
		ask:     map[string]map[string]int{"X1-S1-A": {"G1": 100}, "X1-S1-B": {"G1": 200, "G2": 50}, "X1-S1-C": {"G2": 120}},
		tv:      map[string]map[string]int{"X1-S1-A": {"G1": 1000}, "X1-S1-B": {"G1": 1000, "G2": 1000}, "X1-S1-C": {"G2": 1000}},
	}
	tel := &tourFakeTelemetry{}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{{
		Feasible: true, ProjectedProfit: 6800, Legs: []routing.TourLeg{
			leg("X1-S1-A", "X1-S1", buy("G1", 40, 100)),
			leg("X1-S1-B", "X1-S1", buy("G2", 40, 50), sell("G1", 40, 200)), // buy listed first → executor must sell first
			leg("X1-S1-C", "X1-S1", sell("G2", 40, 120)),
		},
	}}}
	h := newTourHandler(t, fx, planner, tel)

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-1", PlayerID: 1, ContainerID: "ctr-1", ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("tour returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if !r.Completed {
		t.Fatalf("expected a completed tour, got %+v", r)
	}
	if ok, reason := r.CompletionOutcome(); !ok {
		t.Fatalf("expected honest completion, got veto: %s", reason)
	}
	if len(tel.rows) != 4 {
		t.Fatalf("expected 4 telemetry rows (one per trade), got %d: %+v", len(tel.rows), tel.rows)
	}
	if r.ModelVersion != "1@test-era" {
		t.Fatalf("model version = %q, want 1@test-era", r.ModelVersion)
	}
	// Sells-before-buys: leg B (buy G2 listed before sell G1) must execute the sell first.
	want := []string{"BUY:G1", "SELL:G1", "BUY:G2", "SELL:G2"}
	if strings.Join(fx.timeline, ",") != strings.Join(want, ",") {
		t.Fatalf("trade order = %v, want %v (sells before buys within a leg)", fx.timeline, want)
	}
}

// A leg whose live bid has fallen 30% under the plan is skipped and triggers exactly
// one re-plan (OptimizeTradeTour called twice), which then completes clean.
func TestTour_DegradedLegTriggersSingleReplan(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B"}},
		bid:     map[string]map[string]int{"X1-S1-B": {"G": 140}}, // 30% under the plan's 200
		ask:     map[string]map[string]int{"X1-S1-A": {"G": 100}, "X1-S1-B": {"G": 140}},
		tv:      map[string]map[string]int{"X1-S1-A": {"G": 1000}, "X1-S1-B": {"G": 1000}},
	}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{
		{Feasible: true, Legs: []routing.TourLeg{
			leg("X1-S1-A", "X1-S1", buy("G", 40, 100)),
			leg("X1-S1-B", "X1-S1", sell("G", 40, 200)), // planned 200, live 140 → degraded
		}},
		{Feasible: true, Legs: []routing.TourLeg{
			leg("X1-S1-B", "X1-S1", sell("G", 40, 140)), // re-plan at the live price
		}},
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-2", PlayerID: 1, ContainerID: "ctr-2", ReplanLimit: 2, ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("tour returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if planner.calls != 2 {
		t.Fatalf("expected exactly 2 planner calls (initial + 1 re-plan), got %d", planner.calls)
	}
	if r.Replans != 1 {
		t.Fatalf("expected 1 re-plan recorded, got %d", r.Replans)
	}
	if ok, reason := r.CompletionOutcome(); !ok {
		t.Fatalf("expected clean completion after re-plan, got veto: %s", reason)
	}
}

// A planner transport error fails OPEN: a clean "tour unavailable" no-op with no
// trades, no telemetry, and a nil Go error (single-lane fallback stands).
func TestTour_PlannerDownFailsOpenCleanly(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		markets: map[string][]string{"X1-S1": {"X1-S1-A"}},
		bid:     map[string]map[string]int{}, ask: map[string]map[string]int{"X1-S1-A": {"G": 100}},
		tv: map[string]map[string]int{"X1-S1-A": {"G": 1000}},
	}
	tel := &tourFakeTelemetry{}
	planner := &tourFakeRoutingClient{err: errors.New("planner down")}
	h := newTourHandler(t, fx, planner, tel)

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-3", PlayerID: 1, ContainerID: "ctr-3", ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("planner-down must fail open with a nil error, got %v", err)
	}
	r := tourResponse(t, resp)

	if !r.TourUnavailable || !strings.Contains(r.TourUnavailableReason, "tour unavailable") {
		t.Fatalf("expected a 'tour unavailable' no-op, got %+v", r)
	}
	if fx.buys != 0 || fx.sells != 0 {
		t.Fatalf("planner-down must not trade, got %d buys / %d sells", fx.buys, fx.sells)
	}
	if len(tel.rows) != 0 {
		t.Fatalf("planner-down must record no telemetry, got %d rows", len(tel.rows))
	}
	if ok, _ := r.CompletionOutcome(); !ok {
		t.Fatalf("a fail-open no-op is a clean completion, not a veto")
	}
}

// A tour that buys a tranche but can only offload half of it ends holding tour-bought
// cargo — an honest-completion VETO (nil Go error, CompletionOutcome false), NOT arb's
// non-nil-error shape (errata: a dynamically-planned tour cannot be resumed by a
// re-run). The veto reason names the good, stranded units, and the location.
func TestTour_StrandedCargoReportsFailure(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B"}},
		bid:     map[string]map[string]int{"X1-S1-B": {"G": 200}},
		ask:     map[string]map[string]int{"X1-S1-A": {"G": 100}, "X1-S1-B": {"G": 200}},
		tv:      map[string]map[string]int{"X1-S1-A": {"G": 1000}, "X1-S1-B": {"G": 1000}},
		sellCap: map[string]int{"G": 20}, // the sink absorbs only 20 of the 40 held
	}
	h := newTourHandler(t, fx, &tourFakeRoutingClient{plans: []*routing.TourPlan{{
		Feasible: true, Legs: []routing.TourLeg{
			leg("X1-S1-A", "X1-S1", buy("G", 40, 100)),
			leg("X1-S1-B", "X1-S1", sell("G", 40, 200)),
		},
	}}}, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-4", PlayerID: 1, ContainerID: "ctr-4", ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("stranded tour vetoes via CompletionOutcome, not a Go error; got %v", err)
	}
	r := tourResponse(t, resp)

	ok, reason := r.CompletionOutcome()
	if ok {
		t.Fatalf("expected a stranded-cargo veto, got clean completion: %+v", r)
	}
	if !strings.Contains(reason, "G") || !strings.Contains(reason, "20") || !strings.Contains(reason, "X1-S1-B") {
		t.Fatalf("veto reason must name good+units+location, got %q", reason)
	}
}

// sp-wj0h regression: the daemon does NOT set cmd.ModelArtifactPath — the coordinator
// reads the artifact from its handler-configured (absolute) path. With no cmd path, a
// readable handler path lets the tour plan and complete (proving the production wiring
// that DOA'd on the old cwd-relative constant).
func TestTour_UsesHandlerModelArtifactPathWhenCmdEmpty(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B"}},
		bid:     map[string]map[string]int{"X1-S1-B": {"G": 200}},
		ask:     map[string]map[string]int{"X1-S1-A": {"G": 100}, "X1-S1-B": {"G": 200}},
		tv:      map[string]map[string]int{"X1-S1-A": {"G": 1000}, "X1-S1-B": {"G": 1000}},
	}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{{
		Feasible: true, Legs: []routing.TourLeg{
			leg("X1-S1-A", "X1-S1", buy("G", 40, 100)),
			leg("X1-S1-B", "X1-S1", sell("G", 40, 200)),
		},
	}}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	h.SetModelArtifactPath(writeTourArtifact(t)) // production shape: handler-configured path, cmd empty

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-CFG", PlayerID: 1, ContainerID: "ctr-cfg", // NO ModelArtifactPath on the command
	})
	if err != nil {
		t.Fatalf("tour returned error: %v", err)
	}
	r := tourResponse(t, resp)
	if r.TourUnavailable {
		t.Fatalf("handler-configured artifact path must be read; got unavailable: %s", r.TourUnavailableReason)
	}
	if planner.calls == 0 {
		t.Fatalf("expected the planner to be called once the artifact was read via the handler path")
	}
	if !r.Completed {
		t.Fatalf("expected a completed tour, got %+v", r)
	}
}

// An unreadable handler artifact path (the sp-wj0h DOA symptom) fails OPEN cleanly: a
// "tour unavailable" no-op, nil error, no planner call, no trades.
func TestTour_UnreadableModelArtifactFailsClosed(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		markets: map[string][]string{"X1-S1": {"X1-S1-A"}},
		bid:     map[string]map[string]int{}, ask: map[string]map[string]int{"X1-S1-A": {"G": 100}},
		tv: map[string]map[string]int{"X1-S1-A": {"G": 1000}},
	}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{{Feasible: true}}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	h.SetModelArtifactPath(filepath.Join(t.TempDir(), "does-not-exist", "market_model.json"))

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-DOA", PlayerID: 1, ContainerID: "ctr-doa", // no cmd path → uses the unreadable handler path
	})
	if err != nil {
		t.Fatalf("unreadable artifact must fail OPEN with a nil error, got %v", err)
	}
	r := tourResponse(t, resp)
	if !r.TourUnavailable || !strings.Contains(r.TourUnavailableReason, "model artifact unreadable") {
		t.Fatalf("expected fail-closed 'model artifact unreadable', got %+v", r)
	}
	if planner.calls != 0 {
		t.Fatalf("must not call the planner on an unreadable artifact, got %d calls", planner.calls)
	}
	if fx.buys != 0 || fx.sells != 0 {
		t.Fatalf("must not trade on an unreadable artifact, got %d buys / %d sells", fx.buys, fx.sells)
	}
}

// sp-lgnh: every buy and sell a tour executes is dispatched under an operation
// context that normalizes to "tour", so the shared cargo-tx path stamps
// operation_type="tour" on the ledger row. Captured at the mediator seam — the exact
// point the real CargoTransactionHandler reads the context — this proves the
// coordinator threads the tag to BOTH trade directions itself (the incoming ctx here
// carries no operation context), so the graduation baseline (net trade credits
// filtered operation_type<>'tour') never measures the tour against its own trades.
func TestTour_TagsBuyAndSellWritesAsTourOperationType(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B"}},
		bid:     map[string]map[string]int{"X1-S1-B": {"G1": 200}},
		ask:     map[string]map[string]int{"X1-S1-A": {"G1": 100}, "X1-S1-B": {"G1": 200}},
		tv:      map[string]map[string]int{"X1-S1-A": {"G1": 1000}, "X1-S1-B": {"G1": 1000}},
	}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{{
		Feasible: true, Legs: []routing.TourLeg{
			leg("X1-S1-A", "X1-S1", buy("G1", 40, 100)),
			leg("X1-S1-B", "X1-S1", sell("G1", 40, 200)),
		},
	}}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-TAG", PlayerID: 1, ContainerID: "ctr-tag", ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("tour returned error: %v", err)
	}
	if !tourResponse(t, resp).Completed {
		t.Fatalf("expected a completed tour")
	}

	if len(fx.buyOpTypes) == 0 || len(fx.sellOpTypes) == 0 {
		t.Fatalf("expected at least one buy and one sell dispatch, got %d buys / %d sells",
			len(fx.buyOpTypes), len(fx.sellOpTypes))
	}
	for i, got := range fx.buyOpTypes {
		if got != "tour" {
			t.Errorf("buy #%d dispatched under operation_type %q, want \"tour\"", i, got)
		}
	}
	for i, got := range fx.sellOpTypes {
		if got != "tour" {
			t.Errorf("sell #%d dispatched under operation_type %q, want \"tour\"", i, got)
		}
	}
}

// sp-dchv Lane C: a DEPOSIT leg deposits the foreign-bought good into the home
// warehouse via the reserve→transfer→confirm protocol, books ZERO revenue (an
// inventory transfer, not a sale), and — because the cargo left the hull into
// inventory — does NOT strand. The foreign buy's cost is still recorded.
func TestTour_DepositLeg_DepositsIntoWarehouseAndBooksNoRevenue(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 80,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-W"}},
		ask:     map[string]map[string]int{"X1-S1-A": {"ELECTRONICS": 744}},
		bid:     map[string]map[string]int{"X1-S1-A": {"ELECTRONICS": 700}},
		tv:      map[string]map[string]int{"X1-S1-A": {"ELECTRONICS": 40}},
	}
	// Buy 40 ELECTRONICS cheap at A, then DEPOSIT them at the warehouse W.
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{{
		Feasible: true, ProjectedProfit: 90240, DepositValue: 120000,
		Legs: []routing.TourLeg{
			leg("X1-S1-A", "X1-S1", buy("ELECTRONICS", 40, 744)),
			leg("X1-S1-W", "X1-S1", deposit("ELECTRONICS", 40, 3000)),
		},
	}}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	coord := wireWarehouse(t, h, "wh-op", "X1-S1-W", 1000, []string{"ELECTRONICS"})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-1", PlayerID: 1, ContainerID: "ctr-1", ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("tour returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if got := coord.GetTotalCargoAvailable("wh-op", "ELECTRONICS"); got != 40 {
		t.Fatalf("warehouse should hold 40 deposited ELECTRONICS, got %d", got)
	}
	if r.TotalRevenue != 0 {
		t.Fatalf("a deposit books NO revenue (only the foreign buy spent), got revenue %d", r.TotalRevenue)
	}
	if r.TotalSpent != 40*744 {
		t.Fatalf("the foreign buy cost must still be recorded, got spent %d", r.TotalSpent)
	}
	if r.CargoStranded {
		t.Fatalf("deposited cargo left the hull — must not strand: %s", r.CargoStrandedReason)
	}
	if !r.Completed {
		t.Fatalf("tour should complete cleanly, got %+v", r)
	}
}

// sp-5q2c multi-warehouse: a single deposit leg spills across the CO-LOCATED group —
// the newest hull fills to capacity, the remainder lands in the older sibling — so a
// deposit larger than one hull still lands in full (additive capacity), not degrading
// to a re-plan while a sibling sits with free slots.
func TestTour_DepositLeg_SpillsAcrossCoLocatedWarehouses(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 80,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-W"}},
		ask:     map[string]map[string]int{"X1-S1-A": {"ELECTRONICS": 744}},
		bid:     map[string]map[string]int{"X1-S1-A": {"ELECTRONICS": 700}},
		tv:      map[string]map[string]int{"X1-S1-A": {"ELECTRONICS": 40}},
	}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{{
		Feasible: true, ProjectedProfit: 90240, DepositValue: 120000,
		Legs: []routing.TourLeg{
			leg("X1-S1-A", "X1-S1", buy("ELECTRONICS", 40, 744)),
			leg("X1-S1-W", "X1-S1", deposit("ELECTRONICS", 40, 3000)),
		},
	}}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	t0 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	// newest hull holds only 25; the 40-unit deposit must spill 15 into the older one.
	coord := wireWarehouses(t, h, "X1-S1-W", []coLocatedSpec{
		{id: "wh-older", capacity: 1000, createdAt: t0, goods: []string{"ELECTRONICS"}},
		{id: "wh-newer", capacity: 25, createdAt: t0.Add(time.Hour), goods: []string{"ELECTRONICS"}},
	})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-1", PlayerID: 1, ContainerID: "ctr-1", ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("tour returned error: %v", err)
	}
	r := tourResponse(t, resp)

	newer := coord.GetTotalCargoAvailable("wh-newer", "ELECTRONICS")
	older := coord.GetTotalCargoAvailable("wh-older", "ELECTRONICS")
	if newer != 25 {
		t.Fatalf("the newest hull must fill to its 25 capacity first, got %d", newer)
	}
	if older != 15 {
		t.Fatalf("the remaining 15 must spill into the older hull, got %d", older)
	}
	if r.CargoStranded {
		t.Fatalf("the whole 40 landed across the group — must not strand: %s", r.CargoStrandedReason)
	}
	if !r.Completed {
		t.Fatalf("tour should complete cleanly, got %+v", r)
	}
}

// sp-dchv Lane C (stranded-veto composure, ruling #5): a deposit that CANNOT land
// (warehouse full) degrades and re-plans; the re-plan liquidates the held cargo at
// a real market (m5kv), so bought-for-deposit cargo is never silently stranded.
func TestTour_FailedDeposit_ReplansAndLiquidatesAsHeldCargo(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 80,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-W", "X1-S1-B"}},
		ask: map[string]map[string]int{
			"X1-S1-A": {"ELECTRONICS": 744}, "X1-S1-B": {"ELECTRONICS": 9999}},
		bid: map[string]map[string]int{
			"X1-S1-A": {"ELECTRONICS": 700}, "X1-S1-B": {"ELECTRONICS": 3200}},
		tv: map[string]map[string]int{
			"X1-S1-A": {"ELECTRONICS": 40}, "X1-S1-B": {"ELECTRONICS": 40}},
	}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{
		// Plan 1: buy 40 at A, deposit at W — but W is full, so the deposit fails.
		{Feasible: true, Legs: []routing.TourLeg{
			leg("X1-S1-A", "X1-S1", buy("ELECTRONICS", 40, 744)),
			leg("X1-S1-W", "X1-S1", deposit("ELECTRONICS", 40, 3000)),
		}},
		// Plan 2 (re-plan): the hull now HOLDS 40 ELECTRONICS → liquidate at market B.
		{Feasible: true, Legs: []routing.TourLeg{
			leg("X1-S1-B", "X1-S1", sell("ELECTRONICS", 40, 3200)),
		}},
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	// Warehouse hull with ZERO capacity → no space → every deposit reservation fails.
	coord := wireWarehouse(t, h, "wh-op", "X1-S1-W", 0, []string{"ELECTRONICS"})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-2", PlayerID: 1, ContainerID: "ctr-2", ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("tour returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if coord.GetTotalCargoAvailable("wh-op", "ELECTRONICS") != 0 {
		t.Fatalf("the full warehouse must hold nothing — deposit should have failed")
	}
	if r.Replans < 1 {
		t.Fatalf("a failed deposit must trigger a re-plan, got %d replans", r.Replans)
	}
	fx.mu.Lock()
	sells := fx.sells
	timeline := strings.Join(fx.timeline, ",")
	fx.mu.Unlock()
	if sells != 1 || !strings.Contains(timeline, "SELL:ELECTRONICS") {
		t.Fatalf("held cargo should liquidate at market on the re-plan, timeline=%q", timeline)
	}
	if r.CargoStranded {
		t.Fatalf("re-plan liquidated the held cargo — must not strand: %s", r.CargoStrandedReason)
	}
	if r.TotalRevenue != 40*3200 {
		t.Fatalf("expected the market liquidation revenue, got %d", r.TotalRevenue)
	}
}
