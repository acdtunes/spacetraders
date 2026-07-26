package ship_test

// Port-to-port coverage for the executor's fuel budget across a multi-leg route.
// Drives the REAL handlers through RouteExecutor.ExecuteRoute (the driving port)
// against the spy ShipRepository at the driven-port boundary, reusing the harness
// in route_executor_call_savings_test.go.

import (
	"context"
	"testing"

	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// flightModeNamed maps a ship's flight-mode name back to the enum so the spy can
// charge a leg at the mode it is actually flown in.
func flightModeNamed(name string) shared.FlightMode {
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

func twoSegmentRoute(t *testing.T, a, b, c *shared.Waypoint, first, second shared.FlightMode) *domainNavigation.Route {
	t.Helper()
	leg1Distance := a.DistanceTo(b)
	leg2Distance := b.DistanceTo(c)
	segments := []*domainNavigation.RouteSegment{
		domainNavigation.NewRouteSegment(a, b, leg1Distance, first.FuelCost(leg1Distance), 0, first, false),
		domainNavigation.NewRouteSegment(b, c, leg2Distance, second.FuelCost(leg2Distance), 0, second, false),
	}
	route, err := domainNavigation.NewRoute("route-fuel-budget", "TORWIND-1", 1, segments, 400, false)
	if err != nil {
		t.Fatalf("NewRoute: %v", err)
	}
	return route
}

// TestExecuteRoute_UpgradeNeverStarvesTheRestOfThePlan pins the defect behind the
// "every contract leg is downgraded" report: a hull that leaves a fuel station on a
// full tank and still ends up DRIFTing the next leg.
//
// The planner budgets a whole route against the tank: leg1 CRUISE (110 fuel) then
// leg2 BURN (228 fuel) is flyable from 400 with room to spare. The executor's
// speed-up upgrade looks at ONE leg, so it doubles leg1 to BURN (220), leaving 180 -
// less than leg2's planned 228. At a stop that sells no fuel there is no way back,
// and leg2 is flown slower than planned (in production: DRIFT, an 885s leg where
// CRUISE was ~128s).
//
// The observable business outcome is that NO leg is flown slower than the planner
// budgeted for it. The upgrade must therefore hold back the fuel the remaining plan
// needs before the next stop that can refuel - and must still take the speed-up when
// the tank refills at the intervening stop.
func TestExecuteRoute_UpgradeNeverStarvesTheRestOfThePlan(t *testing.T) {
	tests := []struct {
		name          string
		midHasFuel    bool
		wantLegModes  []shared.FlightMode
		upgradeReason string
	}{
		{
			name:          "no fuel at the intervening stop - leg1 keeps its planned CRUISE so leg2 can still BURN",
			midHasFuel:    false,
			wantLegModes:  []shared.FlightMode{shared.FlightModeCruise, shared.FlightModeBurn},
			upgradeReason: "upgrading leg1 to BURN spends 220 of 400 and strands leg2's planned 228",
		},
		{
			name:          "intervening stop sells fuel - leg1 still takes the BURN speed-up",
			midHasFuel:    true,
			wantLegModes:  []shared.FlightMode{shared.FlightModeBurn, shared.FlightModeBurn},
			upgradeReason: "the tank refills at the stop, so leg2 is unaffected by leg1's spend",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := mustWaypoint(t, "X1-TORWIND-A", 0, 0, true)
			b := mustWaypoint(t, "X1-TORWIND-B", 110, 0, tc.midHasFuel)
			c := mustWaypoint(t, "X1-TORWIND-C", 224, 0, true)

			ship := newTourShip(t, 400, 400, a, domainNavigation.NavStatusInOrbit)
			spy := &tourShipRepo{ship: ship, reality: domainNavigation.NavStatusInOrbit}
			_, executor := newTourHarness(spy)

			route := twoSegmentRoute(t, a, b, c, shared.FlightModeCruise, shared.FlightModeBurn)

			if err := executor.ExecuteRoute(context.Background(), route, ship, shared.MustNewPlayerID(1)); err != nil {
				t.Fatalf("ExecuteRoute: %v", err)
			}

			if len(spy.navModes) != len(tc.wantLegModes) {
				t.Fatalf("expected %d legs flown, got %d (%v)", len(tc.wantLegModes), len(spy.navModes), spy.navModes)
			}
			for i, want := range tc.wantLegModes {
				if got := flightModeNamed(spy.navModes[i]); got != want {
					t.Fatalf("leg%d flown as %s, want %s - %s", i+1, got.Name(), want.Name(), tc.upgradeReason)
				}
			}
			for i, segment := range route.Segments() {
				if flightModeNamed(spy.navModes[i]) == shared.FlightModeDrift && segment.FlightMode != shared.FlightModeDrift {
					t.Fatalf("leg%d was forced into DRIFT although the planner budgeted %s", i+1, segment.FlightMode.Name())
				}
			}
		})
	}
}
