package ship_test

// Integration harness + tests for sp-yd84 "cut redundant movement API calls".
//
// External (black-box) test package: it drives the REAL command handlers
// (OrbitShipHandler, DockShipHandler, RefuelShipHandler, NavigateDirectHandler,
// SetFlightModeHandler) through the RouteExecutor via a dispatching mediator,
// with a single spy ShipRepository at the driven-port boundary. That is the
// correct hexagonal seam: everything inside the hexagon (executor + handlers +
// domain) is real; only the API-backed repository is a double. The spy records
// each state-changing call (Orbit/Dock/Refuel/Navigate) — each exactly one live
// API verb in production — and models SERVER-SIDE "reality" nav status
// separately from the in-memory Ship, so a test can inject drift (in-memory
// disagrees with reality) and prove the self-heal recovers.
//
// It lives in ship_test rather than package ship because commands/navigation
// imports the route-executor package, so an internal test importing it would
// form a cycle.

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/mediator"
	appship "github.com/andrescamacho/spacetraders-go/internal/application/ship"
	shipnav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/tactics"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// --- test doubles: event subscriber (unused; executor requires non-nil) ----

type stubEventSubscriber struct{}

func (stubEventSubscriber) SubscribeArrived(string) <-chan domainNavigation.ShipArrivedEvent {
	return nil
}
func (stubEventSubscriber) UnsubscribeArrived(string, <-chan domainNavigation.ShipArrivedEvent) {
}
func (stubEventSubscriber) SubscribeWorkerCompleted(string) <-chan domainNavigation.WorkerCompletedEvent {
	return nil
}
func (stubEventSubscriber) UnsubscribeWorkerCompleted(string, <-chan domainNavigation.WorkerCompletedEvent) {
}
func (stubEventSubscriber) SubscribeTasksBecameReady(int) <-chan domainNavigation.TasksBecameReadyEvent {
	return nil
}
func (stubEventSubscriber) UnsubscribeTasksBecameReady(int, <-chan domainNavigation.TasksBecameReadyEvent) {
}
func (stubEventSubscriber) SubscribeTransportRequested(int) <-chan domainNavigation.TransportRequestedEvent {
	return nil
}
func (stubEventSubscriber) UnsubscribeTransportRequested(int, <-chan domainNavigation.TransportRequestedEvent) {
}
func (stubEventSubscriber) SubscribeTransferCompleted(int) <-chan domainNavigation.TransferCompletedEvent {
	return nil
}
func (stubEventSubscriber) UnsubscribeTransferCompleted(int, <-chan domainNavigation.TransferCompletedEvent) {
}

// --- spy driven-port repository -------------------------------------------

// tourShipRepo is a spy implementation of navigation.ShipRepository. It records
// every state-changing call and tracks the SERVER-SIDE nav status ("reality")
// independently of the in-memory Ship. Refuel requires reality==DOCKED (the live
// API's 4214) and Navigate requires reality==IN_ORBIT (the live API's 4236), so
// a wrong skip that leaves the in-memory state disagreeing with reality is
// faithfully rejected — exactly what the self-heal must recover from.
type tourShipRepo struct {
	domainNavigation.ShipRepository // embedded: any unused method panics if hit

	ship    *domainNavigation.Ship
	reality domainNavigation.NavStatus

	// loseDockOnce injects drift: the next Dock updates the in-memory ship but
	// does NOT take effect server-side (reality is left unchanged), modelling a
	// dock the daemon believed succeeded while the server did not apply it (a
	// raced/lost transition). The following Refuel then hits 4214 and the
	// handler's self-heal must recover.
	loseDockOnce bool

	orbitCalls    int
	dockCalls     int
	refuelCalls   int
	navigateCalls int
	setModeCalls  int
}

func (r *tourShipRepo) FindBySymbol(_ context.Context, _ string, _ shared.PlayerID) (*domainNavigation.Ship, error) {
	return r.ship, nil
}

func (r *tourShipRepo) SyncShipFromAPI(_ context.Context, _ string, _ shared.PlayerID) (*domainNavigation.Ship, error) {
	return r.ship, nil
}

func (r *tourShipRepo) Orbit(_ context.Context, ship *domainNavigation.Ship, _ shared.PlayerID) error {
	r.orbitCalls++
	r.reality = domainNavigation.NavStatusInOrbit
	_, _ = ship.EnsureInOrbit()
	return nil
}

func (r *tourShipRepo) Dock(_ context.Context, ship *domainNavigation.Ship, _ shared.PlayerID) error {
	r.dockCalls++
	_, _ = ship.EnsureDocked() // in-memory always reflects the attempted dock
	if r.loseDockOnce {
		// Drift injection: the server did NOT actually dock (reality unchanged).
		r.loseDockOnce = false
		return nil
	}
	r.reality = domainNavigation.NavStatusDocked
	return nil
}

func (r *tourShipRepo) Refuel(_ context.Context, ship *domainNavigation.Ship, _ shared.PlayerID, _ *int) (*domainNavigation.RefuelResult, error) {
	if r.reality != domainNavigation.NavStatusDocked {
		// Mirror the live wire form of a docked-precondition rejection (4214).
		return nil, fmt.Errorf(`API error (status 400): {"error":{"code":4214,"message":"Ship %s must be docked to refuel."}}`, ship.ShipSymbol())
	}
	r.refuelCalls++
	_, _ = ship.RefuelToFull()
	// CreditsCost 0 keeps the handler's async ledger goroutine a no-op.
	return &domainNavigation.RefuelResult{
		CreditsCost:  0,
		FuelCurrent:  ship.Fuel().Current,
		FuelCapacity: ship.Fuel().Capacity,
	}, nil
}

func (r *tourShipRepo) Navigate(_ context.Context, ship *domainNavigation.Ship, destination *shared.Waypoint, _ shared.PlayerID) (*domainNavigation.Result, error) {
	if r.reality != domainNavigation.NavStatusInOrbit {
		// Mirror the live wire form of a not-in-orbit rejection (4236).
		return nil, fmt.Errorf(`API error (status 400): {"error":{"code":4236,"message":"Ship %s is not currently in orbit."}}`, ship.ShipSymbol())
	}
	r.navigateCalls++
	// Consume a plausible amount of fuel and settle the ship at the destination
	// in orbit (arrival), mirroring the real adapter's StartTransit+Arrive.
	cost := shared.FlightModeCruise.FuelCost(ship.CurrentLocation().DistanceTo(destination))
	if ship.Fuel().Current >= cost {
		_ = ship.ConsumeFuel(cost)
	}
	if err := ship.StartTransit(destination); err == nil {
		_ = ship.Arrive()
	}
	r.reality = domainNavigation.NavStatusInOrbit
	return &domainNavigation.Result{
		ArrivalTimeStr: "", // empty => executor skips the event wait
		FuelCurrent:    ship.Fuel().Current,
		FuelCapacity:   ship.Fuel().Capacity,
	}, nil
}

func (r *tourShipRepo) SetFlightMode(_ context.Context, ship *domainNavigation.Ship, _ shared.PlayerID, mode string) error {
	r.setModeCalls++
	ship.SetFlightMode(mode)
	return nil
}

func (r *tourShipRepo) Save(_ context.Context, _ *domainNavigation.Ship) error { return nil }

// --- dispatching mediator (routes to the real handlers) -------------------

type tourMediator struct {
	orbitH  *tactics.OrbitShipHandler
	dockH   *tactics.DockShipHandler
	refuelH *tactics.RefuelShipHandler
	navH    *shipnav.NavigateDirectHandler
	modeH   *shipnav.SetFlightModeHandler

	commands []mediator.Request
}

func (m *tourMediator) Send(ctx context.Context, request mediator.Request) (mediator.Response, error) {
	m.commands = append(m.commands, request)
	switch request.(type) {
	case *types.OrbitShipCommand:
		return m.orbitH.Handle(ctx, request)
	case *types.DockShipCommand:
		return m.dockH.Handle(ctx, request)
	case *types.RefuelShipCommand:
		return m.refuelH.Handle(ctx, request)
	case *types.NavigateDirectCommand:
		return m.navH.Handle(ctx, request)
	case *types.SetFlightModeCommand:
		return m.modeH.Handle(ctx, request)
	default:
		// Ledger RecordTransactionCommand etc. — not under test.
		return nil, nil
	}
}

func (m *tourMediator) Register(reflect.Type, mediator.RequestHandler) error { return nil }
func (m *tourMediator) RegisterMiddleware(mediator.Middleware)               {}

// --- fixtures -------------------------------------------------------------

func mustWaypoint(t *testing.T, symbol string, x, y float64, hasFuel bool) *shared.Waypoint {
	t.Helper()
	w, err := shared.NewWaypoint(symbol, x, y)
	if err != nil {
		t.Fatalf("NewWaypoint(%s): %v", symbol, err)
	}
	w.HasFuel = hasFuel
	return w
}

func newTourShip(t *testing.T, current, capacity int, location *shared.Waypoint, status domainNavigation.NavStatus) *domainNavigation.Ship {
	t.Helper()
	fuel, err := shared.NewFuel(current, capacity)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	cargo, err := shared.NewCargo(40, 0, nil)
	if err != nil {
		t.Fatalf("NewCargo: %v", err)
	}
	ship, err := domainNavigation.NewShip(
		"TORWIND-1", shared.MustNewPlayerID(1), location, fuel, capacity, 40, cargo,
		9, "FRAME_HAULER", "HAULER", nil, status,
	)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	return ship
}

func newTourHarness(spy *tourShipRepo) (*tourMediator, *appship.RouteExecutor) {
	tm := &tourMediator{}
	tm.orbitH = tactics.NewOrbitShipHandler(spy)
	tm.dockH = tactics.NewDockShipHandler(spy)
	// playerRepo/apiClient are unused on the success path; the async ledger
	// goroutine early-returns on CreditsCost==0, so nil deps are never touched.
	tm.refuelH = tactics.NewRefuelShipHandler(spy, nil, nil, tm)
	tm.navH = shipnav.NewNavigateDirectHandler(spy, nil)
	tm.modeH = shipnav.NewSetFlightModeHandler(spy)

	executor := appship.NewRouteExecutor(spy, tm, nil, nil, nil, nil, nil, stubEventSubscriber{})
	return tm, executor
}

func singleSegmentRoute(t *testing.T, from, to *shared.Waypoint, mode shared.FlightMode, requiresRefuel, refuelAtStart bool) *domainNavigation.Route {
	t.Helper()
	distance := from.DistanceTo(to)
	seg := domainNavigation.NewRouteSegment(from, to, distance, mode.FuelCost(distance), 0, mode, requiresRefuel)
	route, err := domainNavigation.NewRoute(
		"route-savings-1", "TORWIND-1", 1,
		[]*domainNavigation.RouteSegment{seg}, int(distance)*2, refuelAtStart,
	)
	if err != nil {
		t.Fatalf("NewRoute: %v", err)
	}
	return route
}

// --- CUT 2: stay docked after a post-arrival refuel -----------------------

// TestPostArrivalRefuel_StaysDocked_NoOrbitAfter pins CUT 2. A stop with a
// planned post-arrival refuel used to end dock->refuel->ORBIT and then the trade
// coordinator docked AGAIN — 2 dock + 2 orbit per stop. After CUT 2 the
// post-arrival refuel stays DOCKED (no orbit-after), so the very next
// DockShipCommand the trade issues is a CUT-1 no-op skip.
//
// Observable outcomes (all at the driven-port = one live API verb each):
//   - exactly ONE orbit call (the pre-navigate ensureShipInOrbit), NOT two;
//   - the ship ends DOCKED, ready for the trade's sell with no extra dock;
//   - a following trade DockShipCommand issues ZERO additional dock calls.
//
// MUTATION: reverting CUT 2 (orbit-after-refuel unconditionally) makes orbitCalls
// == 2 and leaves the ship IN_ORBIT, so this test fails — proving the guard is
// load-bearing.
func TestPostArrivalRefuel_StaysDocked_NoOrbitAfter(t *testing.T) {
	from := mustWaypoint(t, "X1-TORWIND-A", 0, 0, true)
	to := mustWaypoint(t, "X1-TORWIND-B", 100, 0, true)

	ship := newTourShip(t, 400, 400, from, domainNavigation.NavStatusDocked)
	spy := &tourShipRepo{ship: ship, reality: domainNavigation.NavStatusDocked}
	tm, executor := newTourHarness(spy)

	route := singleSegmentRoute(t, from, to, shared.FlightModeCruise, true /*requiresRefuel*/, false)

	if err := executor.ExecuteRoute(context.Background(), route, ship, shared.MustNewPlayerID(1)); err != nil {
		t.Fatalf("ExecuteRoute: %v", err)
	}

	if spy.orbitCalls != 1 {
		t.Fatalf("expected exactly 1 orbit API call (pre-navigate only), got %d — a post-arrival orbit-after-refuel was issued", spy.orbitCalls)
	}
	if spy.refuelCalls != 1 {
		t.Fatalf("expected exactly 1 refuel API call, got %d", spy.refuelCalls)
	}
	if !ship.IsDocked() {
		t.Fatalf("expected ship DOCKED after post-arrival refuel, got %s", ship.NavStatus())
	}

	// The trade coordinator now docks for the sell: a CUT-1 no-op skip.
	dockCallsBeforeTrade := spy.dockCalls
	if _, err := tm.Send(context.Background(), &types.DockShipCommand{ShipSymbol: ship.ShipSymbol(), PlayerID: shared.MustNewPlayerID(1)}); err != nil {
		t.Fatalf("trade dock: %v", err)
	}
	if spy.dockCalls != dockCallsBeforeTrade {
		t.Fatalf("expected the trade dock to be a CUT-1 no-op skip, but it issued a dock API call (before=%d after=%d)", dockCallsBeforeTrade, spy.dockCalls)
	}
}

// --- CUT 3: skip the pre-departure top-off when fuel already covers the leg -

// TestRefuelBeforeDeparture_SkipsWhenFuelSufficient pins CUT 3. A route planned
// with refuel-at-start used to ALWAYS dock->refuel->orbit before the first leg.
// After CUT 3 that trio is skipped when the ship already holds enough fuel for
// the first leg plus DefaultFuelSafetyMargin — the exact FuelCost primitive the
// affordability guard at route_executor.go:422 uses. The low-fuel case still
// refuels, so a ship is never stranded.
//
// First leg: CRUISE over distance 100 => FuelCost 100; margin 4 => threshold 104.
//
// MUTATION: reverting CUT 3 (unconditional pre-departure refuel) makes the
// sufficient-fuel case issue a refuel (refuelCalls==1), failing this test.
func TestRefuelBeforeDeparture_SkipsWhenFuelSufficient(t *testing.T) {
	tests := []struct {
		name              string
		currentFuel       int
		wantRefuelCalls   int
		wantStartDock     int // dock calls attributable to the pre-departure trio
		fuelSufficientMsg string
	}{
		{name: "sufficient fuel skips the whole trio", currentFuel: 400, wantRefuelCalls: 0, wantStartDock: 0},
		{name: "low fuel still refuels (no stranding)", currentFuel: 102, wantRefuelCalls: 1, wantStartDock: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			from := mustWaypoint(t, "X1-TORWIND-A", 0, 0, true)  // has fuel to refuel at
			to := mustWaypoint(t, "X1-TORWIND-B", 100, 0, false) // no fuel => no post-arrival refuel

			ship := newTourShip(t, tc.currentFuel, 400, from, domainNavigation.NavStatusInOrbit)
			spy := &tourShipRepo{ship: ship, reality: domainNavigation.NavStatusInOrbit}
			_, executor := newTourHarness(spy)

			route := singleSegmentRoute(t, from, to, shared.FlightModeCruise, false /*requiresRefuel*/, true /*refuelAtStart*/)

			if err := executor.ExecuteRoute(context.Background(), route, ship, shared.MustNewPlayerID(1)); err != nil {
				t.Fatalf("ExecuteRoute: %v", err)
			}

			if spy.refuelCalls != tc.wantRefuelCalls {
				t.Fatalf("fuel=%d: expected %d refuel API call(s), got %d", tc.currentFuel, tc.wantRefuelCalls, spy.refuelCalls)
			}
			if spy.dockCalls != tc.wantStartDock {
				t.Fatalf("fuel=%d: expected %d dock API call(s), got %d", tc.currentFuel, tc.wantStartDock, spy.dockCalls)
			}
		})
	}
}

// --- Drift / self-heal at the tour-leg level ------------------------------

// TestTourLeg_DriftAtRefuel_SelfHealsAndCompletes is the Admiral's drift test at
// the full-leg level: a whole ExecuteRoute where a dock does NOT take effect
// server-side (the in-memory ship believes it is DOCKED while reality is
// IN_ORBIT) at the post-arrival refuel decision point. The refuel is rejected
// with 4214; the leg MUST still complete because the handler self-heals — issues
// a real corrective dock and retries — and the ship ends DOCKED, ready for the
// trade's sell with no state that would 4xx. This is the proof that a wrong skip
// can never break trade/nav.
//
// MUTATION: removing the RefuelShipHandler self-heal makes ExecuteRoute return
// the 4214 error and the leg fails — this test fails, proving the safety net is
// load-bearing end-to-end.
func TestTourLeg_DriftAtRefuel_SelfHealsAndCompletes(t *testing.T) {
	from := mustWaypoint(t, "X1-TORWIND-A", 0, 0, true)
	to := mustWaypoint(t, "X1-TORWIND-B", 100, 0, true)

	ship := newTourShip(t, 400, 400, from, domainNavigation.NavStatusDocked)
	spy := &tourShipRepo{
		ship:         ship,
		reality:      domainNavigation.NavStatusDocked,
		loseDockOnce: true, // the post-arrival refuel's dock will be lost server-side
	}
	_, executor := newTourHarness(spy)

	route := singleSegmentRoute(t, from, to, shared.FlightModeCruise, true /*requiresRefuel*/, false)

	if err := executor.ExecuteRoute(context.Background(), route, ship, shared.MustNewPlayerID(1)); err != nil {
		t.Fatalf("tour leg must complete via self-heal despite dock drift, got error: %v", err)
	}

	if spy.refuelCalls != 1 {
		t.Fatalf("expected the refuel to succeed after self-heal re-dock, got %d successful refuel(s)", spy.refuelCalls)
	}
	if spy.dockCalls != 2 {
		t.Fatalf("expected 2 dock calls (the lost dock + the self-heal corrective dock), got %d", spy.dockCalls)
	}
	if !ship.IsDocked() {
		t.Fatalf("expected ship DOCKED after the leg (ready for the trade's sell), got %s", ship.NavStatus())
	}
	if spy.reality != domainNavigation.NavStatusDocked {
		t.Fatalf("expected server reality DOCKED after self-heal, got %s", spy.reality)
	}
}
