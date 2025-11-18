package ship

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// RouteExecutor executes routes by orchestrating atomic ship commands via mediator
//
// This is the CRITICAL orchestration service that replaces the executeRoute() method
// in NavigateShipHandler. It uses the mediator pattern to send atomic commands:
// - OrbitShipCommand
// - DockShipCommand
// - RefuelShipCommand
// - NavigateToWaypointCommand
// - SetFlightModeCommand
//
// It follows the exact same logic as the Python implementation with all safety features:
// 1. Handle IN_TRANSIT from previous command (idempotency)
// 2. Refuel before departure if needed
// 3. Execute each segment step-by-step
// 4. Pre-departure refuel check (prevent DRIFT mode at fuel stations)
// 5. Opportunistic refueling (90% rule)
// 6. Planned refueling (required by routing engine)
// 7. Automatic market scanning at marketplace waypoints
type RouteExecutor struct {
	shipRepo      domainNavigation.ShipRepository
	mediator      common.Mediator
	clock         shared.Clock
	marketScanner *MarketScanner
}

// NewRouteExecutor creates a new route executor
// If clock is nil, uses RealClock (production behavior)
// marketScanner can be nil to disable automatic market scanning
func NewRouteExecutor(
	shipRepo domainNavigation.ShipRepository,
	mediator common.Mediator,
	clock shared.Clock,
	marketScanner *MarketScanner,
) *RouteExecutor {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &RouteExecutor{
		shipRepo:      shipRepo,
		mediator:      mediator,
		clock:         clock,
		marketScanner: marketScanner,
	}
}

// ExecuteRoute executes a route step-by-step using atomic commands
//
// This orchestrates all the atomic commands we created in Phase 2.1-2.3:
// - Uses mediator.Send() to invoke commands
// - Uses domain decision methods (ShouldRefuelOpportunistically, ShouldPreventDriftMode)
// - Follows exact Python implementation logic
func (e *RouteExecutor) ExecuteRoute(
	ctx context.Context,
	route *domainNavigation.Route,
	ship *domainNavigation.Ship,
	playerID int,
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
		logger.Log("INFO", fmt.Sprintf("[ROUTE EXECUTOR] Loop iteration %d: segment=%v, currentIndex=%d, totalSegments=%d",
			segmentCount, segment != nil, route.CurrentSegmentIndex(), len(route.Segments())), nil)

		if segment == nil {
			logger.Log("INFO", "[ROUTE EXECUTOR] NextSegment() returned nil, breaking loop", nil)
			break // Route complete
		}

		logger.Log("INFO", fmt.Sprintf("[ROUTE EXECUTOR] Executing segment %d: %s → %s",
			segmentCount, segment.FromWaypoint.Symbol, segment.ToWaypoint.Symbol), nil)

		if err := e.executeSegment(ctx, segment, ship, playerID); err != nil {
			logger.Log("ERROR", fmt.Sprintf("[ROUTE EXECUTOR] Segment execution failed: %v", err), nil)
			route.FailRoute(err.Error())
			return err
		}

		logger.Log("INFO", fmt.Sprintf("[ROUTE EXECUTOR] Segment %d completed successfully", segmentCount), nil)

		// Complete segment in route
		if err := route.CompleteSegment(); err != nil {
			logger.Log("ERROR", fmt.Sprintf("[ROUTE EXECUTOR] CompleteSegment() failed: %v", err), nil)
			return err
		}

		logger.Log("INFO", fmt.Sprintf("[ROUTE EXECUTOR] After CompleteSegment: currentIndex=%d, status=%s",
			route.CurrentSegmentIndex(), route.Status()), nil)

		segmentCount++
	}

	logger.Log("INFO", fmt.Sprintf("[ROUTE EXECUTOR] Loop finished after %d segments, route status=%s", segmentCount, route.Status()), nil)

	return nil
}

// executeSegment executes a single route segment using atomic commands
func (e *RouteExecutor) executeSegment(
	ctx context.Context,
	segment *domainNavigation.RouteSegment,
	ship *domainNavigation.Ship,
	playerID int,
) error {
	// Extract logger from context
	logger := common.LoggerFromContext(ctx)

	// 1. Ensure in orbit (via OrbitShipCommand)
	orbitCmd := &OrbitShipCommand{
		ShipSymbol: ship.ShipSymbol(),
		PlayerID:   playerID,
	}
	if _, err := e.mediator.Send(ctx, orbitCmd); err != nil {
		return fmt.Errorf("failed to orbit: %w", err)
	}

	// 2. Pre-departure refuel check (prevent DRIFT at fuel stations with low fuel)
	// Use domain decision method
	if ship.ShouldPreventDriftMode(segment, 0.9) {
		logger.Log("INFO", fmt.Sprintf("Pre-departure refuel: preventing DRIFT mode with low fuel at %s",
			segment.FromWaypoint.Symbol), nil)
		if err := e.refuelShip(ctx, ship, playerID); err != nil {
			return err
		}
	}

	// 3. Set flight mode (via SetFlightModeCommand)
	setModeCmd := &SetFlightModeCommand{
		ShipSymbol: ship.ShipSymbol(),
		PlayerID:   playerID,
		Mode:       segment.FlightMode,
	}
	if _, err := e.mediator.Send(ctx, setModeCmd); err != nil {
		return fmt.Errorf("failed to set flight mode: %w", err)
	}

	// 4. Navigate to waypoint (via NavigateToWaypointCommand)
	navCmd := &NavigateToWaypointCommand{
		ShipSymbol:  ship.ShipSymbol(),
		Destination: segment.ToWaypoint.Symbol,
		PlayerID:    playerID,
		FlightMode:  segment.FlightMode.Name(),
	}
	navResp, err := e.mediator.Send(ctx, navCmd)
	if err != nil {
		return fmt.Errorf("failed to navigate: %w", err)
	}

	// 5. Wait for arrival
	navResponse, ok := navResp.(*NavigateToWaypointResponse)
	if !ok {
		return fmt.Errorf("unexpected response type: %T", navResp)
	}

	logger.Log("INFO", fmt.Sprintf("[ROUTE EXECUTOR] NavigateToWaypoint response: Status=%s, ArrivalTimeStr='%s'",
		navResponse.Status, navResponse.ArrivalTimeStr), nil)

	// Check if already at destination (idempotent case)
	if navResponse.Status == "already_at_destination" {
		logger.Log("INFO", "[ROUTE EXECUTOR] Ship already at destination, skipping wait", nil)
		return nil
	}

	// Wait for arrival (Status="navigating")
	if navResponse.ArrivalTimeStr != "" {
		if err := e.waitForArrival(ctx, ship, navResponse.ArrivalTimeStr, playerID); err != nil {
			return err
		}
	} else {
		logger.Log("WARNING", fmt.Sprintf("[ROUTE EXECUTOR] WARNING: ArrivalTimeStr is empty for Status=%s", navResponse.Status), nil)
	}

	// Re-sync ship after arrival (if using real repository)
	if e.shipRepo != nil {
		freshShip, err := e.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
		if err != nil {
			return fmt.Errorf("failed to sync ship: %w", err)
		}
		*ship = *freshShip // Update ship state
	}

	// 6. Opportunistic refueling (90% safety check)
	// Use domain decision method
	if ship.ShouldRefuelOpportunistically(segment.ToWaypoint, 0.9) && !segment.RequiresRefuel {
		logger.Log("INFO", fmt.Sprintf("Opportunistic refuel at %s", segment.ToWaypoint.Symbol), nil)
		if err := e.refuelShip(ctx, ship, playerID); err != nil {
			return err
		}
	}

	// 7. Planned refueling (required by routing engine)
	if segment.RequiresRefuel {
		logger.Log("INFO", fmt.Sprintf("Planned refuel at %s", segment.ToWaypoint.Symbol), nil)
		if err := e.refuelShip(ctx, ship, playerID); err != nil {
			return err
		}
	}

	// 8. Automatic market scanning at marketplace waypoints
	if e.marketScanner != nil && e.isMarketplace(segment.ToWaypoint) {
		logger := common.LoggerFromContext(ctx)
		logger.Log("INFO", fmt.Sprintf("[ROUTE EXECUTOR] Detected marketplace at %s, scanning market data", segment.ToWaypoint.Symbol), nil)

		// Market scanning is non-fatal - log errors but continue route execution
		if err := e.marketScanner.ScanAndSaveMarket(ctx, uint(playerID), segment.ToWaypoint.Symbol); err != nil {
			logger.Log("ERROR", fmt.Sprintf("[ROUTE EXECUTOR] Market scan failed at %s: %v", segment.ToWaypoint.Symbol, err), nil)
			// Continue execution - market scanning failure should not fail navigation
		}
	}

	return nil
}

// waitForCurrentTransit waits for ship to complete its current transit
func (e *RouteExecutor) waitForCurrentTransit(
	ctx context.Context,
	ship *domainNavigation.Ship,
	playerID int,
) error {
	// Extract logger from context
	logger := common.LoggerFromContext(ctx)

	logger.Log("INFO", fmt.Sprintf("Ship %s is IN_TRANSIT from previous command, fetching arrival time from API...",
		ship.ShipSymbol()), nil)

	// Fetch ship data from API to get arrival time (matches Python implementation)
	if e.shipRepo != nil {
		shipData, err := e.shipRepo.GetShipData(ctx, ship.ShipSymbol(), playerID)
		if err != nil {
			return fmt.Errorf("failed to fetch ship data from API: %w", err)
		}

		// If ship is still IN_TRANSIT and has arrival time, wait for it
		if shipData.NavStatus == "IN_TRANSIT" && shipData.ArrivalTime != "" {
			waitTime := CalculateArrivalWaitTime(shipData.ArrivalTime)
			if waitTime > 0 {
				logger.Log("INFO", fmt.Sprintf("Waiting %d seconds for ship to complete previous transit", waitTime+3), nil)
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
		logger.Log("INFO", fmt.Sprintf("[ROUTE EXECUTOR] Ship still IN_TRANSIT after initial wait (attempt %d/%d), polling API in %v...", i+1, maxRetries, retryDelay), nil)
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
		logger.Log("INFO", fmt.Sprintf("[ROUTE EXECUTOR] WARNING: Ship still IN_TRANSIT after %d retries, forcing arrival in domain model", maxRetries), nil)
		if err := ship.Arrive(); err != nil {
			return fmt.Errorf("failed to mark ship as arrived: %w", err)
		}
	}

	logger.Log("INFO", fmt.Sprintf("Ship arrived, status now: %s", ship.NavStatus()), nil)
	return nil
}

// refuelBeforeDeparture refuels ship before starting the journey
func (e *RouteExecutor) refuelBeforeDeparture(
	ctx context.Context,
	ship *domainNavigation.Ship,
	playerID int,
) error {
	// Extract logger from context
	logger := common.LoggerFromContext(ctx)

	logger.Log("INFO", fmt.Sprintf("Refueling before departure at %s", ship.CurrentLocation().Symbol), nil)

	// Dock for refuel (via DockShipCommand)
	// Command handler updates ship state in memory
	dockCmd := &DockShipCommand{
		ShipSymbol: ship.ShipSymbol(),
		PlayerID:   playerID,
	}
	if _, err := e.mediator.Send(ctx, dockCmd); err != nil {
		return fmt.Errorf("failed to dock for refuel: %w", err)
	}

	// Refuel (via RefuelShipCommand)
	// Command handler updates ship state in memory
	refuelCmd := &RefuelShipCommand{
		ShipSymbol: ship.ShipSymbol(),
		PlayerID:   playerID,
		Units:      nil, // Full refuel
	}
	if _, err := e.mediator.Send(ctx, refuelCmd); err != nil {
		return fmt.Errorf("failed to refuel: %w", err)
	}

	// Return to orbit (via OrbitShipCommand)
	// Command handler updates ship state in memory
	orbitCmd := &OrbitShipCommand{
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
	playerID int,
) error {
	// Dock for refuel (via DockShipCommand)
	// Command handler updates ship state in memory
	dockCmd := &DockShipCommand{
		ShipSymbol: ship.ShipSymbol(),
		PlayerID:   playerID,
	}
	if _, err := e.mediator.Send(ctx, dockCmd); err != nil {
		return fmt.Errorf("failed to dock for refuel: %w", err)
	}

	// Refuel (via RefuelShipCommand)
	// Command handler updates ship state in memory
	refuelCmd := &RefuelShipCommand{
		ShipSymbol: ship.ShipSymbol(),
		PlayerID:   playerID,
		Units:      nil, // Full refuel
	}
	if _, err := e.mediator.Send(ctx, refuelCmd); err != nil {
		return fmt.Errorf("failed to refuel: %w", err)
	}

	// Return to orbit (via OrbitShipCommand)
	// Command handler updates ship state in memory
	orbitCmd := &OrbitShipCommand{
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
	playerID int,
) error {
	// Extract logger from context
	logger := common.LoggerFromContext(ctx)

	// Calculate wait time from API arrival time (use NavigationUtils)
	waitTime := CalculateArrivalWaitTime(arrivalTimeStr)

	if waitTime > 0 {
		logger.Log("INFO", fmt.Sprintf("Waiting %d seconds for ship to arrive", waitTime+3), nil)
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
		logger.Log("INFO", fmt.Sprintf("[ROUTE EXECUTOR] Ship still IN_TRANSIT after wait (attempt %d/%d), polling API in %v...", i+1, maxRetries, retryDelay), nil)
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
		logger.Log("INFO", fmt.Sprintf("[ROUTE EXECUTOR] WARNING: Ship still IN_TRANSIT after %d retries, forcing arrival in domain model", maxRetries), nil)
		if err := ship.Arrive(); err != nil {
			return fmt.Errorf("failed to mark ship as arrived: %w", err)
		}
	}

	return nil
}

// isMarketplace checks if a waypoint has the MARKETPLACE trait
func (e *RouteExecutor) isMarketplace(waypoint *shared.Waypoint) bool {
	for _, trait := range waypoint.Traits {
		if trait == "MARKETPLACE" {
			return true
		}
	}
	return false
}
