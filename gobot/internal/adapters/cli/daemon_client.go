package cli

import (
	"context"
	"fmt"
	"time"

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

type ContractFleetCoordinatorResponse struct {
	ContainerID string
	ShipSymbols []string
	Status      string
}

type ContainerInfo struct{
	ContainerID      string
	ContainerType    string
	Status           string
	PlayerID         int32
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

// ContractFleetCoordinator starts a contract fleet coordinator with multiple ships
func (c *DaemonClient) ContractFleetCoordinator(
	ctx context.Context,
	shipSymbols []string,
	playerID int,
	agentSymbol string,
) (*ContractFleetCoordinatorResponse, error) {
	req := &pb.ContractFleetCoordinatorRequest{
		ShipSymbols: shipSymbols,
		PlayerId:    int32(playerID),
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
