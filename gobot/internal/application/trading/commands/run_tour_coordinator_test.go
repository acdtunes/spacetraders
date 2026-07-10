package commands

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
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
		m.fx.location = cmd.Destination
		m.fx.mu.Unlock()
		return nil, nil
	case *shipCargo.PurchaseCargoCommand:
		m.fx.mu.Lock()
		price := m.fx.ask[m.fx.location][cmd.GoodSymbol]
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
		g, err := market.NewTradeGood(good, &supply, &activity,
			r.fx.bid[waypointSymbol][good], r.fx.ask[waypointSymbol][good], r.fx.tv[waypointSymbol][good], market.TradeTypeExport)
		if err != nil {
			r.t.Fatalf("trade good: %v", err)
		}
		tgs = append(tgs, *g)
	}
	m, err := market.NewMarket(waypointSymbol, tgs, time.Now())
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
	// cancel + cancelOnCall simulate a daemon stop: when the call count reaches
	// cancelOnCall the planner cancels the run's context (as interruptAllContainers
	// does), so a test can prove a continuous run exits RESUMABLE at the tour boundary
	// rather than COMPLETING via the starvation streak (sp-ovkn).
	cancel       context.CancelFunc
	cancelOnCall int
}

func (c *tourFakeRoutingClient) OptimizeTradeTour(ctx context.Context, snapshot []routing.TourGoodSnapshot, waypoints []routing.TourWaypoint, ship routing.TourShipState, cons routing.TourConstraints) (*routing.TourPlan, error) {
	c.calls++
	c.positions = append(c.positions, ship.CurrentWaypoint)
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
