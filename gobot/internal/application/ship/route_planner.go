package ship

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainRouting "github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// RoutePlanner handles route planning using routing client
type RoutePlanner struct {
	routingClient domainRouting.RoutingClient
}

// NewRoutePlanner creates a new route planner
func NewRoutePlanner(routingClient domainRouting.RoutingClient) *RoutePlanner {
	return &RoutePlanner{
		routingClient: routingClient,
	}
}

// PlanRoute plans a route from ship's current location to destination
func (p *RoutePlanner) PlanRoute(
	ctx context.Context,
	ship *domainNavigation.Ship,
	destination string,
	waypoints map[string]*shared.Waypoint,
	preferCruise bool,
) (*domainNavigation.Route, error) {
	// Convert waypoints to DTO
	waypointData := make([]*system.WaypointData, 0, len(waypoints))
	for _, wp := range waypoints {
		waypointData = append(waypointData, &system.WaypointData{
			Symbol:  wp.Symbol,
			X:       wp.X,
			Y:       wp.Y,
			HasFuel: wp.HasFuel,
		})
	}

	// Create routing request
	request := &domainRouting.RouteRequest{
		SystemSymbol:  ship.CurrentLocation().SystemSymbol,
		StartWaypoint: ship.CurrentLocation().Symbol,
		GoalWaypoint:  destination,
		CurrentFuel:   ship.Fuel().Current,
		FuelCapacity:  ship.FuelCapacity(),
		EngineSpeed:   ship.EngineSpeed(),
		Waypoints:     waypointData,
		PreferCruise:  preferCruise,
	}

	// Call routing client
	routeResponse, err := p.routingClient.PlanRoute(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("routing client error: %w", err)
	}

	// Convert route response to Route domain entity
	return p.createRouteFromPlan(ctx, routeResponse, ship, waypoints)
}

// createRouteFromPlan creates Route entity from routing engine plan
// Extracted from navigate_ship.go:379-469
func (p *RoutePlanner) createRouteFromPlan(
	ctx context.Context,
	routePlan *domainRouting.RouteResponse,
	ship *domainNavigation.Ship,
	waypointObjects map[string]*shared.Waypoint,
) (*domainNavigation.Route, error) {
	logger := common.LoggerFromContext(ctx)
	segments := []*domainNavigation.RouteSegment{}
	refuelBeforeDeparture := false

	if len(routePlan.Steps) == 0 {
		return nil, fmt.Errorf("no route found: routing engine returned empty plan")
	}

	// Log routing service response
	logger.Log("INFO", "Route planning completed by routing service", map[string]interface{}{
		"ship_symbol": ship.ShipSymbol(),
		"action":      "route_plan_received",
		"step_count":  len(routePlan.Steps),
		"origin":      ship.CurrentLocation().Symbol,
	})
	for i, step := range routePlan.Steps {
		logger.Log("INFO", "Route planning step", map[string]interface{}{
			"ship_symbol":  ship.ShipSymbol(),
			"action":       "route_step",
			"step_index":   i,
			"step_action":  string(step.Action),
			"waypoint":     step.Waypoint,
			"flight_mode":  step.Mode,
			"fuel_cost":    step.FuelCost,
			"time_seconds": step.TimeSeconds,
		})
	}

	// Check if first action is REFUEL (ship at fuel station with low fuel)
	if routePlan.Steps[0].Action == domainRouting.RouteActionRefuel {
		refuelBeforeDeparture = true
	}

	var fromWaypoint *shared.Waypoint
	for _, step := range routePlan.Steps {
		if step.Action == domainRouting.RouteActionTravel {
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

			segment := domainNavigation.NewRouteSegment(
				fromWaypoint,
				toWaypoint,
				fromWaypoint.DistanceTo(toWaypoint),
				step.FuelCost,
				step.TimeSeconds,
				flightMode,
				false, // Will be updated if next step is REFUEL
			)
			segments = append(segments, segment)
		} else if step.Action == domainRouting.RouteActionRefuel {
			// Mark previous segment as requiring refuel (mid-route refueling)
			if len(segments) > 0 {
				prev := segments[len(segments)-1]
				segments[len(segments)-1] = domainNavigation.NewRouteSegment(
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

	// Log created segments
	logger.Log("INFO", "Route segments created", map[string]interface{}{
		"ship_symbol":   ship.ShipSymbol(),
		"action":        "segments_created",
		"segment_count": len(segments),
	})
	for i, seg := range segments {
		logger.Log("INFO", "Route segment details", map[string]interface{}{
			"ship_symbol":     ship.ShipSymbol(),
			"action":          "route_segment",
			"segment_index":   i,
			"from_waypoint":   seg.FromWaypoint.Symbol,
			"to_waypoint":     seg.ToWaypoint.Symbol,
			"fuel_required":   seg.FuelRequired,
			"requires_refuel": seg.RequiresRefuel,
		})
	}

	// Generate route ID
	routeID := fmt.Sprintf("%s_%d", ship.ShipSymbol(), routePlan.TotalTimeSeconds)

	return domainNavigation.NewRoute(
		routeID,
		ship.ShipSymbol(),
		ship.PlayerID(),
		segments,
		ship.FuelCapacity(),
		refuelBeforeDeparture,
	)
}
