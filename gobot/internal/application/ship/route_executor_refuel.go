package ship

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

func (e *RouteExecutor) handlePreDepartureRefuel(ctx context.Context, segment *domainNavigation.RouteSegment, ship *domainNavigation.Ship, playerID shared.PlayerID) error {
	logger := common.LoggerFromContext(ctx)
	if e.refuelStrategy.ShouldRefuelBeforeDeparture(ship, segment) {
		logger.Log("INFO", "Ship refueling before departure", map[string]interface{}{
			"ship_symbol":     ship.ShipSymbol(),
			"action":          "pre_departure_refuel",
			"waypoint":        segment.FromWaypoint.Symbol,
			"reason":          "strategy_decision",
			"refuel_strategy": e.refuelStrategy.GetStrategyName(),
		})
		// A navigate follows this refuel, so return to orbit.
		if err := e.refuelShipWithRetry(ctx, ship, playerID, true); err != nil {
			return err
		}
	}
	return nil
}

func (e *RouteExecutor) handlePostArrivalRefueling(ctx context.Context, segment *domainNavigation.RouteSegment, ship *domainNavigation.Ship, playerID shared.PlayerID) error {
	logger := common.LoggerFromContext(ctx)

	// Check for opportunistic refueling (strategy-based)
	if e.refuelStrategy.ShouldRefuelAfterArrival(ship, segment) {
		logger.Log("INFO", "Ship performing opportunistic refuel", map[string]interface{}{
			"ship_symbol":     ship.ShipSymbol(),
			"action":          "opportunistic_refuel",
			"waypoint":        segment.ToWaypoint.Symbol,
			"refuel_strategy": e.refuelStrategy.GetStrategyName(),
		})
		// CUT 2: stay docked after a post-arrival refuel. The next
		// action at this waypoint is a trade that docks; staying docked makes
		// that dock a CUT-1 no-op skip. A following segment re-orbits via
		// ensureShipInOrbit, so this is never a wrong state for a later navigate.
		if err := e.refuelShipWithRetry(ctx, ship, playerID, false); err != nil {
			return err
		}
	}

	// Always honor planned refuels from routing engine
	if segment.RequiresRefuel {
		logger.Log("INFO", "Ship performing planned refuel", map[string]interface{}{
			"ship_symbol": ship.ShipSymbol(),
			"action":      "planned_refuel",
			"waypoint":    segment.ToWaypoint.Symbol,
		})
		// CUT 2: stay docked after a post-arrival refuel (see above).
		if err := e.refuelShipWithRetry(ctx, ship, playerID, false); err != nil {
			return err
		}
	}

	return nil
}

// refuelBeforeDeparture refuels ship before starting the journey, retrying a
// transient failure with backoff and rerouting to an alternate fuel-capable
// waypoint if needed. Delegates to refuelShipWithRetry so both entry points share
// the same retry/reroute behavior.
func (e *RouteExecutor) refuelBeforeDeparture(
	ctx context.Context,
	route *domainNavigation.Route,
	ship *domainNavigation.Ship,
	playerID shared.PlayerID,
) error {
	// CUT 3: skip the whole dock/refuel/orbit trio when the ship
	// already holds enough fuel for the first leg plus the safety margin. Reuses
	// the same fuel-cost primitive as ensureAffordableFlightMode rather than
	// inventing a new threshold, so a ship that can already make the next leg does
	// not pay three redundant API verbs.
	if e.hasSufficientFuelForFirstLeg(route, ship) {
		common.LoggerFromContext(ctx).Log("INFO", "Skipping pre-departure refuel - sufficient fuel for first leg", map[string]interface{}{
			"ship_symbol":  ship.ShipSymbol(),
			"action":       "pre_departure_refuel_skipped",
			"fuel_current": ship.Fuel().Current,
		})
		return nil
	}
	// A navigate (the first segment) follows, so return to orbit.
	return e.refuelShipWithRetry(ctx, ship, playerID, true)
}

// hasSufficientFuelForFirstLeg reports whether the ship can already fly the
// route's first leg with the safety margin intact — the CUT 3 skip predicate.
//
// It is deliberately CONSERVATIVE: it reuses the exact FuelCost primitive the
// affordability guard uses (segment.FlightMode.FuelCost over the leg distance)
// plus DefaultFuelSafetyMargin, and returns false (i.e. DO refuel) on any
// uncertainty — a nil segment. A zero-capacity ship (probe) never consumes fuel,
// so it is always "sufficient". A wrong true here would strand a ship, so the
// margin buffer and the fail-safe-to-refuel default are load-bearing safety.
func (e *RouteExecutor) hasSufficientFuelForFirstLeg(route *domainNavigation.Route, ship *domainNavigation.Ship) bool {
	if ship.Fuel().Capacity == 0 {
		return true
	}
	segment := route.NextSegment()
	if segment == nil {
		return false
	}
	distance := segment.FromWaypoint.DistanceTo(segment.ToWaypoint)
	required := segment.FlightMode.FuelCost(distance) + domainNavigation.DefaultFuelSafetyMargin
	return ship.Fuel().Current >= required
}

// refuelShip refuels ship at current location.
//
// returnToOrbit controls the final transition (CUT 2). When true the
// ship is returned to orbit after refuelling — the correct choice when a
// navigate immediately follows (pre-departure / affordability backstop /
// alternate-stop reroute), since a navigate requires orbit. When false the ship
// STAYS DOCKED — the correct choice after a post-arrival refuel, because the
// very next action at the same waypoint is a trade that docks: leaving the ship
// docked turns that trade's DockShipCommand into a CUT-1 no-op skip and drops
// one orbit + one dock per stop. A subsequent segment (if any) re-orbits via
// ensureShipInOrbit, so staying docked is never left in a wrong state for a
// following navigate.
func (e *RouteExecutor) refuelShip(
	ctx context.Context,
	ship *domainNavigation.Ship,
	playerID shared.PlayerID,
	returnToOrbit bool,
) error {
	logger := common.LoggerFromContext(ctx)

	// GRACEFUL DEGRADATION: Skip refuel if current location has no fuel station
	// This handles stale waypoint cache data or routing service errors
	if !ship.CurrentLocation().HasFuel {
		logger.Log("WARNING", "Ship cannot refuel - no fuel station at current location", map[string]interface{}{
			"ship_symbol": ship.ShipSymbol(),
			"action":      "refuel_skipped",
			"waypoint":    ship.CurrentLocation().Symbol,
			"reason":      "no_fuel_station",
		})
		return nil // Skip refuel gracefully
	}

	dockCmd := &types.DockShipCommand{
		Ship:     ship,
		PlayerID: playerID,
	}
	if _, err := e.mediator.Send(ctx, dockCmd); err != nil {
		err = e.retryOnceAfterAdoptedTransit(ctx, ship, playerID, err, func() error {
			_, retryErr := e.mediator.Send(ctx, dockCmd)
			return retryErr
		})
		if err != nil {
			return fmt.Errorf("failed to dock for refuel: %w", err)
		}
	}

	refuelCmd := &types.RefuelShipCommand{
		Ship:     ship,
		PlayerID: playerID,
		Units:    nil, // Full refuel
	}
	if _, err := e.mediator.Send(ctx, refuelCmd); err != nil {
		return fmt.Errorf("failed to refuel: %w", err)
	}

	// CUT 2: only return to orbit when a navigate follows. When a trade at the
	// same waypoint follows we deliberately stay docked (see doc comment).
	if !returnToOrbit {
		return nil
	}

	orbitCmd := &types.OrbitShipCommand{
		Ship:     ship,
		PlayerID: playerID,
	}
	if _, err := e.mediator.Send(ctx, orbitCmd); err != nil {
		return fmt.Errorf("failed to orbit after refuel: %w", err)
	}

	return nil
}
