package commands

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/tactics"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// These tests reproduce the LAST trade-route layer (sp-ynuf): the circuit's dock
// step races the async arrival exactly as the goods-factory buy did (sp-n7yp). The
// ONLY faked collaborators are the ShipRepository (the DB/API boundary) and the
// mediator's navigate/buy/sell; the REAL DockShipHandler, runStateTransition,
// LoadShip and domain EnsureDocked all execute, so the tests exercise the actual
// dock-persistence path rather than a re-implemented one. The market fixture is the
// shared exporter/decaying-importer (trFixture / trFakeMarketRepo, defined in
// run_trade_route_coordinator_test.go).

const tdrShip = "TRADER-DOCKRACE"

// capturedLogEntry / capturingLogger record what reaches the container-log stream.
// The renderer prints only level+message and DROPS the metadata map, so a cause
// hidden in metadata never reaches an operator — the exact defect this regresses
// (sp-ynuf defect 1, the sp-iqyq class): the dock-failure warning logged a bare
// "Dock at source failed - ending circuit" with the real cause buried in a dropped
// {"error": ...} field.
type capturedLogEntry struct {
	level   string
	message string
}

type capturingLogger struct {
	mu      sync.Mutex
	entries []capturedLogEntry
}

func (l *capturingLogger) Log(level, message string, _ map[string]interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, capturedLogEntry{level: level, message: message})
}

func (l *capturingLogger) hasMessageContaining(parts ...string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.entries {
		matched := true
		for _, p := range parts {
			if !strings.Contains(e.message, p) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

// tdrShipRepo persists the ship's nav status + location as primitives and rebuilds
// a fresh Ship on every FindBySymbol — mirroring modelToDomain reading DB columns.
// Rebuilding (not sharing a pointer) is essential: it prevents a caller's in-memory
// EnsureDocked mutation from silently leaking into "the DB" and masking the bug.
//
// The race is modelled thus: setTransit (called on navigate) leaves the ship
// IN_TRANSIT — the arrival event has not yet landed — so a dock issued against that
// state is rejected by the real EnsureDocked ("cannot dock while in transit").
// SyncShipFromAPI models the API reporting the true, arrived state: it flips the
// ship to IN_ORBIT so the retried dock can succeed — UNLESS the ship is at
// stuckWaypoint, which models a genuinely undockable ship the resync cannot rescue.
// Embedding the interface makes any unused method panic, keeping the fake honest.
type tdrShipRepo struct {
	navigation.ShipRepository
	mu            sync.Mutex
	location      string
	navStatus     navigation.NavStatus
	stuckWaypoint string // sync leaves IN_TRANSIT here (unrecoverable); "" = always arrives
	dockAPICalls  int
	syncAPICalls  int
}

func (r *tdrShipRepo) buildShip() *navigation.Ship {
	waypoint, err := shared.NewWaypoint(r.location, 0, 0)
	if err != nil {
		panic(err)
	}
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		panic(err)
	}
	cargo, err := shared.NewCargo(40, 0, nil)
	if err != nil {
		panic(err)
	}
	ship, err := navigation.NewShip(
		tdrShip, shared.MustNewPlayerID(1), waypoint, fuel, 100, 40, cargo, 30,
		"FRAME_LIGHT_FREIGHTER", "HAULER", nil, r.navStatus,
	)
	if err != nil {
		panic(err)
	}
	return ship
}

func (r *tdrShipRepo) FindBySymbol(ctx context.Context, symbol string, playerID shared.PlayerID) (*navigation.Ship, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buildShip(), nil
}

// Dock models the concrete repo: unconditionally hits the API and persists DOCKED.
func (r *tdrShipRepo) Dock(ctx context.Context, ship *navigation.Ship, playerID shared.PlayerID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dockAPICalls++
	r.navStatus = navigation.NavStatusDocked
	return nil
}

// SyncShipFromAPI models reconciling against the live server: the arrival has
// actually landed, so the ship is IN_ORBIT — unless it is genuinely stuck.
func (r *tdrShipRepo) SyncShipFromAPI(ctx context.Context, symbol string, playerID shared.PlayerID) (*navigation.Ship, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.syncAPICalls++
	if r.location != r.stuckWaypoint {
		r.navStatus = navigation.NavStatusInOrbit
	}
	return r.buildShip(), nil
}

func (r *tdrShipRepo) Save(ctx context.Context, ship *navigation.Ship) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.navStatus = ship.NavStatus()
	return nil
}

// setTransit models a navigate leg finishing with the arrival event still in flight:
// the ship sits at the destination but the cache/DB still reads IN_TRANSIT.
func (r *tdrShipRepo) setTransit(destination string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.location = destination
	r.navStatus = navigation.NavStatusInTransit
}

func (r *tdrShipRepo) isDocked() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.navStatus == navigation.NavStatusDocked
}

func (r *tdrShipRepo) syncCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.syncAPICalls
}

// tdrMediator routes DockShipCommand to the REAL DockShipHandler (so the resync +
// symbol-reload actually has to persist DOCKED), models navigation arrival as an
// in-flight IN_TRANSIT, and answers buy/sell by faithfully modelling the
// docked-precondition (reload; reject if not actually docked) before recording the
// trade — so a silently-unpersisted dock is caught the same way it crashed prod.
type tdrMediator struct {
	repo        *tdrShipRepo
	dockHandler *tactics.DockShipHandler
	fixture     *trFixture

	mu        sync.Mutex
	purchases []*shipCargo.PurchaseCargoCommand
	sells     []*shipCargo.SellCargoCommand
}

func (m *tdrMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	switch cmd := request.(type) {
	case *shipTypes.DockShipCommand:
		return m.dockHandler.Handle(ctx, cmd)

	case *shipNav.NavigateRouteCommand:
		// Arrival still in flight: the leg lands but the cache reads IN_TRANSIT.
		m.repo.setTransit(cmd.Destination)
		return nil, nil

	case *shipCargo.PurchaseCargoCommand:
		ship, err := m.repo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
		if err != nil {
			return nil, err
		}
		if !ship.IsDocked() {
			return nil, fmt.Errorf("ship must be docked to perform cargo transactions")
		}
		m.mu.Lock()
		m.purchases = append(m.purchases, cmd)
		m.mu.Unlock()
		return &shipCargo.PurchaseCargoResponse{
			TotalCost: cmd.Units * trSourceAsk, UnitsAdded: cmd.Units, TransactionCount: 1,
		}, nil

	case *shipCargo.SellCargoCommand:
		ship, err := m.repo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
		if err != nil {
			return nil, err
		}
		if !ship.IsDocked() {
			return nil, fmt.Errorf("ship must be docked to perform cargo transactions")
		}
		m.mu.Lock()
		m.sells = append(m.sells, cmd)
		m.mu.Unlock()
		m.fixture.recordSell() // importer fills → dest bid decays for the next visit
		return &shipCargo.SellCargoResponse{
			TotalRevenue: cmd.Units * trSellRevenue, UnitsSold: cmd.Units, TransactionCount: 1,
		}, nil

	default:
		return nil, nil
	}
}

func (m *tdrMediator) Register(requestType reflect.Type, handler common.RequestHandler) error {
	return nil
}
func (m *tdrMediator) RegisterMiddleware(middleware common.Middleware) {}

func (m *tdrMediator) buys() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.purchases)
}

// newTdrHarness wires the coordinator against the real dock handler and a repo that
// starts the hull IN_TRANSIT (the stale cache the circuit's dock must survive).
func newTdrHarness(t *testing.T, stuckWaypoint string) (*RunTradeRouteCoordinatorHandler, *tdrShipRepo, *tdrMediator, *capturingLogger, context.Context) {
	t.Helper()
	fixture := &trFixture{}
	repo := &tdrShipRepo{
		location:      trSource,
		navStatus:     navigation.NavStatusInTransit, // arrival not yet landed
		stuckWaypoint: stuckWaypoint,
	}
	mediator := &tdrMediator{
		repo:        repo,
		dockHandler: tactics.NewDockShipHandler(repo),
		fixture:     fixture,
	}
	marketRepo := &trFakeMarketRepo{fixture: fixture}
	handler := NewRunTradeRouteCoordinatorHandler(mediator, repo, marketRepo, nil)
	logger := &capturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)
	return handler, repo, mediator, logger, ctx
}

func runTdr(t *testing.T, h *RunTradeRouteCoordinatorHandler, ctx context.Context) *RunTradeRouteCoordinatorResponse {
	t.Helper()
	resp, err := h.Handle(ctx, &RunTradeRouteCoordinatorCommand{
		ShipSymbol:   tdrShip,
		SystemSymbol: trSystem,
		PlayerID:     1,
	})
	if err != nil {
		t.Fatalf("coordinator returned a transport error: %v", err)
	}
	coord, ok := resp.(*RunTradeRouteCoordinatorResponse)
	if !ok || coord == nil {
		t.Fatalf("unexpected response %T", resp)
	}
	return coord
}

// GREEN behaviour: the dock-at-source (and dock-at-destination) step must survive the
// nav-cache race — resync the ship from the API to clear the stale IN_TRANSIT, then
// retry the dock — so the circuit actually docks and completes its disciplined
// visits. Under the pre-fix code the very first dock is issued against the stale
// cached IN_TRANSIT ship and EnsureDocked rejects it, ending the circuit at zero
// visits. This is the acceptance behaviour: "the circuit that docks is the circuit
// that trades."
func TestTradeRoute_DockStep_ResyncsAndRetriesTheNavCacheRace(t *testing.T) {
	h, repo, mediator, _, ctx := newTdrHarness(t, "") // "" → every resync lands the arrival

	coord := runTdr(t, h, ctx)

	// The circuit must have resynced at least once to clear the stale IN_TRANSIT.
	if repo.syncCalls() < 1 {
		t.Fatalf("expected the dock step to resync from the API to clear the nav-cache race, got %d syncs", repo.syncCalls())
	}
	// Same disciplined economics as the baseline circuit: 3 visits, 54u, net 81000 —
	// proving the dock persisted DOCKED every leg (the buy/sell reject otherwise).
	if coord.Visits != 3 {
		t.Fatalf("expected 3 visits once the dock survives the race, got %d (abort: %q)", coord.Visits, coord.AbortReason)
	}
	if coord.UnitsTraded != 54 {
		t.Fatalf("expected 54 units traded (3x18u), got %d", coord.UnitsTraded)
	}
	if coord.NetProfit != 81000 {
		t.Fatalf("expected net 81000, got %d (cost %d, revenue %d)", coord.NetProfit, coord.TotalCost, coord.TotalRevenue)
	}
	if mediator.buys() != 3 {
		t.Fatalf("expected 3 successful buys, got %d", mediator.buys())
	}
	if !repo.isDocked() {
		t.Fatalf("expected the hull to end DOCKED after a successful dock, got a non-docked state")
	}
}

// GREEN behaviour (defect 1 + bound): a persistent dock failure at the SOURCE must
// (a) surface the underlying cause VERBATIM in the log MESSAGE — not only in a
// dropped metadata field — and in response.AbortReason, and (b) abort the circuit
// cleanly after a BOUNDED number of resync retries (never hang, never spin). Under
// the pre-fix code the warning message is a bare "Dock at source failed - ending
// circuit" with the cause hidden in a dropped {"error": ...} field.
func TestTradeRoute_DockAtSource_PersistentFailure_VerbatimCauseAndBoundedAbort(t *testing.T) {
	h, repo, mediator, logger, ctx := newTdrHarness(t, trSource) // sync never clears transit at source

	coord := runTdr(t, h, ctx)

	if !coord.Completed {
		t.Fatalf("a dock-abort must still complete the handler cleanly (self-diagnosing), got %+v", coord)
	}
	if coord.Visits != 0 || mediator.buys() != 0 {
		t.Fatalf("a persistent dock-at-source failure must trade nothing, got visits=%d buys=%d", coord.Visits, mediator.buys())
	}
	// The verbatim domain cause must reach the operator via the MESSAGE, not a dropped field.
	const cause = "cannot dock while in transit"
	if !strings.Contains(coord.AbortReason, "dock at source") || !strings.Contains(coord.AbortReason, cause) {
		t.Fatalf("AbortReason must name the failing site and the verbatim cause, got %q", coord.AbortReason)
	}
	if !logger.hasMessageContaining("Dock at source", cause) {
		t.Fatalf("the dock-failure WARNING must carry the verbatim cause in its MESSAGE (sp-ynuf defect 1); captured none matching")
	}
	// Bounded: exactly tradeRouteDockRetryLimit resyncs (one before each retry), then abort.
	if got := repo.syncCalls(); got != tradeRouteDockRetryLimit {
		t.Fatalf("expected exactly %d bounded resyncs before aborting, got %d", tradeRouteDockRetryLimit, got)
	}
}

// GREEN behaviour (defect 1, destination site): the SAME verbatim-cause fix must
// cover the dock-at-destination step (:338). Here the source docks fine (resync
// lands the arrival) and the hull buys, but the destination dock is unrecoverable —
// the circuit must abort with cargo aboard and name the verbatim cause at the
// destination site, not swallow it.
func TestTradeRoute_DockAtDestination_PersistentFailure_VerbatimCause(t *testing.T) {
	h, repo, mediator, logger, ctx := newTdrHarness(t, trDest) // only the dest dock is stuck

	coord := runTdr(t, h, ctx)

	if coord.Visits != 0 {
		t.Fatalf("an unrecoverable dock-at-destination must complete no visit, got %d", coord.Visits)
	}
	// The source leg must have succeeded (docked + bought) before the dest dock failed.
	if mediator.buys() != 1 || coord.TotalCost <= 0 {
		t.Fatalf("expected the source leg to dock and buy before the dest dock fails, got buys=%d cost=%d", mediator.buys(), coord.TotalCost)
	}
	const cause = "cannot dock while in transit"
	if !strings.Contains(coord.AbortReason, "dock at destination") || !strings.Contains(coord.AbortReason, cause) {
		t.Fatalf("AbortReason must name the destination site and the verbatim cause, got %q", coord.AbortReason)
	}
	if !logger.hasMessageContaining("Dock at destination", cause) {
		t.Fatalf("the dock-at-destination WARNING must carry the verbatim cause in its MESSAGE (sp-ynuf defect 1); captured none matching")
	}
	// Bounded at the dest site too (source used one recovering resync; dest used the full bound).
	if got := repo.syncCalls(); got < tradeRouteDockRetryLimit {
		t.Fatalf("expected the destination dock to exhaust its bounded resyncs, got %d total", got)
	}
}
