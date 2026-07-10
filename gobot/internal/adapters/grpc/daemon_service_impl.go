package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	playerQuery "github.com/andrescamacho/spacetraders-go/internal/application/player/queries"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipOutfit "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/outfitting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

const containerTimestampFormat = "2006-01-02T15:04:05Z"

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
	cmd := &shipNav.JumpShipCommand{
		ShipSymbol:        req.ShipSymbol,
		DestinationSystem: req.DestinationSystem,
		PlayerID:          &playerID,
	}

	result, err := s.daemon.mediator.Send(ctx, cmd)
	if err != nil {
		// sp-n0x7: surface a workflow.failed event so the watchkeeper sees
		// jump attempts, mirroring container_runner.go's
		// signalCompletionWithStatus for ContainerRunner-driven workflows.
		recordCaptainEvent(captain.EventWorkflowFailed, req.ShipSymbol, playerID, map[string]any{
			"command_type":       "jump_ship",
			"destination_system": req.DestinationSystem,
			"success":            false,
			"error":              err.Error(),
		})
		return &pb.JumpShipResponse{
			Success: false,
			Error:   err.Error(),
		}, nil
	}

	resp, ok := result.(*shipNav.JumpShipResponse)
	if !ok {
		recordCaptainEvent(captain.EventWorkflowFailed, req.ShipSymbol, playerID, map[string]any{
			"command_type":       "jump_ship",
			"destination_system": req.DestinationSystem,
			"success":            false,
			"error":              "unexpected response type from JumpShipCommand",
		})
		return &pb.JumpShipResponse{
			Success: false,
			Error:   "unexpected response type from JumpShipCommand",
		}, nil
	}

	recordCaptainEvent(captain.EventWorkflowFinished, req.ShipSymbol, playerID, map[string]any{
		"command_type":       "jump_ship",
		"destination_system": resp.DestinationSystem,
		"success":            true,
		"cooldown_seconds":   resp.CooldownSeconds,
	})

	return &pb.JumpShipResponse{
		Success:           resp.Success,
		NavigatedToGate:   resp.NavigatedToGate,
		JumpGateSymbol:    resp.JumpGateSymbol,
		DestinationSystem: resp.DestinationSystem,
		CooldownSeconds:   int32(resp.CooldownSeconds),
		Message:           resp.Message,
		Error:             "",
	}, nil
}

// InstallModule installs a module (from the ship's cargo) onto the ship. It
// dispatches the daemon-side outfitting op through the mediator (RULING #3: the
// daemon is the single writer of ship state) and returns the ship's new cargo
// capacity synchronously.
func (s *daemonServiceImpl) InstallModule(ctx context.Context, req *pb.InstallModuleRequest) (*pb.InstallModuleResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return &pb.InstallModuleResponse{Error: fmt.Sprintf("failed to resolve player: %v", err)}, nil
	}

	cmd := &shipOutfit.InstallModuleCommand{
		ShipSymbol:   req.ShipSymbol,
		ModuleSymbol: req.ModuleSymbol,
		PlayerID:     &playerID,
	}

	result, err := s.daemon.mediator.Send(ctx, cmd)
	if err != nil {
		return &pb.InstallModuleResponse{ShipSymbol: req.ShipSymbol, ModuleSymbol: req.ModuleSymbol, Error: err.Error()}, nil
	}
	resp, ok := result.(*shipOutfit.InstallModuleResponse)
	if !ok {
		return &pb.InstallModuleResponse{ShipSymbol: req.ShipSymbol, ModuleSymbol: req.ModuleSymbol, Error: "unexpected response type from InstallModuleCommand"}, nil
	}

	return &pb.InstallModuleResponse{
		Success:       resp.Success,
		ShipSymbol:    resp.ShipSymbol,
		ModuleSymbol:  resp.ModuleSymbol,
		CargoCapacity: int32(resp.CargoCapacity),
		Fee:           int32(resp.Fee),
		Modules:       toProtoShipModules(resp.Modules),
		Message:       resp.Message,
	}, nil
}

// RemoveModule removes an installed module from the ship back into its cargo.
func (s *daemonServiceImpl) RemoveModule(ctx context.Context, req *pb.RemoveModuleRequest) (*pb.RemoveModuleResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return &pb.RemoveModuleResponse{Error: fmt.Sprintf("failed to resolve player: %v", err)}, nil
	}

	cmd := &shipOutfit.RemoveModuleCommand{
		ShipSymbol:   req.ShipSymbol,
		ModuleSymbol: req.ModuleSymbol,
		PlayerID:     &playerID,
	}

	result, err := s.daemon.mediator.Send(ctx, cmd)
	if err != nil {
		return &pb.RemoveModuleResponse{ShipSymbol: req.ShipSymbol, ModuleSymbol: req.ModuleSymbol, Error: err.Error()}, nil
	}
	resp, ok := result.(*shipOutfit.RemoveModuleResponse)
	if !ok {
		return &pb.RemoveModuleResponse{ShipSymbol: req.ShipSymbol, ModuleSymbol: req.ModuleSymbol, Error: "unexpected response type from RemoveModuleCommand"}, nil
	}

	return &pb.RemoveModuleResponse{
		Success:       resp.Success,
		ShipSymbol:    resp.ShipSymbol,
		ModuleSymbol:  resp.ModuleSymbol,
		CargoCapacity: int32(resp.CargoCapacity),
		Fee:           int32(resp.Fee),
		Modules:       toProtoShipModules(resp.Modules),
		Message:       resp.Message,
	}, nil
}

// ListShipModules lists the modules installed on a ship (read-only), plus
// the ship's power/slot/crew budget summary and, when the request carries a
// candidate_symbol, an offline install-feasibility verdict (sp-el60).
func (s *daemonServiceImpl) ListShipModules(ctx context.Context, req *pb.ListShipModulesRequest) (*pb.ListShipModulesResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return &pb.ListShipModulesResponse{Error: fmt.Sprintf("failed to resolve player: %v", err)}, nil
	}

	// req.CandidatePower/Crew/Slots are deprecated and intentionally ignored
	// (sp-el60 acceptance fix): the candidate's requirements are always
	// resolved server-side via ShipRepository.FindModuleRequirements, never
	// taken from caller input. See ListShipModulesRequest's proto comment.
	cmd := &shipOutfit.ListShipModulesQuery{
		ShipSymbol:      req.ShipSymbol,
		PlayerID:        &playerID,
		CandidateSymbol: req.GetCandidateSymbol(),
	}

	result, err := s.daemon.mediator.Send(ctx, cmd)
	if err != nil {
		return &pb.ListShipModulesResponse{ShipSymbol: req.ShipSymbol, Error: err.Error()}, nil
	}
	resp, ok := result.(*shipOutfit.ListShipModulesResponse)
	if !ok {
		return &pb.ListShipModulesResponse{ShipSymbol: req.ShipSymbol, Error: "unexpected response type from ListShipModulesQuery"}, nil
	}

	out := &pb.ListShipModulesResponse{
		ShipSymbol:         resp.ShipSymbol,
		Modules:            toProtoShipModules(resp.Modules),
		ReactorPowerOutput: int32(resp.ReactorPowerOutput),
		PowerUsed:          int32(resp.PowerUsed),
		ModuleSlots:        int32(resp.ModuleSlots),
		ModuleSlotsUsed:    int32(resp.ModuleSlotsUsed),
		MountingPoints:     int32(resp.MountingPoints),
		MountingPointsUsed: int32(resp.MountingPointsUsed),
		CrewCurrent:        int32(resp.CrewCurrent),
		CrewRequired:       int32(resp.CrewRequired),
		CrewCapacity:       int32(resp.CrewCapacity),
	}
	if resp.Feasibility != nil {
		out.Feasibility = &pb.ModuleFeasibility{
			CandidateSymbol:   resp.Feasibility.CandidateSymbol,
			CanInstall:        resp.Feasibility.CanInstall,
			PowerShort:        int32(resp.Feasibility.PowerShort),
			SlotShort:         int32(resp.Feasibility.SlotShort),
			CrewShort:         int32(resp.Feasibility.CrewShort),
			RequirementsKnown: resp.Feasibility.RequirementsKnown,
			RequirementsPower: int32(resp.Feasibility.RequirementsPower),
			RequirementsCrew:  int32(resp.Feasibility.RequirementsCrew),
			RequirementsSlots: int32(resp.Feasibility.RequirementsSlots),
		}
	}

	return out, nil
}

// toProtoShipModules maps the application-layer module list to the proto shape.
func toProtoShipModules(modules []ports.ModuleInfo) []*pb.ShipModuleInfo {
	out := make([]*pb.ShipModuleInfo, 0, len(modules))
	for _, m := range modules {
		out = append(out, &pb.ShipModuleInfo{
			Symbol:   m.Symbol,
			Name:     m.Name,
			Capacity: int32(m.Capacity),
			Range:    int32(m.Range),
			Power:    int32(m.Power),
			Crew:     int32(m.Crew),
			Slots:    int32(m.Slots),
		})
	}
	return out
}

// BatchContractWorkflow executes batch contract workflow
func (s *daemonServiceImpl) BatchContractWorkflow(ctx context.Context, req *pb.BatchContractWorkflowRequest) (*pb.BatchContractWorkflowResponse, error) {
	// Resolve player ID from request (supports both player_id and agent_symbol)
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	containerID, err := s.daemon.BatchContractWorkflow(ctx, req.ShipSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to start batch contract workflow: %w", err)
	}

	response := &pb.BatchContractWorkflowResponse{
		ContainerId: containerID,
		ShipSymbol:  req.ShipSymbol,
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

	// No ship symbols needed - coordinator discovers idle haulers dynamically.
	// dedicated_ships/standby_stations (sp-snmb) are optional operator params
	// for a static dedicated contract fleet; nil/empty when not configured.
	containerID, err := s.daemon.ContractFleetCoordinator(ctx, nil, playerID, req.DedicatedShips, req.StandbyStations)
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

// ScoutPostCoordinator starts the standing scout-post coordinator (sp-cxpq)
func (s *daemonServiceImpl) ScoutPostCoordinator(ctx context.Context, req *pb.ScoutPostCoordinatorRequest) (*pb.ScoutPostCoordinatorResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	containerID, err := s.daemon.ScoutPostCoordinator(ctx, playerID, int(req.TickIntervalSecs))
	if err != nil {
		return nil, fmt.Errorf("failed to start scout post coordinator: %w", err)
	}

	return &pb.ScoutPostCoordinatorResponse{ContainerId: containerID, Status: "RUNNING"}, nil
}

// AddScoutPost adds or updates a desired-state scout post (sp-cxpq)
func (s *daemonServiceImpl) AddScoutPost(ctx context.Context, req *pb.AddScoutPostRequest) (*pb.ScoutPostResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	kind := domainScouting.PostKind(req.Kind)
	if req.Kind == "" {
		kind = domainScouting.PostKindStanding
	}
	freshness := time.Duration(req.FreshnessSeconds) * time.Second

	post, err := s.daemon.AddScoutPost(ctx, playerID, req.SystemSymbol, freshness, kind, int(req.Hulls))
	if err != nil {
		return nil, fmt.Errorf("failed to add scout post: %w", err)
	}

	return &pb.ScoutPostResponse{Post: scoutPostToProto(post)}, nil
}

// RemoveScoutPost removes a scout post and releases its hull (sp-cxpq)
func (s *daemonServiceImpl) RemoveScoutPost(ctx context.Context, req *pb.RemoveScoutPostRequest) (*pb.RemoveScoutPostResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	if err := s.daemon.RemoveScoutPost(ctx, playerID, req.SystemSymbol); err != nil {
		return nil, fmt.Errorf("failed to remove scout post: %w", err)
	}

	return &pb.RemoveScoutPostResponse{Status: "REMOVED"}, nil
}

// ListScoutPosts returns the active scout posts for a player (sp-cxpq)
func (s *daemonServiceImpl) ListScoutPosts(ctx context.Context, req *pb.ListScoutPostsRequest) (*pb.ListScoutPostsResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	posts, err := s.daemon.ListScoutPosts(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to list scout posts: %w", err)
	}

	protoPosts := make([]*pb.ScoutPost, len(posts))
	for i, p := range posts {
		protoPosts[i] = scoutPostToProto(p)
	}
	return &pb.ListScoutPostsResponse{Posts: protoPosts}, nil
}

// scoutPostToProto maps a domain scout post to its wire representation. hulls is the
// probe budget and manned_count how many of those slots currently have a hull (sp-enry).
func scoutPostToProto(p *domainScouting.ScoutPost) *pb.ScoutPost {
	return &pb.ScoutPost{
		SystemSymbol:     p.SystemSymbol,
		FreshnessSeconds: int32(p.FreshnessTarget.Seconds()),
		Kind:             string(p.Kind),
		AssignedHull:     p.AssignedHull,
		TourContainerId:  p.TourContainerID,
		Hulls:            int32(p.HullBudget()),
		MannedCount:      int32(p.MannedCount()),
	}
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
			CreatedAt:         cont.CreatedAt().Format(containerTimestampFormat),
			UpdatedAt:         cont.UpdatedAt().Format(containerTimestampFormat),
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
		CreatedAt:        container.CreatedAt().Format(containerTimestampFormat),
		UpdatedAt:        container.UpdatedAt().Format(containerTimestampFormat),
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

// GetAPIBudget returns API request-budget observability (sp-51ti): per-hull
// req/s, global utilization vs the rate ceiling (429 rate, poll-cadence
// share of the budget, headroom), and the duty-cycle KPI (ship-hours
// earning/day per hull). Reads the daemon-wide singletons set at startup
// (metrics.GetGlobalAPIBudgetTracker, metrics.GetGlobalDutyCycleSampler);
// both trackers are nil-safe on Report(), so a metrics-disabled or
// not-yet-warmed-up daemon returns a zero-value report instead of erroring.
func (s *daemonServiceImpl) GetAPIBudget(ctx context.Context, req *pb.GetAPIBudgetRequest) (*pb.GetAPIBudgetResponse, error) {
	dualReport := metrics.GetGlobalAPIBudgetTracker().Report()
	dutyCycleReport := metrics.GetGlobalDutyCycleSampler().Report()

	return &pb.GetAPIBudgetResponse{
		Current:    apiBudgetReportToProto(dualReport.Current),
		Rolling_5M: apiBudgetReportToProto(dualReport.Rolling5m),
		DutyCycle:  dutyCycleReportToProto(dutyCycleReport),
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

	agentSymbol := stringValue(req.AgentSymbol)

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

	agentSymbol := stringValue(req.AgentSymbol)

	// Call daemon's GetShip method
	shipDetail, err := s.daemon.GetShip(ctx, req.ShipSymbol, playerID, agentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to get ship: %w", err)
	}

	return &pb.GetShipResponse{
		Ship: shipDetail,
	}, nil
}

func (s *daemonServiceImpl) RefreshShip(ctx context.Context, req *pb.RefreshShipRequest) (*pb.RefreshShipResponse, error) {
	// Convert player ID from proto
	var playerID *int
	if req.PlayerId != nil {
		pid := FromProtobufPlayerID(*req.PlayerId)
		playerID = &pid
	}

	agentSymbol := stringValue(req.AgentSymbol)

	// Call daemon's RefreshShip method
	shipDetail, err := s.daemon.RefreshShip(ctx, req.ShipSymbol, playerID, agentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to refresh ship: %w", err)
	}

	return &pb.RefreshShipResponse{
		Ship: shipDetail,
	}, nil
}

func (s *daemonServiceImpl) ReserveShip(ctx context.Context, req *pb.ReserveShipRequest) (*pb.ReserveShipResponse, error) {
	var playerID *int
	if req.PlayerId != nil {
		pid := FromProtobufPlayerID(*req.PlayerId)
		playerID = &pid
	}

	agentSymbol := stringValue(req.AgentSymbol)
	reason := stringValue(req.Reason)

	shipSymbol, respReason, warning, err := s.daemon.ReserveShip(ctx, req.ShipSymbol, reason, playerID, agentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to reserve ship: %w", err)
	}

	return &pb.ReserveShipResponse{
		ShipSymbol: shipSymbol,
		Reason:     respReason,
		Warning:    warning,
	}, nil
}

func (s *daemonServiceImpl) ReleaseShip(ctx context.Context, req *pb.ReleaseShipRequest) (*pb.ReleaseShipResponse, error) {
	var playerID *int
	if req.PlayerId != nil {
		pid := FromProtobufPlayerID(*req.PlayerId)
		playerID = &pid
	}

	agentSymbol := stringValue(req.AgentSymbol)
	reason := stringValue(req.Reason)

	shipSymbol, err := s.daemon.ReleaseShip(ctx, req.ShipSymbol, reason, playerID, agentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to release ship: %w", err)
	}

	return &pb.ReleaseShipResponse{
		ShipSymbol: shipSymbol,
	}, nil
}

func (s *daemonServiceImpl) AssignShipFleet(ctx context.Context, req *pb.AssignShipFleetRequest) (*pb.AssignShipFleetResponse, error) {
	var playerID *int
	if req.PlayerId != nil {
		pid := FromProtobufPlayerID(*req.PlayerId)
		playerID = &pid
	}

	agentSymbol := stringValue(req.AgentSymbol)

	shipSymbol, fleet, err := s.daemon.AssignShipFleet(ctx, req.ShipSymbol, req.Fleet, playerID, agentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to assign ship fleet: %w", err)
	}

	return &pb.AssignShipFleetResponse{
		ShipSymbol: shipSymbol,
		Fleet:      fleet,
	}, nil
}

func (s *daemonServiceImpl) UnassignShipFleet(ctx context.Context, req *pb.UnassignShipFleetRequest) (*pb.UnassignShipFleetResponse, error) {
	var playerID *int
	if req.PlayerId != nil {
		pid := FromProtobufPlayerID(*req.PlayerId)
		playerID = &pid
	}

	agentSymbol := stringValue(req.AgentSymbol)

	// Unassign is assign-to-empty: Fleet "" clears the dedication through the
	// same single write path (sp-l7h2).
	shipSymbol, _, err := s.daemon.AssignShipFleet(ctx, req.ShipSymbol, "", playerID, agentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to unassign ship fleet: %w", err)
	}

	return &pb.UnassignShipFleetResponse{
		ShipSymbol: shipSymbol,
	}, nil
}

func (s *daemonServiceImpl) ListFleets(ctx context.Context, req *pb.ListFleetsRequest) (*pb.ListFleetsResponse, error) {
	var playerID *int
	if req.PlayerId != nil {
		pid := FromProtobufPlayerID(*req.PlayerId)
		playerID = &pid
	}

	agentSymbol := stringValue(req.AgentSymbol)

	fleets, err := s.daemon.ListFleets(ctx, playerID, agentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to list fleets: %w", err)
	}

	return &pb.ListFleetsResponse{
		Fleets: fleets,
	}, nil
}

func (s *daemonServiceImpl) ListWaypoints(ctx context.Context, req *pb.ListWaypointsRequest) (*pb.ListWaypointsResponse, error) {
	var playerID *int
	if req.PlayerId != nil {
		pid := FromProtobufPlayerID(*req.PlayerId)
		playerID = &pid
	}

	agentSymbol := stringValue(req.AgentSymbol)

	trait := stringValue(req.Trait)
	waypointType := stringValue(req.Type)

	waypoints, err := s.daemon.ListWaypoints(ctx, req.SystemSymbol, trait, waypointType, playerID, agentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to list waypoints: %w", err)
	}

	return &pb.ListWaypointsResponse{
		Waypoints: waypoints,
	}, nil
}

func (s *daemonServiceImpl) GetWaypoint(ctx context.Context, req *pb.GetWaypointRequest) (*pb.GetWaypointResponse, error) {
	var playerID *int
	if req.PlayerId != nil {
		pid := FromProtobufPlayerID(*req.PlayerId)
		playerID = &pid
	}

	agentSymbol := stringValue(req.AgentSymbol)

	waypoint, err := s.daemon.GetWaypoint(ctx, req.WaypointSymbol, playerID, agentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to get waypoint: %w", err)
	}

	return &pb.GetWaypointResponse{
		Waypoint: waypoint,
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

	agentSymbol := stringValue(req.AgentSymbol)

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

	// Start goods factory. inputs_only (default false) requests production-only mode:
	// feed the dependency tree but leave the fabricated output in factory stock for a
	// construction pipeline to source, rather than harvesting it (sp-q02m).
	result, err := s.daemon.StartGoodsFactory(ctx, req.TargetGood, systemSymbol, playerID, maxIterations, req.GetInputsOnly())
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

// StartTradeRoute implements the StartTradeRoute RPC: it launches a single-hull
// pure-arbitrage circuit as a recovery-safe daemon container (sp-zewt), delegating to
// DaemonServer.StartTradeRoute which enforces the idle-gap discipline and owns the
// container lifecycle.
func (s *daemonServiceImpl) StartTradeRoute(ctx context.Context, req *pb.StartTradeRouteRequest) (*pb.StartTradeRouteResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}
	if req.ShipSymbol == "" {
		return nil, fmt.Errorf("ship_symbol is required")
	}
	if req.SystemSymbol == "" {
		return nil, fmt.Errorf("system_symbol is required")
	}

	maxVisits := 0
	if req.MaxVisits != nil {
		maxVisits = int(*req.MaxVisits)
	}

	destWaypoint := ""
	if req.DestWaypoint != nil {
		destWaypoint = *req.DestWaypoint
	}

	result, err := s.daemon.StartTradeRoute(ctx, req.ShipSymbol, req.SystemSymbol, maxVisits, playerID, destWaypoint)
	if err != nil {
		return nil, fmt.Errorf("failed to start trade-route: %w", err)
	}

	return &pb.StartTradeRouteResponse{
		ContainerId:  result.ContainerID,
		ShipSymbol:   result.ShipSymbol,
		SystemSymbol: result.SystemSymbol,
		Status:       "RUNNING",
		Message:      fmt.Sprintf("Trade-route circuit started for %s in %s", req.ShipSymbol, req.SystemSymbol),
	}, nil
}

// StartWarehouse implements the StartWarehouse RPC: it launches a passive
// inventory warehouse (sp-dchv Lane B) on a dedicated storage hull as a
// recovery-safe daemon container, delegating to DaemonServer.StartWarehouse
// which enforces the idle-gap discipline and owns the container lifecycle.
func (s *daemonServiceImpl) StartWarehouse(ctx context.Context, req *pb.StartWarehouseRequest) (*pb.StartWarehouseResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}
	if req.ShipSymbol == "" {
		return nil, fmt.Errorf("ship_symbol is required")
	}
	if req.WaypointSymbol == "" {
		return nil, fmt.Errorf("waypoint_symbol is required")
	}
	if len(req.SupportedGoods) == 0 {
		return nil, fmt.Errorf("supported_goods is required (at least one good)")
	}

	result, err := s.daemon.StartWarehouse(ctx, req.ShipSymbol, req.WaypointSymbol, req.SupportedGoods, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to start warehouse: %w", err)
	}

	return &pb.StartWarehouseResponse{
		ContainerId:    result.ContainerID,
		ShipSymbol:     result.ShipSymbol,
		WaypointSymbol: result.WaypointSymbol,
		Status:         "RUNNING",
		Message:        fmt.Sprintf("Warehouse started for %s at %s", req.ShipSymbol, req.WaypointSymbol),
	}, nil
}

// StartArbRun implements the StartArbRun RPC: it launches a one-shot, captain-directed,
// guarded arbitrage run as a recovery-safe daemon container (sp-p4ua), delegating to
// DaemonServer.StartArbRun which enforces the idle-gap discipline and owns the container
// lifecycle.
func (s *daemonServiceImpl) StartArbRun(ctx context.Context, req *pb.StartArbRunRequest) (*pb.StartArbRunResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}
	if req.ShipSymbol == "" {
		return nil, fmt.Errorf("ship_symbol is required")
	}
	if req.Good == "" {
		return nil, fmt.Errorf("good is required")
	}
	if req.BuyAt == "" {
		return nil, fmt.Errorf("buy_at is required")
	}
	if req.SellAt == "" {
		return nil, fmt.Errorf("sell_at is required")
	}

	maxUnits := 0
	if req.MaxUnits != nil {
		maxUnits = int(*req.MaxUnits)
	}
	maxSpend := 0
	if req.MaxSpend != nil {
		maxSpend = int(*req.MaxSpend)
	}
	minMargin := 0
	if req.MinMargin != nil {
		minMargin = int(*req.MinMargin)
	}
	workingCapitalReserve := 0
	if req.WorkingCapitalReserve != nil {
		workingCapitalReserve = int(*req.WorkingCapitalReserve)
	}

	result, err := s.daemon.StartArbRun(ctx, req.ShipSymbol, req.Good, req.BuyAt, req.SellAt, maxUnits, maxSpend, minMargin, workingCapitalReserve, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to start arb-run: %w", err)
	}

	return &pb.StartArbRunResponse{
		ContainerId: result.ContainerID,
		ShipSymbol:  result.ShipSymbol,
		Good:        result.Good,
		BuyAt:       result.BuyAt,
		SellAt:      result.SellAt,
		Status:      "RUNNING",
		Message:     fmt.Sprintf("Arb-run started for %s: buy %s at %s, sell at %s", req.ShipSymbol, req.Good, req.BuyAt, req.SellAt),
	}, nil
}

func (s *daemonServiceImpl) StartTourRun(ctx context.Context, req *pb.StartTourRunRequest) (*pb.StartTourRunResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}
	if req.ShipSymbol == "" {
		return nil, fmt.Errorf("ship_symbol is required")
	}

	maxHops := 0
	if req.MaxHops != nil {
		maxHops = int(*req.MaxHops)
	}
	var maxSpend int64
	if req.MaxSpend != nil {
		maxSpend = *req.MaxSpend
	}
	minMargin := 0
	if req.MinMargin != nil {
		minMargin = int(*req.MinMargin)
	}
	replanLimit := 0
	if req.ReplanLimit != nil {
		replanLimit = int(*req.ReplanLimit)
	}
	var workingCapitalReserve int64
	if req.WorkingCapitalReserve != nil {
		workingCapitalReserve = *req.WorkingCapitalReserve
	}
	agentSymbol := ""
	if req.AgentSymbol != nil {
		agentSymbol = *req.AgentSymbol
	}
	// iterations (sp-m5kv): -1 = continuous, N>0 = N tours, unset/0 = one tour. Passed
	// through verbatim; the coordinator normalizes 0 → one tour.
	iterations := 0
	if req.Iterations != nil {
		iterations = int(*req.Iterations)
	}

	result, err := s.daemon.StartTourRun(ctx, req.ShipSymbol, maxHops, maxSpend, minMargin, replanLimit, workingCapitalReserve, agentSymbol, iterations, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to start tour-run: %w", err)
	}

	return &pb.StartTourRunResponse{
		ContainerId: result.ContainerID,
		ShipSymbol:  result.ShipSymbol,
		Status:      "RUNNING",
		Message:     fmt.Sprintf("Tour-run started for %s", req.ShipSymbol),
	}, nil
}

func (s *daemonServiceImpl) StartStocker(ctx context.Context, req *pb.StartStockerRequest) (*pb.StartStockerResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}
	if req.ShipSymbol == "" {
		return nil, fmt.Errorf("ship_symbol is required")
	}
	if req.WarehouseWaypoint == "" {
		return nil, fmt.Errorf("warehouse_waypoint is required")
	}

	budgetPerLeg := 0
	if req.BudgetPerLeg != nil {
		budgetPerLeg = int(*req.BudgetPerLeg)
	}
	var workingCapitalReserve int64
	if req.WorkingCapitalReserve != nil {
		workingCapitalReserve = *req.WorkingCapitalReserve
	}
	// iterations: -1 = continuous, N>0 = N round-trips, unset/0 = one round-trip. Passed
	// through verbatim; the coordinator normalizes 0 → one round-trip.
	iterations := 0
	if req.Iterations != nil {
		iterations = int(*req.Iterations)
	}
	maxMarketAgeMinutes := 0
	if req.MaxMarketAgeMinutes != nil {
		maxMarketAgeMinutes = int(*req.MaxMarketAgeMinutes)
	}
	targetPerGood := 0
	if req.TargetPerGood != nil {
		targetPerGood = int(*req.TargetPerGood)
	}
	agentSymbol := ""
	if req.AgentSymbol != nil {
		agentSymbol = *req.AgentSymbol
	}

	result, err := s.daemon.StartStocker(ctx, req.ShipSymbol, req.WarehouseWaypoint, budgetPerLeg, workingCapitalReserve, iterations, maxMarketAgeMinutes, targetPerGood, agentSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to start stocker: %w", err)
	}

	return &pb.StartStockerResponse{
		ContainerId:       result.ContainerID,
		ShipSymbol:        result.ShipSymbol,
		WarehouseWaypoint: result.WarehouseWaypoint,
		Status:            "RUNNING",
		Message:           fmt.Sprintf("Stocker started for %s filling warehouse %s", req.ShipSymbol, req.WarehouseWaypoint),
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

// StartParallelManufacturingCoordinator initiates parallel task-based manufacturing operations
func (s *daemonServiceImpl) StartParallelManufacturingCoordinator(ctx context.Context, req *pb.StartParallelManufacturingCoordinatorRequest) (*pb.StartParallelManufacturingCoordinatorResponse, error) {
	// Resolve player ID
	playerID := int(req.PlayerId)

	// Default max workers if not provided
	maxWorkers := int(req.MaxWorkers)
	if maxWorkers == 0 {
		maxWorkers = 5
	}

	// Max pipelines: 0 means DISABLED (no fabrication pipelines)
	// CLI defaults to 3 when flag not specified, so 0 must be explicit
	maxPipelines := int(req.MaxPipelines)

	// Default min price if not provided
	minPrice := int(req.MinPrice)
	if minPrice <= 0 {
		minPrice = 1000
	}

	// Default min balance (0 = no limit)
	minBalance := int(req.MinBalance)

	// Max collection pipelines (0 = unlimited, no default applied)
	maxCollectionPipelines := int(req.MaxCollectionPipelines)

	// Default strategy to prefer-fabricate (recursive supply chain manufacturing)
	strategy := req.Strategy
	if strategy == "" {
		strategy = "prefer-fabricate"
	}

	// Start parallel coordinator via DaemonServer (creates container and runs in background)
	containerID, err := s.daemon.ParallelManufacturingCoordinator(ctx, req.SystemSymbol, playerID, minPrice, maxWorkers, maxPipelines, maxCollectionPipelines, minBalance, strategy)
	if err != nil {
		return nil, fmt.Errorf("failed to start parallel manufacturing coordinator: %w", err)
	}

	return &pb.StartParallelManufacturingCoordinatorResponse{
		ContainerId:  containerID,
		SystemSymbol: req.SystemSymbol,
		MinPrice:     int32(minPrice),
		MaxWorkers:   int32(maxWorkers),
		MaxPipelines: int32(maxPipelines),
		MinBalance:   int32(minBalance),
		Status:       "RUNNING",
		Message:      "Parallel manufacturing coordinator started successfully",
	}, nil
}

// JettisonCargo jettisons cargo from a ship
func (s *daemonServiceImpl) JettisonCargo(ctx context.Context, req *pb.JettisonCargoRequest) (*pb.JettisonCargoResponse, error) {
	// Resolve player ID from request (supports both player_id and agent_symbol)
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	containerID, err := s.daemon.JettisonCargo(ctx, req.ShipSymbol, playerID, req.GoodSymbol, int(req.Units))
	if err != nil {
		return nil, fmt.Errorf("failed to jettison cargo: %w", err)
	}

	response := &pb.JettisonCargoResponse{
		ContainerId:     containerID,
		ShipSymbol:      req.ShipSymbol,
		GoodSymbol:      req.GoodSymbol,
		UnitsJettisoned: req.Units,
		Status:          "PENDING",
		Message:         fmt.Sprintf("Jettisoning %d units of %s from %s", req.Units, req.GoodSymbol, req.ShipSymbol),
	}

	return response, nil
}

// GasExtractionOperation starts a gas extraction operation with siphon and transport ships
func (s *daemonServiceImpl) GasExtractionOperation(ctx context.Context, req *pb.GasExtractionOperationRequest) (*pb.GasExtractionOperationResponse, error) {
	// Resolve player ID from request
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	// Extract optional gas giant field
	gasGiant := ""
	if req.GasGiant != nil {
		gasGiant = *req.GasGiant
	}

	result, err := s.daemon.GasExtractionOperation(
		ctx,
		gasGiant,
		req.SiphonShips,
		req.TransportShips,
		req.Force,
		req.DryRun,
		int(req.MaxLegTime),
		playerID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to start gas extraction operation: %w", err)
	}

	status := "RUNNING"
	if req.DryRun {
		status = "DRY_RUN_COMPLETE"
	}

	// Build response
	resp := &pb.GasExtractionOperationResponse{
		ContainerId:    result.ContainerID,
		GasGiant:       result.GasGiant,
		SiphonShips:    req.SiphonShips,
		TransportShips: req.TransportShips,
		Status:         status,
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

// StartConstructionPipeline starts or resumes a construction pipeline for a construction site
func (s *daemonServiceImpl) StartConstructionPipeline(ctx context.Context, req *pb.StartConstructionPipelineRequest) (*pb.StartConstructionPipelineResponse, error) {
	// Resolve player ID from request
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	// Get system symbol (nil pointer means derive from construction site)
	var systemSymbol string
	if req.SystemSymbol != nil {
		systemSymbol = *req.SystemSymbol
	}

	// Get min-supply floor (nil pointer means unset - preserves the default
	// MODERATE floor unchanged; sp-ezz9)
	var minSupply string
	if req.MinSupply != nil {
		minSupply = *req.MinSupply
	}

	// Call daemon's StartConstructionPipeline method
	result, err := s.daemon.StartConstructionPipeline(ctx, req.ConstructionSite, playerID, int(req.SupplyChainDepth), int(req.MaxWorkers), systemSymbol, minSupply)
	if err != nil {
		return nil, fmt.Errorf("failed to start construction pipeline: %w", err)
	}

	// Convert materials to protobuf format
	pbMaterials := make([]*pb.ConstructionMaterial, len(result.Materials))
	for i, mat := range result.Materials {
		pbMaterials[i] = &pb.ConstructionMaterial{
			TradeSymbol: mat.TradeSymbol,
			Required:    mat.Required,
			Fulfilled:   mat.Fulfilled,
			Progress:    mat.Progress,
		}
	}

	return &pb.StartConstructionPipelineResponse{
		PipelineId:        result.PipelineID,
		ConstructionSite:  result.ConstructionSite,
		IsResumed:         result.IsResumed,
		Materials:         pbMaterials,
		TaskCount:         result.TaskCount,
		Status:            result.Status,
		Message:           result.Message,
		DeferredMaterials: result.DeferredMaterials,
	}, nil
}

// GetConstructionStatus retrieves the status of a construction site and any active pipeline
func (s *daemonServiceImpl) GetConstructionStatus(ctx context.Context, req *pb.GetConstructionStatusRequest) (*pb.GetConstructionStatusResponse, error) {
	// Resolve player ID from request
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	// Call daemon's GetConstructionStatus method
	result, err := s.daemon.GetConstructionStatus(ctx, req.ConstructionSite, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get construction status: %w", err)
	}

	// Convert materials to protobuf format
	pbMaterials := make([]*pb.ConstructionMaterial, len(result.Materials))
	for i, mat := range result.Materials {
		pbMaterials[i] = &pb.ConstructionMaterial{
			TradeSymbol: mat.TradeSymbol,
			Required:    mat.Required,
			Fulfilled:   mat.Fulfilled,
			Remaining:   mat.Remaining,
			Progress:    mat.Progress,
		}
	}

	return &pb.GetConstructionStatusResponse{
		ConstructionSite: result.ConstructionSite,
		IsComplete:       result.IsComplete,
		Progress:         result.Progress,
		Materials:        pbMaterials,
		PipelineId:       result.PipelineID,
		PipelineStatus:   result.PipelineStatus,
		PipelineProgress: result.PipelineProgress,
	}, nil
}

// StopConstructionPipeline cancels the active construction pipeline for a site (sp-yzrv)
func (s *daemonServiceImpl) StopConstructionPipeline(ctx context.Context, req *pb.StopConstructionPipelineRequest) (*pb.StopConstructionPipelineResponse, error) {
	// Resolve player ID from request
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	// Call daemon's StopConstructionPipeline method
	result, err := s.daemon.StopConstructionPipeline(ctx, req.ConstructionSite, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to stop construction pipeline: %w", err)
	}

	return &pb.StopConstructionPipelineResponse{
		PipelineId:       result.PipelineID,
		ConstructionSite: result.ConstructionSite,
		Status:           result.Status,
		TasksCancelled:   result.TasksCancelled,
		Message:          result.Message,
	}, nil
}
