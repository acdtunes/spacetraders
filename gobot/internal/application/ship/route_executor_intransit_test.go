package ship

// Route-level recovery for the arrival-state desync family (the 4214 crash
// cascade): when a navigate/dock/orbit is rejected because the SERVER shows
// the hull mid-transit, the handler adopts the server nav into the caller's
// ship and returns a typed ErrShipInTransit. The executor must WAIT that
// transit out (existing event machinery — bounded, no polling) and resume,
// instead of failing the route: each failed route burns one slot of the
// container's LIFETIME restart budget, and a transit outliving the backoff
// ladder is exactly how tour containers died unrecoverable in production.

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/mediator"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// adoptedTransitShip builds the post-adoption snapshot a handler leaves
// behind: IN_TRANSIT with CurrentLocation()==transit destination and a live
// arrival clock.
func adoptedTransitShip(t *testing.T, destination *shared.Waypoint) *domainNavigation.Ship {
	t.Helper()
	fuel, err := shared.NewFuel(400, 400)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	cargo, err := shared.NewCargo(40, 0, nil)
	if err != nil {
		t.Fatalf("NewCargo: %v", err)
	}
	ship, err := domainNavigation.NewShip(
		"TORWIND-1", shared.MustNewPlayerID(1), destination, fuel, 400, 40, cargo,
		9, "FRAME_HAULER", "HAULER", nil, domainNavigation.NavStatusInTransit,
	)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	ship.SetArrivalTime(time.Now().Add(90 * time.Second).UTC())
	return ship
}

// transitAdoptingMediator simulates the post-fix handler contract: the FIRST
// occurrence of the scripted command overwrites the command's ship with the
// adopted server transit and returns a typed ErrShipInTransit; subsequent
// occurrences succeed (or keep rejecting when alwaysReject is set). All other
// commands succeed unconditionally.
type transitAdoptingMediator struct {
	adopted      *domainNavigation.Ship // server transit to adopt on first rejection
	reject       string                 // one of "navigate", "dock", "orbit"
	alwaysReject bool

	navigateCalls int
	dockCalls     int
	orbitCalls    int
	rejected      bool
}

func (m *transitAdoptingMediator) rejectOnce(ship *domainNavigation.Ship) error {
	if !m.alwaysReject {
		m.rejected = true
	}
	*ship = *m.adopted
	dest := ""
	if loc := m.adopted.CurrentLocation(); loc != nil {
		dest = loc.Symbol
	}
	var arrival time.Time
	if at := m.adopted.ArrivalTime(); at != nil {
		arrival = *at
	}
	return &types.ErrShipInTransit{
		ShipSymbol:  ship.ShipSymbol(),
		Destination: dest,
		Arrival:     arrival,
		Cause:       errors.New(`API error (status 400): {"error":{"code":4214,"message":"Ship is currently in-transit."}}`),
	}
}

func (m *transitAdoptingMediator) Send(_ context.Context, request mediator.Request) (mediator.Response, error) {
	switch cmd := request.(type) {
	case *types.OrbitShipCommand:
		m.orbitCalls++
		if m.reject == "orbit" && !m.rejected {
			return nil, m.rejectOnce(cmd.Ship)
		}
		return &types.OrbitShipResponse{Status: "in_orbit"}, nil
	case *types.DockShipCommand:
		m.dockCalls++
		if m.reject == "dock" && !m.rejected {
			return nil, m.rejectOnce(cmd.Ship)
		}
		return &types.DockShipResponse{Status: "docked"}, nil
	case *types.RefuelShipCommand:
		return &types.RefuelShipResponse{Status: "refueled", CurrentFuel: 400, FuelCapacity: 400}, nil
	case *types.SetFlightModeCommand:
		return &types.SetFlightModeResponse{Status: "set", Mode: cmd.Mode}, nil
	case *types.NavigateDirectCommand:
		m.navigateCalls++
		if m.reject == "navigate" && !m.rejected {
			return nil, m.rejectOnce(cmd.Ship)
		}
		// Empty ArrivalTimeStr => executor skips the event wait.
		return &types.NavigateDirectResponse{Status: "navigating", FuelCurrent: 400, FuelCapacity: 400}, nil
	default:
		return nil, fmt.Errorf("transitAdoptingMediator: unexpected command type %T", request)
	}
}

func (m *transitAdoptingMediator) Register(reflect.Type, mediator.RequestHandler) error { return nil }
func (m *transitAdoptingMediator) RegisterMiddleware(mediator.Middleware)               {}

func singleSegmentRoute(t *testing.T, from, to *shared.Waypoint) *domainNavigation.Route {
	t.Helper()
	leg := domainNavigation.NewRouteSegment(from, to, 100, 100, 0, shared.FlightModeCruise, false)
	route, err := domainNavigation.NewRoute(
		"route-transit-1", "TORWIND-1", 1,
		[]*domainNavigation.RouteSegment{leg}, 400, false,
	)
	if err != nil {
		t.Fatalf("NewRoute: %v", err)
	}
	return route
}

// The adopted transit is already flying THIS segment's leg (the lost-response
// duplicate, or a dock-origin adoption toward the same waypoint): the arrival
// completes the segment — the route must succeed with no re-navigate and no
// surfaced error. On main the typed error fails the route and the container
// burns restart budget until the transit ends — the production crash path.
func TestExecuteRoute_AdoptedTransitToSegmentDestination_CompletesWithoutError(t *testing.T) {
	from := mustWaypoint(t, "X1-TRAN-A", 0, 0)
	to := mustWaypoint(t, "X1-TRAN-B", 100, 0)

	ship := newExecutorTestShip(t, 400, 400, from)
	fake := &transitAdoptingMediator{adopted: adoptedTransitShip(t, to), reject: "navigate"}
	executor := NewRouteExecutor(nil, fake, nil, nil, nil, nil, nil, arrivedNowSubscriber{})

	err := executor.ExecuteRoute(context.Background(), singleSegmentRoute(t, from, to), ship, shared.MustNewPlayerID(1))
	if err != nil {
		t.Fatalf("expected the adopted transit to complete the segment, got error: %v", err)
	}
	if fake.navigateCalls != 1 {
		t.Fatalf("expected no blind re-navigate after adopting the transit, got %d navigate calls", fake.navigateCalls)
	}
	if ship.NavStatus() == domainNavigation.NavStatusInTransit {
		t.Fatalf("expected the wait to resolve the adopted transit, ship still IN_TRANSIT")
	}
	if got := ship.CurrentLocation().Symbol; got != to.Symbol {
		t.Fatalf("expected the hull at the segment destination %s, got %s", to.Symbol, got)
	}
}

// The adopted transit lands the hull back on the segment ORIGIN (e.g. a
// reposition toward the leg's start the local snapshot never recorded): the
// premises still hold, so after the arrival the executor re-issues the
// navigate exactly once and the route completes.
func TestExecuteRoute_AdoptedTransitToSegmentOrigin_RetriesNavigateOnce(t *testing.T) {
	from := mustWaypoint(t, "X1-TRAN-A", 0, 0)
	to := mustWaypoint(t, "X1-TRAN-B", 100, 0)

	ship := newExecutorTestShip(t, 400, 400, from)
	fake := &transitAdoptingMediator{adopted: adoptedTransitShip(t, from), reject: "navigate"}
	executor := NewRouteExecutor(nil, fake, nil, nil, nil, nil, nil, arrivedNowSubscriber{})

	err := executor.ExecuteRoute(context.Background(), singleSegmentRoute(t, from, to), ship, shared.MustNewPlayerID(1))
	if err != nil {
		t.Fatalf("expected a retried navigate after landing on the segment origin, got error: %v", err)
	}
	if fake.navigateCalls != 2 {
		t.Fatalf("expected exactly 2 navigate calls (rejected + retried), got %d", fake.navigateCalls)
	}
}

// The adopted transit lands the hull somewhere that is NEITHER segment
// endpoint: the route's premises are gone and the error must surface (the
// caller re-plans) — but the ship must be left with TRUTHFUL nav state, not
// the phantom position that seeded the desync, and the executor must not
// blind-retry the navigate.
func TestExecuteRoute_AdoptedTransitElsewhere_FailsRouteWithTruthfulState(t *testing.T) {
	from := mustWaypoint(t, "X1-TRAN-A", 0, 0)
	to := mustWaypoint(t, "X1-TRAN-B", 100, 0)
	elsewhere := mustWaypoint(t, "X1-TRAN-C", 0, 100)

	ship := newExecutorTestShip(t, 400, 400, from)
	fake := &transitAdoptingMediator{adopted: adoptedTransitShip(t, elsewhere), reject: "navigate"}
	executor := NewRouteExecutor(nil, fake, nil, nil, nil, nil, nil, arrivedNowSubscriber{})

	err := executor.ExecuteRoute(context.Background(), singleSegmentRoute(t, from, to), ship, shared.MustNewPlayerID(1))
	if err == nil {
		t.Fatalf("expected the route to fail when the hull landed off the segment")
	}
	if fake.navigateCalls != 1 {
		t.Fatalf("expected no blind re-navigate toward broken premises, got %d navigate calls", fake.navigateCalls)
	}
	if ship.NavStatus() == domainNavigation.NavStatusInTransit {
		t.Fatalf("expected truthful post-arrival state, ship still IN_TRANSIT")
	}
	if got := ship.CurrentLocation().Symbol; got != elsewhere.Symbol {
		t.Fatalf("expected the hull's real position %s, got %s", elsewhere.Symbol, got)
	}
}

// The in-transit recovery is bounded to ONE retried navigate. A hull that
// keeps getting rejected (server permanently disagreeing) must surface an
// error after exactly two attempts - never loop adopt->wait->retry forever.
func TestExecuteRoute_AdoptedTransitKeepsRejecting_StopsAfterOneRetry(t *testing.T) {
	from := mustWaypoint(t, "X1-TRAN-A", 0, 0)
	to := mustWaypoint(t, "X1-TRAN-B", 100, 0)

	ship := newExecutorTestShip(t, 400, 400, from)
	// Every navigate rejects and re-adopts a transit landing back on the
	// origin, so an unbounded recovery would spin adopt->wait->retry forever.
	fake := &transitAdoptingMediator{adopted: adoptedTransitShip(t, from), reject: "navigate", alwaysReject: true}
	executor := NewRouteExecutor(nil, fake, nil, nil, nil, nil, nil, arrivedNowSubscriber{})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := executor.ExecuteRoute(ctx, singleSegmentRoute(t, from, to), ship, shared.MustNewPlayerID(1))
	if err == nil {
		t.Fatalf("expected the route to fail when the server keeps rejecting the navigate")
	}
	if fake.navigateCalls != 2 {
		t.Fatalf("expected exactly 2 navigate calls (rejected + one bounded retry), got %d", fake.navigateCalls)
	}
}

// A dock-for-refuel rejected mid-adopted-transit (the tour-run reposition
// signature: docking at the very waypoint the hull is still flying toward)
// must wait the transit out and dock once more instead of failing the leg.
func TestRefuelShip_AdoptedTransitOnDock_WaitsAndRetriesDockOnce(t *testing.T) {
	station := mustWaypoint(t, "X1-TRAN-F", 0, 0)
	station.HasFuel = true

	ship := newExecutorTestShip(t, 100, 400, station)
	fake := &transitAdoptingMediator{adopted: adoptedTransitShip(t, station), reject: "dock"}
	executor := NewRouteExecutor(nil, fake, nil, nil, nil, nil, nil, arrivedNowSubscriber{})

	err := executor.refuelShip(context.Background(), ship, shared.MustNewPlayerID(1), true)
	if err != nil {
		t.Fatalf("expected the refuel dock to recover from the adopted transit, got error: %v", err)
	}
	if fake.dockCalls != 2 {
		t.Fatalf("expected exactly 2 dock calls (rejected + retried), got %d", fake.dockCalls)
	}
}

// An orbit rejected mid-adopted-transit follows the same contract: wait, then
// exactly one more attempt.
func TestEnsureShipInOrbit_AdoptedTransit_WaitsAndRetriesOrbitOnce(t *testing.T) {
	origin := mustWaypoint(t, "X1-TRAN-A", 0, 0)
	dest := mustWaypoint(t, "X1-TRAN-B", 100, 0)

	ship := newExecutorTestShip(t, 400, 400, origin)
	fake := &transitAdoptingMediator{adopted: adoptedTransitShip(t, dest), reject: "orbit"}
	executor := NewRouteExecutor(nil, fake, nil, nil, nil, nil, nil, arrivedNowSubscriber{})

	err := executor.ensureShipInOrbit(context.Background(), ship, shared.MustNewPlayerID(1))
	if err != nil {
		t.Fatalf("expected the orbit to recover from the adopted transit, got error: %v", err)
	}
	if fake.orbitCalls != 2 {
		t.Fatalf("expected exactly 2 orbit calls (rejected + retried), got %d", fake.orbitCalls)
	}
}
