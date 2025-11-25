package services

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"

	goodsServices "github.com/andrescamacho/spacetraders-go/internal/application/goods/services"
)

// PipelinePlanner converts manufacturing opportunities into executable pipelines.
// It walks the supply chain dependency tree and creates tasks with proper dependencies.
//
// The planner creates the following task types:
//   - ACQUIRE: Buy raw material from export market (leaf nodes)
//   - DELIVER: Deliver material to factory (after ACQUIRE)
//   - COLLECT: Buy produced good from factory (after all DELIVERs)
//   - SELL: Sell final product at demand market (after final COLLECT)
type PipelinePlanner struct {
	marketLocator *goodsServices.MarketLocator
}

// NewPipelinePlanner creates a new pipeline planner
func NewPipelinePlanner(marketLocator *goodsServices.MarketLocator) *PipelinePlanner {
	return &PipelinePlanner{
		marketLocator: marketLocator,
	}
}

// PlanningContext holds state during pipeline planning
type PlanningContext struct {
	ctx          context.Context
	systemSymbol string
	playerID     int
	pipeline     *manufacturing.ManufacturingPipeline
	tasks        []*manufacturing.ManufacturingTask
	tasksByGood  map[string]*manufacturing.ManufacturingTask // Last task that produces each good
}

// CreatePipeline converts a ManufacturingOpportunity into a pipeline with tasks.
// It walks the dependency tree and creates the appropriate task graph.
//
// Returns:
//   - pipeline: The manufacturing pipeline entity
//   - tasks: All tasks for the pipeline, with dependencies set
//   - factoryStates: Factory states to track production progress
//   - error: If planning fails
func (p *PipelinePlanner) CreatePipeline(
	ctx context.Context,
	opp *trading.ManufacturingOpportunity,
	systemSymbol string,
	playerID int,
) (*manufacturing.ManufacturingPipeline, []*manufacturing.ManufacturingTask, []*manufacturing.FactoryState, error) {
	// Create the pipeline
	pipeline := manufacturing.NewPipeline(
		opp.Good(),
		opp.SellMarket().Symbol,
		opp.PurchasePrice(),
		playerID,
	)

	// Planning context
	planCtx := &PlanningContext{
		ctx:          ctx,
		systemSymbol: systemSymbol,
		playerID:     playerID,
		pipeline:     pipeline,
		tasks:        make([]*manufacturing.ManufacturingTask, 0),
		tasksByGood:  make(map[string]*manufacturing.ManufacturingTask),
	}

	// Factory states to create
	factoryStates := make([]*manufacturing.FactoryState, 0)

	// Walk the dependency tree and create tasks
	finalCollectTaskID, err := p.createTasksFromTree(planCtx, opp.DependencyTree(), &factoryStates)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to create tasks from tree: %w", err)
	}

	// Create final SELL task (depends on final COLLECT)
	sellTask := manufacturing.NewSellTask(
		pipeline.ID(),
		playerID,
		opp.Good(),
		opp.SellMarket().Symbol,
		[]string{finalCollectTaskID},
	)
	planCtx.tasks = append(planCtx.tasks, sellTask)

	// Add all tasks to pipeline
	for _, task := range planCtx.tasks {
		if err := pipeline.AddTask(task); err != nil {
			return nil, nil, nil, fmt.Errorf("failed to add task to pipeline: %w", err)
		}
	}

	return pipeline, planCtx.tasks, factoryStates, nil
}

// createTasksFromTree recursively creates tasks from the supply chain tree.
// Returns the ID of the task that produces this node's good.
func (p *PipelinePlanner) createTasksFromTree(
	planCtx *PlanningContext,
	node *goods.SupplyChainNode,
	factoryStates *[]*manufacturing.FactoryState,
) (string, error) {
	if node.AcquisitionMethod == goods.AcquisitionBuy {
		// Leaf node: Create ACQUIRE task (no DELIVER since we're buying directly)
		return p.createAcquireTask(planCtx, node)
	}

	// FABRICATE node: Create tasks for all children first
	childTaskIDs := make([]string, 0, len(node.Children))
	deliverTaskIDs := make([]string, 0, len(node.Children))
	requiredInputs := make([]string, 0, len(node.Children))

	// Find factory location for this good
	factoryMarket, err := p.marketLocator.FindExportMarket(
		planCtx.ctx,
		node.Good,
		planCtx.systemSymbol,
		planCtx.playerID,
	)
	if err != nil {
		return "", fmt.Errorf("failed to find factory for %s: %w", node.Good, err)
	}

	for _, child := range node.Children {
		// Recursively create tasks for child
		childTaskID, err := p.createTasksFromTree(planCtx, child, factoryStates)
		if err != nil {
			return "", err
		}
		childTaskIDs = append(childTaskIDs, childTaskID)
		requiredInputs = append(requiredInputs, child.Good)

		// Check if we need a DELIVER task
		// If child was FABRICATE, we got a COLLECT task ID - need DELIVER after that
		// If child was BUY (ACQUIRE), ship has goods - need DELIVER after that
		deliverTask := manufacturing.NewDeliverTask(
			planCtx.pipeline.ID(),
			planCtx.playerID,
			child.Good,
			factoryMarket.WaypointSymbol,
			[]string{childTaskID}, // Depends on child completion
		)
		planCtx.tasks = append(planCtx.tasks, deliverTask)
		deliverTaskIDs = append(deliverTaskIDs, deliverTask.ID())
	}

	// Create COLLECT task (depends on all deliveries + supply HIGH)
	collectTask := manufacturing.NewCollectTask(
		planCtx.pipeline.ID(),
		planCtx.playerID,
		node.Good,
		factoryMarket.WaypointSymbol,
		deliverTaskIDs, // Depends on all deliveries
	)
	planCtx.tasks = append(planCtx.tasks, collectTask)

	// Create factory state for tracking
	factoryState := manufacturing.NewFactoryState(
		factoryMarket.WaypointSymbol,
		node.Good,
		planCtx.pipeline.ID(),
		planCtx.playerID,
		requiredInputs,
	)
	*factoryStates = append(*factoryStates, factoryState)

	// Track that this task produces this good
	planCtx.tasksByGood[node.Good] = collectTask

	return collectTask.ID(), nil
}

// createAcquireTask creates an ACQUIRE task for a leaf node (raw material)
func (p *PipelinePlanner) createAcquireTask(
	planCtx *PlanningContext,
	node *goods.SupplyChainNode,
) (string, error) {
	// Find market to buy from
	var sourceMarket string
	if node.WaypointSymbol != "" {
		// Use the market already identified by supply chain resolver
		sourceMarket = node.WaypointSymbol
	} else {
		// Find export market
		market, err := p.marketLocator.FindExportMarket(
			planCtx.ctx,
			node.Good,
			planCtx.systemSymbol,
			planCtx.playerID,
		)
		if err != nil {
			return "", fmt.Errorf("failed to find market for %s: %w", node.Good, err)
		}
		sourceMarket = market.WaypointSymbol
	}

	// Create ACQUIRE task
	task := manufacturing.NewAcquireTask(
		planCtx.pipeline.ID(),
		planCtx.playerID,
		node.Good,
		sourceMarket,
	)
	planCtx.tasks = append(planCtx.tasks, task)

	// Track that this task produces this good
	planCtx.tasksByGood[node.Good] = task

	return task.ID(), nil
}

// CalculateTotalTasks counts total tasks that would be created for a supply chain
func (p *PipelinePlanner) CalculateTotalTasks(tree *goods.SupplyChainNode) int {
	if tree == nil {
		return 0
	}

	count := 0

	if tree.AcquisitionMethod == goods.AcquisitionBuy {
		// Leaf: 1 ACQUIRE
		return 1
	}

	// FABRICATE: DELIVER per child + COLLECT + recurse
	for _, child := range tree.Children {
		count += p.CalculateTotalTasks(child)
		count++ // DELIVER task
	}
	count++ // COLLECT task

	// Add SELL task for root (will be added by CreatePipeline)
	return count
}

// EstimateTaskCount provides a quick estimate without walking the tree
func EstimateTaskCount(tree *goods.SupplyChainNode) int {
	if tree == nil {
		return 0
	}

	// Count FABRICATE nodes (each produces a COLLECT task)
	buyCount, fabricateCount := tree.CountByAcquisitionMethod()

	// Tasks = ACQUIRE (buy nodes) + DELIVER (one per edge) + COLLECT (fabricate nodes) + SELL (1)
	// Simplified: each fabricate node has avg 2 children, so roughly 2 DELIVER per fabricate
	return buyCount + fabricateCount*2 + 1 // +1 for SELL
}

// ValidatePipeline checks if a pipeline is valid and ready to execute
func ValidatePipeline(pipeline *manufacturing.ManufacturingPipeline) error {
	if pipeline == nil {
		return fmt.Errorf("pipeline is nil")
	}

	if pipeline.TaskCount() == 0 {
		return fmt.Errorf("pipeline has no tasks")
	}

	// Check that there's exactly one SELL task
	sellCount := 0
	for _, task := range pipeline.Tasks() {
		if task.TaskType() == manufacturing.TaskTypeSell {
			sellCount++
		}
	}
	if sellCount != 1 {
		return fmt.Errorf("pipeline should have exactly 1 SELL task, has %d", sellCount)
	}

	// Check that all tasks belong to this pipeline
	for _, task := range pipeline.Tasks() {
		if task.PipelineID() != pipeline.ID() {
			return fmt.Errorf("task %s belongs to different pipeline", task.ID())
		}
	}

	return nil
}
