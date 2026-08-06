package grpc

import (
	"context"
	"fmt"

	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// BatchContractWorkflow executes batch contract workflow
func (s *daemonServiceImpl) BatchContractWorkflow(ctx context.Context, req *pb.BatchContractWorkflowRequest) (*pb.BatchContractWorkflowResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	// iterations selects single-shot vs continuous loop (sp-ehg9). The CLI sends
	// -1 for `--loop`; an unset/0 value (every previous caller) maps to 1 so
	// the plain verb stays byte-identical single-shot.
	iterations := int(req.GetIterations())
	if iterations == 0 {
		iterations = 1
	}

	containerID, err := s.daemon.BatchContractWorkflow(ctx, req.ShipSymbol, playerID, iterations)
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
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	// No ship symbols needed - coordinator discovers idle haulers dynamically.
	// dedicated_ships/standby_stations are optional operator params
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

// TradeFleetCoordinator starts the standing trade-fleet coordinator (sp-1278). The
// agent symbol is threaded through to each tour launch so the tour can live-read the
// agent's treasury for its 25%-of-treasury spend cap; the CLI always supplies it.
func (s *daemonServiceImpl) TradeFleetCoordinator(ctx context.Context, req *pb.TradeFleetCoordinatorRequest) (*pb.TradeFleetCoordinatorResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	agentSymbol := ""
	if req.AgentSymbol != nil {
		agentSymbol = *req.AgentSymbol
	}

	containerID, err := s.daemon.TradeFleetCoordinator(ctx, playerID, agentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to start trade fleet coordinator: %w", err)
	}

	return &pb.TradeFleetCoordinatorResponse{ContainerId: containerID, Status: "RUNNING"}, nil
}

// FleetAutosizerCoordinator starts the standing fleet capacity autosizer (sp-1txd).
func (s *daemonServiceImpl) FleetAutosizerCoordinator(ctx context.Context, req *pb.FleetAutosizerCoordinatorRequest) (*pb.FleetAutosizerCoordinatorResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	agentSymbol := ""
	if req.AgentSymbol != nil {
		agentSymbol = *req.AgentSymbol
	}

	containerID, err := s.daemon.FleetAutosizerCoordinator(ctx, playerID, agentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to start fleet autosizer coordinator: %w", err)
	}

	return &pb.FleetAutosizerCoordinatorResponse{ContainerId: containerID, Status: "RUNNING"}, nil
}

// FleetGrowthCoordinator starts the standing fleet-growth coordinator: the fleet's only heavy buyer.
func (s *daemonServiceImpl) FleetGrowthCoordinator(ctx context.Context, req *pb.FleetGrowthCoordinatorRequest) (*pb.FleetGrowthCoordinatorResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	agentSymbol := ""
	if req.AgentSymbol != nil {
		agentSymbol = *req.AgentSymbol
	}

	containerID, err := s.daemon.FleetGrowthCoordinator(ctx, playerID, agentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to start fleet growth coordinator: %w", err)
	}

	return &pb.FleetGrowthCoordinatorResponse{ContainerId: containerID, Status: "RUNNING"}, nil
}

// LongHaulArbCoordinator starts the standing long-haul arb fleet coordinator (sp-mepj): the
// out-of-horizon arb engine that launches a per-hull worker on every idle long-haul-tagged hull.
// Identity-only launch; idempotent on its own container type.
func (s *daemonServiceImpl) LongHaulArbCoordinator(ctx context.Context, req *pb.LongHaulArbCoordinatorRequest) (*pb.LongHaulArbCoordinatorResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	agentSymbol := ""
	if req.AgentSymbol != nil {
		agentSymbol = *req.AgentSymbol
	}

	containerID, err := s.daemon.LongHaulArbCoordinator(ctx, playerID, agentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to start long-haul arb coordinator: %w", err)
	}

	return &pb.LongHaulArbCoordinatorResponse{ContainerId: containerID, Status: "RUNNING"}, nil
}

// BootstrapCoordinator starts the standing captain bootstrap coordinator (sp-3nbe).
func (s *daemonServiceImpl) BootstrapCoordinator(ctx context.Context, req *pb.BootstrapCoordinatorRequest) (*pb.BootstrapCoordinatorResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	agentSymbol := ""
	if req.AgentSymbol != nil {
		agentSymbol = *req.AgentSymbol
	}

	containerID, err := s.daemon.BootstrapCoordinator(ctx, playerID, agentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to start bootstrap coordinator: %w", err)
	}

	return &pb.BootstrapCoordinatorResponse{ContainerId: containerID, Status: "RUNNING"}, nil
}

// CapacityReconcilerCoordinator refuses the call: the dedicated contract scaler owns contract-fleet
// capacity. Retained only to satisfy the generated DaemonServiceServer interface.
func (s *daemonServiceImpl) CapacityReconcilerCoordinator(ctx context.Context, req *pb.CapacityReconcilerCoordinatorRequest) (*pb.CapacityReconcilerCoordinatorResponse, error) {
	return nil, fmt.Errorf("capacity reconciler removed (sp-y2ptq): the dedicated contract scaler owns contract-fleet capacity")
}

// AutoOutfitCoordinator starts the standing guarded auto-outfit coordinator (sp-buyd):
// the module analogue of hull acquisition. EXPLICIT START ONLY (deploy-inert).
func (s *daemonServiceImpl) AutoOutfitCoordinator(ctx context.Context, req *pb.AutoOutfitCoordinatorRequest) (*pb.AutoOutfitCoordinatorResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	containerID, err := s.daemon.AutoOutfitCoordinator(ctx, playerID, req.DryRun)
	if err != nil {
		return nil, fmt.Errorf("failed to start auto-outfit coordinator: %w", err)
	}

	return &pb.AutoOutfitCoordinatorResponse{ContainerId: containerID, Status: "RUNNING"}, nil
}

// FrontierExpansionCoordinator is retired; the engine was deleted. Always fails.
func (s *daemonServiceImpl) FrontierExpansionCoordinator(ctx context.Context, req *pb.FrontierExpansionCoordinatorRequest) (*pb.FrontierExpansionCoordinatorResponse, error) {
	return nil, fmt.Errorf("frontier expansion coordinator is retired; probe sensing supersedes it")
}

// ShipyardBackfillCoordinator starts the standing shipyard-backfill sweep (sp-s1ek — the launch
// verb for the sp-rhju engine). EXPLICIT START ONLY — never boot-standing-armed (deploy-inert).
func (s *daemonServiceImpl) ShipyardBackfillCoordinator(ctx context.Context, req *pb.ShipyardBackfillCoordinatorRequest) (*pb.ShipyardBackfillCoordinatorResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	containerID, err := s.daemon.ShipyardBackfillCoordinator(
		ctx,
		playerID,
		int(req.TickIntervalSecs),
		int(req.MaxDispatchesPerCycle),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to start shipyard backfill coordinator: %w", err)
	}

	return &pb.ShipyardBackfillCoordinatorResponse{ContainerId: containerID, Status: "RUNNING"}, nil
}
