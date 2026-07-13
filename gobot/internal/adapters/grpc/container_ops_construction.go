package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
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

	// DeferredMaterials names every material (trade symbol) that could not be
	// sourced this call (sp-560b/sp-ooba). Each still has a visible PENDING
	// task that the SupplyMonitor re-sources when supply regenerates; this
	// lets the caller report the gap by name instead of a generic message.
	DeferredMaterials []string
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
// minSupply is the caller-set EXPORT sourcing floor (sp-ezz9), e.g. "SCARCE";
// empty string means unset, preserving the original MODERATE default.
// goodOverrides carries the per-good buy-gating overrides (sp-sdyo): a per-good MinSupply loosens
// the sourcing floor for a single bottleneck good while every other material keeps the global
// floor. The map is persisted on the pipeline so it survives a restart (RULINGS #2). Nil/empty
// preserves today's behaviour for every good.
func (s *DaemonServer) StartConstructionPipeline(ctx context.Context, constructionSite string, playerID int, supplyChainDepth int, maxWorkers int, systemSymbol string, minSupply string, goodOverrides manufacturing.GoodGatingOverrides) (*StartConstructionPipelineResult, error) {
	// Create dependencies for ConstructionPipelinePlanner
	pipelineRepo := persistence.NewGormManufacturingPipelineRepository(s.db)
	taskRepo := persistence.NewGormManufacturingTaskRepository(s.db)
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
		taskRepo,
		constructionRepo,
		marketLocator,
		s.shipRepo,
		s.clock,
	)

	// Start or resume pipeline
	result, err := planner.StartOrResume(ctx, playerID, constructionSite, supplyChainDepth, maxWorkers, systemSymbol, minSupply, goodOverrides)
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
		PipelineID:        result.Pipeline.ID(),
		ConstructionSite:  constructionSite,
		IsResumed:         result.IsResumed,
		Materials:         materials,
		TaskCount:         int32(result.Pipeline.TaskCount()),
		Status:            status,
		Message:           message,
		DeferredMaterials: result.DeferredMaterials,
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

// StopConstructionPipelineResult contains the result of stopping a construction pipeline.
type StopConstructionPipelineResult struct {
	PipelineID       string
	ConstructionSite string
	Status           string
	TasksCancelled   int32
	Message          string
}

// StopConstructionPipeline cancels the active construction pipeline for a site (sp-yzrv).
// Returns a clear error if no active (non-terminal) construction pipeline exists for
// the site - this covers both "never started" and "already stopped" uniformly.
func (s *DaemonServer) StopConstructionPipeline(ctx context.Context, constructionSite string, playerID int) (*StopConstructionPipelineResult, error) {
	pipelineRepo := persistence.NewGormManufacturingPipelineRepository(s.db)
	taskRepo := persistence.NewGormManufacturingTaskRepository(s.db)

	planner := services.NewConstructionPipelinePlanner(
		pipelineRepo,
		taskRepo,
		nil,
		nil,
		s.shipRepo,
		s.clock,
	)

	result, err := planner.Stop(ctx, playerID, constructionSite)
	if err != nil {
		return nil, fmt.Errorf("failed to stop construction pipeline: %w", err)
	}

	return &StopConstructionPipelineResult{
		PipelineID:       result.Pipeline.ID(),
		ConstructionSite: constructionSite,
		Status:           string(result.Pipeline.Status()),
		TasksCancelled:   int32(result.TasksCancelled),
		Message:          fmt.Sprintf("Stopped construction pipeline for %s (%d tasks cancelled)", constructionSite, result.TasksCancelled),
	}, nil
}

// ConstructionCoordinator starts the standing construction-supply drain (sp-382j): a
// recovery-safe container that each tick sources and delivers a gate-construction pipeline's
// READY DELIVER_TO_CONSTRUCTION tasks to their site on the shared ProductionExecutor engine.
// It mirrors WorkerRebalancerCoordinator's shape (build config → buildCommandForType so
// creation and recovery share one builder → NewContainer with iterations=-1 for the infinite
// drain loop → Add → runner → registerContainer → go Start). The launch config carries only the
// operating system + identity; the drain re-polls READY tasks from persistence every tick, so a
// restart resumes supply with no other state (RULINGS #2). This is the dedicated executor the
// bootstrap GATE adoption check looks for, closing the post-jav2 gate-construction execution gap.
func (s *DaemonServer) ConstructionCoordinator(ctx context.Context, playerID int, systemSymbol string) (string, error) {
	containerID := utils.GenerateContainerID("construction_coordinator", fmt.Sprintf("player-%d", playerID))

	config := map[string]interface{}{
		"container_id":  containerID,
		"system_symbol": systemSymbol,
	}

	cmd, err := s.buildCommandForType("construction_coordinator", config, playerID, containerID)
	if err != nil {
		return "", fmt.Errorf("failed to create construction coordinator command: %w", err)
	}

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeConstructionCoordinator,
		playerID,
		-1,  // Infinite iterations (drain loop) — NOT a CoordinatorOwnsIterations type
		nil, // No parent container
		config,
		nil,
	)

	if err := s.containerRepo.Add(ctx, containerEntity, "construction_coordinator"); err != nil {
		return "", fmt.Errorf("failed to persist construction coordinator container: %w", err)
	}

	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipRepo, s.clock)
	s.registerContainer(containerID, runner)

	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Construction coordinator container %s failed: %v\n", containerID, err)
		}
	}()

	return containerID, nil
}

// getAPIClient returns the shared API client for construction operations.
func (s *DaemonServer) getAPIClient() domainPorts.APIClient {
	return s.apiClient
}
