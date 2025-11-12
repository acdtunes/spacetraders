package routing

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
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
func (c *GRPCRoutingClient) PlanRoute(ctx context.Context, req *common.RouteRequest) (*common.RouteResponse, error) {
	// Convert to protobuf request
	pbReq := &pb.PlanRouteRequest{
		SystemSymbol:   req.SystemSymbol,
		StartWaypoint:  req.StartWaypoint,
		GoalWaypoint:   req.GoalWaypoint,
		CurrentFuel:    int32(req.CurrentFuel),
		FuelCapacity:   int32(req.FuelCapacity),
		EngineSpeed:    int32(req.EngineSpeed),
		Waypoints:      convertWaypointsToPb(req.Waypoints),
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
	return &common.RouteResponse{
		Steps:            convertRouteStepsFromPb(pbResp.Steps),
		TotalFuelCost:    int(pbResp.TotalFuelCost),
		TotalTimeSeconds: int(pbResp.TotalTimeSeconds),
		TotalDistance:    pbResp.TotalDistance,
	}, nil
}

// OptimizeTour implements RoutingClient.OptimizeTour using gRPC
func (c *GRPCRoutingClient) OptimizeTour(ctx context.Context, req *common.TourRequest) (*common.TourResponse, error) {
	// Convert to protobuf request
	pbReq := &pb.OptimizeTourRequest{
		SystemSymbol:     req.SystemSymbol,
		StartWaypoint:    req.StartWaypoint,
		TargetWaypoints:  req.Waypoints,
		FuelCapacity:     int32(req.FuelCapacity),
		EngineSpeed:      int32(req.EngineSpeed),
		AllWaypoints:     convertWaypointsToPb(req.AllWaypoints),
		ReturnToStart:    true, // Default to true for tours
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
	return &common.TourResponse{
		VisitOrder:       pbResp.VisitOrder,
		CombinedRoute:    convertRouteStepsFromPb(pbResp.RouteSteps),
		TotalTimeSeconds: int(pbResp.TotalTimeSeconds),
	}, nil
}

// PartitionFleet implements RoutingClient.PartitionFleet using gRPC
func (c *GRPCRoutingClient) PartitionFleet(ctx context.Context, req *common.VRPRequest) (*common.VRPResponse, error) {
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
	assignments := make(map[string]*common.ShipTourData)
	for ship, tour := range pbResp.Assignments {
		assignments[ship] = &common.ShipTourData{
			Waypoints: tour.Waypoints,
			Route:     convertRouteStepsFromPb(tour.RouteSteps),
		}
	}

	return &common.VRPResponse{
		Assignments: assignments,
	}, nil
}

// Helper functions for conversion

func convertWaypointsToPb(waypoints []*common.WaypointData) []*pb.Waypoint {
	pbWaypoints := make([]*pb.Waypoint, len(waypoints))
	for i, wp := range waypoints {
		pbWaypoints[i] = &pb.Waypoint{
			Symbol:  wp.Symbol,
			X:       wp.X,
			Y:       wp.Y,
			HasFuel: wp.HasFuel,
		}
	}
	return pbWaypoints
}

func convertRouteStepsFromPb(pbSteps []*pb.RouteStep) []*common.RouteStepData {
	steps := make([]*common.RouteStepData, len(pbSteps))
	for i, pbStep := range pbSteps {
		action := common.RouteActionTravel
		if pbStep.Action == pb.RouteAction_ROUTE_ACTION_REFUEL {
			action = common.RouteActionRefuel
		}

		// Extract flight mode from protobuf (BURN-first logic is in routing engine)
		mode := "CRUISE" // Default fallback
		if pbStep.Mode != nil && *pbStep.Mode != "" {
			mode = *pbStep.Mode
		}

		steps[i] = &common.RouteStepData{
			Action:      action,
			Waypoint:    pbStep.Waypoint,
			FuelCost:    int(pbStep.FuelCost),
			TimeSeconds: int(pbStep.TimeSeconds),
			Mode:        mode,
		}
	}
	return steps
}
