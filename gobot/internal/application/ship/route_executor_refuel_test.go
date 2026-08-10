package ship

import (
	"context"
	"errors"
	"strings"
	"testing"

	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// fuelStationStop is a waypoint the API designates FUEL_STATION whose cached traits
// are still the pre-charting placeholder, so the derived has-fuel bit reads false.
func fuelStationStop(t *testing.T, symbol string, x, y float64) *shared.Waypoint {
	t.Helper()
	wp := mustWaypoint(t, symbol, x, y)
	wp.Type = "FUEL_STATION"
	wp.Traits = []string{"UNCHARTED"}
	wp.HasFuel = false
	return wp
}

// TestExecuteRoute_RefuelsAtFuelStationTypeWhenCachedTraitsAreStaleUncharted pins the
// stranding: every refuel gate read the derived has-fuel bit, which an uncharted trait
// list makes false, so a hull sat on a fuel station it was refused permission to use.
func TestExecuteRoute_RefuelsAtFuelStationTypeWhenCachedTraitsAreStaleUncharted(t *testing.T) {
	a := fuelStationStop(t, "X1-TORWIND-A", 0, 0)
	b := mustWaypoint(t, "X1-TORWIND-B", 233, 0)

	ship := newExecutorTestShip(t, 160, 600, a)
	leg := domainNavigation.NewRouteSegment(a, b, 233, 466, 0, shared.FlightModeBurn, false)
	route, err := domainNavigation.NewRoute("r", "TORWIND-D", 1, []*domainNavigation.RouteSegment{leg}, 600, true)
	if err != nil {
		t.Fatalf("NewRoute: %v", err)
	}

	fake := &recordingMediator{fuel: 160, capacity: 600, distByDest: map[string]float64{b.Symbol: 233}}
	executor := NewRouteExecutor(nil, fake, nil, nil, nil, nil, nil, stubSubscriber{})

	err = executor.ExecuteRoute(context.Background(), route, ship, shared.MustNewPlayerID(1))
	if err != nil {
		t.Fatalf("ExecuteRoute should refuel at the fuel station and fly the leg, got error: %v", err)
	}

	if got := fake.refuelWaypoints(); len(got) == 0 || got[0] != a.Symbol {
		t.Fatalf("refuelWaypoints = %v, want a refuel at %s", got, a.Symbol)
	}
	if fake.lastIndexOfRefuel() > fake.firstIndexOfNavigate() {
		t.Fatalf("refuel at index %d came after the navigate at index %d - the leg departed unfuelled",
			fake.lastIndexOfRefuel(), fake.firstIndexOfNavigate())
	}
}

// TestExecuteRoute_FuelStationTypeRefuelRejectedByAPIFailsLocally covers the error-shape
// change: a gate that now opens can hand back a REAL refusal, which must surface as a
// named local error rather than an un-fuelable navigate or a panic.
func TestExecuteRoute_FuelStationTypeRefuelRejectedByAPIFailsLocally(t *testing.T) {
	a := fuelStationStop(t, "X1-TORWIND-A", 0, 0)
	b := mustWaypoint(t, "X1-TORWIND-B", 233, 0)

	ship := newExecutorTestShip(t, 160, 600, a)
	leg := domainNavigation.NewRouteSegment(a, b, 233, 466, 0, shared.FlightModeBurn, false)
	route, err := domainNavigation.NewRoute("r", "TORWIND-D", 1, []*domainNavigation.RouteSegment{leg}, 600, true)
	if err != nil {
		t.Fatalf("NewRoute: %v", err)
	}

	fake := &recordingMediator{
		fuel:            160,
		capacity:        600,
		distByDest:      map[string]float64{b.Symbol: 233},
		refuelAlwaysErr: errors.New("API error (status 400): code 4223: Market does not sell fuel"),
	}
	executor := NewRouteExecutor(nil, fake, nil, nil, nil, nil, nil, stubSubscriber{})

	err = executor.ExecuteRoute(context.Background(), route, ship, shared.MustNewPlayerID(1))
	if err == nil {
		t.Fatal("ExecuteRoute should fail when the API refuses the refuel and the tank cannot fly the leg")
	}
	if fake.refuelAttempts() == 0 {
		t.Fatal("no refuel was attempted, so the API never got to refuse one - the gate is still shut")
	}
	if strings.Contains(err.Error(), "4203") {
		t.Fatalf("executor emitted an un-fuelable navigate (API 4203) instead of failing locally: %v", err)
	}
	if len(fake.navigateCommands()) != 0 {
		t.Fatalf("expected no navigate to reach the API, got %d", len(fake.navigateCommands()))
	}
}
