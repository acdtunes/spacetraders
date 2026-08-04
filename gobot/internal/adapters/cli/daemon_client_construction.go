package cli

import (
	"context"
	"fmt"

	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

// StartConstructionPipelineResponse contains the result of starting a construction pipeline
type StartConstructionPipelineResponse struct {
	PipelineID       string
	ConstructionSite string
	IsResumed        bool
	Materials        []*ConstructionMaterialResponse
	TaskCount        int32
	Status           string
	Message          string

	// DeferredMaterials names every material (trade symbol) that could not be
	// sourced this call, so the CLI can report the gap by
	// name instead of a generic "no market" message.
	DeferredMaterials []string
}

// ConstructionMaterialResponse represents a construction material status
type ConstructionMaterialResponse struct {
	TradeSymbol string
	Required    int32
	Fulfilled   int32
	Remaining   int32
	Progress    float64
}

// GetConstructionStatusResponse contains the status of a construction site
type GetConstructionStatusResponse struct {
	ConstructionSite string
	IsComplete       bool
	Progress         float64
	Materials        []*ConstructionMaterialResponse
	PipelineID       *string
	PipelineStatus   *string
	PipelineProgress *float64
}

// StopConstructionPipelineResponse contains the result of stopping a construction pipeline
type StopConstructionPipelineResponse struct {
	PipelineID       string
	ConstructionSite string
	Status           string
	TasksCancelled   int32
	Message          string
}

// StartConstructionPipeline starts a pipeline to supply materials to a construction site.
// goodOverrides is the optional JSON-encoded per-good buy-gating override map (sp-sdyo values,
// sp-pdb3 launch surface); nil/empty preserves the global-default floor for every good.
func (c *DaemonClient) StartConstructionPipeline(
	ctx context.Context,
	constructionSite string,
	playerID int32,
	agentSymbol *string,
	supplyChainDepth int32,
	maxWorkers int32,
	systemSymbol *string,
	minSupply *string,
	goodOverrides *string,
) (*StartConstructionPipelineResponse, error) {
	req := &pb.StartConstructionPipelineRequest{
		ConstructionSite: constructionSite,
		PlayerId:         playerID,
		AgentSymbol:      agentSymbol,
		SupplyChainDepth: supplyChainDepth,
		MaxWorkers:       maxWorkers,
		SystemSymbol:     systemSymbol,
		MinSupply:        minSupply,
		GoodOverrides:    goodOverrides,
	}

	resp, err := c.client.StartConstructionPipeline(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	materials := make([]*ConstructionMaterialResponse, len(resp.Materials))
	for i, mat := range resp.Materials {
		materials[i] = &ConstructionMaterialResponse{
			TradeSymbol: mat.TradeSymbol,
			Required:    mat.Required,
			Fulfilled:   mat.Fulfilled,
			Remaining:   mat.Remaining,
			Progress:    mat.Progress,
		}
	}

	return &StartConstructionPipelineResponse{
		PipelineID:        resp.PipelineId,
		ConstructionSite:  resp.ConstructionSite,
		IsResumed:         resp.IsResumed,
		Materials:         materials,
		TaskCount:         resp.TaskCount,
		Status:            resp.Status,
		Message:           resp.Message,
		DeferredMaterials: resp.DeferredMaterials,
	}, nil
}

// GetConstructionStatus retrieves the status of a construction site
func (c *DaemonClient) GetConstructionStatus(
	ctx context.Context,
	constructionSite string,
	playerID int32,
	agentSymbol *string,
) (*GetConstructionStatusResponse, error) {
	req := &pb.GetConstructionStatusRequest{
		ConstructionSite: constructionSite,
		PlayerId:         playerID,
		AgentSymbol:      agentSymbol,
	}

	resp, err := c.client.GetConstructionStatus(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	materials := make([]*ConstructionMaterialResponse, len(resp.Materials))
	for i, mat := range resp.Materials {
		materials[i] = &ConstructionMaterialResponse{
			TradeSymbol: mat.TradeSymbol,
			Required:    mat.Required,
			Fulfilled:   mat.Fulfilled,
			Remaining:   mat.Remaining,
			Progress:    mat.Progress,
		}
	}

	return &GetConstructionStatusResponse{
		ConstructionSite: resp.ConstructionSite,
		IsComplete:       resp.IsComplete,
		Progress:         resp.Progress,
		Materials:        materials,
		PipelineID:       resp.PipelineId,
		PipelineStatus:   resp.PipelineStatus,
		PipelineProgress: resp.PipelineProgress,
	}, nil
}

// StopConstructionPipeline cancels the active construction pipeline for a site
func (c *DaemonClient) StopConstructionPipeline(
	ctx context.Context,
	constructionSite string,
	playerID int32,
	agentSymbol *string,
) (*StopConstructionPipelineResponse, error) {
	req := &pb.StopConstructionPipelineRequest{
		ConstructionSite: constructionSite,
		PlayerId:         playerID,
		AgentSymbol:      agentSymbol,
	}

	resp, err := c.client.StopConstructionPipeline(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return &StopConstructionPipelineResponse{
		PipelineID:       resp.PipelineId,
		ConstructionSite: resp.ConstructionSite,
		Status:           resp.Status,
		TasksCancelled:   resp.TasksCancelled,
		Message:          resp.Message,
	}, nil
}

// ConstructionGoodOverride sets or clears one good's per-good buy-gating override on a running
// construction pipeline live, with no restart. The daemon is the single writer of the
// persisted override (RULINGS #3); the coordinator re-reads it on its next discovery pass.
func (c *DaemonClient) ConstructionGoodOverride(ctx context.Context, req *pb.ConstructionGoodOverrideRequest) (*pb.ConstructionGoodOverrideResponse, error) {
	resp, err := c.client.ConstructionGoodOverride(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}
	return resp, nil
}

// --- Contract depot management (sp-u9xa) ---

// ConstructionWorkerCap sets the concurrent-worker cap (max_workers) on a running construction
// pipeline live, with no pipeline/daemon restart.
func (c *DaemonClient) ConstructionWorkerCap(ctx context.Context, constructionSite string, count int, playerIdent *PlayerIdentifier) (*pb.ConstructionWorkerCapResponse, error) {
	playerID, agentSymbol := playerPointers(playerIdent)
	req := &pb.ConstructionWorkerCapRequest{
		ConstructionSite: constructionSite,
		Count:            int32(count),
		PlayerId:         playerID,
		AgentSymbol:      agentSymbol,
	}

	resp, err := c.client.ConstructionWorkerCap(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return resp, nil
}

// ConstructionDeliveryFloors sends the gate delivery fleet's buy/resume floor tune to the
// daemon. The request is built and validated by the CLI boundary; this only carries it.
func (c *DaemonClient) ConstructionDeliveryFloors(ctx context.Context, req *pb.ConstructionDeliveryFloorsRequest) (*pb.ConstructionDeliveryFloorsResponse, error) {
	resp, err := c.client.ConstructionDeliveryFloors(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}
	return resp, nil
}
