package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

// StartConstructionPipeline starts or resumes a construction pipeline for a construction site
func (s *daemonServiceImpl) StartConstructionPipeline(ctx context.Context, req *pb.StartConstructionPipelineRequest) (*pb.StartConstructionPipelineResponse, error) {
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

	// Decode the optional per-good buy-gating overrides (sp-sdyo values, launch surface).
	// The CLI encodes the validated+clamped GoodGatingOverrides map to JSON in good_overrides; an
	// unset/empty field decodes to nil, preserving the global-default behaviour for every good. A
	// malformed blob is a hard error rather than a silently-dropped bottleneck override.
	var goodOverrides manufacturing.GoodGatingOverrides
	if req.GoodOverrides != nil {
		goodOverrides, err = manufacturing.DecodeGoodGatingOverrides(*req.GoodOverrides)
		if err != nil {
			return nil, fmt.Errorf("invalid good_overrides: %w", err)
		}
	}

	// sp-sdyo plumbing persists/reloads the decoded per-good overrides on the pipeline;
	// nil/empty keeps every good on the global floor.
	result, err := s.daemon.StartConstructionPipeline(ctx, req.ConstructionSite, playerID, int(req.SupplyChainDepth), int(req.MaxWorkers), systemSymbol, minSupply, goodOverrides)
	if err != nil {
		return nil, fmt.Errorf("failed to start construction pipeline: %w", err)
	}

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
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	result, err := s.daemon.GetConstructionStatus(ctx, req.ConstructionSite, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get construction status: %w", err)
	}

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

// StopConstructionPipeline cancels the active construction pipeline for a site
func (s *daemonServiceImpl) StopConstructionPipeline(ctx context.Context, req *pb.StopConstructionPipelineRequest) (*pb.StopConstructionPipelineResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

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

// ConstructionGoodOverride sets or clears one good's per-good buy-gating override on a running
// construction pipeline live. It resolves the player, builds a patch from the optional
// request knobs (a nil field leaves that dimension unchanged so an operator can tune one at a
// time), and delegates the persisted-map mutation to the daemon — the single writer (RULINGS #3).
// The multiplier is clamped to the domain hard cap inside the mutation (RULINGS #4). The
// coordinator re-reads the persisted overrides on its next discovery pass — no restart.
func (s *daemonServiceImpl) ConstructionGoodOverride(ctx context.Context, req *pb.ConstructionGoodOverrideRequest) (*pb.ConstructionGoodOverrideResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	patch := goodOverridePatch{
		minSupply:        req.MinSupply,
		priceCeilingMult: req.PriceCeilingMult,
	}

	result, err := s.daemon.MutateConstructionGoodOverride(ctx, req.ConstructionSite, playerID, req.Good, patch, req.Clear)
	if err != nil {
		return nil, fmt.Errorf("failed to set construction good override: %w", err)
	}

	return &pb.ConstructionGoodOverrideResponse{
		ConstructionSite: result.ConstructionSite,
		Good:             result.Good,
		Cleared:          result.Cleared,
		Changed:          result.Changed,
		PriceCeilingMult: result.Override.PriceCeilingMult,
		MinSupply:        result.Override.MinSupply,
	}, nil
}

// ConstructionWorkerCap sets the live concurrent-worker cap (max_workers) on a running construction
// pipeline. Resolves the player from player_id or agent_symbol (like the other coordinator
// RPCs), then delegates the persisted-row mutation to the daemon — the single writer (RULINGS #3).
// The running drain re-reads the cap each tick and converges its fan-out to N with no restart.
func (s *daemonServiceImpl) ConstructionWorkerCap(ctx context.Context, req *pb.ConstructionWorkerCapRequest) (*pb.ConstructionWorkerCapResponse, error) {
	var pid int32
	if req.PlayerId != nil {
		pid = *req.PlayerId
	}
	playerID, err := s.resolvePlayerID(ctx, pid, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	result, err := s.daemon.MutateConstructionMaxWorkers(ctx, req.ConstructionSite, playerID, int(req.Count))
	if err != nil {
		return nil, fmt.Errorf("failed to set construction worker cap: %w", err)
	}

	return &pb.ConstructionWorkerCapResponse{
		ConstructionSite: result.ConstructionSite,
		WorkerCap:        int32(result.WorkerCap),
		Changed:          result.Changed,
	}, nil
}

// ConstructionDeliveryFloors sets the gate delivery fleet's supply buy/resume thresholds on
// a running construction pipeline. Resolves the player like the sibling construction RPCs,
// then delegates the persisted-row mutation to the daemon — the single writer (RULINGS #3).
// The drain re-reads the floors off the pipeline row on every leg, so the tune converges on
// the next leg with no restart.
func (s *daemonServiceImpl) ConstructionDeliveryFloors(ctx context.Context, req *pb.ConstructionDeliveryFloorsRequest) (*pb.ConstructionDeliveryFloorsResponse, error) {
	var pid int32
	if req.PlayerId != nil {
		pid = *req.PlayerId
	}
	playerID, err := s.resolvePlayerID(ctx, pid, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	buyFloor, resumeFloor := "", ""
	if req.BuyFloor != nil {
		buyFloor = *req.BuyFloor
	}
	if req.ResumeFloor != nil {
		resumeFloor = *req.ResumeFloor
	}

	result, err := s.daemon.MutateConstructionDeliveryFloors(ctx, req.ConstructionSite, playerID, buyFloor, resumeFloor)
	if err != nil {
		return nil, fmt.Errorf("failed to set construction delivery floors: %w", err)
	}

	return &pb.ConstructionDeliveryFloorsResponse{
		ConstructionSite:     result.ConstructionSite,
		BuyFloor:             result.BuyFloor,
		ResumeFloor:          result.ResumeFloor,
		BuyFloorIsDefault:    result.BuyFloorIsDefault,
		ResumeFloorIsDefault: result.ResumeFloorIsDefault,
		Changed:              result.Changed,
	}, nil
}
