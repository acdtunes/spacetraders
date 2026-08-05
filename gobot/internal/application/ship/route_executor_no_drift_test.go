package ship_test

// Port-to-port coverage for the no-DRIFT flight policy. Drives the REAL handlers
// through RouteExecutor.ExecuteRoute (the driving port) against the spy
// ShipRepository at the driven-port boundary, reusing the harness in
// route_executor_call_savings_test.go.

import (
	"context"
	"strings"
	"testing"

	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// TestExecuteRoute_LegTooExpensiveForCruiseRefuelsOrFails pins the policy: a leg
// the tank cannot pay for at CRUISE is refuelled where fuel is sold, and fails
// loudly where none is. DRIFT saves the fuel but costs ~7x the time, so it is
// never the answer - a stranded-but-visible hull beats one that quietly crawls.
func TestExecuteRoute_LegTooExpensiveForCruiseRefuelsOrFails(t *testing.T) {
	tests := []struct {
		name            string
		plannedMode     shared.FlightMode
		distance        float64
		shipFuel        int
		shipCapacity    int
		originHasFuel   bool
		wantErr         string
		wantRefuelCalls int
		wantLegModes    []shared.FlightMode
	}{
		{
			name:            "fuel sold where the hull sits - top off and fly the leg",
			plannedMode:     shared.FlightModeCruise,
			distance:        100,
			shipFuel:        20,
			shipCapacity:    150,
			originHasFuel:   true,
			wantRefuelCalls: 1,
			wantLegModes:    []shared.FlightMode{shared.FlightModeCruise},
		},
		{
			name:          "no fuel sold where the hull sits - fail loudly, fly nothing",
			plannedMode:   shared.FlightModeCruise,
			distance:      100,
			shipFuel:      20,
			shipCapacity:  150,
			originHasFuel: false,
			wantErr:       "insufficient fuel to depart",
			wantLegModes:  nil,
		},
		{
			// A leg costing 200 at CRUISE against a 100-unit tank is unflyable no
			// matter how much fuel is bought: the planner owes a refuel stop. Saying
			// so beats accepting the DRIFT the plan asked for.
			//
			// ONE refuel call, not two. The departure top-off fills 95->100; the
			// affordability backstop then asks for a refuel the tank cannot take
			// and is skipped at the fullness precondition (sp-l7zha) instead of
			// spending a second API call on a purchase of zero units. The outcome
			// is unchanged - still a loud failure, still zero legs flown.
			name:            "a planned DRIFT leg longer than the tank fails loudly rather than crawling",
			plannedMode:     shared.FlightModeDrift,
			distance:        200,
			shipFuel:        95,
			shipCapacity:    100,
			originHasFuel:   true,
			wantErr:         "insufficient fuel to depart",
			wantRefuelCalls: 1,
			wantLegModes:    nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			from := mustWaypoint(t, "X1-TORWIND-A", 0, 0, tc.originHasFuel)
			to := mustWaypoint(t, "X1-TORWIND-B", tc.distance, 0, false)

			ship := newTourShip(t, tc.shipFuel, tc.shipCapacity, from, domainNavigation.NavStatusInOrbit)
			spy := &tourShipRepo{ship: ship, reality: domainNavigation.NavStatusInOrbit}
			_, executor := newTourHarness(spy)

			route := singleSegmentRoute(t, from, to, tc.plannedMode, false, false)

			err := executor.ExecuteRoute(context.Background(), route, ship, shared.MustNewPlayerID(1))
			if tc.wantErr == "" && err != nil {
				t.Fatalf("ExecuteRoute: %v", err)
			}
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected the segment to fail loudly with %q, got nil - the hull was quietly degraded instead", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got: %v", tc.wantErr, err)
				}
			}

			if spy.refuelCalls != tc.wantRefuelCalls {
				t.Fatalf("expected %d refuel API call(s), got %d", tc.wantRefuelCalls, spy.refuelCalls)
			}
			if len(spy.navModes) != len(tc.wantLegModes) {
				t.Fatalf("expected %d leg(s) flown, got %d (%v)", len(tc.wantLegModes), len(spy.navModes), spy.navModes)
			}
			for i, want := range tc.wantLegModes {
				if got := flightModeNamed(spy.navModes[i]); got != want {
					t.Fatalf("leg%d flown as %s, want %s", i+1, got.Name(), want.Name())
				}
			}
		})
	}
}

// TestExecuteRoute_PlannedDriftLegFliesCruise pins the other half of the policy:
// DRIFT is unreachable even when the PLANNER asks for it. The routing service can
// emit a DRIFT step; the executor raises it to CRUISE and pays the fuel.
//
// The hull holds a full 150-unit tank: enough for CRUISE over distance 100 (100
// fuel), not enough for BURN (200), so CRUISE is the exact expected mode.
func TestExecuteRoute_PlannedDriftLegFliesCruise(t *testing.T) {
	from := mustWaypoint(t, "X1-TORWIND-A", 0, 0, true)
	to := mustWaypoint(t, "X1-TORWIND-B", 100, 0, false)

	ship := newTourShip(t, 150, 150, from, domainNavigation.NavStatusInOrbit)
	spy := &tourShipRepo{ship: ship, reality: domainNavigation.NavStatusInOrbit}
	_, executor := newTourHarness(spy)

	route := singleSegmentRoute(t, from, to, shared.FlightModeDrift, false, false)

	if err := executor.ExecuteRoute(context.Background(), route, ship, shared.MustNewPlayerID(1)); err != nil {
		t.Fatalf("ExecuteRoute: %v", err)
	}

	if len(spy.navModes) != 1 {
		t.Fatalf("expected exactly 1 leg flown, got %d (%v)", len(spy.navModes), spy.navModes)
	}
	if got := flightModeNamed(spy.navModes[0]); got != shared.FlightModeCruise {
		t.Fatalf("a planned DRIFT leg was flown as %s, want CRUISE - the tank covers it", got.Name())
	}
}

// TestExecuteRoute_LowTankOnAFuelStationTopsOffForABurnLeg pins the departure
// check. It fires on the fuel the leg ahead actually needs, whatever mode that
// leg is planned at: a hull sitting on fuel with 200 of a 400-unit tank and a
// BURN leg costing 228 must fill up and fly the leg as planned, not depart
// under-fuelled and get clamped down.
func TestExecuteRoute_LowTankOnAFuelStationTopsOffForABurnLeg(t *testing.T) {
	from := mustWaypoint(t, "X1-TORWIND-A", 0, 0, true)
	to := mustWaypoint(t, "X1-TORWIND-B", 114, 0, false)

	ship := newTourShip(t, 200, 400, from, domainNavigation.NavStatusInOrbit)
	spy := &tourShipRepo{ship: ship, reality: domainNavigation.NavStatusInOrbit}
	_, executor := newTourHarness(spy)

	route := singleSegmentRoute(t, from, to, shared.FlightModeBurn, false, false)

	if err := executor.ExecuteRoute(context.Background(), route, ship, shared.MustNewPlayerID(1)); err != nil {
		t.Fatalf("ExecuteRoute: %v", err)
	}

	if spy.refuelCalls != 1 {
		t.Fatalf("expected 1 pre-departure refuel (200 held, %d needed for the planned BURN), got %d",
			shared.FlightModeBurn.FuelCost(114), spy.refuelCalls)
	}
	if len(spy.navModes) != 1 {
		t.Fatalf("expected exactly 1 leg flown, got %d (%v)", len(spy.navModes), spy.navModes)
	}
	if got := flightModeNamed(spy.navModes[0]); got != shared.FlightModeBurn {
		t.Fatalf("leg flown as %s, want the planned BURN - the top-off pays for it", got.Name())
	}
}
