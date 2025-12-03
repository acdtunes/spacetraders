package services

import (
	"context"
	"fmt"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// ConstructionPipelinePlanner creates and manages construction pipelines.
// It handles:
//   - Idempotent creation (checks for existing pipeline)
//   - Auto-discovery of construction site requirements
//   - Task creation based on supply chain depth
//   - DELIVER_TO_CONSTRUCTION tasks for final delivery
type ConstructionPipelinePlanner struct {
	pipelineRepo     manufacturing.PipelineRepository
	constructionRepo manufacturing.ConstructionSiteRepository
	marketLocator    *MarketLocator
}

// NewConstructionPipelinePlanner creates a new construction pipeline planner.
func NewConstructionPipelinePlanner(
	pipelineRepo manufacturing.PipelineRepository,
	constructionRepo manufacturing.ConstructionSiteRepository,
	marketLocator *MarketLocator,
) *ConstructionPipelinePlanner {
	return &ConstructionPipelinePlanner{
		pipelineRepo:     pipelineRepo,
		constructionRepo: constructionRepo,
		marketLocator:    marketLocator,
	}
}

// StartOrResumeResult contains the result of starting or resuming a pipeline.
type StartOrResumeResult struct {
	Pipeline  *manufacturing.ManufacturingPipeline
	IsResumed bool // true if resuming existing, false if newly created
}

// StartOrResume starts a new construction pipeline or resumes an existing one.
// This method is IDEMPOTENT - calling it multiple times with the same construction site
// will return the existing pipeline instead of creating a new one.
//
// Parameters:
//   - constructionSite: The waypoint symbol of the construction site (e.g., "X1-FB5-I61")
//   - supplyChainDepth: How deep to go in the supply chain (0=full, 1=raw only, 2=intermediates, 3=buy final)
//   - maxWorkers: Maximum parallel workers (0=unlimited, default 5)
//   - systemSymbol: System to search for markets (empty string = derive from constructionSite)
func (p *ConstructionPipelinePlanner) StartOrResume(
	ctx context.Context,
	playerID int,
	constructionSite string,
	supplyChainDepth int,
	maxWorkers int,
	systemSymbol string,
) (*StartOrResumeResult, error) {
	logger := common.LoggerFromContext(ctx)

	// 1. IDEMPOTENCY CHECK: Check if pipeline already exists for this construction site
	existingPipeline, err := p.pipelineRepo.FindByConstructionSite(ctx, constructionSite, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to check for existing pipeline: %w", err)
	}

	if existingPipeline != nil {
		logger.Log("INFO", "Resuming existing construction pipeline", map[string]interface{}{
			"pipeline_id":       existingPipeline.ID(),
			"construction_site": constructionSite,
			"status":            existingPipeline.Status(),
			"progress":          fmt.Sprintf("%.1f%%", existingPipeline.ConstructionProgress()),
		})
		return &StartOrResumeResult{
			Pipeline:  existingPipeline,
			IsResumed: true,
		}, nil
	}

	// 2. AUTO-DISCOVERY: Fetch construction requirements from API
	logger.Log("INFO", "Fetching construction site requirements", map[string]interface{}{
		"construction_site": constructionSite,
		"player_id":         playerID,
	})

	constructionSiteData, err := p.constructionRepo.FindByWaypoint(ctx, constructionSite, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch construction site data: %w", err)
	}

	if constructionSiteData.IsComplete() {
		return nil, fmt.Errorf("construction site %s is already complete", constructionSite)
	}

	// 3. Get unfulfilled materials (only materials that still need delivery)
	unfulfilledMaterials := constructionSiteData.UnfulfilledMaterials()
	if len(unfulfilledMaterials) == 0 {
		return nil, fmt.Errorf("construction site %s has no remaining materials to deliver", constructionSite)
	}

	logger.Log("INFO", "Found construction materials to deliver", map[string]interface{}{
		"construction_site": constructionSite,
		"materials_count":   len(unfulfilledMaterials),
	})

	// 4. Create pipeline
	pipeline := manufacturing.NewConstructionPipeline(constructionSite, playerID, supplyChainDepth, maxWorkers)

	// 5. Add material targets to pipeline
	for _, mat := range unfulfilledMaterials {
		remaining := mat.Remaining()
		materialTarget := manufacturing.NewConstructionMaterialTarget(mat.TradeSymbol(), remaining)
		if err := pipeline.AddMaterial(materialTarget); err != nil {
			return nil, fmt.Errorf("failed to add material %s: %w", mat.TradeSymbol(), err)
		}

		logger.Log("INFO", "Added material target to pipeline", map[string]interface{}{
			"material":  mat.TradeSymbol(),
			"remaining": remaining,
		})
	}

	// 6. Determine system symbol - use provided value or derive from waypoint
	if systemSymbol == "" {
		systemSymbol = extractSystemSymbol(constructionSite)
	}

	// 7. Create tasks for each material
	for _, mat := range unfulfilledMaterials {
		if err := p.createTasksForMaterial(ctx, pipeline, mat.TradeSymbol(), systemSymbol, constructionSite, supplyChainDepth, playerID); err != nil {
			return nil, fmt.Errorf("failed to create tasks for %s: %w", mat.TradeSymbol(), err)
		}
	}

	// 8. Persist pipeline
	if err := p.pipelineRepo.Create(ctx, pipeline); err != nil {
		return nil, fmt.Errorf("failed to save pipeline: %w", err)
	}

	logger.Log("INFO", "Created new construction pipeline", map[string]interface{}{
		"pipeline_id":       pipeline.ID(),
		"construction_site": constructionSite,
		"materials_count":   len(unfulfilledMaterials),
		"task_count":        pipeline.TaskCount(),
		"supply_chain_depth": supplyChainDepth,
	})

	return &StartOrResumeResult{
		Pipeline:  pipeline,
		IsResumed: false,
	}, nil
}

// createTasksForMaterial creates tasks for producing and delivering a single material.
// Based on supply chain depth:
//   - 0: Full production (mine/produce everything)
//   - 1: Buy raw materials only
//   - 2: Buy intermediate goods
//   - 3: Buy final product (no production)
func (p *ConstructionPipelinePlanner) createTasksForMaterial(
	ctx context.Context,
	pipeline *manufacturing.ManufacturingPipeline,
	targetGood string,
	systemSymbol string,
	constructionSite string,
	supplyChainDepth int,
	playerID int,
) error {
	logger := common.LoggerFromContext(ctx)

	// For depth 3 (buy final product), just create DELIVER_TO_CONSTRUCTION directly
	if supplyChainDepth >= 3 || goods.IsRawMaterial(targetGood) {
		// Find market to buy from
		market, err := p.marketLocator.FindExportMarketWithGoodSupply(ctx, targetGood, systemSymbol, playerID)
		if err != nil {
			return fmt.Errorf("failed to find market for %s: %w", targetGood, err)
		}
		if market == nil {
			return fmt.Errorf("no market with good supply for %s", targetGood)
		}

		// Create DELIVER_TO_CONSTRUCTION task (buy from market, deliver to construction)
		task := manufacturing.NewDeliverToConstructionTask(
			pipeline.ID(),
			playerID,
			targetGood,
			market.WaypointSymbol, // sourceMarket
			"",                    // factorySymbol (not used - buying from market)
			constructionSite,
			[]string{}, // No dependencies
		)
		if err := pipeline.AddTask(task); err != nil {
			return fmt.Errorf("failed to add task: %w", err)
		}

		logger.Log("DEBUG", "Created DELIVER_TO_CONSTRUCTION task (buy from market)", map[string]interface{}{
			"good":              targetGood,
			"source_market":     market.WaypointSymbol,
			"construction_site": constructionSite,
		})

		return nil
	}

	// For other depths, we need to walk the supply chain and create tasks
	// This is a simplified version - for a complete implementation, we would
	// need to track factory locations and dependencies more carefully

	// Get required inputs for this good
	inputs := goods.GetRequiredInputs(targetGood)
	if len(inputs) == 0 {
		// No inputs means this is a raw material - treat like depth 3
		return p.createBuyAndDeliverTask(ctx, pipeline, targetGood, systemSymbol, constructionSite, playerID)
	}

	// Find factory that produces this good
	factory, err := p.marketLocator.FindFactoryForProduction(ctx, targetGood, inputs, systemSymbol, playerID)
	if err != nil {
		return fmt.Errorf("failed to find factory for %s: %w", targetGood, err)
	}

	// Create ACQUIRE_DELIVER tasks for each input
	inputTaskIDs := make([]string, 0, len(inputs))
	for _, input := range inputs {
		var inputTaskID string

		// Recursively handle inputs based on depth
		if supplyChainDepth >= 2 {
			// Buy intermediate - just buy the input
			inputTaskID, err = p.createAcquireDeliverTask(ctx, pipeline, input, systemSymbol, factory.WaypointSymbol, playerID)
		} else if supplyChainDepth >= 1 {
			// Buy raw only - check if input is raw or needs production
			if goods.IsRawMaterial(input) {
				inputTaskID, err = p.createAcquireDeliverTask(ctx, pipeline, input, systemSymbol, factory.WaypointSymbol, playerID)
			} else {
				// Input needs production - recurse
				err = p.createProductionTasksForInput(ctx, pipeline, input, systemSymbol, factory.WaypointSymbol, supplyChainDepth, playerID, &inputTaskIDs)
				// The task IDs are added by the recursive call
				continue
			}
		} else {
			// Full production (depth 0) - recurse for everything
			err = p.createProductionTasksForInput(ctx, pipeline, input, systemSymbol, factory.WaypointSymbol, supplyChainDepth, playerID, &inputTaskIDs)
			continue
		}

		if err != nil {
			return fmt.Errorf("failed to create task for input %s: %w", input, err)
		}
		if inputTaskID != "" {
			inputTaskIDs = append(inputTaskIDs, inputTaskID)
		}
	}

	// Create DELIVER_TO_CONSTRUCTION task (collect from factory, deliver to construction)
	task := manufacturing.NewDeliverToConstructionTask(
		pipeline.ID(),
		playerID,
		targetGood,
		"",                     // sourceMarket (not used - collecting from factory)
		factory.WaypointSymbol, // factorySymbol
		constructionSite,
		inputTaskIDs, // Depends on input deliveries
	)
	if err := pipeline.AddTask(task); err != nil {
		return fmt.Errorf("failed to add DELIVER_TO_CONSTRUCTION task: %w", err)
	}

	logger.Log("DEBUG", "Created DELIVER_TO_CONSTRUCTION task (from factory)", map[string]interface{}{
		"good":              targetGood,
		"factory":           factory.WaypointSymbol,
		"construction_site": constructionSite,
		"dependencies":      len(inputTaskIDs),
	})

	return nil
}

// createBuyAndDeliverTask creates a simple buy-and-deliver task for raw materials.
func (p *ConstructionPipelinePlanner) createBuyAndDeliverTask(
	ctx context.Context,
	pipeline *manufacturing.ManufacturingPipeline,
	good string,
	systemSymbol string,
	constructionSite string,
	playerID int,
) error {
	market, err := p.marketLocator.FindExportMarketWithGoodSupply(ctx, good, systemSymbol, playerID)
	if err != nil {
		return fmt.Errorf("failed to find market for %s: %w", good, err)
	}
	if market == nil {
		return fmt.Errorf("no market with good supply for %s", good)
	}

	task := manufacturing.NewDeliverToConstructionTask(
		pipeline.ID(),
		playerID,
		good,
		market.WaypointSymbol,
		"",
		constructionSite,
		[]string{},
	)
	return pipeline.AddTask(task)
}

// createAcquireDeliverTask creates an ACQUIRE_DELIVER task to buy from market and deliver to factory.
func (p *ConstructionPipelinePlanner) createAcquireDeliverTask(
	ctx context.Context,
	pipeline *manufacturing.ManufacturingPipeline,
	good string,
	systemSymbol string,
	factorySymbol string,
	playerID int,
) (string, error) {
	market, err := p.marketLocator.FindExportMarketWithGoodSupply(ctx, good, systemSymbol, playerID)
	if err != nil {
		return "", fmt.Errorf("failed to find market for %s: %w", good, err)
	}
	if market == nil {
		return "", fmt.Errorf("no market with good supply for %s", good)
	}

	task := manufacturing.NewAcquireDeliverTask(
		pipeline.ID(),
		playerID,
		good,
		market.WaypointSymbol,
		factorySymbol,
		nil, // No dependencies for raw acquisition
	)
	if err := pipeline.AddTask(task); err != nil {
		return "", err
	}
	return task.ID(), nil
}

// createProductionTasksForInput recursively creates tasks for producing an intermediate good.
func (p *ConstructionPipelinePlanner) createProductionTasksForInput(
	ctx context.Context,
	pipeline *manufacturing.ManufacturingPipeline,
	good string,
	systemSymbol string,
	deliveryDestination string,
	supplyChainDepth int,
	playerID int,
	parentTaskIDs *[]string,
) error {
	logger := common.LoggerFromContext(ctx)

	inputs := goods.GetRequiredInputs(good)
	if len(inputs) == 0 {
		// Raw material - just buy and deliver
		taskID, err := p.createAcquireDeliverTask(ctx, pipeline, good, systemSymbol, deliveryDestination, playerID)
		if err != nil {
			return err
		}
		*parentTaskIDs = append(*parentTaskIDs, taskID)
		return nil
	}

	// Find factory that produces this good
	factory, err := p.marketLocator.FindFactoryForProduction(ctx, good, inputs, systemSymbol, playerID)
	if err != nil {
		return fmt.Errorf("failed to find factory for %s: %w", good, err)
	}

	// Create tasks for each input
	inputTaskIDs := make([]string, 0, len(inputs))
	for _, input := range inputs {
		if goods.IsRawMaterial(input) || (supplyChainDepth >= 1 && goods.IsRawMaterial(input)) {
			// Buy raw material
			taskID, err := p.createAcquireDeliverTask(ctx, pipeline, input, systemSymbol, factory.WaypointSymbol, playerID)
			if err != nil {
				return err
			}
			inputTaskIDs = append(inputTaskIDs, taskID)
		} else if supplyChainDepth >= 2 {
			// Buy intermediate
			taskID, err := p.createAcquireDeliverTask(ctx, pipeline, input, systemSymbol, factory.WaypointSymbol, playerID)
			if err != nil {
				return err
			}
			inputTaskIDs = append(inputTaskIDs, taskID)
		} else {
			// Recurse for production
			if err := p.createProductionTasksForInput(ctx, pipeline, input, systemSymbol, factory.WaypointSymbol, supplyChainDepth, playerID, &inputTaskIDs); err != nil {
				return err
			}
		}
	}

	// Create ACQUIRE_DELIVER task to collect from this factory and deliver to next destination
	collectTask := manufacturing.NewAcquireDeliverTask(
		pipeline.ID(),
		playerID,
		good,
		factory.WaypointSymbol, // Collect from factory
		deliveryDestination,    // Deliver to next factory or construction site
		inputTaskIDs,           // Wait for inputs
	)
	if err := pipeline.AddTask(collectTask); err != nil {
		return err
	}
	*parentTaskIDs = append(*parentTaskIDs, collectTask.ID())

	logger.Log("DEBUG", "Created production task chain for intermediate good", map[string]interface{}{
		"good":        good,
		"factory":     factory.WaypointSymbol,
		"destination": deliveryDestination,
		"inputs":      len(inputs),
	})

	return nil
}

// extractSystemSymbol extracts system from waypoint (e.g., "X1-FB5-I61" -> "X1-FB5").
func extractSystemSymbol(waypointSymbol string) string {
	parts := strings.Split(waypointSymbol, "-")
	if len(parts) >= 2 {
		return parts[0] + "-" + parts[1]
	}
	return waypointSymbol
}
