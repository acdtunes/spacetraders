package helpers

import (
	"context"
	"fmt"
	"sync"

	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// MockRoutingClient simulates routing service operations for testing
type MockRoutingClient struct {
	mu sync.RWMutex

	vrpEnabled       bool
	vrpResult        map[string][]string // ship -> markets assignment
	shouldReturnNoRoute bool             // For testing "no route found" scenarios
	customRouteResponse *routing.RouteResponse // Custom route response for testing
}

// NewMockRoutingClient creates a new mock routing client
func NewMockRoutingClient() *MockRoutingClient {
	return &MockRoutingClient{
		vrpEnabled: true, // By default, VRP is enabled
		vrpResult:  make(map[string][]string),
	}
}

// SetVRPResult configures a custom VRP result
func (m *MockRoutingClient) SetVRPResult(assignments map[string][]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.vrpResult = assignments
}

// SetShouldReturnNoRoute configures whether PlanRoute should return no route
func (m *MockRoutingClient) SetShouldReturnNoRoute(shouldReturnNoRoute bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.shouldReturnNoRoute = shouldReturnNoRoute
}

// SetCustomRouteResponse configures a custom route response
func (m *MockRoutingClient) SetCustomRouteResponse(response *routing.RouteResponse) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.customRouteResponse = response
}

// PlanRoute implements route planning with mock behavior
func (m *MockRoutingClient) PlanRoute(ctx context.Context, request *routing.RouteRequest) (*routing.RouteResponse, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// If custom response configured, return it
	if m.customRouteResponse != nil {
		return m.customRouteResponse, nil
	}

	// If configured to return no route, return nil
	if m.shouldReturnNoRoute {
		return nil, nil
	}

	// Default behavior: create a simple direct route
	// This is a simplified mock - real routing service would do pathfinding
	steps := []*routing.RouteStepData{
		{
			Action:      routing.RouteActionTravel,
			Waypoint:    request.GoalWaypoint,
			Mode:        "CRUISE",
			FuelCost:    10, // Simplified fuel cost
			TimeSeconds: 60, // Simplified time
		},
	}

	return &routing.RouteResponse{
		Steps:            steps,
		TotalFuelCost:    10,
		TotalTimeSeconds: 60,
		TotalDistance:    100.0,
	}, nil
}

// OptimizeTour implements tour optimization (not used in ScoutMarkets)
func (m *MockRoutingClient) OptimizeTour(ctx context.Context, request *routing.TourRequest) (*routing.TourResponse, error) {
	return nil, fmt.Errorf("OptimizeTour not implemented in mock")
}

// PartitionFleet implements VRP fleet partitioning
func (m *MockRoutingClient) PartitionFleet(ctx context.Context, request *routing.VRPRequest) (*routing.VRPResponse, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// If custom result configured, use it
	if len(m.vrpResult) > 0 {
		assignments := make(map[string]*routing.ShipTourData)
		for ship, markets := range m.vrpResult {
			assignments[ship] = &routing.ShipTourData{
				Waypoints: markets,
				Route:     []*routing.RouteStepData{},
			}
		}
		return &routing.VRPResponse{
			Assignments: assignments,
		}, nil
	}

	// Default behavior: distribute markets evenly across ships
	numShips := len(request.ShipSymbols)

	if numShips == 0 {
		return nil, fmt.Errorf("no ships provided")
	}

	assignments := make(map[string]*routing.ShipTourData)

	// Simple round-robin distribution
	for i, market := range request.MarketWaypoints {
		shipIndex := i % numShips
		shipSymbol := request.ShipSymbols[shipIndex]

		if _, exists := assignments[shipSymbol]; !exists {
			assignments[shipSymbol] = &routing.ShipTourData{
				Waypoints: []string{},
				Route:     []*routing.RouteStepData{},
			}
		}
		assignments[shipSymbol].Waypoints = append(assignments[shipSymbol].Waypoints, market)
	}

	return &routing.VRPResponse{
		Assignments: assignments,
	}, nil
}

// Reset clears all configured state
func (m *MockRoutingClient) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.vrpResult = make(map[string][]string)
	m.shouldReturnNoRoute = false
	m.customRouteResponse = nil
}
