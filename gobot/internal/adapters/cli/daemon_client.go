package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// DaemonClient provides a client interface to communicate with the daemon via gRPC
type DaemonClient struct {
	conn       *grpc.ClientConn
	client     pb.DaemonServiceClient
	socketPath string
}

// Response types (mirrors protobuf messages)

type NavigateResponse struct {
	ContainerID   string
	ShipSymbol    string
	Destination   string
	Status        string
	EstimatedTime int32
}

type DockResponse struct {
	ContainerID string
	ShipSymbol  string
	Status      string
}

type OrbitResponse struct {
	ContainerID string
	ShipSymbol  string
	Status      string
}

type RefuelResponse struct {
	ContainerID string
	ShipSymbol  string
	FuelAdded   int32
	CreditsCost int32
	Status      string
}

type JumpResponse struct {
	Success           bool
	NavigatedToGate   bool
	JumpGateSymbol    string
	DestinationSystem string
	CooldownSeconds   int32
	Message           string
	Error             string
}

type BatchContractWorkflowResponse struct {
	ContainerID string
	ShipSymbol  string
	Iterations  int
	Status      string
}

type ScoutTourResponse struct {
	ContainerID string
	ShipSymbol  string
	Markets     []string
	Iterations  int
	Status      string
}

type ScoutMarketsResponse struct {
	ContainerIDs     []string
	Assignments      map[string]*MarketAssignment
	ReusedContainers []string
}

type MarketAssignment struct {
	Markets []string
}

type MiningOperationResponse struct {
	ContainerID    string
	AsteroidField  string
	MinerShips     []string
	TransportShips []string
	Status         string
	// Dry-run results
	MarketSymbol string
	ShipRoutes   []common.ShipRouteDTO
	Errors       []string
}

type TourSellResponse struct {
	ContainerID string
	ShipSymbol  string
	Status      string
}

type ContractFleetCoordinatorResponse struct {
	ContainerID string
	ShipSymbols []string
	Status      string
}

// ContainerInfo mirrors the protobuf ContainerInfo message for CLI display.
// This struct includes all fields needed for user-facing container information.
// Note: PlayerID is int32 per protobuf requirements (converted from domain int).
type ContainerInfo struct {
	ContainerID      string
	ContainerType    string
	Status           string
	PlayerID         int32 // Protobuf int32 (convert from domain int)
	CreatedAt        string
	UpdatedAt        string
	CurrentIteration int32
	MaxIterations    int32
	RestartCount     int32
	Metadata         string
}

type StopContainerResponse struct {
	ContainerID string
	Status      string
	Message     string
}

type LogEntry struct {
	Timestamp string
	Level     string
	Message   string
	Metadata  string
}

type HealthResponse struct {
	Status           string
	Version          string
	ActiveContainers int32
}

// NewDaemonClient creates a new daemon client
func NewDaemonClient(socketPath string) (*DaemonClient, error) {
	// Create context with timeout for connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Connect to Unix socket via gRPC
	// Use "unix:" scheme for Unix domain sockets
	conn, err := grpc.DialContext(
		ctx,
		"unix:"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to daemon socket: %w", err)
	}

	// Create gRPC client
	client := pb.NewDaemonServiceClient(conn)

	return &DaemonClient{
		conn:       conn,
		client:     client,
		socketPath: socketPath,
	}, nil
}

// Close closes the client connection
func (c *DaemonClient) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// NavigateShip initiates ship navigation
func (c *DaemonClient) NavigateShip(
	ctx context.Context,
	shipSymbol, destination string,
	playerID int,
	agentSymbol string,
) (*NavigateResponse, error) {
	// Build request
	req := &pb.NavigateShipRequest{
		ShipSymbol:  shipSymbol,
		Destination: destination,
		PlayerId:    int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}

	// Call gRPC service
	resp, err := c.client.NavigateShip(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("gRPC call failed: %w", err)
	}

	// Convert to client response type
	return &NavigateResponse{
		ContainerID:   resp.ContainerId,
		ShipSymbol:    resp.ShipSymbol,
		Destination:   resp.Destination,
		Status:        resp.Status,
		EstimatedTime: resp.EstimatedTimeSeconds,
	}, nil
}

// DockShip initiates ship docking
func (c *DaemonClient) DockShip(
	ctx context.Context,
	shipSymbol string,
	playerID int,
	agentSymbol string,
) (*DockResponse, error) {
	req := &pb.DockShipRequest{
		ShipSymbol: shipSymbol,
		PlayerId:   int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}

	resp, err := c.client.DockShip(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("gRPC call failed: %w", err)
	}

	return &DockResponse{
		ContainerID: resp.ContainerId,
		ShipSymbol:  resp.ShipSymbol,
		Status:      resp.Status,
	}, nil
}

// OrbitShip initiates ship orbit
func (c *DaemonClient) OrbitShip(
	ctx context.Context,
	shipSymbol string,
	playerID int,
	agentSymbol string,
) (*OrbitResponse, error) {
	req := &pb.OrbitShipRequest{
		ShipSymbol: shipSymbol,
		PlayerId:   int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}

	resp, err := c.client.OrbitShip(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("gRPC call failed: %w", err)
	}

	return &OrbitResponse{
		ContainerID: resp.ContainerId,
		ShipSymbol:  resp.ShipSymbol,
		Status:      resp.Status,
	}, nil
}

// RefuelShip initiates ship refuel
func (c *DaemonClient) RefuelShip(
	ctx context.Context,
	shipSymbol string,
	playerID int,
	agentSymbol string,
	units *int,
) (*RefuelResponse, error) {
	req := &pb.RefuelShipRequest{
		ShipSymbol: shipSymbol,
		PlayerId:   int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}
	if units != nil {
		u := int32(*units)
		req.Units = &u
	}

	resp, err := c.client.RefuelShip(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("gRPC call failed: %w", err)
	}

	return &RefuelResponse{
		ContainerID: resp.ContainerId,
		ShipSymbol:  resp.ShipSymbol,
		FuelAdded:   resp.FuelAdded,
		CreditsCost: resp.CreditsCost,
		Status:      resp.Status,
	}, nil
}

// JumpShip executes a jump to a different star system via jump gate
func (c *DaemonClient) JumpShip(
	ctx context.Context,
	shipSymbol string,
	destinationSystem string,
	playerID int,
	agentSymbol string,
) (*JumpResponse, error) {
	req := &pb.JumpShipRequest{
		ShipSymbol:        shipSymbol,
		DestinationSystem: destinationSystem,
		PlayerId:          int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}

	resp, err := c.client.JumpShip(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("gRPC call failed: %w", err)
	}

	return &JumpResponse{
		Success:           resp.Success,
		NavigatedToGate:   resp.NavigatedToGate,
		JumpGateSymbol:    resp.JumpGateSymbol,
		DestinationSystem: resp.DestinationSystem,
		CooldownSeconds:   resp.CooldownSeconds,
		Message:           resp.Message,
		Error:             resp.Error,
	}, nil
}

// BatchContractWorkflow initiates batch contract workflow
func (c *DaemonClient) BatchContractWorkflow(
	ctx context.Context,
	shipSymbol string,
	iterations int,
	playerID int,
	agentSymbol string,
) (*BatchContractWorkflowResponse, error) {
	req := &pb.BatchContractWorkflowRequest{
		ShipSymbol: shipSymbol,
		Iterations: int32(iterations),
		PlayerId:   int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}

	resp, err := c.client.BatchContractWorkflow(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("gRPC call failed: %w", err)
	}

	return &BatchContractWorkflowResponse{
		ContainerID: resp.ContainerId,
		ShipSymbol:  resp.ShipSymbol,
		Iterations:  int(resp.Iterations),
		Status:      resp.Status,
	}, nil
}

// ScoutTour initiates market scouting tour (single ship)
func (c *DaemonClient) ScoutTour(
	ctx context.Context,
	shipSymbol string,
	markets []string,
	iterations int,
	playerID int,
	agentSymbol string,
) (*ScoutTourResponse, error) {
	req := &pb.ScoutTourRequest{
		ShipSymbol: shipSymbol,
		Markets:    markets,
		Iterations: int32(iterations),
		PlayerId:   int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}

	resp, err := c.client.ScoutTour(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("gRPC call failed: %w", err)
	}

	return &ScoutTourResponse{
		ContainerID: resp.ContainerId,
		ShipSymbol:  resp.ShipSymbol,
		Markets:     resp.Markets,
		Iterations:  int(resp.Iterations),
		Status:      resp.Status,
	}, nil
}

// ScoutMarkets initiates fleet market scouting with VRP optimization (multi-ship)
func (c *DaemonClient) ScoutMarkets(
	ctx context.Context,
	shipSymbols []string,
	systemSymbol string,
	markets []string,
	iterations int,
	playerID int,
	agentSymbol string,
) (*ScoutMarketsResponse, error) {
	req := &pb.ScoutMarketsRequest{
		ShipSymbols:  shipSymbols,
		SystemSymbol: systemSymbol,
		Markets:      markets,
		Iterations:   int32(iterations),
		PlayerId:     int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}

	resp, err := c.client.ScoutMarkets(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("gRPC call failed: %w", err)
	}

	// Convert protobuf response to client response type
	assignments := make(map[string]*MarketAssignment)
	for ship, pbAssignment := range resp.Assignments {
		assignments[ship] = &MarketAssignment{
			Markets: pbAssignment.Markets,
		}
	}

	return &ScoutMarketsResponse{
		ContainerIDs:     resp.ContainerIds,
		Assignments:      assignments,
		ReusedContainers: resp.ReusedContainers,
	}, nil
}

// AssignScoutingFleetResponse contains the fleet-assignment container ID
type AssignScoutingFleetResponse struct {
	ContainerID string
}

// AssignScoutingFleet creates a fleet-assignment container for async VRP optimization
func (c *DaemonClient) AssignScoutingFleet(
	ctx context.Context,
	systemSymbol string,
	playerID int,
	agentSymbol string,
) (*AssignScoutingFleetResponse, error) {
	req := &pb.AssignScoutingFleetRequest{
		SystemSymbol: systemSymbol,
		PlayerId:     int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}

	resp, err := c.client.AssignScoutingFleet(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("gRPC call failed: %w", err)
	}

	return &AssignScoutingFleetResponse{
		ContainerID: resp.ContainerId,
	}, nil
}

// ListContainers lists all containers
func (c *DaemonClient) ListContainers(
	ctx context.Context,
	playerID *int,
	status *string,
) ([]*ContainerInfo, error) {
	req := &pb.ListContainersRequest{}
	if playerID != nil {
		p := int32(*playerID)
		req.PlayerId = &p
	}
	if status != nil {
		req.Status = status
	}

	resp, err := c.client.ListContainers(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("gRPC call failed: %w", err)
	}

	// Convert to client response type
	containers := make([]*ContainerInfo, 0, len(resp.Containers))
	for _, pbCont := range resp.Containers {
		containers = append(containers, &ContainerInfo{
			ContainerID:      pbCont.ContainerId,
			ContainerType:    pbCont.ContainerType,
			Status:           pbCont.Status,
			PlayerID:         pbCont.PlayerId,
			CreatedAt:        pbCont.CreatedAt,
			UpdatedAt:        pbCont.UpdatedAt,
			CurrentIteration: pbCont.CurrentIteration,
			MaxIterations:    pbCont.MaxIterations,
			RestartCount:     pbCont.RestartCount,
		})
	}

	return containers, nil
}

// GetContainer retrieves container details
func (c *DaemonClient) GetContainer(
	ctx context.Context,
	containerID string,
) (*ContainerInfo, error) {
	req := &pb.GetContainerRequest{
		ContainerId: containerID,
	}

	resp, err := c.client.GetContainer(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("gRPC call failed: %w", err)
	}

	pbCont := resp.Container
	return &ContainerInfo{
		ContainerID:      pbCont.ContainerId,
		ContainerType:    pbCont.ContainerType,
		Status:           pbCont.Status,
		PlayerID:         pbCont.PlayerId,
		CreatedAt:        pbCont.CreatedAt,
		UpdatedAt:        pbCont.UpdatedAt,
		CurrentIteration: pbCont.CurrentIteration,
		MaxIterations:    pbCont.MaxIterations,
		RestartCount:     pbCont.RestartCount,
		Metadata:         resp.Metadata,
	}, nil
}

// StopContainer stops a container
func (c *DaemonClient) StopContainer(
	ctx context.Context,
	containerID string,
) (*StopContainerResponse, error) {
	req := &pb.StopContainerRequest{
		ContainerId: containerID,
	}

	resp, err := c.client.StopContainer(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("gRPC call failed: %w", err)
	}

	return &StopContainerResponse{
		ContainerID: resp.ContainerId,
		Status:      resp.Status,
		Message:     resp.Message,
	}, nil
}

// GetContainerLogs retrieves container logs
func (c *DaemonClient) GetContainerLogs(
	ctx context.Context,
	containerID string,
	limit *int,
	level *string,
) ([]*LogEntry, error) {
	req := &pb.GetContainerLogsRequest{
		ContainerId: containerID,
	}
	if limit != nil {
		l := int32(*limit)
		req.Limit = &l
	}
	if level != nil {
		req.Level = level
	}

	resp, err := c.client.GetContainerLogs(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("gRPC call failed: %w", err)
	}

	// Convert to client response type
	logs := make([]*LogEntry, 0, len(resp.Logs))
	for _, pbLog := range resp.Logs {
		logs = append(logs, &LogEntry{
			Timestamp: pbLog.Timestamp,
			Level:     pbLog.Level,
			Message:   pbLog.Message,
			Metadata:  pbLog.Metadata,
		})
	}

	return logs, nil
}

// HealthCheck verifies daemon health
func (c *DaemonClient) HealthCheck(ctx context.Context) (*HealthResponse, error) {
	req := &pb.HealthCheckRequest{}

	resp, err := c.client.HealthCheck(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("gRPC call failed: %w", err)
	}

	return &HealthResponse{
		Status:           resp.Status,
		Version:          resp.Version,
		ActiveContainers: resp.ActiveContainers,
	}, nil
}

// ListShips lists all ships for a player
func (c *DaemonClient) ListShips(ctx context.Context, playerID *int32, agentSymbol *string) (*pb.ListShipsResponse, error) {
	req := &pb.ListShipsRequest{
		PlayerId:    playerID,
		AgentSymbol: agentSymbol,
	}

	resp, err := c.client.ListShips(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("gRPC call failed: %w", err)
	}

	return resp, nil
}

// GetShip gets detailed ship information
func (c *DaemonClient) GetShip(ctx context.Context, shipSymbol string, playerID *int32, agentSymbol *string) (*pb.GetShipResponse, error) {
	req := &pb.GetShipRequest{
		ShipSymbol:  shipSymbol,
		PlayerId:    playerID,
		AgentSymbol: agentSymbol,
	}

	resp, err := c.client.GetShip(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("gRPC call failed: %w", err)
	}

	return resp, nil
}

// GetShipyardListings gets shipyard listings at a waypoint
func (c *DaemonClient) GetShipyardListings(ctx context.Context, systemSymbol, waypointSymbol string, playerID int) (*pb.GetShipyardListingsResponse, error) {
	req := &pb.GetShipyardListingsRequest{
		SystemSymbol:   systemSymbol,
		WaypointSymbol: waypointSymbol,
		PlayerId:       int32(playerID),
	}

	resp, err := c.client.GetShipyardListings(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("gRPC call failed: %w", err)
	}

	return resp, nil
}

// PurchaseShip purchases a ship from a shipyard
func (c *DaemonClient) PurchaseShip(ctx context.Context, purchasingShipSymbol, shipType string, playerID int, agentSymbol, shipyardWaypoint string) (*pb.PurchaseShipResponse, error) {
	req := &pb.PurchaseShipRequest{
		PurchasingShipSymbol: purchasingShipSymbol,
		ShipType:             shipType,
		PlayerId:             int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}
	if shipyardWaypoint != "" {
		req.ShipyardWaypoint = &shipyardWaypoint
	}

	resp, err := c.client.PurchaseShip(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("gRPC call failed: %w", err)
	}

	return resp, nil
}

// BatchPurchaseShips purchases multiple ships in batch
func (c *DaemonClient) BatchPurchaseShips(ctx context.Context, purchasingShipSymbol, shipType string, quantity, maxBudget, playerID int, agentSymbol, shipyardWaypoint string) (*pb.BatchPurchaseShipsResponse, error) {
	req := &pb.BatchPurchaseShipsRequest{
		PurchasingShipSymbol: purchasingShipSymbol,
		ShipType:             shipType,
		Quantity:             int32(quantity),
		MaxBudget:            int32(maxBudget),
		PlayerId:             int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}
	if shipyardWaypoint != "" {
		req.ShipyardWaypoint = &shipyardWaypoint
	}

	resp, err := c.client.BatchPurchaseShips(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("gRPC call failed: %w", err)
	}

	return resp, nil
}

// ContractFleetCoordinator starts a contract fleet coordinator
// Uses all available idle light hauler ships (no pre-assignment needed)
func (c *DaemonClient) ContractFleetCoordinator(
	ctx context.Context,
	shipSymbols []string, // Deprecated: kept for backward compatibility, ignored by server
	playerID int,
	agentSymbol string,
) (*ContractFleetCoordinatorResponse, error) {
	req := &pb.ContractFleetCoordinatorRequest{
		PlayerId: int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}

	resp, err := c.client.ContractFleetCoordinator(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("gRPC call failed: %w", err)
	}

	return &ContractFleetCoordinatorResponse{
		ContainerID: resp.ContainerId,
		ShipSymbols: shipSymbols,
		Status:      resp.Status,
	}, nil
}

// MiningOperation starts a mining operation with Transport-as-Sink pattern
func (c *DaemonClient) MiningOperation(
	ctx context.Context,
	asteroidField string,
	minerShips []string,
	transportShips []string,
	topNOres int,
	miningType string,
	force bool,
	dryRun bool,
	maxLegTime int,
	playerID int,
	agentSymbol string,
) (*MiningOperationResponse, error) {
	req := &pb.MiningOperationRequest{
		MinerShips:     minerShips,
		TransportShips: transportShips,
		TopNOres:       int32(topNOres),
		PlayerId:       int32(playerID),
		Force:          force,
		DryRun:         dryRun,
		MaxLegTime:     int32(maxLegTime),
	}
	if asteroidField != "" {
		req.AsteroidField = &asteroidField
	}
	if miningType != "" {
		req.MiningType = &miningType
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}

	resp, err := c.client.MiningOperation(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("gRPC call failed: %w", err)
	}

	result := &MiningOperationResponse{
		ContainerID:    resp.ContainerId,
		AsteroidField:  resp.AsteroidField,
		MinerShips:     resp.MinerShips,
		TransportShips: resp.TransportShips,
		Status:         resp.Status,
		MarketSymbol:   resp.MarketSymbol,
		Errors:         resp.Errors,
	}

	// Convert ship routes for dry-run
	if len(resp.ShipRoutes) > 0 {
		result.ShipRoutes = make([]common.ShipRouteDTO, len(resp.ShipRoutes))
		for i, route := range resp.ShipRoutes {
			segments := make([]common.RouteSegmentDTO, len(route.Segments))
			for j, seg := range route.Segments {
				segments[j] = common.RouteSegmentDTO{
					From:       seg.From,
					To:         seg.To,
					FlightMode: seg.FlightMode,
					FuelCost:   int(seg.FuelCost),
					TravelTime: int(seg.TravelTime),
				}
			}
			result.ShipRoutes[i] = common.ShipRouteDTO{
				ShipSymbol: route.ShipSymbol,
				ShipType:   route.ShipType,
				Segments:   segments,
				TotalFuel:  int(route.TotalFuel),
				TotalTime:  int(route.TotalTime),
			}
		}
	}

	return result, nil
}

// TourSell executes optimized cargo selling tour for a ship
func (c *DaemonClient) TourSell(
	ctx context.Context,
	shipSymbol string,
	returnWaypoint string,
	playerID int,
	agentSymbol string,
) (*TourSellResponse, error) {
	req := &pb.TourSellRequest{
		ShipSymbol: shipSymbol,
		PlayerId:   int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}
	if returnWaypoint != "" {
		req.ReturnWaypoint = &returnWaypoint
	}

	resp, err := c.client.TourSell(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("gRPC call failed: %w", err)
	}

	return &TourSellResponse{
		ContainerID: resp.ContainerId,
		ShipSymbol:  resp.ShipSymbol,
		Status:      resp.Status,
	}, nil
}

// StartGoodsFactory starts a goods factory for automated production
func (c *DaemonClient) StartGoodsFactory(
	ctx context.Context,
	targetGood string,
	systemSymbol *string,
	playerID int,
	agentSymbol *string,
	maxIterations *int32,
) (*StartGoodsFactoryResult, error) {
	resp, err := c.client.StartGoodsFactory(ctx, &pb.StartGoodsFactoryRequest{
		PlayerId:      int32(playerID),
		TargetGood:    targetGood,
		SystemSymbol:  systemSymbol,
		AgentSymbol:   agentSymbol,
		MaxIterations: maxIterations,
	})
	if err != nil {
		return nil, err
	}

	return &StartGoodsFactoryResult{
		FactoryID:  resp.FactoryId,
		TargetGood: resp.TargetGood,
		Status:     resp.Status,
		Message:    resp.Message,
		NodesTotal: int(resp.NodesTotal),
	}, nil
}

// StopGoodsFactory stops a running goods factory
func (c *DaemonClient) StopGoodsFactory(
	ctx context.Context,
	factoryID string,
	playerID int,
) (*StopGoodsFactoryResult, error) {
	resp, err := c.client.StopGoodsFactory(ctx, &pb.StopGoodsFactoryRequest{
		PlayerId:  int32(playerID),
		FactoryId: factoryID,
	})
	if err != nil {
		return nil, err
	}

	return &StopGoodsFactoryResult{
		FactoryID: resp.FactoryId,
		Status:    resp.Status,
		Message:   resp.Message,
	}, nil
}

// GetFactoryStatus retrieves the status of a goods factory
func (c *DaemonClient) GetFactoryStatus(
	ctx context.Context,
	factoryID string,
	playerID int,
) (*GoodsFactoryStatusResult, error) {
	resp, err := c.client.GetFactoryStatus(ctx, &pb.GetFactoryStatusRequest{
		PlayerId:  int32(playerID),
		FactoryId: factoryID,
	})
	if err != nil {
		return nil, err
	}

	return &GoodsFactoryStatusResult{
		FactoryID:        resp.FactoryId,
		TargetGood:       resp.TargetGood,
		Status:           resp.Status,
		DependencyTree:   resp.DependencyTree,
		QuantityAcquired: int(resp.QuantityAcquired),
		TotalCost:        int(resp.TotalCost),
		NodesCompleted:   int(resp.NodesCompleted),
		NodesTotal:       int(resp.NodesTotal),
		SystemSymbol:     resp.SystemSymbol,
		ShipsUsed:        int(resp.ShipsUsed),
		MarketQueries:    int(resp.MarketQueries),
		ParallelLevels:   int(resp.ParallelLevels),
		EstimatedSpeedup: float64(resp.EstimatedSpeedup),
	}, nil
}

// StartGoodsFactoryResult contains the result of starting a goods factory
type StartGoodsFactoryResult struct {
	FactoryID  string
	TargetGood string
	Status     string
	Message    string
	NodesTotal int
}

// StopGoodsFactoryResult contains the result of stopping a goods factory
type StopGoodsFactoryResult struct {
	FactoryID string
	Status    string
	Message   string
}

// GoodsFactoryStatusResult contains detailed status of a goods factory
type GoodsFactoryStatusResult struct {
	FactoryID        string
	TargetGood       string
	Status           string
	DependencyTree   string
	QuantityAcquired int
	TotalCost        int
	NodesCompleted   int
	NodesTotal       int
	SystemSymbol     string
	ShipsUsed        int
	MarketQueries    int
	ParallelLevels   int
	EstimatedSpeedup float64
}

// ArbitrageOpportunityResult represents a single arbitrage opportunity
type ArbitrageOpportunityResult struct {
	Good            string
	BuyMarket       string
	SellMarket      string
	BuyPrice        int
	SellPrice       int
	ProfitPerUnit   int
	ProfitMargin    float64
	EstimatedProfit int
	Distance        float64
	BuySupply       string
	SellActivity    string
	Score           float64
}

// ScanArbitrageOpportunitiesResult contains scan results
type ScanArbitrageOpportunitiesResult struct {
	Opportunities []ArbitrageOpportunityResult
	TotalScanned  int
	SystemSymbol  string
}

// StartArbitrageCoordinatorResult contains the result of starting arbitrage coordinator
type StartArbitrageCoordinatorResult struct {
	ContainerID  string
	SystemSymbol string
	MinMargin    float64
	MaxWorkers   int
	MinBalance   int
	Status       string
	Message      string
}

// StartManufacturingCoordinatorResult contains the result of starting a manufacturing coordinator
type StartManufacturingCoordinatorResult struct {
	ContainerID  string
	SystemSymbol string
	MinPrice     int
	MaxWorkers   int
	MinBalance   int
	Status       string
	Message      string
}

// ScanArbitrageOpportunities scans for arbitrage opportunities in a system
func (c *DaemonClient) ScanArbitrageOpportunities(
	ctx context.Context,
	systemSymbol string,
	playerID int,
	minMargin float64,
	limit int,
) (*ScanArbitrageOpportunitiesResult, error) {
	resp, err := c.client.ScanArbitrageOpportunities(ctx, &pb.ScanArbitrageOpportunitiesRequest{
		PlayerId:     int32(playerID),
		SystemSymbol: systemSymbol,
		MinMargin:    minMargin,
		Limit:        int32(limit),
	})
	if err != nil {
		return nil, err
	}

	// Convert protobuf opportunities to result format
	opportunities := make([]ArbitrageOpportunityResult, len(resp.Opportunities))
	for i, opp := range resp.Opportunities {
		opportunities[i] = ArbitrageOpportunityResult{
			Good:            opp.Good,
			BuyMarket:       opp.BuyMarket,
			SellMarket:      opp.SellMarket,
			BuyPrice:        int(opp.BuyPrice),
			SellPrice:       int(opp.SellPrice),
			ProfitPerUnit:   int(opp.ProfitPerUnit),
			ProfitMargin:    opp.ProfitMargin,
			EstimatedProfit: int(opp.EstimatedProfit),
			Distance:        opp.Distance,
			BuySupply:       opp.BuySupply,
			SellActivity:    opp.SellActivity,
			Score:           opp.Score,
		}
	}

	return &ScanArbitrageOpportunitiesResult{
		Opportunities: opportunities,
		TotalScanned:  len(opportunities),
		SystemSymbol:  systemSymbol,
	}, nil
}

// StartArbitrageCoordinator starts an arbitrage coordinator
func (c *DaemonClient) StartArbitrageCoordinator(
	ctx context.Context,
	systemSymbol string,
	playerID int,
	minMargin float64,
	maxWorkers int,
	minBalance int,
) (*StartArbitrageCoordinatorResult, error) {
	resp, err := c.client.StartArbitrageCoordinator(ctx, &pb.StartArbitrageCoordinatorRequest{
		PlayerId:     int32(playerID),
		SystemSymbol: systemSymbol,
		MinMargin:    minMargin,
		MaxWorkers:   int32(maxWorkers),
		MinBalance:   int32(minBalance),
	})
	if err != nil {
		return nil, err
	}

	return &StartArbitrageCoordinatorResult{
		ContainerID:  resp.ContainerId,
		SystemSymbol: resp.SystemSymbol,
		MinMargin:    resp.MinMargin,
		MaxWorkers:   int(resp.MaxWorkers),
		MinBalance:   int(resp.MinBalance),
		Status:       resp.Status,
		Message:      resp.Message,
	}, nil
}

// StartParallelManufacturingCoordinator starts a parallel task-based manufacturing coordinator
func (c *DaemonClient) StartParallelManufacturingCoordinator(
	ctx context.Context,
	systemSymbol string,
	playerID int,
	minPrice int,
	maxWorkers int,
	minBalance int,
) (*StartManufacturingCoordinatorResult, error) {
	resp, err := c.client.StartParallelManufacturingCoordinator(ctx, &pb.StartParallelManufacturingCoordinatorRequest{
		PlayerId:     int32(playerID),
		SystemSymbol: systemSymbol,
		MinPrice:     int32(minPrice),
		MaxWorkers:   int32(maxWorkers),
		MinBalance:   int32(minBalance),
	})
	if err != nil {
		return nil, err
	}

	return &StartManufacturingCoordinatorResult{
		ContainerID:  resp.ContainerId,
		SystemSymbol: resp.SystemSymbol,
		MinPrice:     int(resp.MinPrice),
		MaxWorkers:   int(resp.MaxWorkers),
		MinBalance:   int(resp.MinBalance),
		Status:       resp.Status,
		Message:      resp.Message,
	}, nil
}
