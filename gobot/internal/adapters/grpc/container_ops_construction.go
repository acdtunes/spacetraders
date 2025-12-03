package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

// StartConstructionPipelineResult contains the result of starting a construction pipeline.
type StartConstructionPipelineResult struct {
	PipelineID       string
	ConstructionSite string
	IsResumed        bool
	Materials        []ConstructionMaterialResult
	TaskCount        int32
	Status           string
	Message          string
}

// ConstructionMaterialResult represents material progress info.
type ConstructionMaterialResult struct {
	TradeSymbol string
	Required    int32
	Fulfilled   int32
	Remaining   int32
	Progress    float64
}

// GetConstructionStatusResult contains construction site status info.
type GetConstructionStatusResult struct {
	ConstructionSite string
	IsComplete       bool
	Progress         float64
	Materials        []ConstructionMaterialResult
	PipelineID       *string
	PipelineStatus   *string
	PipelineProgress *float64
}

// StartConstructionPipeline starts or resumes a construction pipeline for a construction site.
func (s *DaemonServer) StartConstructionPipeline(ctx context.Context, constructionSite string, playerID int, supplyChainDepth int, maxWorkers int, systemSymbol string) (*StartConstructionPipelineResult, error) {
	// Create dependencies for ConstructionPipelinePlanner
	pipelineRepo := persistence.NewGormManufacturingPipelineRepository(s.db)
	constructionRepo := api.NewConstructionSiteRepository(
		s.getAPIClient(),
		s.playerRepo,
	)
	apiClient := s.getAPIClient()
	marketLocator := services.NewMarketLocator(
		persistence.NewMarketRepository(s.db),
		s.waypointRepo,
		s.playerRepo,
		apiClient,
	)

	// Create planner
	planner := services.NewConstructionPipelinePlanner(
		pipelineRepo,
		constructionRepo,
		marketLocator,
	)

	// Start or resume pipeline
	result, err := planner.StartOrResume(ctx, playerID, constructionSite, supplyChainDepth, maxWorkers, systemSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to start construction pipeline: %w", err)
	}

	// Convert materials to response format
	materials := make([]ConstructionMaterialResult, len(result.Pipeline.Materials()))
	for i, mat := range result.Pipeline.Materials() {
		materials[i] = ConstructionMaterialResult{
			TradeSymbol: mat.TradeSymbol(),
			Required:    int32(mat.TargetQuantity()),
			Fulfilled:   int32(mat.DeliveredQuantity()),
			Remaining:   int32(mat.RemainingQuantity()),
			Progress:    mat.Progress(),
		}
	}

	// Build status message
	status := string(result.Pipeline.Status())
	message := ""
	if result.IsResumed {
		message = fmt.Sprintf("Resumed existing pipeline for %s", constructionSite)
	} else {
		message = fmt.Sprintf("Created new pipeline with %d tasks for %s", result.Pipeline.TaskCount(), constructionSite)
	}

	return &StartConstructionPipelineResult{
		PipelineID:       result.Pipeline.ID(),
		ConstructionSite: constructionSite,
		IsResumed:        result.IsResumed,
		Materials:        materials,
		TaskCount:        int32(result.Pipeline.TaskCount()),
		Status:           status,
		Message:          message,
	}, nil
}

// GetConstructionStatus retrieves the status of a construction site and any active pipeline.
func (s *DaemonServer) GetConstructionStatus(ctx context.Context, constructionSite string, playerID int) (*GetConstructionStatusResult, error) {
	// Create dependencies
	constructionRepo := api.NewConstructionSiteRepository(
		s.getAPIClient(),
		s.playerRepo,
	)
	pipelineRepo := persistence.NewGormManufacturingPipelineRepository(s.db)

	// Get construction site data from API
	site, err := constructionRepo.FindByWaypoint(ctx, constructionSite, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get construction site: %w", err)
	}

	// Convert materials to response format
	siteMaterials := site.Materials()
	materials := make([]ConstructionMaterialResult, len(siteMaterials))
	for i, mat := range siteMaterials {
		materials[i] = ConstructionMaterialResult{
			TradeSymbol: mat.TradeSymbol(),
			Required:    int32(mat.Required()),
			Fulfilled:   int32(mat.Fulfilled()),
			Remaining:   int32(mat.Remaining()),
			Progress:    mat.Progress(),
		}
	}

	result := &GetConstructionStatusResult{
		ConstructionSite: constructionSite,
		IsComplete:       site.IsComplete(),
		Progress:         site.Progress(),
		Materials:        materials,
	}

	// Check for active pipeline
	pipeline, err := pipelineRepo.FindByConstructionSite(ctx, constructionSite, playerID)
	if err != nil {
		// Non-fatal - just log and continue without pipeline info
		fmt.Printf("Warning: failed to check for pipeline: %v\n", err)
	} else if pipeline != nil {
		pipelineID := pipeline.ID()
		pipelineStatus := string(pipeline.Status())
		pipelineProgress := pipeline.ConstructionProgress()

		result.PipelineID = &pipelineID
		result.PipelineStatus = &pipelineStatus
		result.PipelineProgress = &pipelineProgress
	}

	return result, nil
}

// getAPIClient returns an API client for construction operations.
// SpaceTradersClient is stateless, so creating a new instance is safe.
func (s *DaemonServer) getAPIClient() domainPorts.APIClient {
	return api.NewSpaceTradersClient()
}
