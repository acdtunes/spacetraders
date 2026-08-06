package ship

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// TestExecuteRoute_PlannedRefuelStaysEssentialForLegsBeyondTheFuelStop pins WHICH fuel figure
// decides that a planned refuel is essential.
//
// The flight-mode reserve is the plausible substitute, and it stops accumulating at the first
// waypoint that sells fuel — which is every waypoint a refuel is attempted at. Handed to this
// decision it reads zero exactly where the decision is made, excusing refuels the remaining
// plan depends on; the remaining-plan total keeps counting the legs past that stop.
//
//	A --BURN 25--> B (sells fuel, and fails) --CRUISE 300--> C --CRUISE 280--> D
//
// The hull reaches B able to fly the next leg but not the two beyond it, so the refuel there is
// load-bearing and its failure must stop the route. Excused, the hull departs its last fuel
// station and runs dry between two that sell none.
//
// The two refuel-attempt checks below stand in for "the fake really moved the hull". Only the
// emptiness one can fire as the fixture stands: a hull left at the departure waypoint stands
// where nothing is sold, so every refuel is skipped and no attempt is recorded. The per-waypoint
// one is a guard against the drift that would silence that — give A a fuel station and a
// stranded hull refuels there, leaving the emptiness check green on a run that proves nothing.
func TestExecuteRoute_PlannedRefuelStaysEssentialForLegsBeyondTheFuelStop(t *testing.T) {
	const capacity = 600

	a := mustWaypoint(t, "X1-KP46-A1", 0, 0)
	b := mustWaypoint(t, "X1-KP46-B2", 25, 0)
	c := mustWaypoint(t, "X1-KP46-C3", 325, 0)
	d := mustWaypoint(t, "X1-KP46-D4", 605, 0)
	b.HasFuel = true // the route's only fuel station

	// The opening leg is short and flown at its own BURN, so no speed-up upgrade enters the
	// arithmetic and the hull lands at B still above the conservative top-off threshold.
	seg0 := domainNavigation.NewRouteSegment(a, b, 25, 50, 0, shared.FlightModeBurn, true)
	seg1 := domainNavigation.NewRouteSegment(b, c, 300, 300, 0, shared.FlightModeCruise, false)
	seg2 := domainNavigation.NewRouteSegment(c, d, 280, 280, 0, shared.FlightModeCruise, false)

	route, err := domainNavigation.NewRoute(
		"route-planned-refuel-scope", "TORWIND-1", 1,
		[]*domainNavigation.RouteSegment{seg0, seg1, seg2}, capacity, false,
	)
	if err != nil {
		t.Fatalf("NewRoute: %v", err)
	}

	if reserve := route.FuelReserveAfterCurrentSegment(); reserve != 0 {
		t.Fatalf("fixture: the reserve must read 0 at the fuel station for the two figures to differ, got %d", reserve)
	}
	if remaining := fuelForSegmentsAfter(route, 0); remaining <= 0 {
		t.Fatalf("fixture: the legs past the fuel station must still cost fuel, got %d", remaining)
	}

	ship := newExecutorTestShip(t, capacity, capacity, a)

	mockClock := &shared.MockClock{CurrentTime: time.Now()}
	fake := &recordingMediator{
		fuel:            capacity,
		capacity:        capacity,
		distByDest:      map[string]float64{b.Symbol: 25, c.Symbol: 300, d.Symbol: 280},
		refuelAlwaysErr: transientRefuelErr(),
		clock:           mockClock,
	}
	// No waypoint repository, so the escalation has nowhere to reroute to and ends in its own
	// typed error rather than flying the hull somewhere this fixture would have to model.
	executor := NewRouteExecutor(nil, fake, mockClock, nil, nil, nil, nil, stubSubscriber{})

	counter := &countingNavMetrics{nonEssential: map[string]int{}}
	metrics.SetGlobalNavigationCollector(counter)
	defer metrics.SetGlobalNavigationCollector(nil)

	execErr := executor.ExecuteRoute(context.Background(), route, ship, shared.MustNewPlayerID(1))

	if len(fake.refuelWaypoints()) == 0 {
		t.Fatal("no refuel was attempted anywhere, so the planned refuel was never reached and nothing below proves anything")
	}
	for i, wp := range fake.refuelWaypoints() {
		if wp != b.Symbol {
			t.Fatalf("refuel attempt %d ran at %s, not at the fuel station %s - the hull is not where this test claims it is", i, wp, b.Symbol)
		}
	}

	if execErr == nil {
		t.Fatal("the legs past the fuel station cannot be flown on what the hull holds, so this refuel is load-bearing and its failure must stop the route")
	}
	// Only the essential path reaches for an alternate stop, so only it wraps the result.
	var unrecoverable *ErrRefuelUnrecoverable
	if !errors.As(execErr, &unrecoverable) {
		t.Fatalf("expected the escalating path's typed unrecoverable-refuel error, got %T: %v", execErr, execErr)
	}
	if len(counter.nonEssential) != 0 {
		t.Fatalf("a refuel the remaining legs depend on must not be absorbed as optional, got %v", counter.nonEssential)
	}
	if navs := fake.navigateCommands(); len(navs) != 1 {
		t.Fatalf("the hull must not leave the fuel station it could not refuel at, got %d navigate(s)", len(navs))
	}
	if route.Status() != domainNavigation.RouteStatusFailed {
		t.Fatalf("expected route FAILED, got %s", route.Status())
	}
}
