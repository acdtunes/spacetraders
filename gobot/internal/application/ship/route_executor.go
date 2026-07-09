package ship

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/strategies"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// RouteExecutor executes routes by orchestrating atomic ship commands via mediator
//
// This is the CRITICAL orchestration service that replaces the executeRoute() method
// in NavigateRouteHandler. It uses the mediator pattern to send atomic commands:
// - OrbitShipCommand
// - DockShipCommand
// - RefuelShipCommand
// - NavigateDirectCommand
// - SetFlightModeCommand
//
// It follows the exact same logic as the Python implementation with all safety features:
// 1. Handle IN_TRANSIT from previous command (idempotency)
// 2. Refuel before departure if needed
// 3. Execute each segment step-by-step
// 4. Pre-departure refuel check (prevent DRIFT mode at fuel stations)
// 5. Opportunistic refueling (configurable via strategy)
// 6. Planned refueling (required by routing engine)
// 7. Automatic market scanning at marketplace waypoints
//
// The refueling behavior is now extensible via the Strategy pattern:
//   - ConservativeRefuelStrategy: Maintains high fuel levels (default)
//   - MinimalRefuelStrategy: Only refuels when necessary
//   - AlwaysTopOffStrategy: Refuels at every opportunity
//
// Event-driven arrival waiting:
// Uses ShipEventSubscriber to wait for ship arrivals via events from ShipStateScheduler.
// This eliminates race conditions between timer-based state transitions and polling.
type RouteExecutor struct {
	shipRepo            domainNavigation.ShipRepository
	mediator            common.Mediator
	clock               shared.Clock
	marketScanner       *MarketScanner
	refuelStrategy      strategies.RefuelStrategy
	shipEventSubscriber domainNavigation.ShipEventSubscriber
}

// NewRouteExecutor creates a new route executor
// If clock is nil, uses RealClock (production behavior)
// If marketScanner is nil, disables automatic market scanning
// If refuelStrategy is nil, uses default ConservativeRefuelStrategy (90% threshold)
// shipEventSubscriber is required for event-based arrival waiting
func NewRouteExecutor(
	shipRepo domainNavigation.ShipRepository,
	mediator common.Mediator,
	clock shared.Clock,
	marketScanner *MarketScanner,
	refuelStrategy strategies.RefuelStrategy,
	shipEventSubscriber domainNavigation.ShipEventSubscriber,
) *RouteExecutor {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	if refuelStrategy == nil {
		refuelStrategy = strategies.NewDefaultRefuelStrategy()
	}
	if shipEventSubscriber == nil {
		panic("shipEventSubscriber is required for RouteExecutor")
	}
	return &RouteExecutor{
		shipRepo:            shipRepo,
		mediator:            mediator,
		clock:               clock,
		marketScanner:       marketScanner,
		refuelStrategy:      refuelStrategy,
		shipEventSubscriber: shipEventSubscriber,
	}
}

// ExecuteRoute executes a route step-by-step using atomic commands
//
// This orchestrates all the atomic commands we created in Phase 2.1-2.3:
// - Uses mediator.Send() to invoke commands
// - Uses domain decision methods (ShouldRefuelOpportunistically, ShouldPreventDriftMode)
// - Follows exact Python implementation logic
//
// The operation context (if any) should be added to ctx using shared.WithOperationContext()
// before calling this method. It will be automatically propagated to all child operations.
func (e *RouteExecutor) ExecuteRoute(
	ctx context.Context,
	route *domainNavigation.Route,
	ship *domainNavigation.Ship,
	playerID shared.PlayerID,
) error {
	// Extract logger from context
	logger := common.LoggerFromContext(ctx)

	// 0. Start route execution (transition from PLANNED to EXECUTING)
	if err := route.StartExecution(); err != nil {
		return fmt.Errorf("failed to start route execution: %w", err)
	}

	// 1. Handle IN_TRANSIT from previous command (idempotency)
	// This makes navigation commands idempotent - you can send them at any time
	if ship.NavStatus() == domainNavigation.NavStatusInTransit {
		if err := e.waitForCurrentTransit(ctx, ship, playerID); err != nil {
			return err
		}
	}

	// 2. Refuel before departure if needed (ship at fuel station with low fuel)
	if route.HasRefuelAtStart() {
		if err := e.refuelBeforeDeparture(ctx, ship, playerID); err != nil {
			return err
		}
	}

	// 3. Execute each segment
	segmentCount := 0
	for {
		segment := route.NextSegment()

		if segment == nil {
			logger.Log("INFO", "Route execution complete - no more segments", map[string]interface{}{
				"ship_symbol":       ship.ShipSymbol(),
				"action":            "route_complete",
				"segments_executed": segmentCount,
				"total_segments":    len(route.Segments()),
			})
			break // Route complete
		}

		logger.Log("INFO", "Route segment execution started", map[string]interface{}{
			"ship_symbol":   ship.ShipSymbol(),
			"action":        "execute_segment",
			"segment_index": segmentCount,
			"from":          segment.FromWaypoint.Symbol,
			"to":            segment.ToWaypoint.Symbol,
		})

		if err := e.executeSegment(ctx, segment, ship, playerID); err != nil {
			logger.Log("ERROR", "Route segment execution failed", map[string]interface{}{
				"ship_symbol":   ship.ShipSymbol(),
				"action":        "execute_segment",
				"segment_index": segmentCount,
				"from":          segment.FromWaypoint.Symbol,
				"to":            segment.ToWaypoint.Symbol,
				"error":         err.Error(),
			})
			if failErr := route.FailRoute(err.Error()); failErr != nil {
				logger.Log("ERROR", "Failed to mark route as failed", map[string]interface{}{
					"ship_symbol": ship.ShipSymbol(),
					"action":      "fail_route",
					"error":       failErr.Error(),
				})
			}

			// Record route failure metrics
			duration := time.Since(route.CreatedAt()).Seconds()
			metrics.RecordRouteCompletion(
				route.PlayerID(),
				route.Status(),
				duration,
				int(route.TotalDistance()),
				route.TotalFuelRequired(),
			)

			return err
		}

		logger.Log("INFO", "Route segment completed successfully", map[string]interface{}{
			"ship_symbol":   ship.ShipSymbol(),
			"action":        "segment_complete",
			"segment_index": segmentCount,
		})

		// Complete segment in route
		if err := route.CompleteSegment(); err != nil {
			logger.Log("ERROR", "Failed to mark segment as complete", map[string]interface{}{
				"ship_symbol":   ship.ShipSymbol(),
				"action":        "complete_segment",
				"segment_index": segmentCount,
				"error":         err.Error(),
			})
			return err
		}

		// Record segment completion metrics
		metrics.RecordSegmentCompletion(
			route.PlayerID(),
			int(segment.Distance),
			segment.FuelRequired,
		)

		segmentCount++
	}

	// Record route completion metrics
	duration := time.Since(route.CreatedAt()).Seconds()
	metrics.RecordRouteCompletion(
		route.PlayerID(),
		route.Status(),
		duration,
		int(route.TotalDistance()),
		route.TotalFuelRequired(),
	)

	logger.Log("INFO", "Route execution finished", map[string]interface{}{
		"ship_symbol":       ship.ShipSymbol(),
		"action":            "route_finished",
		"segments_executed": segmentCount,
		"status":            string(route.Status()),
	})

	return nil
}

// executeSegment executes a single route segment using atomic commands
func (e *RouteExecutor) executeSegment(
	ctx context.Context,
	segment *domainNavigation.RouteSegment,
	ship *domainNavigation.Ship,
	playerID shared.PlayerID,
) error {
	// OPTIMIZATION: Only reload ship if it might be in transit
	// The previous segment's waitForArrival already updated ship state
	// We only need to check/wait if the ship is IN_TRANSIT
	if ship.NavStatus() == domainNavigation.NavStatusInTransit {
		if err := e.waitForCurrentTransit(ctx, ship, playerID); err != nil {
			return fmt.Errorf("failed to wait for transit before segment: %w", err)
		}
	}

	if err := e.ensureShipInOrbit(ctx, ship, playerID); err != nil {
		return err
	}

	if err := e.handlePreDepartureRefuel(ctx, segment, ship, playerID); err != nil {
		return err
	}

	flightMode := e.selectOptimalFlightMode(ctx, segment, ship)

	flightMode, err := e.ensureAffordableFlightMode(ctx, segment, ship, playerID, flightMode)
	if err != nil {
		return err
	}

	if err := e.setShipFlightMode(ctx, ship, playerID, flightMode); err != nil {
		return err
	}

	if err := e.navigateToSegmentDestination(ctx, segment, ship, playerID, flightMode); err != nil {
		return err
	}

	if err := e.handlePostArrivalRefueling(ctx, segment, ship, playerID); err != nil {
		return err
	}

	e.scanMarketIfPresent(ctx, segment, ship, playerID)

	return nil
}

func (e *RouteExecutor) ensureShipInOrbit(ctx context.Context, ship *domainNavigation.Ship, playerID shared.PlayerID) error {
	orbitCmd := &types.OrbitShipCommand{
		Ship:     ship,
		PlayerID: playerID,
	}
	if _, err := e.mediator.Send(ctx, orbitCmd); err != nil {
		return fmt.Errorf("failed to orbit: %w", err)
	}
	return nil
}

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
		if err := e.refuelShip(ctx, ship, playerID); err != nil {
			return err
		}
	}
	return nil
}

func (e *RouteExecutor) selectOptimalFlightMode(ctx context.Context, segment *domainNavigation.RouteSegment, ship *domainNavigation.Ship) shared.FlightMode {
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
	fuelService := domainNavigation.NewShipFuelService()
	optimalMode := fuelService.SelectOptimalFlightMode(ship.Fuel().Current, distance, domainNavigation.DefaultFuelSafetyMargin)

	flightMode := segment.FlightMode
	if optimalMode > segment.FlightMode {
		logger.Log("INFO", "Ship flight mode upgraded after refuel", map[string]interface{}{
			"ship_symbol":   ship.ShipSymbol(),
			"action":        "upgrade_flight_mode",
			"from_mode":     segment.FlightMode.Name(),
			"to_mode":       optimalMode.Name(),
			"distance":      distance,
			"fuel_current":  ship.Fuel().Current,
			"fuel_capacity": ship.Fuel().Capacity,
		})
		flightMode = optimalMode
	}

	// Affordability clamp (sp-c2bc): never issue a Navigate whose fuel cost
	// exceeds the ship's ACTUAL fuel. The planner budgets each leg against the
	// ship's projected fuel, but an earlier BURN upgrade (or a stale plan) can
	// leave the ship unable to afford the planned mode by the time this leg runs
	// — producing an un-fuelable BURN and an API 4203 crash. Downgrading to
	// optimalMode turns an un-fuelable leg into a slower-but-successful one:
	// optimalMode is affordable by construction for BURN/CRUISE (FlightModeSelector
	// only picks them when fuel covers the cost plus margin). Its DRIFT fallback is
	// the lone exception — DriftModeStrategy.CanUse is unconditional and DRIFT's
	// FuelCost floors at 1 — so a tank drained to ~0 is NOT caught here; that
	// residual is handled by ensureAffordableFlightMode before the Navigate.
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

// ensureAffordableFlightMode is the last-resort affordability backstop for sp-c2bc.
//
// selectOptimalFlightMode downgrades to the fuel-optimal mode, which is affordable
// by construction EXCEPT for its DRIFT fallback: DriftModeStrategy.CanUse always
// returns true and FlightMode.FuelCost floors DRIFT at 1, so a ship that has
// drained to (effectively) zero fuel is still handed a DRIFT leg it cannot pay
// for. Emitting that Navigate makes the API reject it with 4203 — the exact crash
// this bead targets ("never emit a leg with fuelAvailable < fuelRequired").
//
// When even the selected mode is unaffordable, refuel at the departure waypoint
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

	if err := e.refuelShip(ctx, ship, playerID); err != nil {
		return flightMode, err
	}

	// Re-pick against the (possibly) replenished tank so a successful refuel still
	// yields the fastest affordable mode rather than defaulting to DRIFT.
	flightMode = e.selectOptimalFlightMode(ctx, segment, ship)
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

func (e *RouteExecutor) navigateToSegmentDestination(ctx context.Context, segment *domainNavigation.RouteSegment, ship *domainNavigation.Ship, playerID shared.PlayerID, flightMode shared.FlightMode) error {
	logger := common.LoggerFromContext(ctx)

	navCmd := &types.NavigateDirectCommand{
		Ship:                ship,
		Destination:         segment.ToWaypoint.Symbol,
		DestinationWaypoint: segment.ToWaypoint, // Pass enriched waypoint with HasFuel
		PlayerID:            playerID,
		FlightMode:          flightMode.Name(),
	}
	navResp, err := e.mediator.Send(ctx, navCmd)
	if err != nil {
		return fmt.Errorf("failed to navigate: %w", err)
	}

	navResponse, ok := navResp.(*types.NavigateDirectResponse)
	if !ok {
		return fmt.Errorf("unexpected response type: %T", navResp)
	}

	logger.Log("INFO", "Ship navigation command executed", map[string]interface{}{
		"ship_symbol":  ship.ShipSymbol(),
		"action":       "navigate_command_sent",
		"status":       navResponse.Status,
		"arrival_time": navResponse.ArrivalTimeStr,
	})

	if navResponse.Status == "already_at_destination" {
		logger.Log("INFO", "Ship already at segment destination", map[string]interface{}{
			"ship_symbol": ship.ShipSymbol(),
			"action":      "navigate",
			"result":      "already_present",
		})
		return nil
	}

	if navResponse.ArrivalTimeStr != "" {
		if err := e.waitForArrival(ctx, ship, navResponse.ArrivalTimeStr, playerID); err != nil {
			return err
		}
	} else {
		logger.Log("WARNING", "Navigation response missing arrival time", map[string]interface{}{
			"ship_symbol": ship.ShipSymbol(),
			"action":      "navigate",
			"status":      navResponse.Status,
			"warning":     "empty_arrival_time",
		})
	}

	// Record fuel consumption metrics
	// The segment's fuel required is consumed during this navigation
	metrics.RecordFuelConsumption(
		playerID.Value(),
		flightMode,
		segment.FuelRequired,
	)

	// OPTIMIZATION: Use fuel state from navigate response instead of reloading ship
	// This saves 1 API call per navigation segment
	if navResponse.FuelCurrent > 0 || navResponse.FuelCapacity > 0 {
		if err := ship.UpdateFuelFromAPI(navResponse.FuelCurrent, navResponse.FuelCapacity); err != nil {
			return fmt.Errorf("failed to update fuel from navigation response: %w", err)
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
		if err := e.refuelShip(ctx, ship, playerID); err != nil {
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
		if err := e.refuelShip(ctx, ship, playerID); err != nil {
			return err
		}
	}

	return nil
}

func (e *RouteExecutor) scanMarketIfPresent(ctx context.Context, segment *domainNavigation.RouteSegment, ship *domainNavigation.Ship, playerID shared.PlayerID) {
	if e.marketScanner != nil && segment.ToWaypoint.IsMarketplace() {
		logger := common.LoggerFromContext(ctx)
		logger.Log("INFO", "Marketplace detected - scanning market data", map[string]interface{}{
			"ship_symbol": ship.ShipSymbol(),
			"action":      "scan_market",
			"waypoint":    segment.ToWaypoint.Symbol,
		})

		if err := e.marketScanner.ScanAndSaveMarket(ctx, uint(playerID.Value()), segment.ToWaypoint.Symbol); err != nil {
			logger.Log("ERROR", "Market scan failed", map[string]interface{}{
				"ship_symbol": ship.ShipSymbol(),
				"action":      "scan_market",
				"waypoint":    segment.ToWaypoint.Symbol,
				"error":       err.Error(),
			})
		}
	}
}

// waitForCurrentTransit waits for ship to complete its current transit using event-based notification.
// CRITICAL: After waiting, persists ship state to DB to prevent stale state loops.
func (e *RouteExecutor) waitForCurrentTransit(
	ctx context.Context,
	ship *domainNavigation.Ship,
	playerID shared.PlayerID,
) error {
	logger := common.LoggerFromContext(ctx)

	// If ship is not in transit, nothing to wait for
	if ship.NavStatus() != domainNavigation.NavStatusInTransit {
		return nil
	}

	logger.Log("INFO", "Ship in transit from previous command", map[string]interface{}{
		"ship_symbol": ship.ShipSymbol(),
		"action":      "wait_previous_transit",
		"status":      "IN_TRANSIT",
	})

	// Calculate wait time from DB arrival time
	var waitTimeSeconds int
	if ship.ArrivalTime() != nil {
		waitTime := time.Until(*ship.ArrivalTime())
		if waitTime > 0 {
			waitTimeSeconds = int(waitTime.Seconds())
		}
	}

	// Event-based waiting, with a timeout->resync->park backstop if the
	// ARRIVED event is lost or raced against subscription (sp-pafv).
	if err := WaitForShipArrival(ctx, e.shipRepo, e.shipEventSubscriber, ship, playerID, waitTimeSeconds, logger); err != nil {
		return err
	}

	// Persist ship state to DB after arrival to prevent stale state loops
	if e.shipRepo != nil && ship.NavStatus() != domainNavigation.NavStatusInTransit {
		// Clear arrival time since ship has arrived
		ship.ClearArrivalTime()

		if err := e.shipRepo.Save(ctx, ship); err != nil {
			logger.Log("WARNING", "Failed to persist ship state after transit wait", map[string]interface{}{
				"ship_symbol": ship.ShipSymbol(),
				"error":       err.Error(),
			})
		} else {
			logger.Log("DEBUG", "Persisted ship state after transit wait", map[string]interface{}{
				"ship_symbol": ship.ShipSymbol(),
				"location":    ship.CurrentLocation().Symbol,
				"nav_status":  string(ship.NavStatus()),
			})
		}
	}

	logger.Log("INFO", "Ship arrival confirmed", map[string]interface{}{
		"ship_symbol": ship.ShipSymbol(),
		"action":      "arrival_confirmed",
		"status":      string(ship.NavStatus()),
	})
	return nil
}

// refuelBeforeDeparture refuels ship before starting the journey
func (e *RouteExecutor) refuelBeforeDeparture(
	ctx context.Context,
	ship *domainNavigation.Ship,
	playerID shared.PlayerID,
) error {
	// Extract logger from context
	logger := common.LoggerFromContext(ctx)

	// GRACEFUL DEGRADATION: Skip refuel if current location has no fuel station
	// This handles stale waypoint cache data or routing service errors
	if !ship.CurrentLocation().HasFuel {
		logger.Log("WARNING", "Ship cannot refuel before departure - no fuel station at current location", map[string]interface{}{
			"ship_symbol": ship.ShipSymbol(),
			"action":      "refuel_before_departure_skipped",
			"waypoint":    ship.CurrentLocation().Symbol,
			"reason":      "no_fuel_station",
		})
		return nil // Skip refuel gracefully
	}

	logger.Log("INFO", "Ship refueling before departure", map[string]interface{}{
		"ship_symbol": ship.ShipSymbol(),
		"action":      "refuel_before_departure",
		"waypoint":    ship.CurrentLocation().Symbol,
	})

	// Dock for refuel (via DockShipCommand)
	// Command handler updates ship state in memory
	dockCmd := &types.DockShipCommand{
		Ship:     ship,
		PlayerID: playerID,
	}
	if _, err := e.mediator.Send(ctx, dockCmd); err != nil {
		return fmt.Errorf("failed to dock for refuel: %w", err)
	}

	// Refuel (via RefuelShipCommand)
	// Command handler updates ship state in memory
	refuelCmd := &types.RefuelShipCommand{
		Ship:     ship,
		PlayerID: playerID,
		Units:    nil, // Full refuel
	}
	if _, err := e.mediator.Send(ctx, refuelCmd); err != nil {
		return fmt.Errorf("failed to refuel: %w", err)
	}

	// Return to orbit (via OrbitShipCommand)
	// Command handler updates ship state in memory
	orbitCmd := &types.OrbitShipCommand{
		Ship:     ship,
		PlayerID: playerID,
	}
	if _, err := e.mediator.Send(ctx, orbitCmd); err != nil {
		return fmt.Errorf("failed to orbit after refuel: %w", err)
	}

	return nil
}

// refuelShip refuels ship at current location
func (e *RouteExecutor) refuelShip(
	ctx context.Context,
	ship *domainNavigation.Ship,
	playerID shared.PlayerID,
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

	// Dock for refuel (via DockShipCommand)
	// Command handler updates ship state in memory
	dockCmd := &types.DockShipCommand{
		Ship:     ship,
		PlayerID: playerID,
	}
	if _, err := e.mediator.Send(ctx, dockCmd); err != nil {
		return fmt.Errorf("failed to dock for refuel: %w", err)
	}

	// Refuel (via RefuelShipCommand)
	// Command handler updates ship state in memory
	refuelCmd := &types.RefuelShipCommand{
		Ship:     ship,
		PlayerID: playerID,
		Units:    nil, // Full refuel
	}
	if _, err := e.mediator.Send(ctx, refuelCmd); err != nil {
		return fmt.Errorf("failed to refuel: %w", err)
	}

	// Return to orbit (via OrbitShipCommand)
	// Command handler updates ship state in memory
	orbitCmd := &types.OrbitShipCommand{
		Ship:     ship,
		PlayerID: playerID,
	}
	if _, err := e.mediator.Send(ctx, orbitCmd); err != nil {
		return fmt.Errorf("failed to orbit after refuel: %w", err)
	}

	return nil
}

// waitForArrival waits for ship to arrive at destination using event-based notification.
// Uses ShipEventSubscriber to receive ARRIVED event from ShipStateScheduler.
func (e *RouteExecutor) waitForArrival(
	ctx context.Context,
	ship *domainNavigation.Ship,
	arrivalTimeStr string,
	playerID shared.PlayerID,
) error {
	logger := common.LoggerFromContext(ctx)

	arrivalTime, err := shared.NewArrivalTime(arrivalTimeStr)
	if err != nil {
		return fmt.Errorf("failed to parse arrival time: %w", err)
	}
	waitTime := arrivalTime.CalculateWaitTime()

	// If ship is not in transit, no need to wait
	if ship.NavStatus() != domainNavigation.NavStatusInTransit {
		return nil
	}

	// Event-based waiting, with a timeout->resync->park backstop if the
	// ARRIVED event is lost or raced against subscription (sp-pafv).
	return WaitForShipArrival(ctx, e.shipRepo, e.shipEventSubscriber, ship, playerID, waitTime, logger)
}
