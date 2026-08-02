package ship

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

func (e *RouteExecutor) selectOptimalFlightMode(ctx context.Context, segment *domainNavigation.RouteSegment, ship *domainNavigation.Ship, fuelReserve int) shared.FlightMode {
	logger := common.LoggerFromContext(ctx)

	// Special case: Ships with 0 fuel capacity (e.g., probes) don't consume fuel
	// They should ALWAYS use BURN mode for fastest travel
	if ship.Fuel().Capacity == 0 {
		if segment.FlightMode != shared.FlightModeBurn {
			logger.Log("INFO", "Zero-fuel ship using BURN mode", map[string]interface{}{
				"ship_symbol": ship.ShipSymbol(),
				"action":      "zero_fuel_burn",
				"reason":      "probes_always_burn",
			})
		}
		return shared.FlightModeBurn
	}

	distance := segment.FromWaypoint.DistanceTo(segment.ToWaypoint)

	// DRIFT is never flown as a route leg — ~7x CRUISE's travel time for fuel the
	// ship can go and buy — so a leg planned for it flies CRUISE instead. Paying for
	// that is handlePreDepartureRefuel's job, and ensureAffordableFlightMode's when
	// the tank still falls short.
	flightMode := segment.FlightMode.ForRouteLeg()

	fuelService := domainNavigation.NewShipFuelService()
	optimalMode, affordable := fuelService.SelectOptimalFlightMode(ship.Fuel().Current, distance, domainNavigation.DefaultFuelSafetyMargin)
	if !affordable {
		// The tank affords no mode at all. Hold the leg's own mode so the
		// affordability backstop refuels for it or fails the segment out loud.
		return flightMode
	}

	// The speed-up upgrade is a ONE-LEG decision against a WHOLE-ROUTE fuel budget,
	// so it may only spend what the rest of the plan does not need: BURN costs 2x,
	// and a leg upgraded out of a fuel station into a stop that sells none leaves
	// the following leg — budgeted by the planner against the tank it was supposed
	// to arrive with — unflyable, and it is then downgraded to a slower mode.
	// fuelReserve is that remaining need up to the next stop that can refuel; it is
	// 0 whenever the tank refills on arrival, so the speed-up survives untouched
	// wherever it is actually free.
	if optimalMode.IsFasterThan(flightMode) && ship.Fuel().Current >= optimalMode.FuelCost(distance)+fuelReserve {
		logger.Log("INFO", "Ship flight mode upgraded after refuel", map[string]interface{}{
			"ship_symbol":   ship.ShipSymbol(),
			"action":        "upgrade_flight_mode",
			"from_mode":     flightMode.Name(),
			"to_mode":       optimalMode.Name(),
			"distance":      distance,
			"fuel_current":  ship.Fuel().Current,
			"fuel_capacity": ship.Fuel().Capacity,
			"fuel_reserve":  fuelReserve,
		})
		flightMode = optimalMode
	}

	// Affordability clamp: never issue a Navigate whose fuel cost
	// exceeds the ship's ACTUAL fuel. The planner budgets each leg against the
	// ship's projected fuel, but an earlier BURN upgrade (or a stale plan) can
	// leave the ship unable to afford the planned mode by the time this leg runs
	// — producing an un-fuelable BURN and an API 4203 crash. Downgrading to
	// optimalMode turns an un-fuelable BURN into a flyable CRUISE; it can go no
	// lower, since optimalMode is affordable by construction here (the selector
	// reported it so) and never DRIFT.
	// Runs AFTER the upgrade branch so an upgraded mode is validated too.
	if required := flightMode.FuelCost(distance); ship.Fuel().Current < required {
		logger.Log("WARNING", "Ship flight mode downgraded - insufficient fuel for planned mode", map[string]interface{}{
			"ship_symbol":   ship.ShipSymbol(),
			"action":        "downgrade_flight_mode",
			"from_mode":     flightMode.Name(),
			"to_mode":       optimalMode.Name(),
			"distance":      distance,
			"required":      required,
			"fuel_current":  ship.Fuel().Current,
			"fuel_capacity": ship.Fuel().Capacity,
		})
		flightMode = optimalMode
	}
	return flightMode
}

// ensureAffordableFlightMode is the last-resort affordability backstop: it guarantees
// a Navigate is never emitted with fuelAvailable < fuelRequired.
//
// A leg is never degraded below CRUISE to fit the tank, so a tank too small for
// CRUISE reaches this point still holding a mode it cannot pay for. Emitting that
// Navigate makes the API reject it with error 4203.
//
// The remedy is fuel, not a slower mode: refuel at the departure waypoint
// (refuelShip no-ops when there is no fuel station) and re-pick the mode against
// the replenished tank. If the ship still cannot afford to move, fail the segment
// locally with a precise error instead of letting the opaque API 4203 surface and
// crash-loop the workflow container.
func (e *RouteExecutor) ensureAffordableFlightMode(
	ctx context.Context,
	segment *domainNavigation.RouteSegment,
	ship *domainNavigation.Ship,
	playerID shared.PlayerID,
	flightMode shared.FlightMode,
	fuelReserve int,
) (shared.FlightMode, error) {
	// Zero-capacity ships (e.g. probes) never consume fuel — nothing to guard.
	if ship.Fuel().Capacity == 0 {
		return flightMode, nil
	}

	distance := segment.FromWaypoint.DistanceTo(segment.ToWaypoint)
	if ship.Fuel().Current >= flightMode.FuelCost(distance) {
		return flightMode, nil
	}

	logger := common.LoggerFromContext(ctx)
	logger.Log("WARNING", "Ship cannot afford selected flight mode - attempting refuel backstop", map[string]interface{}{
		"ship_symbol":  ship.ShipSymbol(),
		"action":       "affordability_backstop",
		"mode":         flightMode.Name(),
		"distance":     distance,
		"required":     flightMode.FuelCost(distance),
		"fuel_current": ship.Fuel().Current,
		"waypoint":     segment.FromWaypoint.Symbol,
	})

	// A navigate follows this affordability backstop, so return to orbit.
	if err := e.refuelShipWithRetry(ctx, ship, playerID, true); err != nil {
		return flightMode, err
	}

	// Re-pick against the (possibly) replenished tank so a successful refuel still
	// yields the fastest affordable mode rather than the floor.
	flightMode = e.selectOptimalFlightMode(ctx, segment, ship, fuelReserve)
	if ship.Fuel().Current < flightMode.FuelCost(distance) {
		// Genuinely stranded: no fuel station here and too little fuel to move.
		return flightMode, fmt.Errorf(
			"insufficient fuel to depart %s for %s: have %d, need %d for %s over distance %.0f and no fuel station to refuel",
			segment.FromWaypoint.Symbol, segment.ToWaypoint.Symbol,
			ship.Fuel().Current, flightMode.FuelCost(distance), flightMode.Name(), distance,
		)
	}
	return flightMode, nil
}

func (e *RouteExecutor) setShipFlightMode(ctx context.Context, ship *domainNavigation.Ship, playerID shared.PlayerID, flightMode shared.FlightMode) error {
	setModeCmd := &types.SetFlightModeCommand{
		Ship:     ship,
		PlayerID: playerID,
		Mode:     flightMode,
	}
	if _, err := e.mediator.Send(ctx, setModeCmd); err != nil {
		return fmt.Errorf("failed to set flight mode: %w", err)
	}
	return nil
}
