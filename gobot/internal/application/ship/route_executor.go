package ship

import (
	"context"
	"fmt"
	"log"
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
type RouteExecutor struct {
	shipRepo domainNavigation.ShipRepository
	mediator common.Mediator
	clock    shared.Clock
}

// NewRouteExecutor creates a new route executor
// If clock is nil, uses RealClock (production behavior)
func NewRouteExecutor(
	shipRepo domainNavigation.ShipRepository,
	mediator common.Mediator,
	clock shared.Clock,
) *RouteExecutor {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &RouteExecutor{
		shipRepo: shipRepo,
		mediator: mediator,
		clock:    clock,
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
	for {
		segment := route.NextSegment()
		if segment == nil {
			break // Route complete
		}

		if err := e.executeSegment(ctx, segment, ship, playerID); err != nil {
			return err
		}

		// Complete segment in route
		if err := route.CompleteSegment(); err != nil {
			return err
		}
	}

	return nil
}

// executeSegment executes a single route segment using atomic commands
func (e *RouteExecutor) executeSegment(
	ctx context.Context,
	segment *domainNavigation.RouteSegment,
	ship *domainNavigation.Ship,
	playerID int,
) error {
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
		log.Printf("Pre-departure refuel: preventing DRIFT mode with low fuel at %s",
			segment.FromWaypoint.Symbol)
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
	if navResp, ok := navResp.(*NavigateToWaypointResponse); ok && navResp.ArrivalTimeStr != "" {
		if err := e.waitForArrival(ctx, ship, navResp.ArrivalTimeStr, playerID); err != nil {
			return err
		}
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
		log.Printf("Opportunistic refuel at %s", segment.ToWaypoint.Symbol)
		if err := e.refuelShip(ctx, ship, playerID); err != nil {
			return err
		}
	}

	// 7. Planned refueling (required by routing engine)
	if segment.RequiresRefuel {
		log.Printf("Planned refuel at %s", segment.ToWaypoint.Symbol)
		if err := e.refuelShip(ctx, ship, playerID); err != nil {
			return err
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
	log.Printf("Ship %s is IN_TRANSIT from previous command, waiting for arrival...",
		ship.ShipSymbol())

	// In a real implementation, we'd get the arrival time from API
	// For now, wait a bit and re-sync
	// Uses clock for testability (instant in tests, real sleep in production)
	e.clock.Sleep(5 * time.Second)

	// Re-sync ship state (if using real repository)
	if e.shipRepo != nil {
		freshShip, err := e.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
		if err != nil {
			return fmt.Errorf("failed to sync ship state after waiting: %w", err)
		}
		*ship = *freshShip
	}

	// Call arrive() if still IN_TRANSIT
	if ship.NavStatus() == domainNavigation.NavStatusInTransit {
		if err := ship.Arrive(); err != nil {
			return fmt.Errorf("failed to mark ship as arrived: %w", err)
		}
	}

	log.Printf("Ship arrived, status now: %s", ship.NavStatus())
	return nil
}

// refuelBeforeDeparture refuels ship before starting the journey
func (e *RouteExecutor) refuelBeforeDeparture(
	ctx context.Context,
	ship *domainNavigation.Ship,
	playerID int,
) error {
	log.Printf("Refueling before departure at %s", ship.CurrentLocation().Symbol)

	// Dock for refuel (via DockShipCommand)
	dockCmd := &DockShipCommand{
		ShipSymbol: ship.ShipSymbol(),
		PlayerID:   playerID,
	}
	if _, err := e.mediator.Send(ctx, dockCmd); err != nil {
		return fmt.Errorf("failed to dock for refuel: %w", err)
	}

	// Re-sync ship state (if using real repository)
	if e.shipRepo != nil {
		freshShip, err := e.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
		if err != nil {
			return fmt.Errorf("failed to sync ship after dock: %w", err)
		}
		*ship = *freshShip
	}

	// Refuel (via RefuelShipCommand)
	refuelCmd := &RefuelShipCommand{
		ShipSymbol: ship.ShipSymbol(),
		PlayerID:   playerID,
		Units:      nil, // Full refuel
	}
	if _, err := e.mediator.Send(ctx, refuelCmd); err != nil {
		return fmt.Errorf("failed to refuel: %w", err)
	}

	// Re-sync ship state (if using real repository)
	if e.shipRepo != nil {
		freshShip, err := e.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
		if err != nil {
			return fmt.Errorf("failed to sync ship after refuel: %w", err)
		}
		*ship = *freshShip
	}

	// Return to orbit (via OrbitShipCommand)
	orbitCmd := &OrbitShipCommand{
		ShipSymbol: ship.ShipSymbol(),
		PlayerID:   playerID,
	}
	if _, err := e.mediator.Send(ctx, orbitCmd); err != nil {
		return fmt.Errorf("failed to orbit after refuel: %w", err)
	}

	// Re-sync ship state (if using real repository)
	if e.shipRepo != nil {
		freshShip, err := e.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
		if err != nil {
			return fmt.Errorf("failed to sync ship after orbit: %w", err)
		}
		*ship = *freshShip
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
	dockCmd := &DockShipCommand{
		ShipSymbol: ship.ShipSymbol(),
		PlayerID:   playerID,
	}
	if _, err := e.mediator.Send(ctx, dockCmd); err != nil {
		return fmt.Errorf("failed to dock for refuel: %w", err)
	}

	// Re-sync ship state (if using real repository)
	if e.shipRepo != nil {
		freshShip, err := e.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
		if err != nil {
			return fmt.Errorf("failed to sync ship after dock: %w", err)
		}
		*ship = *freshShip
	}

	// Refuel (via RefuelShipCommand)
	refuelCmd := &RefuelShipCommand{
		ShipSymbol: ship.ShipSymbol(),
		PlayerID:   playerID,
		Units:      nil, // Full refuel
	}
	if _, err := e.mediator.Send(ctx, refuelCmd); err != nil {
		return fmt.Errorf("failed to refuel: %w", err)
	}

	// Re-sync ship state (if using real repository)
	if e.shipRepo != nil {
		freshShip, err := e.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
		if err != nil {
			return fmt.Errorf("failed to sync ship after refuel: %w", err)
		}
		*ship = *freshShip
	}

	// Return to orbit (via OrbitShipCommand)
	orbitCmd := &OrbitShipCommand{
		ShipSymbol: ship.ShipSymbol(),
		PlayerID:   playerID,
	}
	if _, err := e.mediator.Send(ctx, orbitCmd); err != nil {
		return fmt.Errorf("failed to orbit after refuel: %w", err)
	}

	// Re-sync ship state (if using real repository)
	if e.shipRepo != nil {
		freshShip, err := e.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
		if err != nil {
			return fmt.Errorf("failed to sync ship after orbit: %w", err)
		}
		*ship = *freshShip
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
	// Calculate wait time from API arrival time (use NavigationUtils)
	waitTime := CalculateArrivalWaitTime(arrivalTimeStr)

	if waitTime > 0 {
		log.Printf("Waiting %d seconds for ship to arrive", waitTime+3)
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

	// Call arrive() if still IN_TRANSIT
	if ship.NavStatus() == domainNavigation.NavStatusInTransit {
		if err := ship.Arrive(); err != nil {
			return fmt.Errorf("failed to mark ship as arrived: %w", err)
		}
	}

	return nil
}
