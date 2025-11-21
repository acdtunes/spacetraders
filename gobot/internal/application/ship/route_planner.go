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
func (p *RoutePlanner) createRouteFromPlan(
	ctx context.Context,
	routePlan *domainRouting.RouteResponse,
	ship *domainNavigation.Ship,
	waypointObjects map[string]*shared.Waypoint,
) (*domainNavigation.Route, error) {
	if len(routePlan.Steps) == 0 {
		return nil, fmt.Errorf("no route found: routing engine returned empty plan")
	}

	p.logRoutePlan(ctx, routePlan, ship)

	refuelBeforeDeparture := p.checkForInitialRefuel(routePlan)
	segments, err := p.processRoutePlanSteps(routePlan, ship, waypointObjects)
	if err != nil {
		return nil, err
	}

	p.logCreatedSegments(ctx, segments, ship)

	routeID := fmt.Sprintf("%s_%d", ship.ShipSymbol(), routePlan.TotalTimeSeconds)

	return domainNavigation.NewRoute(
		routeID,
		ship.ShipSymbol(),
		ship.PlayerID().Value(),
		segments,
		ship.FuelCapacity(),
		refuelBeforeDeparture,
	)
}

func (p *RoutePlanner) logRoutePlan(ctx context.Context, routePlan *domainRouting.RouteResponse, ship *domainNavigation.Ship) {
	logger := common.LoggerFromContext(ctx)
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
			"step_action":  int(step.Action),
			"waypoint":     step.Waypoint,
			"flight_mode":  step.Mode,
			"fuel_cost":    step.FuelCost,
			"time_seconds": step.TimeSeconds,
		})
	}
}

func (p *RoutePlanner) checkForInitialRefuel(routePlan *domainRouting.RouteResponse) bool {
	return routePlan.Steps[0].Action == domainRouting.RouteActionRefuel
}

func (p *RoutePlanner) processRoutePlanSteps(
	routePlan *domainRouting.RouteResponse,
	ship *domainNavigation.Ship,
	waypointObjects map[string]*shared.Waypoint,
) ([]*domainNavigation.RouteSegment, error) {
	segments := []*domainNavigation.RouteSegment{}

	for _, step := range routePlan.Steps {
		if step.Action == domainRouting.RouteActionTravel {
			segment, err := p.createSegmentFromTravelStep(step, segments, ship, waypointObjects)
			if err != nil {
				return nil, err
			}
			segments = append(segments, segment)
		} else if step.Action == domainRouting.RouteActionRefuel {
			p.markLastSegmentForRefuel(&segments)
		}
	}

	if len(segments) == 0 {
		return nil, fmt.Errorf("route plan has no TRAVEL steps")
	}

	return segments, nil
}

func (p *RoutePlanner) createSegmentFromTravelStep(
	step *domainRouting.RouteStepData,
	existingSegments []*domainNavigation.RouteSegment,
	ship *domainNavigation.Ship,
	waypointObjects map[string]*shared.Waypoint,
) (*domainNavigation.RouteSegment, error) {
	fromWaypoint := p.determineFromWaypoint(existingSegments, ship)

	toWaypoint, ok := waypointObjects[step.Waypoint]
	if !ok {
		return nil, fmt.Errorf("waypoint %s not found in cache", step.Waypoint)
	}

	flightMode := p.parseFlightMode(step.Mode)

	return domainNavigation.NewRouteSegment(
		fromWaypoint,
		toWaypoint,
		fromWaypoint.DistanceTo(toWaypoint),
		step.FuelCost,
		step.TimeSeconds,
		flightMode,
		false,
	), nil
}

func (p *RoutePlanner) determineFromWaypoint(segments []*domainNavigation.RouteSegment, ship *domainNavigation.Ship) *shared.Waypoint {
	if len(segments) > 0 {
		return segments[len(segments)-1].ToWaypoint
	}
	return ship.CurrentLocation()
}

func (p *RoutePlanner) parseFlightMode(mode string) shared.FlightMode {
	switch mode {
	case "BURN":
		return shared.FlightModeBurn
	case "CRUISE":
		return shared.FlightModeCruise
	case "DRIFT":
		return shared.FlightModeDrift
	case "STEALTH":
		return shared.FlightModeStealth
	default:
		return shared.FlightModeCruise
	}
}

func (p *RoutePlanner) markLastSegmentForRefuel(segments *[]*domainNavigation.RouteSegment) {
	if len(*segments) > 0 {
		prev := (*segments)[len(*segments)-1]
		(*segments)[len(*segments)-1] = domainNavigation.NewRouteSegment(
			prev.FromWaypoint,
			prev.ToWaypoint,
			prev.Distance,
			prev.FuelRequired,
			prev.TravelTime,
			prev.FlightMode,
			true,
		)
	}
}

func (p *RoutePlanner) logCreatedSegments(ctx context.Context, segments []*domainNavigation.RouteSegment, ship *domainNavigation.Ship) {
	logger := common.LoggerFromContext(ctx)
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
}
