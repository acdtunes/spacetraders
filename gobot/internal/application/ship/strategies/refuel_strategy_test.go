package strategies_test

// The opportunistic-refuel threshold, exercised through the RefuelStrategy port
// the RouteExecutor consumes. Every assertion is a refuel decision, never the
// stored number: the threshold only matters through the decision it makes.

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/ship/strategies"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// fuelStopSegment is a landed segment whose destination sells fuel and which the
// routing engine did NOT plan a refuel for — the only shape in which the
// opportunistic threshold is ever consulted.
func fuelStopSegment(t *testing.T) *navigation.RouteSegment {
	t.Helper()
	from, err := shared.NewWaypoint("X1-TORWIND-A", 0, 0)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	to, err := shared.NewWaypoint("X1-TORWIND-B", 100, 0)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	to.HasFuel = true
	return navigation.NewRouteSegment(from, to, 100, 100, 0, shared.FlightModeCruise, false)
}

// hullParkedAt builds a hull standing at the segment's destination with the given tank.
func hullParkedAt(t *testing.T, at *shared.Waypoint, current, capacity int) *navigation.Ship {
	t.Helper()
	fuel, err := shared.NewFuel(current, capacity)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	cargo, err := shared.NewCargo(40, 0, nil)
	if err != nil {
		t.Fatalf("NewCargo: %v", err)
	}
	ship, err := navigation.NewShip(
		"TORWIND-1", shared.MustNewPlayerID(1), at, fuel, capacity, 40, cargo,
		9, "FRAME_HAULER", "HAULER", nil, navigation.NavStatusInOrbit,
	)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	return ship
}

// TestOpportunisticRefuelThreshold pins the three things an operator can do with
// the threshold knob, each as a refuel decision at a fuel-selling waypoint.
//
// Capacity is 400, so a tank reads in steps of 0.25% and the boundary rows pin
// the resolved floor to a quarter of a percentage point.
//
// MUTATIONS, one per row group:
//   - restoring the 0.9 default flips "a tank at the floor is left alone" to a refuel;
//   - ignoring the knob flips "a knob above the floor is honoured" to no refuel;
//   - dropping the clamp flips "a knob below the floor is held at it" to no refuel.
func TestOpportunisticRefuelThreshold(t *testing.T) {
	tests := []struct {
		name      string
		threshold float64
		fuel      int
		want      bool
	}{
		{name: "unset knob tops off a tank just under the floor", threshold: 0, fuel: 279, want: true},
		{name: "unset knob leaves a tank at the floor alone", threshold: 0, fuel: 280, want: false},
		{name: "a knob above the floor is honoured", threshold: 0.9, fuel: 320, want: true},
		{name: "a knob below the floor is held at it", threshold: 0.5, fuel: 279, want: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			segment := fuelStopSegment(t)
			ship := hullParkedAt(t, segment.ToWaypoint, tc.fuel, 400)

			got := strategies.NewConservativeRefuelStrategy(tc.threshold).ShouldRefuelAfterArrival(ship, segment)

			if got != tc.want {
				t.Fatalf("ShouldRefuelAfterArrival at %d/400 fuel with threshold %v = %v, want %v",
					tc.fuel, tc.threshold, got, tc.want)
			}
		})
	}
}

// TestDefaultRefuelStrategyHoldsTheFloor covers the OTHER way a strategy reaches
// the executor: the one it substitutes for a nil injection. It must sit on the
// same floor, or the unconfigured path is quietly the expensive one.
func TestDefaultRefuelStrategyHoldsTheFloor(t *testing.T) {
	segment := fuelStopSegment(t)
	strategy := strategies.NewDefaultRefuelStrategy()

	if strategy.ShouldRefuelAfterArrival(hullParkedAt(t, segment.ToWaypoint, 280, 400), segment) {
		t.Fatal("the substituted default topped off a tank AT the floor - the nil-strategy path is more expensive than the configured one")
	}
	if !strategy.ShouldRefuelAfterArrival(hullParkedAt(t, segment.ToWaypoint, 279, 400), segment) {
		t.Fatal("the substituted default left a tank UNDER the floor alone - the nil-strategy path would strand a hull")
	}
}
