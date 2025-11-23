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
type RouteExecutor struct {
	shipRepo      domainNavigation.ShipRepository
	mediator      common.Mediator
	clock         shared.Clock
	marketScanner *MarketScanner
	refuelStrategy strategies.RefuelStrategy
}

// NewRouteExecutor creates a new route executor
// If clock is nil, uses RealClock (production behavior)
// If marketScanner is nil, disables automatic market scanning
// If refuelStrategy is nil, uses default ConservativeRefuelStrategy (90% threshold)
func NewRouteExecutor(
	shipRepo domainNavigation.ShipRepository,
	mediator common.Mediator,
	clock shared.Clock,
	marketScanner *MarketScanner,
	refuelStrategy strategies.RefuelStrategy,
) *RouteExecutor {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	if refuelStrategy == nil {
		refuelStrategy = strategies.NewDefaultRefuelStrategy()
	}
	return &RouteExecutor{
		shipRepo:       shipRepo,
		mediator:       mediator,
		clock:          clock,
		marketScanner:  marketScanner,
		refuelStrategy: refuelStrategy,
	}
}

// ExecuteRoute executes a route step-by-step using atomic commands
//
// This orchestrates all the atomic commands we created in Phase 2.1-2.3:
// - Uses mediator.Send() to invoke commands
// - Uses domain decision methods (ShouldRefuelOpportunistically, ShouldPreventDriftMode)
// - Follows exact Python implementation logic
//
// The operationContext parameter (optional) links all transactions to the parent operation
func (e *RouteExecutor) ExecuteRoute(
	ctx context.Context,
	route *domainNavigation.Route,
	ship *domainNavigation.Ship,
	playerID shared.PlayerID,
	operationContext *shared.OperationContext,
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
		if err := e.refuelBeforeDeparture(ctx, ship, playerID, operationContext); err != nil {
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

		if err := e.executeSegment(ctx, segment, ship, playerID, operationContext); err != nil {
			logger.Log("ERROR", "Route segment execution failed", map[string]interface{}{
				"ship_symbol":   ship.ShipSymbol(),
				"action":        "execute_segment",
				"segment_index": segmentCount,
				"from":          segment.FromWaypoint.Symbol,
				"to":            segment.ToWaypoint.Symbol,
				"error":         err.Error(),
			})
			route.FailRoute(err.Error())

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
	operationContext *shared.OperationContext,
) error {
	// Reload ship to get latest state before segment execution
	// This prevents stale state issues from rapid navigation
	if e.shipRepo != nil {
		freshShip, err := e.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
		if err != nil {
			return fmt.Errorf("failed to reload ship before segment: %w", err)
		}
		*ship = *freshShip
	}

	// Check if ship is in transit and wait for arrival before proceeding
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
		ShipSymbol: ship.ShipSymbol(),
		PlayerID:   playerID,
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
		if err := e.refuelShip(ctx, ship, playerID, operationContext); err != nil {
			return err
		}
	}
	return nil
}

func (e *RouteExecutor) selectOptimalFlightMode(ctx context.Context, segment *domainNavigation.RouteSegment, ship *domainNavigation.Ship) shared.FlightMode {
	logger := common.LoggerFromContext(ctx)
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
	return flightMode
}

func (e *RouteExecutor) setShipFlightMode(ctx context.Context, ship *domainNavigation.Ship, playerID shared.PlayerID, flightMode shared.FlightMode) error {
	setModeCmd := &types.SetFlightModeCommand{
		ShipSymbol: ship.ShipSymbol(),
		PlayerID:   playerID,
		Mode:       flightMode,
	}
	if _, err := e.mediator.Send(ctx, setModeCmd); err != nil {
		return fmt.Errorf("failed to set flight mode: %w", err)
	}
	return nil
}

func (e *RouteExecutor) navigateToSegmentDestination(ctx context.Context, segment *domainNavigation.RouteSegment, ship *domainNavigation.Ship, playerID shared.PlayerID, flightMode shared.FlightMode) error {
	logger := common.LoggerFromContext(ctx)

	navCmd := &types.NavigateDirectCommand{
		ShipSymbol:  ship.ShipSymbol(),
		Destination: segment.ToWaypoint.Symbol,
		PlayerID:    playerID,
		FlightMode:  flightMode.Name(),
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

	if e.shipRepo != nil {
		freshShip, err := e.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
		if err != nil {
			return fmt.Errorf("failed to sync ship: %w", err)
		}
		*ship = *freshShip
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
		if err := e.refuelShip(ctx, ship, playerID, operationContext); err != nil {
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
		if err := e.refuelShip(ctx, ship, playerID, operationContext); err != nil {
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

// waitForCurrentTransit waits for ship to complete its current transit
func (e *RouteExecutor) waitForCurrentTransit(
	ctx context.Context,
	ship *domainNavigation.Ship,
	playerID shared.PlayerID,
) error {
	// Extract logger from context
	logger := common.LoggerFromContext(ctx)

	logger.Log("INFO", "Ship in transit from previous command", map[string]interface{}{
		"ship_symbol": ship.ShipSymbol(),
		"action":      "wait_previous_transit",
		"status":      "IN_TRANSIT",
	})

	// Fetch ship data from API to get arrival time (matches Python implementation)
	if e.shipRepo != nil {
		shipData, err := e.shipRepo.GetShipData(ctx, ship.ShipSymbol(), playerID)
		if err != nil {
			return fmt.Errorf("failed to fetch ship data from API: %w", err)
		}

		// If ship is still IN_TRANSIT and has arrival time, wait for it
		if shipData.NavStatus == "IN_TRANSIT" && shipData.ArrivalTime != "" {
			arrivalTime, err := shared.NewArrivalTime(shipData.ArrivalTime)
			if err != nil {
				return fmt.Errorf("failed to parse arrival time: %w", err)
			}
			waitTime := arrivalTime.CalculateWaitTime()
			if waitTime > 0 {
				logger.Log("INFO", "Waiting for ship to complete previous transit", map[string]interface{}{
					"ship_symbol":  ship.ShipSymbol(),
					"action":       "wait_transit",
					"wait_seconds": waitTime + 3,
				})
				e.clock.Sleep(time.Duration(waitTime+3) * time.Second) // +3 second buffer
			}
		}
	}

	// Re-sync ship state after waiting
	if e.shipRepo != nil {
		freshShip, err := e.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
		if err != nil {
			return fmt.Errorf("failed to sync ship state after waiting: %w", err)
		}
		*ship = *freshShip
	}

	// Poll API until ship is no longer IN_TRANSIT (handles API lag)
	maxRetries := 5
	retryDelay := 2 * time.Second
	for i := 0; i < maxRetries && ship.NavStatus() == domainNavigation.NavStatusInTransit; i++ {
		logger.Log("INFO", "Ship still in transit - polling API", map[string]interface{}{
			"ship_symbol": ship.ShipSymbol(),
			"action":      "poll_transit_status",
			"attempt":     i + 1,
			"max_retries": maxRetries,
			"retry_delay": retryDelay.String(),
		})
		e.clock.Sleep(retryDelay)

		if e.shipRepo != nil {
			freshShip, err := e.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
			if err != nil {
				return fmt.Errorf("failed to sync ship after transit retry: %w", err)
			}
			*ship = *freshShip
		}
	}

	// If still IN_TRANSIT after retries, call arrive() as last resort
	if ship.NavStatus() == domainNavigation.NavStatusInTransit {
		logger.Log("INFO", "Ship still in transit after retries - forcing arrival", map[string]interface{}{
			"ship_symbol": ship.ShipSymbol(),
			"action":      "force_arrival",
			"retries":     maxRetries,
			"warning":     "api_lag_detected",
		})
		if err := ship.Arrive(); err != nil {
			return fmt.Errorf("failed to mark ship as arrived: %w", err)
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
	operationContext *shared.OperationContext,
) error {
	// Extract logger from context
	logger := common.LoggerFromContext(ctx)

	logger.Log("INFO", "Ship refueling before departure", map[string]interface{}{
		"ship_symbol": ship.ShipSymbol(),
		"action":      "refuel_before_departure",
		"waypoint":    ship.CurrentLocation().Symbol,
	})

	// Dock for refuel (via DockShipCommand)
	// Command handler updates ship state in memory
	dockCmd := &types.DockShipCommand{
		ShipSymbol: ship.ShipSymbol(),
		PlayerID:   playerID,
	}
	if _, err := e.mediator.Send(ctx, dockCmd); err != nil {
		return fmt.Errorf("failed to dock for refuel: %w", err)
	}

	// Refuel (via RefuelShipCommand)
	// Command handler updates ship state in memory
	refuelCmd := &types.RefuelShipCommand{
		ShipSymbol: ship.ShipSymbol(),
		PlayerID:   playerID,
		Units:      nil, // Full refuel
		Context:    operationContext,
	}
	if _, err := e.mediator.Send(ctx, refuelCmd); err != nil {
		return fmt.Errorf("failed to refuel: %w", err)
	}

	// Return to orbit (via OrbitShipCommand)
	// Command handler updates ship state in memory
	orbitCmd := &types.OrbitShipCommand{
		ShipSymbol: ship.ShipSymbol(),
		PlayerID:   playerID,
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
	operationContext *shared.OperationContext,
) error {
	// Dock for refuel (via DockShipCommand)
	// Command handler updates ship state in memory
	dockCmd := &types.DockShipCommand{
		ShipSymbol: ship.ShipSymbol(),
		PlayerID:   playerID,
	}
	if _, err := e.mediator.Send(ctx, dockCmd); err != nil {
		return fmt.Errorf("failed to dock for refuel: %w", err)
	}

	// Refuel (via RefuelShipCommand)
	// Command handler updates ship state in memory
	refuelCmd := &types.RefuelShipCommand{
		ShipSymbol: ship.ShipSymbol(),
		PlayerID:   playerID,
		Units:      nil, // Full refuel
		Context:    operationContext,
	}
	if _, err := e.mediator.Send(ctx, refuelCmd); err != nil {
		return fmt.Errorf("failed to refuel: %w", err)
	}

	// Return to orbit (via OrbitShipCommand)
	// Command handler updates ship state in memory
	orbitCmd := &types.OrbitShipCommand{
		ShipSymbol: ship.ShipSymbol(),
		PlayerID:   playerID,
	}
	if _, err := e.mediator.Send(ctx, orbitCmd); err != nil {
		return fmt.Errorf("failed to orbit after refuel: %w", err)
	}

	return nil
}

// waitForArrival waits for ship to arrive at destination
func (e *RouteExecutor) waitForArrival(
	ctx context.Context,
	ship *domainNavigation.Ship,
	arrivalTimeStr string,
	playerID shared.PlayerID,
) error {
	// Extract logger from context
	logger := common.LoggerFromContext(ctx)

	// Calculate wait time from API arrival time using ArrivalTime value object
	arrivalTime, err := shared.NewArrivalTime(arrivalTimeStr)
	if err != nil {
		return fmt.Errorf("failed to parse arrival time: %w", err)
	}
	waitTime := arrivalTime.CalculateWaitTime()

	if waitTime > 0 {
		logger.Log("INFO", "Waiting for ship to arrive at destination", map[string]interface{}{
			"ship_symbol":  ship.ShipSymbol(),
			"action":       "wait_arrival",
			"wait_seconds": waitTime + 3,
		})
		// Uses clock for testability (instant in tests, real sleep in production)
		e.clock.Sleep(time.Duration(waitTime+3) * time.Second) // +3 second buffer
	}

	// Re-sync ship state (if using real repository)
	if e.shipRepo != nil {
		freshShip, err := e.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
		if err != nil {
			return fmt.Errorf("failed to sync ship after arrival: %w", err)
		}
		*ship = *freshShip
	}

	// Poll API until ship is no longer IN_TRANSIT (handles API lag)
	// The SpaceTraders API is the source of truth, not our domain model
	maxRetries := 5
	retryDelay := 2 * time.Second
	for i := 0; i < maxRetries && ship.NavStatus() == domainNavigation.NavStatusInTransit; i++ {
		logger.Log("INFO", "Ship still in transit after wait - polling API", map[string]interface{}{
			"ship_symbol": ship.ShipSymbol(),
			"action":      "poll_arrival_status",
			"attempt":     i + 1,
			"max_retries": maxRetries,
			"retry_delay": retryDelay.String(),
		})
		e.clock.Sleep(retryDelay)

		if e.shipRepo != nil {
			freshShip, err := e.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
			if err != nil {
				return fmt.Errorf("failed to sync ship after arrival retry: %w", err)
			}
			*ship = *freshShip
		}
	}

	// If still IN_TRANSIT after retries, call arrive() as last resort
	// This updates our domain model even if API is lagging
	if ship.NavStatus() == domainNavigation.NavStatusInTransit {
		logger.Log("INFO", "Ship still in transit after retries - forcing arrival", map[string]interface{}{
			"ship_symbol": ship.ShipSymbol(),
			"action":      "force_arrival",
			"retries":     maxRetries,
			"warning":     "api_lag_detected",
		})
		if err := ship.Arrive(); err != nil {
			return fmt.Errorf("failed to mark ship as arrived: %w", err)
		}
	}

	return nil
}
