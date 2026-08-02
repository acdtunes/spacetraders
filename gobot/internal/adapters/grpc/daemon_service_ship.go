package grpc

import (
	"context"
	"fmt"

	gasCmd "github.com/andrescamacho/spacetraders-go/internal/application/gas/commands"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipOutfit "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/outfitting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

// NavigateShip initiates ship navigation
func (s *daemonServiceImpl) NavigateShip(ctx context.Context, req *pb.NavigateShipRequest) (*pb.NavigateShipResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	containerID, err := s.daemon.NavigateShip(ctx, req.ShipSymbol, req.Destination, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate ship: %w", err)
	}

	response := &pb.NavigateShipResponse{
		ContainerId:          containerID,
		ShipSymbol:           req.ShipSymbol,
		Destination:          req.Destination,
		Status:               "PENDING",
		EstimatedTimeSeconds: 0, // TODO: Calculate estimated time when routing is wired
	}

	return response, nil
}

// RouteShip initiates cross-system point-to-point travel
func (s *daemonServiceImpl) RouteShip(ctx context.Context, req *pb.RouteShipRequest) (*pb.RouteShipResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	containerID, err := s.daemon.RouteShip(ctx, req.ShipSymbol, req.Destination, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to route ship: %w", err)
	}

	response := &pb.RouteShipResponse{
		ContainerId: containerID,
		ShipSymbol:  req.ShipSymbol,
		Destination: req.Destination,
		Status:      "PENDING",
	}

	return response, nil
}

// WarpShip initiates an off-gate warp
func (s *daemonServiceImpl) WarpShip(ctx context.Context, req *pb.WarpShipRequest) (*pb.WarpShipResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	containerID, err := s.daemon.WarpShip(ctx, req.ShipSymbol, req.Destination, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to warp ship: %w", err)
	}

	response := &pb.WarpShipResponse{
		ContainerId: containerID,
		ShipSymbol:  req.ShipSymbol,
		Destination: req.Destination,
		Status:      "PENDING",
	}

	return response, nil
}

// DockShip docks a ship
func (s *daemonServiceImpl) DockShip(ctx context.Context, req *pb.DockShipRequest) (*pb.DockShipResponse, error) {
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
		// Surface a workflow.failed event so the watchkeeper sees
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

// TransferCargo moves cargo from one hull to another through the EXISTING gas
// TransferCargoCommand — the same command the siphon workers, the stocker and the tour
// deposit already dispatch (RULING #3: the daemon performs the move). It is synchronous
// like the outfit verbs it completes: the move is instantaneous, so there is no progress
// to track and no reason to make the operator go read a container log for the outcome.
//
// A refusal rides back in the response's error field VERBATIM. The two an operator hits
// — hulls at different waypoints, and a receiver with no room — each name their own
// condition in full, and a gRPC status error would bury that under a transport prefix.
func (s *daemonServiceImpl) TransferCargo(ctx context.Context, req *pb.TransferCargoRequest) (*pb.TransferCargoResponse, error) {
	refusal := func(reason string) *pb.TransferCargoResponse {
		return &pb.TransferCargoResponse{
			FromShipSymbol: req.FromShipSymbol,
			ToShipSymbol:   req.ToShipSymbol,
			GoodSymbol:     req.GoodSymbol,
			Error:          reason,
		}
	}

	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return refusal(fmt.Sprintf("failed to resolve player: %v", err)), nil
	}

	cmd := &gasCmd.TransferCargoCommand{
		FromShip:   req.FromShipSymbol,
		ToShip:     req.ToShipSymbol,
		GoodSymbol: req.GoodSymbol,
		Units:      int(req.Units),
		PlayerID:   shared.MustNewPlayerID(playerID),
	}

	result, err := s.daemon.mediator.Send(ctx, cmd)
	if err != nil {
		return refusal(err.Error()), nil
	}
	resp, ok := result.(*gasCmd.TransferCargoResponse)
	if !ok {
		return refusal("unexpected response type from TransferCargoCommand"), nil
	}

	return &pb.TransferCargoResponse{
		Success:          true,
		FromShipSymbol:   req.FromShipSymbol,
		ToShipSymbol:     req.ToShipSymbol,
		GoodSymbol:       req.GoodSymbol,
		UnitsTransferred: int32(resp.UnitsTransferred),
		RemainingUnits:   int32(remainingUnitsOfGood(resp.RemainingCargo, req.GoodSymbol)),
	}, nil
}

// remainingUnitsOfGood reads how much of good is left in a post-transfer cargo hold.
// A hold the API did not report back reads as zero rather than failing the move that
// already committed.
func remainingUnitsOfGood(cargo *navigation.CargoData, good string) int {
	if cargo == nil {
		return 0
	}
	for _, item := range cargo.Inventory {
		if item.Symbol == good {
			return item.Units
		}
	}
	return 0
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

// JettisonCargo jettisons cargo from a ship
func (s *daemonServiceImpl) JettisonCargo(ctx context.Context, req *pb.JettisonCargoRequest) (*pb.JettisonCargoResponse, error) {
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
