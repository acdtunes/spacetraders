package ship

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/strategies"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	domainSystem "github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// RouteExecutor executes routes by orchestrating atomic ship commands via mediator.
//
// It uses the mediator pattern to send atomic commands:
// - OrbitShipCommand
// - DockShipCommand
// - RefuelShipCommand
// - NavigateDirectCommand
// - SetFlightModeCommand
//
// Every step is defensive:
// 1. Handle IN_TRANSIT from previous command (idempotency)
// 2. Refuel before departure if needed
// 3. Execute each segment step-by-step
// 4. Pre-departure refuel check (buy the fuel the leg ahead needs)
// 5. Opportunistic refueling (configurable via strategy)
// 6. Planned refueling (required by routing engine)
// 7. Automatic market scanning at marketplace waypoints
//
// Refueling behavior is extensible via the Strategy pattern:
//   - ConservativeRefuelStrategy: Maintains high fuel levels (the only
//     implementation; injected at construction)
//
// Event-driven arrival waiting:
// Uses ShipEventSubscriber to wait for ship arrivals via events from ShipStateScheduler.
// This eliminates race conditions between timer-based state transitions and polling.
type RouteExecutor struct {
	shipRepo            domainNavigation.ShipRepository
	mediator            common.Mediator
	clock               shared.Clock
	marketScanner       *MarketScanner
	shipyardScanner     *ShipyardScanner
	refuelStrategy      strategies.RefuelStrategy
	waypointRepo        domainSystem.WaypointRepository
	shipEventSubscriber domainNavigation.ShipEventSubscriber

	// Off-gate warp support, attached post-construction via WithWarpSupport so every
	// existing NewRouteExecutor call site is unchanged. All nil until wired:
	// ExecuteWarpRoute fails closed when warpNavigator or warpEscapeReader is absent,
	// and chart-on-arrival is skipped when systemCharter is absent.
	warpNavigator    WarpNavigator
	systemCharter    SystemCharter
	warpEscapeReader WarpEscapeReader
}

// NewRouteExecutor creates a new route executor
// If clock is nil, uses RealClock (production behavior)
// If marketScanner is nil, disables automatic market scanning
// If shipyardScanner is nil, disables the piggybacked shipyard-inventory scan
// that fires alongside the market scan at marketplace arrivals
// If refuelStrategy is nil, uses default ConservativeRefuelStrategy (90% threshold)
// If waypointRepo is nil, refuelShipWithRetry's alternate-fuel-stop reroute
// is disabled and a retry-exhausted refuel fails outright instead
// of rerouting - retry-with-backoff at the original waypoint still applies.
// shipEventSubscriber is required for event-based arrival waiting
func NewRouteExecutor(
	shipRepo domainNavigation.ShipRepository,
	mediator common.Mediator,
	clock shared.Clock,
	marketScanner *MarketScanner,
	shipyardScanner *ShipyardScanner,
	refuelStrategy strategies.RefuelStrategy,
	waypointRepo domainSystem.WaypointRepository,
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
		shipyardScanner:     shipyardScanner,
		refuelStrategy:      refuelStrategy,
		waypointRepo:        waypointRepo,
		shipEventSubscriber: shipEventSubscriber,
	}
}

// WithWarpSupport attaches the off-gate warp capability to an already
// constructed executor and returns it for chaining. It is deliberately separate
// from the constructor so the eight-arg NewRouteExecutor signature - and every
// existing call site - stays untouched; warp is an additive capability, inert
// until a caller invokes ExecuteWarpRoute - and none does today (sp-bwzy3).
//
// warpNavigator is the API boundary a warp leg crosses; escapeReader answers whether
// a destination system can be LEFT again, and a nil one refuses every warp (a safety
// guard must never be bypassable by omission - it is a parameter rather than an
// option so the compiler names every wiring site). charter may be nil, in which case
// chart-on-arrival is skipped (the warp still executes). Intended to be called once
// at wiring time, before the executor is used concurrently.
func (e *RouteExecutor) WithWarpSupport(warpNavigator WarpNavigator, charter SystemCharter, escapeReader WarpEscapeReader) *RouteExecutor {
	e.warpNavigator = warpNavigator
	e.systemCharter = charter
	e.warpEscapeReader = escapeReader
	return e
}

// ExecuteRoute executes a route step-by-step using atomic commands
//
// This orchestrates all the atomic commands we created in Phase 2.1-2.3:
// - Uses mediator.Send() to invoke commands
// - Uses domain decision methods (ShouldRefuelOpportunistically, ShouldTopOffBeforeDeparture)
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
		if err := e.refuelBeforeDeparture(ctx, route, ship, playerID); err != nil {
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

		if err := e.executeSegment(ctx, segment, ship, playerID, route.FuelReserveAfterCurrentSegment()); err != nil {
			return e.reactToSegmentFailure(ctx, route, ship, segment, segmentCount, err)
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

// reactToSegmentFailure decides how ExecuteRoute responds to a failed segment.
//
// A genuine *ErrArrivalWaitExhausted is a RECOVERABLE PARK, not a route failure:
// the hull is still IN_TRANSIT and the ARRIVED event was lost/raced, which a later
// run re-syncs and resolves. Failing the route here propagates a hard error that
// burns the container's restart budget on an "unrecoverable crash" for what is a
// transient, self-healing condition. So this DEFERS instead: it logs a park (WARNING,
// mirroring run_factory_coordinator.go's per-node park of this same error type), does
// NOT mark the route FAILED, and does NOT emit the route-completion FAILURE metric —
// while still returning the error with its TYPE PRESERVED so the caller keeps its
// recoverable classification. It deliberately does not fabricate arrival (the ship
// really is still in transit); the caller/container simply retries.
//
// Any other error is a genuine route failure: mark the route FAILED and record the
// route-completion (failure) metric.
func (e *RouteExecutor) reactToSegmentFailure(
	ctx context.Context,
	route *domainNavigation.Route,
	ship *domainNavigation.Ship,
	segment *domainNavigation.RouteSegment,
	segmentCount int,
	err error,
) error {
	logger := common.LoggerFromContext(ctx)

	var arrivalErr *ErrArrivalWaitExhausted
	if errors.As(err, &arrivalErr) {
		logger.Log("WARNING", "Route segment parked on arrival-wait exhaustion - ship still IN_TRANSIT, deferring for retry rather than failing the route", map[string]interface{}{
			"ship_symbol":   ship.ShipSymbol(),
			"action":        "route_segment_parked",
			"segment_index": segmentCount,
			"from":          segment.FromWaypoint.Symbol,
			"to":            segment.ToWaypoint.Symbol,
			"attempts":      arrivalErr.Attempts,
		})
		return err
	}

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

// executeSegment executes a single route segment using atomic commands.
//
// fuelReserve is the fuel the plan's remaining legs need after this one lands
// (see Route.FuelReserveAfterCurrentSegment); the speed-up upgrade may not spend it.
func (e *RouteExecutor) executeSegment(
	ctx context.Context,
	segment *domainNavigation.RouteSegment,
	ship *domainNavigation.Ship,
	playerID shared.PlayerID,
	fuelReserve int,
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

	flightMode := e.selectOptimalFlightMode(ctx, segment, ship, fuelReserve)

	flightMode, err := e.ensureAffordableFlightMode(ctx, segment, ship, playerID, flightMode, fuelReserve)
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
	e.scanShipyardIfPresent(ctx, segment, ship, playerID)

	return nil
}

func (e *RouteExecutor) navigateToSegmentDestination(ctx context.Context, segment *domainNavigation.RouteSegment, ship *domainNavigation.Ship, playerID shared.PlayerID, flightMode shared.FlightMode) error {
	return e.attemptSegmentNavigate(ctx, segment, ship, playerID, flightMode, true)
}

// attemptSegmentNavigate issues the segment's navigate and processes its
// outcome. adoptTransitRecovery guards the in-transit recovery so a retried
// attempt cannot recurse a second time.
func (e *RouteExecutor) attemptSegmentNavigate(ctx context.Context, segment *domainNavigation.RouteSegment, ship *domainNavigation.Ship, playerID shared.PlayerID, flightMode shared.FlightMode, adoptTransitRecovery bool) error {
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
		var transitErr *types.ErrShipInTransit
		if adoptTransitRecovery && errors.As(err, &transitErr) {
			return e.resumeSegmentAfterAdoptedTransit(ctx, segment, ship, playerID, flightMode, transitErr)
		}
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
