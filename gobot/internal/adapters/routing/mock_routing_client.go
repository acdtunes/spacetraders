package routing

import (
	"context"
	"math"

	domainRouting "github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// MockRoutingClient provides simple routing for testing (no OR-Tools required)
type MockRoutingClient struct {
	// CannedTourPlan, if set, is returned verbatim by OptimizeTradeTour; TourErr, if
	// set, is returned instead (transport-failure simulation). Both zero → a benign
	// infeasible plan, so a default-constructed mock never fabricates a tour.
	CannedTourPlan *domainRouting.TourPlan
	TourErr        error
}

// NewMockRoutingClient creates a new mock routing client
func NewMockRoutingClient() *MockRoutingClient {
	return &MockRoutingClient{}
}

// PlanRoute returns a simple direct route
func (c *MockRoutingClient) PlanRoute(ctx context.Context, req *domainRouting.RouteRequest) (*domainRouting.RouteResponse, error) {

	var startWP, goalWP *system.WaypointData
	for _, wp := range req.Waypoints {
		if wp.Symbol == req.StartWaypoint {
			startWP = wp
		}
		if wp.Symbol == req.GoalWaypoint {
			goalWP = wp
		}
	}

	distance := calculateDistance(startWP.X, startWP.Y, goalWP.X, goalWP.Y)

	// Simple fuel calculation: 1 fuel per distance unit, rounded up
	fuelCost := int(math.Ceil(distance))

	timeSeconds := int(math.Ceil(distance / float64(req.EngineSpeed) * 60)) // Approximate: 60 seconds per speed unit

	steps := []*domainRouting.RouteStepData{}

	if fuelCost > req.CurrentFuel {
		fuelStation := findNearestFuelStation(startWP, req.Waypoints)
		if fuelStation != nil {
			distToFuel := calculateDistance(startWP.X, startWP.Y, fuelStation.X, fuelStation.Y)
			fuelToStation := int(math.Ceil(distToFuel))
			timeToStation := int(math.Ceil(distToFuel / float64(req.EngineSpeed) * 60))

			steps = append(steps, &domainRouting.RouteStepData{
				Action:      domainRouting.RouteActionTravel,
				Waypoint:    fuelStation.Symbol,
				FuelCost:    fuelToStation,
				TimeSeconds: timeToStation,
			})

			steps = append(steps, &domainRouting.RouteStepData{
				Action:      domainRouting.RouteActionRefuel,
				Waypoint:    fuelStation.Symbol,
				FuelCost:    0,
				TimeSeconds: 10, // Assume 10 seconds for refuel
			})

			distFromFuel := calculateDistance(fuelStation.X, fuelStation.Y, goalWP.X, goalWP.Y)
			fuelFromStation := int(math.Ceil(distFromFuel))
			timeFromStation := int(math.Ceil(distFromFuel / float64(req.EngineSpeed) * 60))

			steps = append(steps, &domainRouting.RouteStepData{
				Action:      domainRouting.RouteActionTravel,
				Waypoint:    goalWP.Symbol,
				FuelCost:    fuelFromStation,
				TimeSeconds: timeFromStation,
			})

			totalFuel := fuelToStation + fuelFromStation
			totalTime := timeToStation + 10 + timeFromStation

			return &domainRouting.RouteResponse{
				Steps:            steps,
				TotalFuelCost:    totalFuel,
				TotalTimeSeconds: totalTime,
				TotalDistance:    distToFuel + distFromFuel,
			}, nil
		}
	}

	steps = append(steps, &domainRouting.RouteStepData{
		Action:      domainRouting.RouteActionTravel,
		Waypoint:    goalWP.Symbol,
		FuelCost:    fuelCost,
		TimeSeconds: timeSeconds,
	})

	return &domainRouting.RouteResponse{
		Steps:            steps,
		TotalFuelCost:    fuelCost,
		TotalTimeSeconds: timeSeconds,
		TotalDistance:    distance,
	}, nil
}

// OptimizeTour returns waypoints in order (no optimization for POC)
func (c *MockRoutingClient) OptimizeTour(ctx context.Context, req *domainRouting.TourRequest) (*domainRouting.TourResponse, error) {
	return &domainRouting.TourResponse{
		VisitOrder:       req.Waypoints,
		CombinedRoute:    []*domainRouting.RouteStepData{},
		TotalTimeSeconds: 0,
	}, nil
}

// OptimizeFueledTour returns waypoints with basic fuel planning (no global optimization for POC)
func (c *MockRoutingClient) OptimizeFueledTour(ctx context.Context, req *domainRouting.FueledTourRequest) (*domainRouting.FueledTourResponse, error) {
	legs := make([]*domainRouting.TourLegData, len(req.TargetWaypoints))

	prevWaypoint := req.StartWaypoint
	for i, wp := range req.TargetWaypoints {
		legs[i] = &domainRouting.TourLegData{
			FromWaypoint: prevWaypoint,
			ToWaypoint:   wp,
			FlightMode:   "CRUISE",
			FuelCost:     10,
			TimeSeconds:  100,
			Distance:     10.0,
			RefuelBefore: false,
		}
		prevWaypoint = wp
	}

	return &domainRouting.FueledTourResponse{
		VisitOrder:       req.TargetWaypoints,
		Legs:             legs,
		TotalTimeSeconds: len(req.TargetWaypoints) * 100,
		TotalFuelCost:    len(req.TargetWaypoints) * 10,
		TotalDistance:    float64(len(req.TargetWaypoints)) * 10.0,
		RefuelStops:      0,
	}, nil
}

// PartitionFleet returns simple 1:1 ship-to-market assignment (no VRP optimization)
func (c *MockRoutingClient) PartitionFleet(ctx context.Context, req *domainRouting.VRPRequest) (*domainRouting.VRPResponse, error) {
	assignments := make(map[string]*domainRouting.ShipTourData)

	for i, ship := range req.ShipSymbols {
		if i < len(req.MarketWaypoints) {
			assignments[ship] = &domainRouting.ShipTourData{
				Waypoints: []string{req.MarketWaypoints[i]},
				Route:     []*domainRouting.RouteStepData{},
			}
		}
	}

	return &domainRouting.VRPResponse{
		Assignments: assignments,
	}, nil
}

// OptimizeTradeTour returns the configured canned plan (or error), or a benign
// infeasible plan when neither is set — the mock never fabricates a tour.
func (c *MockRoutingClient) OptimizeTradeTour(
	ctx context.Context,
	snapshot []domainRouting.TourGoodSnapshot,
	waypoints []domainRouting.TourWaypoint,
	ship domainRouting.TourShipState,
	cons domainRouting.TourConstraints,
	deposits []domainRouting.TourDepositCandidate,
	absorption []domainRouting.TourMarketAbsorption,
) (*domainRouting.TourPlan, error) {
	if c.TourErr != nil {
		return nil, c.TourErr
	}
	if c.CannedTourPlan != nil {
		return c.CannedTourPlan, nil
	}
	return &domainRouting.TourPlan{Feasible: false, InfeasibleReason: "mock: no canned tour plan configured"}, nil
}

// calculateDistance computes Euclidean distance between two points
func calculateDistance(x1, y1, x2, y2 float64) float64 {
	dx := x2 - x1
	dy := y2 - y1
	return math.Sqrt(dx*dx + dy*dy)
}

// findNearestFuelStation finds the closest waypoint with fuel
func findNearestFuelStation(from *system.WaypointData, waypoints []*system.WaypointData) *system.WaypointData {
	var nearest *system.WaypointData
	minDistance := math.MaxFloat64

	for _, wp := range waypoints {
		if wp.HasFuel && wp.Symbol != from.Symbol {
			dist := calculateDistance(from.X, from.Y, wp.X, wp.Y)
			if dist < minDistance {
				minDistance = dist
				nearest = wp
			}
		}
	}

	return nearest
}
