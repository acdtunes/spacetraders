package routing

import (
	"context"
	"math"

	domainRouting "github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// MockRoutingClient provides simple routing for testing (no OR-Tools required)
// This is a POC implementation that returns basic routes without sophisticated optimization
type MockRoutingClient struct{}

// NewMockRoutingClient creates a new mock routing client
func NewMockRoutingClient() *MockRoutingClient {
	return &MockRoutingClient{}
}

// PlanRoute returns a simple direct route
func (c *MockRoutingClient) PlanRoute(ctx context.Context, req *domainRouting.RouteRequest) (*domainRouting.RouteResponse, error) {
	// Simple implementation for POC:
	// 1. Find start and goal waypoints in the waypoint list
	// 2. Calculate distance
	// 3. Calculate fuel cost and time
	// 4. Return a single-step route if we have enough fuel
	// 5. If not enough fuel, add refuel stop at nearest fuel station

	var startWP, goalWP *system.WaypointData
	for _, wp := range req.Waypoints {
		if wp.Symbol == req.StartWaypoint {
			startWP = wp
		}
		if wp.Symbol == req.GoalWaypoint {
			goalWP = wp
		}
	}

	// Calculate distance between start and goal
	distance := calculateDistance(startWP.X, startWP.Y, goalWP.X, goalWP.Y)

	// Simple fuel calculation: 1 fuel per distance unit, rounded up
	fuelCost := int(math.Ceil(distance))

	// Time calculation: distance / speed (in seconds)
	timeSeconds := int(math.Ceil(distance / float64(req.EngineSpeed) * 60)) // Approximate: 60 seconds per speed unit

	// Check if we need to refuel
	steps := []*domainRouting.RouteStepData{}

	if fuelCost > req.CurrentFuel {
		// Need to refuel - find nearest fuel station
		fuelStation := findNearestFuelStation(startWP, req.Waypoints)
		if fuelStation != nil {
			// Add travel to fuel station
			distToFuel := calculateDistance(startWP.X, startWP.Y, fuelStation.X, fuelStation.Y)
			fuelToStation := int(math.Ceil(distToFuel))
			timeToStation := int(math.Ceil(distToFuel / float64(req.EngineSpeed) * 60))

			steps = append(steps, &domainRouting.RouteStepData{
				Action:      domainRouting.RouteActionTravel,
				Waypoint:    fuelStation.Symbol,
				FuelCost:    fuelToStation,
				TimeSeconds: timeToStation,
			})

			// Add refuel action
			steps = append(steps, &domainRouting.RouteStepData{
				Action:      domainRouting.RouteActionRefuel,
				Waypoint:    fuelStation.Symbol,
				FuelCost:    0,
				TimeSeconds: 10, // Assume 10 seconds for refuel
			})

			// Add travel from fuel station to goal
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

	// Direct route - we have enough fuel
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
	// Simple implementation: return waypoints in the order they were provided
	return &domainRouting.TourResponse{
		VisitOrder:       req.Waypoints,
		CombinedRoute:    []*domainRouting.RouteStepData{},
		TotalTimeSeconds: 0,
	}, nil
}

// PartitionFleet returns simple 1:1 ship-to-market assignment (no VRP optimization)
func (c *MockRoutingClient) PartitionFleet(ctx context.Context, req *domainRouting.VRPRequest) (*domainRouting.VRPResponse, error) {
	// Simple implementation: assign first N markets to first N ships
	assignments := make(map[string]*domainRouting.ShipTourData)

	for i, ship := range req.ShipSymbols {
		if i < len(req.MarketWaypoints) {
			// Assign one market per ship
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

// Helper functions

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
