package routing

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	domainRouting "github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/routing"
)

// GRPCRoutingClient implements RoutingClient using gRPC to communicate with Python OR-Tools service
type GRPCRoutingClient struct {
	conn   *grpc.ClientConn
	client pb.RoutingServiceClient
}

// NewGRPCRoutingClient creates a new gRPC routing client
func NewGRPCRoutingClient(address string) (*GRPCRoutingClient, error) {
	// Connect to routing service with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to routing service at %s: %w", address, err)
	}

	client := pb.NewRoutingServiceClient(conn)

	return &GRPCRoutingClient{
		conn:   conn,
		client: client,
	}, nil
}

// Close closes the gRPC connection
func (c *GRPCRoutingClient) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// PlanRoute implements RoutingClient.PlanRoute using gRPC
func (c *GRPCRoutingClient) PlanRoute(ctx context.Context, req *domainRouting.RouteRequest) (*domainRouting.RouteResponse, error) {
	// Convert to protobuf request
	pbReq := &pb.PlanRouteRequest{
		SystemSymbol:  req.SystemSymbol,
		StartWaypoint: req.StartWaypoint,
		GoalWaypoint:  req.GoalWaypoint,
		CurrentFuel:   int32(req.CurrentFuel),
		FuelCapacity:  int32(req.FuelCapacity),
		EngineSpeed:   int32(req.EngineSpeed),
		Waypoints:     convertWaypointsToPb(req.Waypoints),
		FuelEfficient: req.FuelEfficient,
		PreferCruise:  req.PreferCruise,
	}

	// Call gRPC service
	pbResp, err := c.client.PlanRoute(ctx, pbReq)
	if err != nil {
		return nil, fmt.Errorf("gRPC PlanRoute failed: %w", err)
	}

	if !pbResp.Success {
		errorMsg := "unknown error"
		if pbResp.ErrorMessage != nil {
			errorMsg = *pbResp.ErrorMessage
		}
		return nil, fmt.Errorf("routing failed: %s", errorMsg)
	}

	// Convert response
	return &domainRouting.RouteResponse{
		Steps:            convertRouteStepsFromPb(pbResp.Steps),
		TotalFuelCost:    int(pbResp.TotalFuelCost),
		TotalTimeSeconds: int(pbResp.TotalTimeSeconds),
		TotalDistance:    pbResp.TotalDistance,
	}, nil
}

// OptimizeTour implements RoutingClient.OptimizeTour using gRPC
// Tours always return to start by definition
func (c *GRPCRoutingClient) OptimizeTour(ctx context.Context, req *domainRouting.TourRequest) (*domainRouting.TourResponse, error) {
	// Convert to protobuf request
	pbReq := &pb.OptimizeTourRequest{
		SystemSymbol:    req.SystemSymbol,
		StartWaypoint:   req.StartWaypoint,
		TargetWaypoints: req.Waypoints,
		FuelCapacity:    int32(req.FuelCapacity),
		EngineSpeed:     int32(req.EngineSpeed),
		AllWaypoints:    convertWaypointsToPb(req.AllWaypoints),
	}

	// Call gRPC service
	pbResp, err := c.client.OptimizeTour(ctx, pbReq)
	if err != nil {
		return nil, fmt.Errorf("gRPC OptimizeTour failed: %w", err)
	}

	if !pbResp.Success {
		errorMsg := "unknown error"
		if pbResp.ErrorMessage != nil {
			errorMsg = *pbResp.ErrorMessage
		}
		return nil, fmt.Errorf("tour optimization failed: %s", errorMsg)
	}

	// Convert response
	return &domainRouting.TourResponse{
		VisitOrder:       pbResp.VisitOrder,
		CombinedRoute:    convertRouteStepsFromPb(pbResp.RouteSteps),
		TotalTimeSeconds: int(pbResp.TotalTimeSeconds),
	}, nil
}

// OptimizeFueledTour implements RoutingClient.OptimizeFueledTour using gRPC
// This endpoint globally optimizes visit order + flight modes + refuel stops
func (c *GRPCRoutingClient) OptimizeFueledTour(ctx context.Context, req *domainRouting.FueledTourRequest) (*domainRouting.FueledTourResponse, error) {
	// Convert to protobuf request
	pbReq := &pb.OptimizeFueledTourRequest{
		SystemSymbol:    req.SystemSymbol,
		StartWaypoint:   req.StartWaypoint,
		TargetWaypoints: req.TargetWaypoints,
		CurrentFuel:     int32(req.CurrentFuel),
		FuelCapacity:    int32(req.FuelCapacity),
		EngineSpeed:     int32(req.EngineSpeed),
		AllWaypoints:    convertWaypointsToPb(req.AllWaypoints),
	}

	// Set optional return waypoint
	if req.ReturnWaypoint != "" {
		pbReq.ReturnWaypoint = &req.ReturnWaypoint
	}

	// Call gRPC service
	pbResp, err := c.client.OptimizeFueledTour(ctx, pbReq)
	if err != nil {
		return nil, fmt.Errorf("gRPC OptimizeFueledTour failed: %w", err)
	}

	if !pbResp.Success {
		errorMsg := "unknown error"
		if pbResp.ErrorMessage != nil {
			errorMsg = *pbResp.ErrorMessage
		}
		return nil, fmt.Errorf("fueled tour optimization failed: %s", errorMsg)
	}

	// Convert legs from protobuf
	legs := make([]*domainRouting.TourLegData, len(pbResp.Legs))
	for i, pbLeg := range pbResp.Legs {
		// Convert intermediate stops
		stops := make([]*domainRouting.IntermediateStopData, len(pbLeg.IntermediateStops))
		for j, pbStop := range pbLeg.IntermediateStops {
			stops[j] = &domainRouting.IntermediateStopData{
				Waypoint:     pbStop.Waypoint,
				FlightMode:   pbStop.FlightMode,
				FuelCost:     int(pbStop.FuelCost),
				TimeSeconds:  int(pbStop.TimeSeconds),
				RefuelAmount: int(pbStop.RefuelAmount),
			}
		}

		refuelAmount := 0
		if pbLeg.RefuelAmount != nil {
			refuelAmount = int(*pbLeg.RefuelAmount)
		}

		legs[i] = &domainRouting.TourLegData{
			FromWaypoint:      pbLeg.FromWaypoint,
			ToWaypoint:        pbLeg.ToWaypoint,
			FlightMode:        pbLeg.FlightMode,
			FuelCost:          int(pbLeg.FuelCost),
			TimeSeconds:       int(pbLeg.TimeSeconds),
			Distance:          pbLeg.Distance,
			RefuelBefore:      pbLeg.RefuelBefore,
			RefuelAmount:      refuelAmount,
			IntermediateStops: stops,
		}
	}

	return &domainRouting.FueledTourResponse{
		VisitOrder:       pbResp.VisitOrder,
		Legs:             legs,
		TotalTimeSeconds: int(pbResp.TotalTimeSeconds),
		TotalFuelCost:    int(pbResp.TotalFuelCost),
		TotalDistance:    pbResp.TotalDistance,
		RefuelStops:      int(pbResp.RefuelStops),
	}, nil
}

// PartitionFleet implements RoutingClient.PartitionFleet using gRPC
func (c *GRPCRoutingClient) PartitionFleet(ctx context.Context, req *domainRouting.VRPRequest) (*domainRouting.VRPResponse, error) {
	// Convert ship configs to protobuf
	pbShipConfigs := make(map[string]*pb.ShipConfig)
	for ship, config := range req.ShipConfigs {
		pbShipConfigs[ship] = &pb.ShipConfig{
			CurrentLocation: config.CurrentLocation,
			FuelCapacity:    int32(config.FuelCapacity),
			EngineSpeed:     int32(config.EngineSpeed),
			CurrentFuel:     int32(config.FuelCapacity), // Assume full fuel
		}
	}

	// Convert to protobuf request
	pbReq := &pb.PartitionFleetRequest{
		SystemSymbol:    req.SystemSymbol,
		ShipSymbols:     req.ShipSymbols,
		MarketWaypoints: req.MarketWaypoints,
		ShipConfigs:     pbShipConfigs,
		AllWaypoints:    convertWaypointsToPb(req.AllWaypoints),
		Iterations:      1, // Default to single iteration
	}

	// Call gRPC service
	pbResp, err := c.client.PartitionFleet(ctx, pbReq)
	if err != nil {
		return nil, fmt.Errorf("gRPC PartitionFleet failed: %w", err)
	}

	if !pbResp.Success {
		errorMsg := "unknown error"
		if pbResp.ErrorMessage != nil {
			errorMsg = *pbResp.ErrorMessage
		}
		return nil, fmt.Errorf("fleet partitioning failed: %s", errorMsg)
	}

	// Convert assignments
	assignments := make(map[string]*domainRouting.ShipTourData)
	for ship, tour := range pbResp.Assignments {
		assignments[ship] = &domainRouting.ShipTourData{
			Waypoints: tour.Waypoints,
			Route:     convertRouteStepsFromPb(tour.RouteSteps),
		}
	}

	return &domainRouting.VRPResponse{
		Assignments: assignments,
	}, nil
}

// Helper functions for conversion

func convertWaypointsToPb(waypoints []*system.WaypointData) []*pb.Waypoint {
	pbWaypoints := make([]*pb.Waypoint, len(waypoints))
	for i, wp := range waypoints {
		// Debug: Log has_fuel for key waypoints
		if wp.Symbol == "X1-AU21-H51" || wp.Symbol == "X1-AU21-I56" || wp.Symbol == "X1-AU21-J58" {
			fmt.Printf("[DEBUG] convertWaypointsToPb: %s HasFuel=%v\n", wp.Symbol, wp.HasFuel)
		}
		pbWaypoints[i] = &pb.Waypoint{
			Symbol:  wp.Symbol,
			X:       wp.X,
			Y:       wp.Y,
			HasFuel: wp.HasFuel,
		}
	}
	return pbWaypoints
}

func convertRouteStepsFromPb(pbSteps []*pb.RouteStep) []*domainRouting.RouteStepData {
	steps := make([]*domainRouting.RouteStepData, len(pbSteps))
	for i, pbStep := range pbSteps {
		action := domainRouting.RouteActionTravel
		if pbStep.Action == pb.RouteAction_ROUTE_ACTION_REFUEL {
			action = domainRouting.RouteActionRefuel
		}

		// Extract flight mode from protobuf (BURN-first logic is in routing engine)
		mode := "CRUISE" // Default fallback
		if pbStep.Mode != nil && *pbStep.Mode != "" {
			mode = *pbStep.Mode
		}

		steps[i] = &domainRouting.RouteStepData{
			Action:      action,
			Waypoint:    pbStep.Waypoint,
			FuelCost:    int(pbStep.FuelCost),
			TimeSeconds: int(pbStep.TimeSeconds),
			Mode:        mode,
		}
	}
	return steps
}
