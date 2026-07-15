package ship

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/mediator"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// --- Test doubles ---------------------------------------------------------

// stubSubscriber satisfies domainNavigation.ShipEventSubscriber. The executor
// constructor panics on a nil subscriber, but none of these methods are
// exercised by the tests below (arrival waits are skipped via empty arrival
// times), so every method is a no-op.
type stubSubscriber struct{}

func (stubSubscriber) SubscribeArrived(string) <-chan domainNavigation.ShipArrivedEvent { return nil }
func (stubSubscriber) UnsubscribeArrived(string, <-chan domainNavigation.ShipArrivedEvent) {
}
func (stubSubscriber) SubscribeWorkerCompleted(string) <-chan domainNavigation.WorkerCompletedEvent {
	return nil
}
func (stubSubscriber) UnsubscribeWorkerCompleted(string, <-chan domainNavigation.WorkerCompletedEvent) {
}
func (stubSubscriber) SubscribeTasksBecameReady(int) <-chan domainNavigation.TasksBecameReadyEvent {
	return nil
}
func (stubSubscriber) UnsubscribeTasksBecameReady(int, <-chan domainNavigation.TasksBecameReadyEvent) {
}
func (stubSubscriber) SubscribeTransportRequested(int) <-chan domainNavigation.TransportRequestedEvent {
	return nil
}
func (stubSubscriber) UnsubscribeTransportRequested(int, <-chan domainNavigation.TransportRequestedEvent) {
}
func (stubSubscriber) SubscribeTransferCompleted(int) <-chan domainNavigation.TransferCompletedEvent {
	return nil
}
func (stubSubscriber) UnsubscribeTransferCompleted(int, <-chan domainNavigation.TransferCompletedEvent) {
}

// recordingMediator is a fake common.Mediator that records every command it
// receives and simulates the SpaceTraders navigate API's fuel accounting.
//
// Navigate spends mode.FuelCost(distanceToDestination) from a tracked fuel
// balance. If the requested mode costs more than the ship currently holds, it
// returns the exact 4203 error the real API returns (HTTP 400), reproducing the
// "requires N more fuel" crash. Orbit/Dock/SetFlightMode/Refuel all succeed.
type recordingMediator struct {
	commands   []mediator.Request
	fuel       int
	capacity   int
	distByDest map[string]float64 // destination symbol -> leg distance

	// refuelErrors, if non-empty, is consumed FIFO: each RefuelShipCommand
	// sent while the queue is non-empty pops and returns the front error
	// instead of succeeding; once empty, refuel succeeds normally (sp-vsfn).
	// Kept as a plain queue rather than keyed by ship location because the
	// NavigateDirectCommand case below does not mutate ship.CurrentLocation()
	// - only the real handler does that - so a location-keyed queue would not
	// observe a simulated reroute anyway.
	refuelErrors []error
}

func (m *recordingMediator) Send(_ context.Context, request mediator.Request) (mediator.Response, error) {
	m.commands = append(m.commands, request)

	switch cmd := request.(type) {
	case *types.OrbitShipCommand:
		return &types.OrbitShipResponse{Status: "in_orbit"}, nil
	case *types.DockShipCommand:
		return &types.DockShipResponse{Status: "docked"}, nil
	case *types.RefuelShipCommand:
		if len(m.refuelErrors) > 0 {
			err := m.refuelErrors[0]
			m.refuelErrors = m.refuelErrors[1:]
			return nil, err
		}
		m.fuel = m.capacity
		return &types.RefuelShipResponse{Status: "refueled", CurrentFuel: m.fuel, FuelCapacity: m.capacity}, nil
	case *types.SetFlightModeCommand:
		return &types.SetFlightModeResponse{Status: "set", Mode: cmd.Mode}, nil
	case *types.NavigateDirectCommand:
		mode := flightModeFromName(cmd.FlightMode)
		distance := m.distByDest[cmd.Destination]
		cost := mode.FuelCost(distance)
		if cost > m.fuel {
			// Mirror client.go's formatting of the API's 4203 rejection.
			return nil, fmt.Errorf(
				"API error (status 400): code 4203: Navigate request failed. Ship %s requires %d more fuel for navigation. fuelRequired: %d, fuelAvailable: %d",
				cmd.Ship.ShipSymbol(), cost-m.fuel, cost, m.fuel,
			)
		}
		m.fuel -= cost
		// Empty ArrivalTimeStr => executor skips the event wait.
		return &types.NavigateDirectResponse{
			Status:       "navigating",
			FuelCurrent:  m.fuel,
			FuelCapacity: m.capacity,
		}, nil
	default:
		return nil, fmt.Errorf("recordingMediator: unexpected command type %T", request)
	}
}

func (m *recordingMediator) Register(reflect.Type, mediator.RequestHandler) error { return nil }
func (m *recordingMediator) RegisterMiddleware(mediator.Middleware)               {}

// navigateCommands returns the NavigateDirectCommands in the order they were sent.
func (m *recordingMediator) navigateCommands() []*types.NavigateDirectCommand {
	var out []*types.NavigateDirectCommand
	for _, c := range m.commands {
		if nav, ok := c.(*types.NavigateDirectCommand); ok {
			out = append(out, nav)
		}
	}
	return out
}

// refuelAttempts returns the number of RefuelShipCommands sent, successful or not.
func (m *recordingMediator) refuelAttempts() int {
	count := 0
	for _, c := range m.commands {
		if _, ok := c.(*types.RefuelShipCommand); ok {
			count++
		}
	}
	return count
}

// flightModeFromName maps the executor's mode name (FlightMode.Name()) back to
// the enum so the fake can compute the leg's fuel cost.
func flightModeFromName(name string) shared.FlightMode {
	switch name {
	case "BURN":
		return shared.FlightModeBurn
	case "DRIFT":
		return shared.FlightModeDrift
	case "STEALTH":
		return shared.FlightModeStealth
	default:
		return shared.FlightModeCruise
	}
}

// --- Fixtures -------------------------------------------------------------

func mustWaypoint(t *testing.T, symbol string, x, y float64) *shared.Waypoint {
	t.Helper()
	w, err := shared.NewWaypoint(symbol, x, y)
	if err != nil {
		t.Fatalf("NewWaypoint(%s): %v", symbol, err)
	}
	return w
}

func newExecutorTestShip(t *testing.T, current, capacity int, location *shared.Waypoint) *domainNavigation.Ship {
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
		"TORWIND-1",
		shared.MustNewPlayerID(1),
		location,
		fuel,
		capacity,
		40,
		cargo,
		9,
		"FRAME_HAULER",
		"HAULER",
		nil,
		domainNavigation.NavStatusInOrbit,
	)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	return ship
}

// --- Tests ----------------------------------------------------------------

// TestSelectOptimalFlightMode_DowngradesPlannedBurnWhenFuelInsufficient pins the
// decision defect at route_executor.go:314. A leg planned as BURN over distance
// 114 requires 228 fuel (2x), but the ship holds only 180 (228-180 = 48 short).
// The executor must NOT keep the un-fuelable planned BURN; it must fall back to
// the affordable optimal mode (CRUISE, cost 114).
//
// Before the fix this returns BURN (the upgrade-only comparison never downgrades),
// which is exactly the 180-available/228-required signature from daemon.log.
func TestSelectOptimalFlightMode_DowngradesPlannedBurnWhenFuelInsufficient(t *testing.T) {
	from := mustWaypoint(t, "X1-TORWIND-A", 0, 0)
	to := mustWaypoint(t, "X1-TORWIND-B", 114, 0) // distance 114 from A

	ship := newExecutorTestShip(t, 180, 400, from)

	// Planned BURN leg: distance 114 -> 228 fuel required, ship holds only 180.
	segment := domainNavigation.NewRouteSegment(from, to, 114, 228, 0, shared.FlightModeBurn, false)

	executor := NewRouteExecutor(nil, nil, nil, nil, nil, nil, nil, stubSubscriber{})

	got := executor.selectOptimalFlightMode(context.Background(), segment, ship)

	if got != shared.FlightModeCruise {
		t.Fatalf("expected CRUISE downgrade (fuelAvailable 180 < BURN fuelRequired %d for distance 114), got %s",
			shared.FlightModeBurn.FuelCost(114), got.Name())
	}
}

// TestExecuteRoute_BurnUpgradeDoesNotStrandLaterBurnLeg pins the end-to-end
// divergence. leg1 (planned CRUISE, distance 110) gets upgraded to BURN on a full
// tank, spending 220 instead of 110 and leaving 180 fuel. leg2 (planned BURN,
// distance 114) then needs 228 but only 180 is available.
//
// Before the fix, leg2 is issued as BURN and the API rejects it with 4203
// ("requires 48 more fuel"), failing the route. After the fix, leg2 is clamped to
// the affordable CRUISE mode and the route completes.
func TestExecuteRoute_BurnUpgradeDoesNotStrandLaterBurnLeg(t *testing.T) {
	a := mustWaypoint(t, "X1-TORWIND-A", 0, 0)
	b := mustWaypoint(t, "X1-TORWIND-B", 110, 0) // A->B distance 110
	c := mustWaypoint(t, "X1-TORWIND-C", 224, 0) // B->C distance 114
	b.HasFuel = false                            // no top-off opportunity between the legs

	ship := newExecutorTestShip(t, 400, 400, a) // departs on a full tank

	leg1 := domainNavigation.NewRouteSegment(a, b, 110, 110, 0, shared.FlightModeCruise, false)
	leg2 := domainNavigation.NewRouteSegment(b, c, 114, 228, 0, shared.FlightModeBurn, false)

	route, err := domainNavigation.NewRoute(
		"route-torwind-1", "TORWIND-1", 1,
		[]*domainNavigation.RouteSegment{leg1, leg2}, 400, false,
	)
	if err != nil {
		t.Fatalf("NewRoute: %v", err)
	}

	fake := &recordingMediator{
		fuel:     400,
		capacity: 400,
		distByDest: map[string]float64{
			b.Symbol: 110,
			c.Symbol: 114,
		},
	}
	executor := NewRouteExecutor(nil, fake, nil, nil, nil, nil, nil, stubSubscriber{})

	err = executor.ExecuteRoute(context.Background(), route, ship, shared.MustNewPlayerID(1))
	if err != nil {
		t.Fatalf("ExecuteRoute should succeed once legs are planned against current fuel, got error: %v", err)
	}

	navCmds := fake.navigateCommands()
	if len(navCmds) != 2 {
		t.Fatalf("expected 2 navigate commands (one per leg), got %d", len(navCmds))
	}
	if navCmds[1].FlightMode != shared.FlightModeCruise.Name() {
		t.Fatalf("expected leg2 downgraded to CRUISE (ship holds 180, BURN needs 228), got %s", navCmds[1].FlightMode)
	}
}

// TestExecuteRoute_ZeroFuelStrandedFailsLocallyNotWith4203 pins the residual gap
// the affordability clamp does not close. The clamp downgrades to the fuel-optimal
// mode, but that mode's DRIFT fallback is never itself fuel-checked and DRIFT's
// cost floors at 1 — so a ship drained to exactly 0 fuel is still handed a DRIFT
// leg it cannot afford.
//
// leg1 (DRIFT, distance 90, cost 1) drains the tank from 1 to 0. leg2 then departs
// a no-fuel waypoint with 0 fuel and cannot afford even DRIFT (cost 1).
//
// Before ensureAffordableFlightMode, leg2 is issued as DRIFT and the API rejects it
// with 4203 ("requires 1 more fuel"), crash-looping the workflow container — the
// exact class of failure sp-c2bc targets. After the backstop, no un-fuelable
// Navigate is emitted: the executor fails the segment locally with a precise,
// non-4203 error, and leg2 never reaches the API.
func TestExecuteRoute_ZeroFuelStrandedFailsLocallyNotWith4203(t *testing.T) {
	a := mustWaypoint(t, "X1-TORWIND-A", 0, 0)
	b := mustWaypoint(t, "X1-TORWIND-B", 90, 0)  // A->B distance 90
	c := mustWaypoint(t, "X1-TORWIND-C", 180, 0) // B->C distance 90
	a.HasFuel = false                            // no fuel station anywhere on the route
	b.HasFuel = false

	ship := newExecutorTestShip(t, 1, 400, a) // one unit of fuel: exactly one DRIFT leg

	leg1 := domainNavigation.NewRouteSegment(a, b, 90, 1, 0, shared.FlightModeDrift, false)
	leg2 := domainNavigation.NewRouteSegment(b, c, 90, 1, 0, shared.FlightModeDrift, false)

	route, err := domainNavigation.NewRoute(
		"route-torwind-1", "TORWIND-1", 1,
		[]*domainNavigation.RouteSegment{leg1, leg2}, 400, false,
	)
	if err != nil {
		t.Fatalf("NewRoute: %v", err)
	}

	fake := &recordingMediator{
		fuel:     1,
		capacity: 400,
		distByDest: map[string]float64{
			b.Symbol: 90,
			c.Symbol: 90,
		},
	}
	executor := NewRouteExecutor(nil, fake, nil, nil, nil, nil, nil, stubSubscriber{})

	err = executor.ExecuteRoute(context.Background(), route, ship, shared.MustNewPlayerID(1))
	if err == nil {
		t.Fatalf("expected ExecuteRoute to fail locally when stranded at a no-fuel waypoint with 0 fuel")
	}
	if strings.Contains(err.Error(), "4203") {
		t.Fatalf("executor emitted an un-fuelable Navigate (API 4203) instead of failing locally: %v", err)
	}
	if !strings.Contains(err.Error(), "insufficient fuel to depart") {
		t.Fatalf("expected explicit local fuel error, got: %v", err)
	}

	navCmds := fake.navigateCommands()
	if len(navCmds) != 1 {
		t.Fatalf("expected exactly 1 navigate (leg1 only); leg2 must never reach the API, got %d", len(navCmds))
	}
	if navCmds[0].FlightMode != shared.FlightModeDrift.Name() {
		t.Fatalf("expected leg1 to fly DRIFT, got %s", navCmds[0].FlightMode)
	}
}

// fakeWaypointRepo is a minimal domainSystem.WaypointRepository test double.
// Only ListBySystemWithTrait is exercised by refuelAtAlternateStop (sp-vsfn);
// the other methods panic if called since no test in this file drives them.
type fakeWaypointRepo struct {
	bySystemTrait map[string][]*shared.Waypoint // key: systemSymbol+"|"+trait
}

func (f *fakeWaypointRepo) FindBySymbol(context.Context, string, string) (*shared.Waypoint, error) {
	panic("fakeWaypointRepo.FindBySymbol: not exercised by these tests")
}

func (f *fakeWaypointRepo) ListBySystem(context.Context, string) ([]*shared.Waypoint, error) {
	panic("fakeWaypointRepo.ListBySystem: not exercised by these tests")
}

func (f *fakeWaypointRepo) ListBySystemWithTrait(_ context.Context, systemSymbol, trait string) ([]*shared.Waypoint, error) {
	return f.bySystemTrait[systemSymbol+"|"+trait], nil
}

func (f *fakeWaypointRepo) Add(context.Context, *shared.Waypoint) error {
	panic("fakeWaypointRepo.Add: not exercised by these tests")
}

// TestRefuelShipWithRetry_TransientFailureRetriesWithBackoffThenSucceeds pins
// the first half of the sp-vsfn acceptance: a transient refuel 500 must retry
// with backoff instead of immediately surfacing as a terminal error. Two
// injected transient failures are followed by a success on the third
// attempt, all at the SAME waypoint (no reroute should be needed).
func TestRefuelShipWithRetry_TransientFailureRetriesWithBackoffThenSucceeds(t *testing.T) {
	origin := mustWaypoint(t, "X1-KA42-B7", 0, 0)
	origin.HasFuel = true

	ship := newExecutorTestShip(t, 50, 400, origin)

	fake := &recordingMediator{
		fuel:     50,
		capacity: 400,
		refuelErrors: []error{
			fmt.Errorf("max retries exceeded: server error (500)"),
			fmt.Errorf("max retries exceeded: server error (500)"),
		},
	}
	mockClock := &shared.MockClock{}
	executor := NewRouteExecutor(nil, fake, mockClock, nil, nil, nil, nil, stubSubscriber{})

	err := executor.refuelShipWithRetry(context.Background(), ship, shared.MustNewPlayerID(1))
	if err != nil {
		t.Fatalf("expected refuel to succeed after retrying the transient 500s, got error: %v", err)
	}

	if got := fake.refuelAttempts(); got != 3 {
		t.Fatalf("expected 3 refuel attempts (2 transient failures + 1 success), got %d", got)
	}
	if len(fake.navigateCommands()) != 0 {
		t.Fatalf("expected no reroute navigation since retry succeeded at the original waypoint, got %d navigate commands", len(fake.navigateCommands()))
	}
	if fake.fuel != fake.capacity {
		t.Fatalf("expected ship refueled to capacity, got fuel=%d capacity=%d", fake.fuel, fake.capacity)
	}
}

// TestRefuelShipWithRetry_NonTransientFailureFailsFastWithoutRetry pins the
// negative case: a non-transient error (e.g. insufficient credits) must NOT
// be retried or rerouted around - it should surface immediately so the
// caller sees the real problem instead of masking it behind a retry budget.
func TestRefuelShipWithRetry_NonTransientFailureFailsFastWithoutRetry(t *testing.T) {
	origin := mustWaypoint(t, "X1-KA42-B7", 0, 0)
	origin.HasFuel = true

	ship := newExecutorTestShip(t, 50, 400, origin)

	fake := &recordingMediator{
		fuel:     50,
		capacity: 400,
		refuelErrors: []error{
			fmt.Errorf("insufficient credits to purchase fuel"),
		},
	}
	mockClock := &shared.MockClock{}
	executor := NewRouteExecutor(nil, fake, mockClock, nil, nil, nil, nil, stubSubscriber{})

	err := executor.refuelShipWithRetry(context.Background(), ship, shared.MustNewPlayerID(1))
	if err == nil {
		t.Fatalf("expected refuel to fail fast on a non-transient error, got nil")
	}
	if !strings.Contains(err.Error(), "insufficient credits") {
		t.Fatalf("expected the original non-transient error to surface, got: %v", err)
	}

	if got := fake.refuelAttempts(); got != 1 {
		t.Fatalf("expected exactly 1 attempt (no retry for a non-transient error), got %d", got)
	}
	if len(fake.navigateCommands()) != 0 {
		t.Fatalf("expected no reroute attempt for a non-transient error, got %d navigate commands", len(fake.navigateCommands()))
	}
}

// TestRefuelShipWithRetry_RetriesExhaustedReroutesToAlternateFuelStop pins
// the second half of the sp-vsfn acceptance: once retries are exhausted at
// the original waypoint, the executor reroutes to the nearest ALTERNATE
// fuel-capable marketplace instead of giving up. A closer but non-fuel
// -selling marketplace (noFuelMarket) is seeded as a decoy the executor must
// skip - this pins the "verify fuel-stop selection is sane" half of the fix,
// not just "retry a second candidate blindly."
func TestRefuelShipWithRetry_RetriesExhaustedReroutesToAlternateFuelStop(t *testing.T) {
	origin := mustWaypoint(t, "X1-KA42-B7", 0, 0)
	origin.HasFuel = true
	origin.Traits = []string{"MARKETPLACE"}

	noFuelMarket := mustWaypoint(t, "X1-KA42-B8", 10, 0) // closer than alt, but sells no fuel
	noFuelMarket.HasFuel = false
	noFuelMarket.Traits = []string{"MARKETPLACE"}

	alt := mustWaypoint(t, "X1-KA42-B9", 30, 0) // farther, but the only fuel-capable alternate
	alt.HasFuel = true
	alt.Traits = []string{"MARKETPLACE"}

	ship := newExecutorTestShip(t, 100, 400, origin)

	fake := &recordingMediator{
		fuel:     100,
		capacity: 400,
		distByDest: map[string]float64{
			alt.Symbol: 30,
		},
		refuelErrors: []error{
			fmt.Errorf("max retries exceeded: server error (500)"),
			fmt.Errorf("max retries exceeded: server error (500)"),
			fmt.Errorf("max retries exceeded: server error (500)"),
		},
	}
	waypointRepo := &fakeWaypointRepo{
		bySystemTrait: map[string][]*shared.Waypoint{
			origin.SystemSymbol + "|MARKETPLACE": {origin, noFuelMarket, alt},
		},
	}
	mockClock := &shared.MockClock{}
	executor := NewRouteExecutor(nil, fake, mockClock, nil, nil, nil, waypointRepo, stubSubscriber{})

	err := executor.refuelShipWithRetry(context.Background(), ship, shared.MustNewPlayerID(1))
	if err != nil {
		t.Fatalf("expected reroute to the alternate fuel stop to succeed, got error: %v", err)
	}

	navCmds := fake.navigateCommands()
	if len(navCmds) != 1 {
		t.Fatalf("expected exactly 1 reroute navigate command, got %d", len(navCmds))
	}
	if navCmds[0].Destination != alt.Symbol {
		t.Fatalf("expected reroute to the fuel-capable alternate %s (skipping no-fuel decoy %s), got destination %s",
			alt.Symbol, noFuelMarket.Symbol, navCmds[0].Destination)
	}

	if got := fake.refuelAttempts(); got != 4 {
		t.Fatalf("expected 4 refuel attempts (3 exhausted at origin + 1 success at the alternate), got %d", got)
	}
}

// TestRefuelShipWithRetry_BothRetryAndRerouteExhaustedReturnsParkableError
// pins the THIRD and most critical half of the sp-vsfn acceptance: when
// retry-with-backoff at the original waypoint AND the alternate-fuel-stop
// reroute both fail to recover, refuelShipWithRetry must return a typed,
// unwrappable *ErrRefuelUnrecoverable rather than either panicking or
// returning an opaque error - this is exactly the signal a goods_factory
// coordinator needs to PARK (preserve chain state, resume next poll) instead
// of terminally crashing the way goods_factory-SHIP_PARTS-c7e2ecb2 and the
// SHIP_PLATING recurrence did. No fuel-capable alternate exists in this
// fixture's system (the only other marketplace present sells no fuel),
// so refuelAtAlternateStop's own "no candidate" failure feeds the wrap.
func TestRefuelShipWithRetry_BothRetryAndRerouteExhaustedReturnsParkableError(t *testing.T) {
	origin := mustWaypoint(t, "X1-KA42-B7", 0, 0)
	origin.HasFuel = true
	origin.Traits = []string{"MARKETPLACE"}

	noFuelMarket := mustWaypoint(t, "X1-KA42-B8", 10, 0) // only other marketplace, sells no fuel
	noFuelMarket.HasFuel = false
	noFuelMarket.Traits = []string{"MARKETPLACE"}

	ship := newExecutorTestShip(t, 50, 400, origin)

	fake := &recordingMediator{
		fuel:     50,
		capacity: 400,
		refuelErrors: []error{
			fmt.Errorf("max retries exceeded: server error (500)"),
			fmt.Errorf("max retries exceeded: server error (500)"),
			fmt.Errorf("max retries exceeded: server error (500)"),
		},
	}
	waypointRepo := &fakeWaypointRepo{
		bySystemTrait: map[string][]*shared.Waypoint{
			origin.SystemSymbol + "|MARKETPLACE": {origin, noFuelMarket},
		},
	}
	mockClock := &shared.MockClock{}
	executor := NewRouteExecutor(nil, fake, mockClock, nil, nil, nil, waypointRepo, stubSubscriber{})

	err := executor.refuelShipWithRetry(context.Background(), ship, shared.MustNewPlayerID(1))
	if err == nil {
		t.Fatalf("expected an error when both retry and reroute are exhausted, got nil")
	}

	var unrecoverable *ErrRefuelUnrecoverable
	if !errors.As(err, &unrecoverable) {
		t.Fatalf("expected err to be (or wrap) *ErrRefuelUnrecoverable so callers can park on it, got %T: %v", err, err)
	}
	if unrecoverable.ShipSymbol != ship.ShipSymbol() {
		t.Errorf("expected ShipSymbol %s, got %s", ship.ShipSymbol(), unrecoverable.ShipSymbol)
	}
	if unrecoverable.Waypoint != origin.Symbol {
		t.Errorf("expected Waypoint %s, got %s", origin.Symbol, unrecoverable.Waypoint)
	}
	if unrecoverable.Attempts != DefaultRefuelMaxAttempts {
		t.Errorf("expected Attempts %d, got %d", DefaultRefuelMaxAttempts, unrecoverable.Attempts)
	}
	if unrecoverable.Cause == nil || !strings.Contains(unrecoverable.Cause.Error(), "max retries exceeded") {
		t.Errorf("expected Cause to preserve the last transient refuel error, got: %v", unrecoverable.Cause)
	}

	if got := fake.refuelAttempts(); got != DefaultRefuelMaxAttempts {
		t.Fatalf("expected exactly %d refuel attempts (all at origin; no fuel-capable alternate to retry at), got %d",
			DefaultRefuelMaxAttempts, got)
	}
	if len(fake.navigateCommands()) != 0 {
		t.Fatalf("expected no reroute navigation since no fuel-capable alternate exists, got %d navigate commands", len(fake.navigateCommands()))
	}
}
