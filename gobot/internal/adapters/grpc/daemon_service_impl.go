package grpc

import (
	"context"
	"encoding/json"
	"fmt"

	playerQuery "github.com/andrescamacho/spacetraders-go/internal/application/player/queries"
	shipCommands "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands"
	tradingQueries "github.com/andrescamacho/spacetraders-go/internal/application/trading/queries"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// daemonServiceImpl implements the DaemonServiceServer interface
// It bridges gRPC requests to the DaemonServer business logic
type daemonServiceImpl struct {
	pb.UnimplementedDaemonServiceServer
	daemon *DaemonServer
}

// resolvePlayerID resolves a player_id from either the provided player_id or agent_symbol
// Priority: player_id > agent_symbol
// Returns an error if both are missing or if agent_symbol lookup fails
func (s *daemonServiceImpl) resolvePlayerID(ctx context.Context, playerID int32, agentSymbol *string) (int, error) {
	// If player_id is provided and non-zero, use it directly
	if playerID != 0 {
		return int(playerID), nil
	}

	// If agent_symbol is provided, resolve it to player_id
	if agentSymbol != nil && *agentSymbol != "" {
		response, err := s.daemon.mediator.Send(ctx, &playerQuery.GetPlayerQuery{
			AgentSymbol: *agentSymbol,
		})
		if err != nil {
			return 0, fmt.Errorf("failed to resolve agent symbol %s to player_id: %w", *agentSymbol, err)
		}

		getPlayerResp, ok := response.(*playerQuery.GetPlayerResponse)
		if !ok {
			return 0, fmt.Errorf("unexpected response type from GetPlayerQuery")
		}

		return getPlayerResp.Player.ID.Value(), nil
	}

	// Neither player_id nor agent_symbol provided
	return 0, fmt.Errorf("either player_id or agent_symbol must be provided")
}

// newDaemonServiceImpl creates a new gRPC service implementation
func newDaemonServiceImpl(daemon *DaemonServer) *daemonServiceImpl {
	return &daemonServiceImpl{
		daemon: daemon,
	}
}

// NewDaemonServiceImpl creates a new gRPC service implementation (exported for testing)
func NewDaemonServiceImpl(daemon *DaemonServer) pb.DaemonServiceServer {
	return newDaemonServiceImpl(daemon)
}

// NavigateShip initiates ship navigation
func (s *daemonServiceImpl) NavigateShip(ctx context.Context, req *pb.NavigateShipRequest) (*pb.NavigateShipResponse, error) {
	// Resolve player ID from request (supports both player_id and agent_symbol)
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	// Call daemon's NavigateShip method
	containerID, err := s.daemon.NavigateShip(ctx, req.ShipSymbol, req.Destination, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate ship: %w", err)
	}

	// Build response
	response := &pb.NavigateShipResponse{
		ContainerId:          containerID,
		ShipSymbol:           req.ShipSymbol,
		Destination:          req.Destination,
		Status:               "PENDING",
		EstimatedTimeSeconds: 0, // TODO: Calculate estimated time when routing is wired
	}

	return response, nil
}

// DockShip docks a ship
func (s *daemonServiceImpl) DockShip(ctx context.Context, req *pb.DockShipRequest) (*pb.DockShipResponse, error) {
	// Resolve player ID from request (supports both player_id and agent_symbol)
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	containerID, err := s.daemon.DockShip(ctx, req.ShipSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to dock ship: %w", err)
	}

	response := &pb.DockShipResponse{
		ContainerId: containerID,
		ShipSymbol:  req.ShipSymbol,
		Status:      "PENDING",
	}

	return response, nil
}

// OrbitShip puts a ship into orbit
func (s *daemonServiceImpl) OrbitShip(ctx context.Context, req *pb.OrbitShipRequest) (*pb.OrbitShipResponse, error) {
	// Resolve player ID from request (supports both player_id and agent_symbol)
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	containerID, err := s.daemon.OrbitShip(ctx, req.ShipSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to orbit ship: %w", err)
	}

	response := &pb.OrbitShipResponse{
		ContainerId: containerID,
		ShipSymbol:  req.ShipSymbol,
		Status:      "PENDING",
	}

	return response, nil
}

// RefuelShip refuels a ship
func (s *daemonServiceImpl) RefuelShip(ctx context.Context, req *pb.RefuelShipRequest) (*pb.RefuelShipResponse, error) {
	// Resolve player ID from request (supports both player_id and agent_symbol)
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	// Handle optional units parameter
	var units *int
	if req.Units != nil {
		u := int(*req.Units)
		units = &u
	}

	containerID, err := s.daemon.RefuelShip(ctx, req.ShipSymbol, playerID, units)
	if err != nil {
		return nil, fmt.Errorf("failed to refuel ship: %w", err)
	}

	response := &pb.RefuelShipResponse{
		ContainerId: containerID,
		ShipSymbol:  req.ShipSymbol,
		FuelAdded:   0, // TODO: Get from actual operation result
		CreditsCost: 0, // TODO: Get from actual operation result
		Status:      "PENDING",
	}

	return response, nil
}

// JumpShip executes a jump to a different star system via jump gate
func (s *daemonServiceImpl) JumpShip(ctx context.Context, req *pb.JumpShipRequest) (*pb.JumpShipResponse, error) {
	// Import command dynamically to avoid circular dependencies
	// We'll need to add the import at the top of the file
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return &pb.JumpShipResponse{
			Success: false,
			Error:   fmt.Sprintf("failed to resolve player: %v", err),
		}, nil
	}

	// Call the JumpShip command handler through mediator
	// We'll need to import the commands package
	cmd := &shipCommands.JumpShipCommand{
		ShipSymbol:        req.ShipSymbol,
		DestinationSystem: req.DestinationSystem,
		PlayerID:          &playerID,
	}

	result, err := s.daemon.mediator.Send(ctx, cmd)
	if err != nil {
		return &pb.JumpShipResponse{
			Success: false,
			Error:   err.Error(),
		}, nil
	}

	resp, ok := result.(*shipCommands.JumpShipResponse)
	if !ok {
		return &pb.JumpShipResponse{
			Success: false,
			Error:   "unexpected response type from JumpShipCommand",
		}, nil
	}

	return &pb.JumpShipResponse{
		Success:          resp.Success,
		NavigatedToGate:  resp.NavigatedToGate,
		JumpGateSymbol:   resp.JumpGateSymbol,
		DestinationSystem: resp.DestinationSystem,
		CooldownSeconds:  int32(resp.CooldownSeconds),
		Message:          resp.Message,
		Error:            "",
	}, nil
}

// BatchContractWorkflow executes batch contract workflow
func (s *daemonServiceImpl) BatchContractWorkflow(ctx context.Context, req *pb.BatchContractWorkflowRequest) (*pb.BatchContractWorkflowResponse, error) {
	// Resolve player ID from request (supports both player_id and agent_symbol)
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	containerID, err := s.daemon.BatchContractWorkflow(ctx, req.ShipSymbol, int(req.Iterations), playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to start batch contract workflow: %w", err)
	}

	response := &pb.BatchContractWorkflowResponse{
		ContainerId: containerID,
		ShipSymbol:  req.ShipSymbol,
		Iterations:  req.Iterations,
		Status:      "RUNNING",
	}

	return response, nil
}

// ContractFleetCoordinator starts a contract fleet coordinator
// Uses all available idle light hauler ships (no pre-assignment needed)
func (s *daemonServiceImpl) ContractFleetCoordinator(ctx context.Context, req *pb.ContractFleetCoordinatorRequest) (*pb.ContractFleetCoordinatorResponse, error) {
	// Resolve player ID from request (supports both player_id and agent_symbol)
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	// No ship symbols needed - coordinator discovers idle haulers dynamically
	containerID, err := s.daemon.ContractFleetCoordinator(ctx, nil, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to start contract fleet coordinator: %w", err)
	}

	response := &pb.ContractFleetCoordinatorResponse{
		ContainerId: containerID,
		Status:      "RUNNING",
	}

	return response, nil
}

// ScoutTour executes market scouting tour (single ship)
func (s *daemonServiceImpl) ScoutTour(ctx context.Context, req *pb.ScoutTourRequest) (*pb.ScoutTourResponse, error) {
	// Resolve player ID from request (supports both player_id and agent_symbol)
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	// Generate container ID for this scout tour
	containerID := utils.GenerateContainerID("scout_tour", req.ShipSymbol)

	_, err = s.daemon.ScoutTour(ctx, containerID, req.ShipSymbol, req.Markets, int(req.Iterations), playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to start scout tour: %w", err)
	}

	response := &pb.ScoutTourResponse{
		ContainerId: containerID,
		ShipSymbol:  req.ShipSymbol,
		Markets:     req.Markets,
		Iterations:  req.Iterations,
		Status:      "RUNNING",
	}

	return response, nil
}

// ScoutMarkets orchestrates fleet deployment for market scouting (multi-ship with VRP)
func (s *daemonServiceImpl) ScoutMarkets(ctx context.Context, req *pb.ScoutMarketsRequest) (*pb.ScoutMarketsResponse, error) {
	// Resolve player ID from request (supports both player_id and agent_symbol)
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	containerIDs, assignments, reusedContainers, err := s.daemon.ScoutMarkets(
		ctx,
		req.ShipSymbols,
		req.SystemSymbol,
		req.Markets,
		int(req.Iterations),
		playerID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to start scout markets: %w", err)
	}

	// Convert assignments map to protobuf format
	pbAssignments := make(map[string]*pb.MarketAssignment)
	for ship, markets := range assignments {
		pbAssignments[ship] = &pb.MarketAssignment{
			Markets: markets,
		}
	}

	response := &pb.ScoutMarketsResponse{
		ContainerIds:     containerIDs,
		Assignments:      pbAssignments,
		ReusedContainers: reusedContainers,
	}

	return response, nil
}

// AssignScoutingFleet creates a fleet-assignment container for async VRP optimization
func (s *daemonServiceImpl) AssignScoutingFleet(ctx context.Context, req *pb.AssignScoutingFleetRequest) (*pb.AssignScoutingFleetResponse, error) {
	// Resolve player ID from request (supports both player_id and agent_symbol)
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	// Create fleet-assignment container (returns immediately)
	containerID, err := s.daemon.AssignScoutingFleet(
		ctx,
		req.SystemSymbol,
		playerID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create fleet assignment container: %w", err)
	}

	response := &pb.AssignScoutingFleetResponse{
		ContainerId: containerID,
	}

	return response, nil
}

// ListContainers returns all containers
func (s *daemonServiceImpl) ListContainers(ctx context.Context, req *pb.ListContainersRequest) (*pb.ListContainersResponse, error) {
	// Handle optional filters
	var playerID *int
	if req.PlayerId != nil {
		p := FromProtobufPlayerID(*req.PlayerId)
		playerID = &p
	}

	// Apply status filter with smart defaults
	var status *string
	if req.Status != nil && *req.Status != "" {
		// User explicitly requested a status - use as-is
		status = req.Status
	} else {
		// DEFAULT: Only show active containers (RUNNING, INTERRUPTED)
		// Rationale: Operators care about what's currently active, not history
		// Use comma-separated list for multiple statuses
		defaultStatuses := "RUNNING,INTERRUPTED"
		status = &defaultStatuses
	}

	// Get containers from daemon
	containers := s.daemon.ListContainers(playerID, status)

	// Convert to protobuf response
	pbContainers := make([]*pb.ContainerInfo, 0, len(containers))
	for _, cont := range containers {
		var parentID *string
		if cont.ParentContainerID() != nil {
			parentID = cont.ParentContainerID()
		}

		pbContainers = append(pbContainers, &pb.ContainerInfo{
			ContainerId:       cont.ID(),
			ContainerType:     string(cont.Type()),
			Status:            string(cont.Status()),
			PlayerId:          ToProtobufPlayerID(cont.PlayerID()),
			ParentContainerId: parentID,
			CreatedAt:         cont.CreatedAt().Format("2006-01-02T15:04:05Z"),
			UpdatedAt:         cont.UpdatedAt().Format("2006-01-02T15:04:05Z"),
			CurrentIteration:  int32(cont.CurrentIteration()),
			MaxIterations:     int32(cont.MaxIterations()),
			RestartCount:      int32(cont.RestartCount()),
		})
	}

	return &pb.ListContainersResponse{
		Containers: pbContainers,
	}, nil
}

// GetContainer retrieves container details
func (s *daemonServiceImpl) GetContainer(ctx context.Context, req *pb.GetContainerRequest) (*pb.GetContainerResponse, error) {
	container, err := s.daemon.GetContainer(req.ContainerId)
	if err != nil {
		return nil, fmt.Errorf("failed to get container: %w", err)
	}

	// Serialize metadata to JSON
	metadataJSON, err := json.Marshal(container.Metadata())
	if err != nil {
		return nil, fmt.Errorf("failed to serialize metadata: %w", err)
	}

	pbContainer := &pb.ContainerInfo{
		ContainerId:      container.ID(),
		ContainerType:    string(container.Type()),
		Status:           string(container.Status()),
		PlayerId:         ToProtobufPlayerID(container.PlayerID()),
		CreatedAt:        container.CreatedAt().Format("2006-01-02T15:04:05Z"),
		UpdatedAt:        container.UpdatedAt().Format("2006-01-02T15:04:05Z"),
		CurrentIteration: int32(container.CurrentIteration()),
		MaxIterations:    int32(container.MaxIterations()),
		RestartCount:     int32(container.RestartCount()),
	}

	return &pb.GetContainerResponse{
		Container: pbContainer,
		Metadata:  string(metadataJSON),
	}, nil
}

// StopContainer stops a container
func (s *daemonServiceImpl) StopContainer(ctx context.Context, req *pb.StopContainerRequest) (*pb.StopContainerResponse, error) {
	err := s.daemon.StopContainer(req.ContainerId)
	if err != nil {
		return nil, fmt.Errorf("failed to stop container: %w", err)
	}

	return &pb.StopContainerResponse{
		ContainerId: req.ContainerId,
		Status:      "STOPPED",
		Message:     "Container stopped successfully",
	}, nil
}

// GetContainerLogs retrieves container logs
func (s *daemonServiceImpl) GetContainerLogs(ctx context.Context, req *pb.GetContainerLogsRequest) (*pb.GetContainerLogsResponse, error) {
	// TODO: Implement log retrieval when logging infrastructure is wired
	// For now, return empty logs
	return &pb.GetContainerLogsResponse{
		Logs: []*pb.LogEntry{},
	}, nil
}

// HealthCheck verifies daemon health
func (s *daemonServiceImpl) HealthCheck(ctx context.Context, req *pb.HealthCheckRequest) (*pb.HealthCheckResponse, error) {
	// Get active container count
	containers := s.daemon.ListContainers(nil, nil)
	activeCount := 0
	for _, cont := range containers {
		if cont.Status() == "RUNNING" {
			activeCount++
		}
	}

	return &pb.HealthCheckResponse{
		Status:           "ok",
		Version:          "0.1.0",
		ActiveContainers: int32(activeCount),
	}, nil
}

// ListShips lists all ships for a player
func (s *daemonServiceImpl) ListShips(ctx context.Context, req *pb.ListShipsRequest) (*pb.ListShipsResponse, error) {
	// Convert player ID from proto
	var playerID *int
	if req.PlayerId != nil {
		pid := FromProtobufPlayerID(*req.PlayerId)
		playerID = &pid
	}

	agentSymbol := ""
	if req.AgentSymbol != nil {
		agentSymbol = *req.AgentSymbol
	}

	// Call daemon's ListShips method
	ships, err := s.daemon.ListShips(ctx, playerID, agentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to list ships: %w", err)
	}

	return &pb.ListShipsResponse{
		Ships: ships,
	}, nil
}

// GetShip retrieves detailed ship information
func (s *daemonServiceImpl) GetShip(ctx context.Context, req *pb.GetShipRequest) (*pb.GetShipResponse, error) {
	// Convert player ID from proto
	var playerID *int
	if req.PlayerId != nil {
		pid := FromProtobufPlayerID(*req.PlayerId)
		playerID = &pid
	}

	agentSymbol := ""
	if req.AgentSymbol != nil {
		agentSymbol = *req.AgentSymbol
	}

	// Call daemon's GetShip method
	shipDetail, err := s.daemon.GetShip(ctx, req.ShipSymbol, playerID, agentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to get ship: %w", err)
	}

	return &pb.GetShipResponse{
		Ship: shipDetail,
	}, nil
}

// GetShipyardListings retrieves available ships at a shipyard
func (s *daemonServiceImpl) GetShipyardListings(ctx context.Context, req *pb.GetShipyardListingsRequest) (*pb.GetShipyardListingsResponse, error) {
	// Resolve player ID from request
	var playerID *int
	if req.PlayerId != 0 {
		pid := FromProtobufPlayerID(req.PlayerId)
		playerID = &pid
	}

	agentSymbol := ""
	if req.AgentSymbol != nil {
		agentSymbol = *req.AgentSymbol
	}

	// Call daemon's GetShipyardListings method
	listings, shipyardSymbol, modificationFee, err := s.daemon.GetShipyardListings(ctx, req.SystemSymbol, req.WaypointSymbol, playerID, agentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to get shipyard listings: %w", err)
	}

	return &pb.GetShipyardListingsResponse{
		Listings:        listings,
		ShipyardSymbol:  shipyardSymbol,
		ModificationFee: modificationFee,
	}, nil
}

// PurchaseShip purchases a single ship from a shipyard
func (s *daemonServiceImpl) PurchaseShip(ctx context.Context, req *pb.PurchaseShipRequest) (*pb.PurchaseShipResponse, error) {
	// Resolve player ID from request
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	// Convert optional shipyard waypoint
	var shipyardWaypoint *string
	if req.ShipyardWaypoint != nil {
		shipyardWaypoint = req.ShipyardWaypoint
	}

	// Call daemon's PurchaseShip method
	containerID, purchasedShipSymbol, purchasePrice, agentCredits, status, err := s.daemon.PurchaseShip(
		ctx,
		req.PurchasingShipSymbol,
		req.ShipType,
		playerID,
		shipyardWaypoint,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to purchase ship: %w", err)
	}

	return &pb.PurchaseShipResponse{
		ContainerId:         containerID,
		PurchasedShipSymbol: purchasedShipSymbol,
		PurchasePrice:       int32(purchasePrice),
		AgentCredits:        int32(agentCredits),
		Status:              status,
	}, nil
}

// BatchPurchaseShips purchases multiple ships from a shipyard as a background operation
func (s *daemonServiceImpl) BatchPurchaseShips(ctx context.Context, req *pb.BatchPurchaseShipsRequest) (*pb.BatchPurchaseShipsResponse, error) {
	// Resolve player ID from request
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	// Convert optional parameters
	var shipyardWaypoint *string
	if req.ShipyardWaypoint != nil {
		shipyardWaypoint = req.ShipyardWaypoint
	}

	var iterations *int
	if req.Iterations != nil {
		iter := int(*req.Iterations)
		iterations = &iter
	}

	// Call daemon's BatchPurchaseShips method
	containerID, shipsToPurchase, maxBudget, resolvedShipyard, status, err := s.daemon.BatchPurchaseShips(
		ctx,
		req.PurchasingShipSymbol,
		req.ShipType,
		int(req.Quantity),
		int(req.MaxBudget),
		playerID,
		shipyardWaypoint,
		iterations,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to batch purchase ships: %w", err)
	}

	return &pb.BatchPurchaseShipsResponse{
		ContainerId:      containerID,
		ShipsToPurchase:  shipsToPurchase,
		MaxBudget:        maxBudget,
		ShipyardWaypoint: resolvedShipyard,
		Status:           status,
	}, nil
}

// MiningOperation starts a mining operation with Transport-as-Sink pattern
func (s *daemonServiceImpl) MiningOperation(ctx context.Context, req *pb.MiningOperationRequest) (*pb.MiningOperationResponse, error) {
	// Resolve player ID from request
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	// Default topNOres if not provided
	topNOres := int(req.TopNOres)
	if topNOres == 0 {
		topNOres = 3
	}

	// Extract optional fields
	asteroidField := ""
	if req.AsteroidField != nil {
		asteroidField = *req.AsteroidField
	}
	miningType := ""
	if req.MiningType != nil {
		miningType = *req.MiningType
	}

	result, err := s.daemon.MiningOperation(
		ctx,
		asteroidField,
		req.MinerShips,
		req.TransportShips,
		topNOres,
		miningType,
		req.Force,
		req.DryRun,
		int(req.MaxLegTime),
		playerID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to start mining operation: %w", err)
	}

	status := "RUNNING"
	if req.DryRun {
		status = "DRY_RUN_COMPLETE"
	}

	// Build response
	resp := &pb.MiningOperationResponse{
		ContainerId:    result.ContainerID,
		AsteroidField:  result.AsteroidField,
		MinerShips:     req.MinerShips,
		TransportShips: req.TransportShips,
		Status:         status,
		MarketSymbol:   result.MarketSymbol,
		Errors:         result.Errors,
	}

	// Convert ship routes for dry-run
	if req.DryRun && len(result.ShipRoutes) > 0 {
		resp.ShipRoutes = make([]*pb.ShipRoute, len(result.ShipRoutes))
		for i, route := range result.ShipRoutes {
			segments := make([]*pb.RouteSegment, len(route.Segments))
			for j, seg := range route.Segments {
				segments[j] = &pb.RouteSegment{
					From:       seg.From,
					To:         seg.To,
					FlightMode: seg.FlightMode,
					FuelCost:   int32(seg.FuelCost),
					TravelTime: int32(seg.TravelTime),
				}
			}
			resp.ShipRoutes[i] = &pb.ShipRoute{
				ShipSymbol: route.ShipSymbol,
				ShipType:   route.ShipType,
				Segments:   segments,
				TotalFuel:  int32(route.TotalFuel),
				TotalTime:  int32(route.TotalTime),
			}
		}
	}

	return resp, nil
}

// TourSell executes optimized cargo selling tour for a ship (container-based)
func (s *daemonServiceImpl) TourSell(ctx context.Context, req *pb.TourSellRequest) (*pb.TourSellResponse, error) {
	// Resolve player ID from request
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	// Generate container ID for this tour sell operation
	containerID := utils.GenerateContainerID("tour_sell", req.ShipSymbol)

	// Get optional return waypoint
	returnWaypoint := ""
	if req.ReturnWaypoint != nil {
		returnWaypoint = *req.ReturnWaypoint
	}

	// Start tour sell in container
	_, err = s.daemon.TourSell(ctx, containerID, req.ShipSymbol, returnWaypoint, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to start tour sell: %w", err)
	}

	return &pb.TourSellResponse{
		ContainerId: containerID,
		ShipSymbol:  req.ShipSymbol,
		Status:      "RUNNING",
	}, nil
}

// StartGoodsFactory implements the StartGoodsFactory RPC
func (s *daemonServiceImpl) StartGoodsFactory(ctx context.Context, req *pb.StartGoodsFactoryRequest) (*pb.StartGoodsFactoryResponse, error) {
	// Resolve player ID
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	// Default system symbol if not provided (would need to get current system from a ship)
	systemSymbol := ""
	if req.SystemSymbol != nil {
		systemSymbol = *req.SystemSymbol
	} else {
		// TODO: Default to a system - for now require it
		return nil, fmt.Errorf("system_symbol is required")
	}

	// Extract max_iterations (default to 1 if not provided)
	maxIterations := 1
	if req.MaxIterations != nil {
		maxIterations = int(*req.MaxIterations)
	}

	// Start goods factory
	result, err := s.daemon.StartGoodsFactory(ctx, req.TargetGood, systemSymbol, playerID, maxIterations)
	if err != nil {
		return nil, fmt.Errorf("failed to start goods factory: %w", err)
	}

	return &pb.StartGoodsFactoryResponse{
		FactoryId:  result.FactoryID,
		TargetGood: result.TargetGood,
		Status:     "RUNNING",
		Message:    fmt.Sprintf("Goods factory started for %s", req.TargetGood),
		NodesTotal: int32(result.NodesTotal),
	}, nil
}

// StopGoodsFactory implements the StopGoodsFactory RPC
func (s *daemonServiceImpl) StopGoodsFactory(ctx context.Context, req *pb.StopGoodsFactoryRequest) (*pb.StopGoodsFactoryResponse, error) {
	// Resolve player ID
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	// Stop the factory
	err = s.daemon.StopGoodsFactory(ctx, req.FactoryId, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to stop goods factory: %w", err)
	}

	return &pb.StopGoodsFactoryResponse{
		FactoryId: req.FactoryId,
		Status:    "STOPPED",
		Message:   "Goods factory stopped successfully",
	}, nil
}

// GetFactoryStatus implements the GetFactoryStatus RPC
func (s *daemonServiceImpl) GetFactoryStatus(ctx context.Context, req *pb.GetFactoryStatusRequest) (*pb.GetFactoryStatusResponse, error) {
	// Resolve player ID
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	// Get factory status
	status, err := s.daemon.GetFactoryStatus(ctx, req.FactoryId, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get factory status: %w", err)
	}

	return &pb.GetFactoryStatusResponse{
		FactoryId:        status.FactoryID,
		TargetGood:       status.TargetGood,
		Status:           status.Status,
		DependencyTree:   status.DependencyTree,
		QuantityAcquired: int32(status.QuantityAcquired),
		TotalCost:        int32(status.TotalCost),
		NodesCompleted:   int32(status.NodesCompleted),
		NodesTotal:       int32(status.NodesTotal),
		SystemSymbol:     status.SystemSymbol,
		ShipsUsed:        int32(status.ShipsUsed),
		MarketQueries:    int32(status.MarketQueries),
		ParallelLevels:   int32(status.ParallelLevels),
		EstimatedSpeedup: float32(status.EstimatedSpeedup),
	}, nil
}

// ScanArbitrageOpportunities scans markets for profitable arbitrage opportunities
func (s *daemonServiceImpl) ScanArbitrageOpportunities(ctx context.Context, req *pb.ScanArbitrageOpportunitiesRequest) (*pb.ScanArbitrageOpportunitiesResponse, error) {
	// Resolve player ID
	playerID := int(req.PlayerId)

	// Query for arbitrage opportunities via mediator
	// CargoCapacity defaults to 40 in the handler if not provided
	query := &tradingQueries.FindArbitrageOpportunitiesQuery{
		SystemSymbol:  req.SystemSymbol,
		PlayerID:      playerID,
		CargoCapacity: 0, // Use default (40 units)
		MinMargin:     req.MinMargin,
		Limit:         int(req.Limit),
	}

	result, err := s.daemon.mediator.Send(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to scan for arbitrage opportunities: %w", err)
	}

	response, ok := result.(*tradingQueries.FindArbitrageOpportunitiesResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type from FindArbitrageOpportunitiesQuery")
	}

	// Convert opportunities to protobuf format
	pbOpportunities := make([]*pb.ArbitrageOpportunity, len(response.Opportunities))
	for i, opp := range response.Opportunities {
		pbOpportunities[i] = &pb.ArbitrageOpportunity{
			Good:            opp.Good,
			BuyMarket:       opp.BuyMarket,
			SellMarket:      opp.SellMarket,
			BuyPrice:        int32(opp.BuyPrice),
			SellPrice:       int32(opp.SellPrice),
			ProfitPerUnit:   int32(opp.ProfitPerUnit),
			ProfitMargin:    opp.ProfitMargin,
			EstimatedProfit: int32(opp.EstimatedProfit),
			Distance:        opp.Distance,
			BuySupply:       opp.BuySupply,
			SellActivity:    opp.SellActivity,
			Score:           opp.Score,
		}
	}

	return &pb.ScanArbitrageOpportunitiesResponse{
		Opportunities: pbOpportunities,
	}, nil
}

// StartArbitrageCoordinator initiates automated arbitrage trading operations
func (s *daemonServiceImpl) StartArbitrageCoordinator(ctx context.Context, req *pb.StartArbitrageCoordinatorRequest) (*pb.StartArbitrageCoordinatorResponse, error) {
	// Resolve player ID
	playerID := int(req.PlayerId)

	// Default max workers if not provided
	maxWorkers := int(req.MaxWorkers)
	if maxWorkers == 0 {
		maxWorkers = 10
	}

	// Default min margin if not provided
	minMargin := req.MinMargin
	if minMargin <= 0 {
		minMargin = 10.0
	}

	// Default min balance (0 = no limit)
	minBalance := int(req.MinBalance)

	// Start coordinator via DaemonServer (creates container and runs in background)
	containerID, err := s.daemon.ArbitrageCoordinator(ctx, req.SystemSymbol, playerID, minMargin, maxWorkers, minBalance)
	if err != nil {
		return nil, fmt.Errorf("failed to start arbitrage coordinator: %w", err)
	}

	return &pb.StartArbitrageCoordinatorResponse{
		ContainerId:  containerID,
		SystemSymbol: req.SystemSymbol,
		MinMargin:    minMargin,
		MaxWorkers:   int32(maxWorkers),
		MinBalance:   int32(minBalance),
		Status:       "RUNNING",
		Message:      "Arbitrage coordinator started successfully",
	}, nil
}

// StartManufacturingCoordinator initiates automated manufacturing operations
func (s *daemonServiceImpl) StartManufacturingCoordinator(ctx context.Context, req *pb.StartManufacturingCoordinatorRequest) (*pb.StartManufacturingCoordinatorResponse, error) {
	// Resolve player ID
	playerID := int(req.PlayerId)

	// Default max workers if not provided
	maxWorkers := int(req.MaxWorkers)
	if maxWorkers == 0 {
		maxWorkers = 5 // Manufacturing is slower than arbitrage, use fewer workers
	}

	// Default min price if not provided
	minPrice := int(req.MinPrice)
	if minPrice <= 0 {
		minPrice = 1000
	}

	// Default min balance (0 = no limit)
	minBalance := int(req.MinBalance)

	// Start coordinator via DaemonServer (creates container and runs in background)
	containerID, err := s.daemon.ManufacturingCoordinator(ctx, req.SystemSymbol, playerID, minPrice, maxWorkers, minBalance)
	if err != nil {
		return nil, fmt.Errorf("failed to start manufacturing coordinator: %w", err)
	}

	return &pb.StartManufacturingCoordinatorResponse{
		ContainerId:  containerID,
		SystemSymbol: req.SystemSymbol,
		MinPrice:     int32(minPrice),
		MaxWorkers:   int32(maxWorkers),
		MinBalance:   int32(minBalance),
		Status:       "RUNNING",
		Message:      "Manufacturing coordinator started successfully",
	}, nil
}

// StartParallelManufacturingCoordinator initiates parallel task-based manufacturing operations
func (s *daemonServiceImpl) StartParallelManufacturingCoordinator(ctx context.Context, req *pb.StartParallelManufacturingCoordinatorRequest) (*pb.StartParallelManufacturingCoordinatorResponse, error) {
	// Resolve player ID
	playerID := int(req.PlayerId)

	// Default max workers if not provided
	maxWorkers := int(req.MaxWorkers)
	if maxWorkers == 0 {
		maxWorkers = 5
	}

	// Default min price if not provided
	minPrice := int(req.MinPrice)
	if minPrice <= 0 {
		minPrice = 1000
	}

	// Default min balance (0 = no limit)
	minBalance := int(req.MinBalance)

	// Start parallel coordinator via DaemonServer (creates container and runs in background)
	containerID, err := s.daemon.ParallelManufacturingCoordinator(ctx, req.SystemSymbol, playerID, minPrice, maxWorkers, minBalance)
	if err != nil {
		return nil, fmt.Errorf("failed to start parallel manufacturing coordinator: %w", err)
	}

	return &pb.StartParallelManufacturingCoordinatorResponse{
		ContainerId:  containerID,
		SystemSymbol: req.SystemSymbol,
		MinPrice:     int32(minPrice),
		MaxWorkers:   int32(maxWorkers),
		MinBalance:   int32(minBalance),
		Status:       "RUNNING",
		Message:      "Parallel manufacturing coordinator started successfully",
	}, nil
}
