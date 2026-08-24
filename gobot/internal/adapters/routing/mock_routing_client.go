package routing

import (
	"context"

	domainRouting "github.com/andrescamacho/spacetraders-go/internal/domain/routing"
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
