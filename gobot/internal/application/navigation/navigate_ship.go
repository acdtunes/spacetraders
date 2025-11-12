package navigation

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// extractSystemSymbol extracts system symbol from waypoint symbol
// Format: SYSTEM-SECTOR-WAYPOINT -> SYSTEM-SECTOR
// Example: X1-ABC123-AB12 -> X1-ABC123
func extractSystemSymbol(waypointSymbol string) string {
	// Find the last hyphen
	for i := len(waypointSymbol) - 1; i >= 0; i-- {
		if waypointSymbol[i] == '-' {
			return waypointSymbol[:i]
		}
	}
	return waypointSymbol
}

// calculateArrivalWaitTime calculates seconds to wait until arrival
//
// Args:
//     arrivalTimeStr: ISO format arrival time from API (e.g., "2024-01-01T12:00:00Z")
//
// Returns:
//     Seconds to wait (minimum 0)
func calculateArrivalWaitTime(arrivalTimeStr string) int {
	// Handle both Z suffix and +00:00 suffix
	arrivalTimeStr = strings.Replace(arrivalTimeStr, "Z", "+00:00", 1)

	arrivalTime, err := time.Parse(time.RFC3339, arrivalTimeStr)
	if err != nil {
		log.Printf("Warning: failed to parse arrival time %s: %v", arrivalTimeStr, err)
		return 0
	}

	now := time.Now().UTC()
	waitSeconds := arrivalTime.Sub(now).Seconds()

	if waitSeconds < 0 {
		return 0
	}

	return int(waitSeconds)
}

// NavigateShipCommand represents a command to navigate a ship to a destination
type NavigateShipCommand struct {
	ShipSymbol  string
	Destination string
	PlayerID    int
}

// NavigateShipResponse represents the result of navigation
type NavigateShipResponse struct {
	Status          string
	ArrivalTime     int
	CurrentLocation string
	FuelRemaining   int
	Error           string
	Route           *navigation.Route
}

// NavigateShipHandler handles the NavigateShip command with full Python feature parity
type NavigateShipHandler struct {
	shipRepo      common.ShipRepository
	waypointRepo  common.WaypointRepository
	graphProvider common.ISystemGraphProvider
	routingClient common.RoutingClient
}

// NewNavigateShipHandler creates a new NavigateShipHandler with graph provider
func NewNavigateShipHandler(
	shipRepo common.ShipRepository,
	waypointRepo common.WaypointRepository,
	graphProvider common.ISystemGraphProvider,
	routingClient common.RoutingClient,
) *NavigateShipHandler {
	return &NavigateShipHandler{
		shipRepo:      shipRepo,
		waypointRepo:  waypointRepo,
		graphProvider: graphProvider,
		routingClient: routingClient,
	}
}

// Handle executes the NavigateShip command with full Python implementation
func (h *NavigateShipHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*NavigateShipCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type: expected *NavigateShipCommand")
	}

	// 1. Load ship from repository and sync from API to ensure fresh state
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return &NavigateShipResponse{
			Status: "error",
			Error:  fmt.Sprintf("failed to get ship: %v", err),
		}, nil
	}

	// 2. Extract system symbol
	systemSymbol := extractSystemSymbol(ship.CurrentLocation().Symbol)

	// 3. Get system graph
	graphResult, err := h.graphProvider.GetGraph(ctx, systemSymbol, false)
	if err != nil {
		return &NavigateShipResponse{
			Status: "error",
			Error:  fmt.Sprintf("failed to get system graph: %v", err),
		}, nil
	}

	log.Printf("Loaded graph for %s from %s", systemSymbol, graphResult.Source)

	// 3a. Query waypoints table for trait enrichment data
	waypointList, err := h.waypointRepo.ListBySystem(ctx, systemSymbol)
	var waypointTraits map[string]*shared.Waypoint
	if err != nil {
		log.Printf("Warning: failed to load waypoint traits: %v (continuing with graph-only data)", err)
		waypointTraits = make(map[string]*shared.Waypoint)
	} else {
		// Create lookup map for has_fuel by waypoint symbol
		waypointTraits = make(map[string]*shared.Waypoint)
		for _, wp := range waypointList {
			waypointTraits[wp.Symbol] = wp
		}
	}

	// Convert graph waypoints to Waypoint objects with trait enrichment
	waypointObjects := h.convertGraphToWaypoints(graphResult.Graph, waypointTraits)

	// Validate waypoint cache has waypoints
	if len(waypointObjects) == 0 {
		return &NavigateShipResponse{
			Status: "error",
			Error: fmt.Sprintf("No waypoints found for system %s. "+
				"The waypoint cache is empty. Please sync waypoints from API first.", systemSymbol),
		}, nil
	}

	// Validate ship location exists in waypoint cache
	if _, ok := waypointObjects[ship.CurrentLocation().Symbol]; !ok {
		return &NavigateShipResponse{
			Status: "error",
			Error: fmt.Sprintf("Waypoint %s not found in cache for system %s. "+
				"Ship location is missing from waypoint cache. Please sync waypoints from API.",
				ship.CurrentLocation().Symbol, systemSymbol),
		}, nil
	}

	// Validate destination exists in waypoint cache
	if _, ok := waypointObjects[cmd.Destination]; !ok {
		return &NavigateShipResponse{
			Status: "error",
			Error: fmt.Sprintf("Waypoint %s not found in cache for system %s. "+
				"Destination waypoint is missing from waypoint cache. Please sync waypoints from API.",
				cmd.Destination, systemSymbol),
		}, nil
	}

	// 4. Check if ship is already at destination (idempotent command)
	if ship.CurrentLocation().Symbol == cmd.Destination {
		// Ship is already at destination - create empty route and mark as completed
		routeID := fmt.Sprintf("%s_already_at_destination", cmd.ShipSymbol)
		route, err := navigation.NewRoute(
			routeID,
			cmd.ShipSymbol,
			cmd.PlayerID,
			[]*navigation.RouteSegment{}, // No segments needed
			ship.FuelCapacity(),
			false,
		)
		if err != nil {
			return &NavigateShipResponse{
				Status: "error",
				Error:  fmt.Sprintf("failed to create route: %v", err),
			}, nil
		}

		route.StartExecution()
		route.CompleteSegment() // Sets status to COMPLETED since no segments

		return &NavigateShipResponse{
			Status:          "already_at_destination",
			CurrentLocation: ship.CurrentLocation().Symbol,
			FuelRemaining:   ship.Fuel().Current,
			Route:           route,
		}, nil
	}

	// 5. Find optimal path using routing engine
	// Pass waypoint_objects (flat map) directly to routing engine
	route, err := h.planRoute(ctx, ship, cmd.Destination, waypointObjects)
	if err != nil {
		return &NavigateShipResponse{
			Status: "error",
			Error:  fmt.Sprintf("failed to plan route: %v", err),
		}, nil
	}

	if route == nil {
		waypointCount := len(waypointObjects)
		fuelStations := 0
		for _, wp := range waypointObjects {
			if wp.HasFuel {
				fuelStations++
			}
		}

		return &NavigateShipResponse{
			Status: "error",
			Error: fmt.Sprintf("No route found from %s to %s. "+
				"The routing engine could not find a valid path. "+
				"System %s has %d waypoints cached with %d fuel stations. "+
				"Ship fuel: %d/%d. "+
				"Route may be unreachable or require multi-hop refueling not supported by current fuel levels.",
				ship.CurrentLocation().Symbol, cmd.Destination, systemSymbol,
				waypointCount, fuelStations, ship.Fuel().Current, ship.FuelCapacity()),
		}, nil
	}

	// 6. Execute route with all safety features
	route.StartExecution()

	// Wrap execution in error handler for route failure marking
	defer func() {
		if r := recover(); r != nil {
			route.FailRoute(fmt.Sprintf("panic during execution: %v", r))
		}
	}()

	if err := h.executeRoute(ctx, route, ship, cmd.PlayerID); err != nil {
		route.FailRoute(err.Error())
		return &NavigateShipResponse{
			Status: "error",
			Error:  fmt.Sprintf("failed to execute route: %v", err),
			Route:  route,
		}, nil
	}

	// 7. Return success response
	return &NavigateShipResponse{
		Status:          "completed",
		ArrivalTime:     route.TotalTravelTime(),
		CurrentLocation: cmd.Destination,
		FuelRemaining:   ship.Fuel().Current,
		Route:           route,
	}, nil
}

// convertGraphToWaypoints converts graph waypoints to Waypoint objects with trait enrichment
//
// Args:
//     graph: Graph structure from system_graphs table (structure-only)
//     waypointTraits: Optional lookup dict of Waypoint objects from waypoints table
//                    Maps waypoint_symbol -> Waypoint with full trait data
//
// Returns:
//     Dict of waypoint_symbol -> Waypoint objects with correct has_fuel data
func (h *NavigateShipHandler) convertGraphToWaypoints(
	graph map[string]interface{},
	waypointTraits map[string]*shared.Waypoint,
) map[string]*shared.Waypoint {
	waypointObjects := make(map[string]*shared.Waypoint)

	graphWaypoints, ok := graph["waypoints"].(map[string]interface{})
	if !ok {
		return waypointObjects
	}

	for symbol, wpData := range graphWaypoints {
		wpMap, ok := wpData.(map[string]interface{})
		if !ok {
			continue
		}

		// Check if we have trait data from waypoints table
		if traitWp, exists := waypointTraits[symbol]; exists {
			// Use full Waypoint object from waypoints table (has correct has_fuel)
			waypointObjects[symbol] = traitWp
		} else {
			// Fallback: create Waypoint from graph structure-only data
			x, _ := wpMap["x"].(float64)
			y, _ := wpMap["y"].(float64)
			wpType, _ := wpMap["type"].(string)
			systemSymbol, _ := wpMap["systemSymbol"].(string)

			// Try to extract has_fuel from graph (may not exist in structure-only graph)
			hasFuel := false
			if hasFuelVal, ok := wpMap["has_fuel"].(bool); ok {
				hasFuel = hasFuelVal
			} else {
				// Fallback: check if traits contain MARKETPLACE
				if traits, ok := wpMap["traits"].([]string); ok {
					for _, trait := range traits {
						if trait == "MARKETPLACE" || trait == "FUEL_STATION" {
							hasFuel = true
							break
						}
					}
				}
			}

			wp, err := shared.NewWaypoint(symbol, x, y)
			if err != nil {
				log.Printf("Warning: failed to create waypoint %s: %v", symbol, err)
				continue
			}

			wp.Type = wpType
			wp.SystemSymbol = systemSymbol
			wp.HasFuel = hasFuel

			// Extract orbitals if present
			if orbitals, ok := wpMap["orbitals"].([]string); ok {
				wp.Orbitals = orbitals
			}

			waypointObjects[symbol] = wp
		}
	}

	return waypointObjects
}

// planRoute plans route using routing client
func (h *NavigateShipHandler) planRoute(
	ctx context.Context,
	ship *navigation.Ship,
	destination string,
	waypoints map[string]*shared.Waypoint,
) (*navigation.Route, error) {
	// Convert waypoints to DTO
	waypointData := make([]*common.WaypointData, 0, len(waypoints))
	for _, wp := range waypoints {
		waypointData = append(waypointData, &common.WaypointData{
			Symbol:  wp.Symbol,
			X:       wp.X,
			Y:       wp.Y,
			HasFuel: wp.HasFuel,
		})
	}

	request := &common.RouteRequest{
		SystemSymbol:  ship.CurrentLocation().SystemSymbol,
		StartWaypoint: ship.CurrentLocation().Symbol,
		GoalWaypoint:  destination,
		CurrentFuel:   ship.Fuel().Current,
		FuelCapacity:  ship.FuelCapacity(),
		EngineSpeed:   ship.EngineSpeed(),
		Waypoints:     waypointData,
	}

	routeResponse, err := h.routingClient.PlanRoute(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("routing client error: %w", err)
	}

	// Convert route response to Route domain entity
	return h.createRouteFromPlan(routeResponse, ship, waypoints)
}

// createRouteFromPlan creates Route entity from routing engine plan
func (h *NavigateShipHandler) createRouteFromPlan(
	routePlan *common.RouteResponse,
	ship *navigation.Ship,
	waypointObjects map[string]*shared.Waypoint,
) (*navigation.Route, error) {
	segments := []*navigation.RouteSegment{}
	refuelBeforeDeparture := false

	if len(routePlan.Steps) == 0 {
		return nil, fmt.Errorf("no route found: routing engine returned empty plan")
	}

	// Check if first action is REFUEL (ship at fuel station with low fuel)
	if routePlan.Steps[0].Action == common.RouteActionRefuel {
		refuelBeforeDeparture = true
	}

	var fromWaypoint *shared.Waypoint
	for _, step := range routePlan.Steps {
		if step.Action == common.RouteActionTravel {
			// Find the from_waypoint (previous step's destination or start)
			if len(segments) > 0 {
				fromWaypoint = segments[len(segments)-1].ToWaypoint
			} else {
				fromWaypoint = ship.CurrentLocation()
			}

			toWaypoint, ok := waypointObjects[step.Waypoint]
			if !ok {
				return nil, fmt.Errorf("waypoint %s not found in cache", step.Waypoint)
			}

		// Determine flight mode from step (routing engine uses BURN-first logic)
		flightMode := shared.FlightModeCruise // Default fallback
		if step.Mode != "" {
			switch step.Mode {
			case "BURN":
				flightMode = shared.FlightModeBurn
			case "CRUISE":
				flightMode = shared.FlightModeCruise
			case "DRIFT":
				flightMode = shared.FlightModeDrift
			case "STEALTH":
				flightMode = shared.FlightModeStealth
			}
		}

			segment := navigation.NewRouteSegment(
				fromWaypoint,
				toWaypoint,
				fromWaypoint.DistanceTo(toWaypoint),
				step.FuelCost,
				step.TimeSeconds,
				flightMode,
				false, // Will be updated if next step is REFUEL
			)
			segments = append(segments, segment)
		} else if step.Action == common.RouteActionRefuel {
			// Mark previous segment as requiring refuel (mid-route refueling)
			if len(segments) > 0 {
				prev := segments[len(segments)-1]
				segments[len(segments)-1] = navigation.NewRouteSegment(
					prev.FromWaypoint,
					prev.ToWaypoint,
					prev.Distance,
					prev.FuelRequired,
					prev.TravelTime,
					prev.FlightMode,
					true, // Requires refuel
				)
			}
		}
	}

	if len(segments) == 0 {
		return nil, fmt.Errorf("route plan has no TRAVEL steps")
	}

	// Generate route ID
	routeID := fmt.Sprintf("%s_%d", ship.ShipSymbol(), routePlan.TotalTimeSeconds)

	return navigation.NewRoute(
		routeID,
		ship.ShipSymbol(),
		ship.PlayerID(),
		segments,
		ship.FuelCapacity(),
		refuelBeforeDeparture,
	)
}

// executeRoute executes route step by step with all safety features from Python implementation
//
// For each segment:
// 1. Handle IN_TRANSIT from previous command (wait for arrival)
// 2. Refuel before departure if route requires it
// 3. Ensure ship is in orbit (domain handles state transition)
// 4. Pre-departure refuel check (prevent DRIFT mode at fuel stations)
// 5. Set flight mode before navigation
// 6. Call API navigate
// 7. Wait for arrival with time calculation
// 8. Auto-sync ship state from API after each operation
// 9. 90% opportunistic refueling (defense-in-depth safety check)
// 10. Handle planned refueling if required
// 11. Complete segment
func (h *NavigateShipHandler) executeRoute(
	ctx context.Context,
	route *navigation.Route,
	ship *navigation.Ship,
	playerID int,
) error {
	// IDEMPOTENCY: If ship is IN_TRANSIT from a previous command, wait for arrival first
	// This makes navigation commands idempotent - you can send them at any time
	if ship.NavStatus() == navigation.NavStatusInTransit {
		log.Printf("Ship %s is IN_TRANSIT from previous command, waiting for arrival...", ship.ShipSymbol())

		// Fetch current ship state to get arrival time (need to call API directly)
		// For now, just wait a bit and re-sync
		// TODO: Get arrival time from API
		time.Sleep(5 * time.Second)

		// Re-sync ship state
		freshShip, err := h.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
		if err != nil {
			return fmt.Errorf("failed to sync ship state after waiting for arrival: %w", err)
		}
		ship = freshShip

		// Call arrive() if still IN_TRANSIT
		if ship.NavStatus() == navigation.NavStatusInTransit {
			if err := ship.Arrive(); err != nil {
				return fmt.Errorf("failed to mark ship as arrived: %w", err)
			}
		}

		log.Printf("Ship arrived, status now: %s", ship.NavStatus())
	}

	// Handle refuel before departure if needed (ship at fuel station with low fuel)
	if route.RefuelBeforeDeparture() {
		log.Printf("Refueling before departure")

		// Dock for refuel - domain handles state transition
		stateChanged, err := ship.EnsureDocked()
		if err != nil {
			return fmt.Errorf("failed to ensure docked for refuel: %w", err)
		}

		if stateChanged {
			// Call API to dock ship
			if err := h.shipRepo.Dock(ctx, ship, playerID); err != nil {
				return fmt.Errorf("failed to dock ship: %w", err)
			}

			// Auto-sync: Fetch full ship state after dock
			ship, err = h.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
			if err != nil {
				return fmt.Errorf("failed to sync ship after dock: %w", err)
			}
		}

		// Refuel before starting journey
		if err := h.shipRepo.Refuel(ctx, ship, playerID, nil); err != nil {
			return fmt.Errorf("failed to refuel before departure: %w", err)
		}

		// Auto-sync: Extract ship state after refuel
		ship, err = h.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
		if err != nil {
			return fmt.Errorf("failed to sync ship after refuel: %w", err)
		}

		// Return to orbit - domain handles DOCKED → IN_ORBIT transition
		stateChanged, err = ship.EnsureInOrbit()
		if err != nil {
			return fmt.Errorf("failed to ensure in orbit after refuel: %w", err)
		}

		if stateChanged {
			// Call API to orbit ship
			if err := h.shipRepo.Orbit(ctx, ship, playerID); err != nil {
				return fmt.Errorf("failed to orbit ship: %w", err)
			}

			// Auto-sync: Fetch full ship state after orbit
			ship, err = h.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
			if err != nil {
				return fmt.Errorf("failed to sync ship after orbit: %w", err)
			}
		}
	}

	// Execute route segments
	for segmentIdx, segment := range route.Segments() {
		isLastSegment := (segmentIdx == len(route.Segments())-1)

		log.Printf("Executing segment %d/%d: %s → %s",
			segmentIdx+1, len(route.Segments()),
			segment.FromWaypoint.Symbol, segment.ToWaypoint.Symbol)

		// Ensure ship is in orbit - domain handles DOCKED → IN_ORBIT transition
		stateChanged, err := ship.EnsureInOrbit()
		if err != nil {
			return fmt.Errorf("failed to ensure in orbit: %w", err)
		}

		if stateChanged {
			// Call API to orbit ship
			if err := h.shipRepo.Orbit(ctx, ship, playerID); err != nil {
				return fmt.Errorf("failed to orbit ship: %w", err)
			}

			// Auto-sync: Fetch full ship state after orbit
			ship, err = h.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
			if err != nil {
				return fmt.Errorf("failed to sync ship after orbit: %w", err)
			}
		}

		// Pre-departure refuel check: If planned to use DRIFT mode at a fuel station, refuel instead
		currentLocation := ship.CurrentLocation()
		fuelPercentage := 0.0
		if ship.FuelCapacity() > 0 {
			fuelPercentage = float64(ship.Fuel().Current) / float64(ship.FuelCapacity())
		}

		// Only refuel if: (1) using DRIFT mode, (2) at fuel station, (3) low fuel
		if segment.FlightMode == shared.FlightModeDrift &&
			fuelPercentage < 0.9 &&
			segment.FromWaypoint.Symbol == currentLocation.Symbol &&
			currentLocation.HasFuel {

			log.Printf("Pre-departure refuel at %s: Preventing DRIFT mode with %.1f%% fuel",
				currentLocation.Symbol, fuelPercentage*100)

			// Dock if in orbit
			if ship.NavStatus() != navigation.NavStatusDocked {
				stateChanged, err := ship.EnsureDocked()
				if err != nil {
					return fmt.Errorf("failed to dock for pre-departure refuel: %w", err)
				}

				if stateChanged {
					if err := h.shipRepo.Dock(ctx, ship, playerID); err != nil {
						return fmt.Errorf("failed to dock: %w", err)
					}

					ship, err = h.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
					if err != nil {
						return fmt.Errorf("failed to sync after dock: %w", err)
					}
				}
			}

			// Refuel
			if err := h.shipRepo.Refuel(ctx, ship, playerID, nil); err != nil {
				return fmt.Errorf("failed to refuel: %w", err)
			}

			ship, err = h.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
			if err != nil {
				return fmt.Errorf("failed to sync after refuel: %w", err)
			}

			// Orbit for departure
			stateChanged, err = ship.EnsureInOrbit()
			if err != nil {
				return fmt.Errorf("failed to orbit after refuel: %w", err)
			}

			if stateChanged {
				if err := h.shipRepo.Orbit(ctx, ship, playerID); err != nil {
					return fmt.Errorf("failed to orbit: %w", err)
				}

				ship, err = h.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
				if err != nil {
					return fmt.Errorf("failed to sync after orbit: %w", err)
				}
			}
		}

		// Set flight mode before navigation
		// This ensures the ship uses the mode planned by the routing engine
		if err := h.shipRepo.SetFlightMode(ctx, ship, playerID, segment.FlightMode.Name()); err != nil {
			return fmt.Errorf("failed to set flight mode: %w", err)
		}

		// Navigate to destination
		log.Printf("Navigating to %s with mode %s", segment.ToWaypoint.Symbol, segment.FlightMode.Name())

		navResult, err := h.shipRepo.Navigate(ctx, ship, segment.ToWaypoint, playerID)
		if err != nil {
			return fmt.Errorf("failed to navigate to %s: %w", segment.ToWaypoint.Symbol, err)
		}

		// Auto-sync: Fetch full ship state after navigation
		ship, err = h.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
		if err != nil {
			return fmt.Errorf("failed to sync ship after navigation: %w", err)
		}

		// Wait for arrival if ship is IN_TRANSIT
		// This prevents attempting to navigate to next segment while still in transit
		// CRITICAL: Use ACTUAL arrival time from API, not pre-calculated segment time (Python pattern)
		if ship.NavStatus() == navigation.NavStatusInTransit {
			// Extract arrival time from API response and calculate wait time
			if navResult.ArrivalTimeStr != "" {
				waitTime := calculateArrivalWaitTime(navResult.ArrivalTimeStr)

				if waitTime > 0 {
					log.Printf("Waiting %d seconds for ship to arrive at %s", waitTime+3, segment.ToWaypoint.Symbol)
					time.Sleep(time.Duration(waitTime+3) * time.Second) // +3 second buffer for API delays
				}
			}

			// Re-sync ship state after arrival
			ship, err = h.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
			if err != nil {
				return fmt.Errorf("failed to sync ship after arrival: %w", err)
			}

			// Call arrive() if still IN_TRANSIT
			if ship.NavStatus() == navigation.NavStatusInTransit {
				if err := ship.Arrive(); err != nil {
					return fmt.Errorf("failed to mark ship as arrived: %w", err)
				}
			}
		}

		// Opportunistic refueling safety check (90% rule)
		// Defense-in-depth: catch cases where routing engine didn't add refuel
		currentWaypoint := segment.ToWaypoint
		fuelPercentage = 0.0
		if ship.FuelCapacity() > 0 {
			fuelPercentage = float64(ship.Fuel().Current) / float64(ship.FuelCapacity())
		}

		if ship.FuelCapacity() > 0 &&
			currentWaypoint.HasFuel &&
			fuelPercentage < 0.9 &&
			!segment.RequiresRefuel {

			log.Printf("Opportunistic refuel at %s: Fuel at %.1f%% (%d/%d)",
				currentWaypoint.Symbol, fuelPercentage*100,
				ship.Fuel().Current, ship.FuelCapacity())

			// Dock for refuel
			stateChanged, err := ship.EnsureDocked()
			if err != nil {
				return fmt.Errorf("failed to dock for opportunistic refuel: %w", err)
			}

			if stateChanged {
				if err := h.shipRepo.Dock(ctx, ship, playerID); err != nil {
					return fmt.Errorf("failed to dock: %w", err)
				}

				ship, err = h.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
				if err != nil {
					return fmt.Errorf("failed to sync after dock: %w", err)
				}
			}

			// Refuel
			if err := h.shipRepo.Refuel(ctx, ship, playerID, nil); err != nil {
				return fmt.Errorf("failed to refuel: %w", err)
			}

			ship, err = h.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
			if err != nil {
				return fmt.Errorf("failed to sync after refuel: %w", err)
			}

			// Return to orbit
			stateChanged, err = ship.EnsureInOrbit()
			if err != nil {
				return fmt.Errorf("failed to orbit after refuel: %w", err)
			}

			if stateChanged {
				if err := h.shipRepo.Orbit(ctx, ship, playerID); err != nil {
					return fmt.Errorf("failed to orbit: %w", err)
				}

				ship, err = h.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
				if err != nil {
					return fmt.Errorf("failed to sync after orbit: %w", err)
				}
			}
		}

		// Handle refueling if required (planned refuel from routing engine)
		if segment.RequiresRefuel {
			log.Printf("Planned refuel at %s", currentWaypoint.Symbol)

			// Dock for refuel - domain handles IN_ORBIT → DOCKED transition
			stateChanged, err := ship.EnsureDocked()
			if err != nil {
				return fmt.Errorf("failed to dock for planned refuel: %w", err)
			}

			if stateChanged {
				// Call API to dock ship
				if err := h.shipRepo.Dock(ctx, ship, playerID); err != nil {
					return fmt.Errorf("failed to dock: %w", err)
				}

				// Auto-sync: Fetch full ship state after dock
				ship, err = h.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
				if err != nil {
					return fmt.Errorf("failed to sync after dock: %w", err)
				}
			}

			// Refuel
			if err := h.shipRepo.Refuel(ctx, ship, playerID, nil); err != nil {
				return fmt.Errorf("failed to refuel: %w", err)
			}

			ship, err = h.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
			if err != nil {
				return fmt.Errorf("failed to sync after refuel: %w", err)
			}

			// Return to orbit - domain handles DOCKED → IN_ORBIT transition
			stateChanged, err = ship.EnsureInOrbit()
			if err != nil {
				return fmt.Errorf("failed to orbit after refuel: %w", err)
			}

			if stateChanged {
				// Call API to orbit ship
				if err := h.shipRepo.Orbit(ctx, ship, playerID); err != nil {
					return fmt.Errorf("failed to orbit: %w", err)
				}

				// Auto-sync: Fetch full ship state after orbit
				ship, err = h.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
				if err != nil {
					return fmt.Errorf("failed to sync after orbit: %w", err)
				}
			}
		}

		// Complete segment
		if err := route.CompleteSegment(); err != nil {
			return fmt.Errorf("failed to complete segment: %w", err)
		}

		log.Printf("Segment %d/%d completed", segmentIdx+1, len(route.Segments()))
		_ = isLastSegment // Silence unused variable warning
	}

	log.Printf("Route execution completed successfully")
	return nil
}
