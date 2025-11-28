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
// It walks the supply chain dependency tree and creates atomic tasks with proper dependencies.
//
// The planner creates the following ATOMIC task types (same ship does full operation):
//   - ACQUIRE_DELIVER: Buy from export market AND deliver to factory (raw materials)
//   - COLLECT_SELL: Collect from factory AND sell/deliver to destination (produced goods)
//
// This atomic approach prevents the "orphaned cargo" bug where a ship buys goods
// but a different ship gets assigned to deliver them.
type PipelinePlanner struct {
	marketLocator *goodsServices.MarketLocator
}

// NewPipelinePlanner creates a new pipeline planner
func NewPipelinePlanner(marketLocator *goodsServices.MarketLocator) *PipelinePlanner {
	return &PipelinePlanner{
		marketLocator: marketLocator,
	}
}

// MarketLocator returns the market locator used by this planner.
// This allows other services (like SupplyMonitor) to share the same locator.
func (p *PipelinePlanner) MarketLocator() *goodsServices.MarketLocator {
	return p.marketLocator
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

// CreatePipeline converts a ManufacturingOpportunity into a pipeline with atomic tasks.
// It walks the dependency tree and creates tasks that combine related operations.
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

	// The root node can be:
	// - FABRICATE: produces the final good via manufacturing (needs factory state)
	// - BUY: direct arbitrage (HIGH/ABUNDANT source, just buy and sell)
	// Its destination is the sell market (not another factory)
	sellMarket := opp.SellMarket().Symbol

	// Walk the dependency tree and create atomic tasks
	// Pass the sell market as the destination for the final product
	finalTaskID, err := p.createTasksFromTreeAtomic(planCtx, opp.DependencyTree(), sellMarket, true, &factoryStates)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to create tasks from tree: %w", err)
	}

	// The final task is already a COLLECT_SELL that handles both collect and sell
	_ = finalTaskID

	// Add all tasks to pipeline
	for _, task := range planCtx.tasks {
		if err := pipeline.AddTask(task); err != nil {
			return nil, nil, nil, fmt.Errorf("failed to add task to pipeline: %w", err)
		}
	}

	return pipeline, planCtx.tasks, factoryStates, nil
}

// createTasksFromTreeAtomic creates atomic tasks from the supply chain tree.
// Each task combines acquisition/collection with delivery, ensuring same ship does both.
//
// Parameters:
//   - destination: where to deliver/sell the produced good (factory or market waypoint)
//   - isFinalProduct: true if this is the root (final product goes to sell market)
//
// Returns the ID of the final task for this subtree.
func (p *PipelinePlanner) createTasksFromTreeAtomic(
	planCtx *PlanningContext,
	node *goods.SupplyChainNode,
	destination string,
	isFinalProduct bool,
	factoryStates *[]*manufacturing.FactoryState,
) (string, error) {
	if node.AcquisitionMethod == goods.AcquisitionBuy {
		if isFinalProduct {
			// DIRECT ARBITRAGE: Root node with HIGH/ABUNDANT source
			// Create COLLECT_SELL (buy from source, sell to destination)
			// No factory state needed - just buy and sell
			return p.createDirectArbitrageTask(planCtx, node, destination)
		}
		// Leaf node: Create ACQUIRE_DELIVER (buy from source AND deliver to destination)
		return p.createAcquireDeliverTask(planCtx, node, destination)
	}

	// FABRICATE node: This node produces a good at a factory
	// 1. Collect required input goods from children
	// 2. Find factory that produces output AND accepts all inputs
	// 3. Process children (they deliver inputs to THIS factory)
	// 4. Create COLLECT_SELL task (collect from this factory, deliver to destination)

	// First, collect the required input goods from children
	requiredInputs := make([]string, 0, len(node.Children))
	for _, child := range node.Children {
		requiredInputs = append(requiredInputs, child.Good)
	}

	// Find factory that produces the output good AND accepts all input goods
	// This fixes the bug where a factory was selected that doesn't trade the inputs
	factoryMarket, err := p.marketLocator.FindFactoryForProduction(
		planCtx.ctx,
		node.Good,       // Output good (factory must SELL this)
		requiredInputs,  // Input goods (factory must BUY these)
		planCtx.systemSymbol,
		planCtx.playerID,
	)
	if err != nil {
		return "", fmt.Errorf("failed to find factory for %s with inputs %v: %w", node.Good, requiredInputs, err)
	}

	// Process all children - they deliver their goods to THIS factory
	childTaskIDs := make([]string, 0, len(node.Children))

	for _, child := range node.Children {
		// Child delivers to THIS factory (not the final destination)
		childTaskID, err := p.createTasksFromTreeAtomic(planCtx, child, factoryMarket.WaypointSymbol, false, factoryStates)
		if err != nil {
			return "", err
		}
		childTaskIDs = append(childTaskIDs, childTaskID)
	}

	// Create factory state for tracking production
	factoryState := manufacturing.NewFactoryState(
		factoryMarket.WaypointSymbol,
		node.Good,
		planCtx.pipeline.ID(),
		planCtx.playerID,
		requiredInputs,
	)
	*factoryStates = append(*factoryStates, factoryState)

	// Store childTaskIDs for reference (used by factory state to track expected deliveries)
	_ = childTaskIDs

	// Create task based on whether this is the final product or an intermediate
	// - Final product: COLLECT_SELL (collect from factory → sell to market)
	// - Intermediate: ACQUIRE_DELIVER (collect from factory → deliver to next factory)
	var task *manufacturing.ManufacturingTask

	if isFinalProduct {
		// COLLECT_SELL for final product: collect from factory, sell to market
		// STREAMING MODEL: No structural dependencies - collection is gated by:
		//   1. Factory supply level (HIGH/ABUNDANT) checked by SupplyMonitor
		//   2. At least one delivery recorded (prevents premature collection)
		task = manufacturing.NewCollectSellTask(
			planCtx.pipeline.ID(),
			planCtx.playerID,
			node.Good,
			factoryMarket.WaypointSymbol, // Where to collect from
			destination,                   // Where to sell to (market)
			[]string{},                    // No structural dependencies - gated by supply monitor
		)
	} else {
		// ACQUIRE_DELIVER for intermediate product: collect from factory, deliver to next factory
		// This task type is used for both:
		//   1. Buying raw materials from export markets (leaf nodes)
		//   2. Collecting intermediate goods from factories (FABRICATE nodes)
		task = manufacturing.NewAcquireDeliverTask(
			planCtx.pipeline.ID(),
			planCtx.playerID,
			node.Good,
			factoryMarket.WaypointSymbol, // Where to collect from (this factory)
			destination,                   // Where to deliver to (next factory)
			[]string{},                    // No structural dependencies - gated by supply monitor
		)
	}

	planCtx.tasks = append(planCtx.tasks, task)

	// Track that this task produces this good
	planCtx.tasksByGood[node.Good] = task

	return task.ID(), nil
}

// createDirectArbitrageTask creates a COLLECT_SELL task for direct arbitrage.
// This is used when the root node has AcquisitionBuy (HIGH/ABUNDANT source).
// The task buys from the source market and sells to the destination (no factory involved).
func (p *PipelinePlanner) createDirectArbitrageTask(
	planCtx *PlanningContext,
	node *goods.SupplyChainNode,
	destination string,
) (string, error) {
	// Find source market (where to buy from)
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

	// Create COLLECT_SELL task: buy from source, sell to destination
	// This is the only task in a direct arbitrage pipeline
	task := manufacturing.NewCollectSellTask(
		planCtx.pipeline.ID(),
		planCtx.playerID,
		node.Good,
		sourceMarket,  // Where to buy from (HIGH/ABUNDANT source)
		destination,   // Where to sell to (market)
		[]string{},    // No dependencies - direct arbitrage is ready immediately
	)
	planCtx.tasks = append(planCtx.tasks, task)

	// Track that this task produces this good
	planCtx.tasksByGood[node.Good] = task

	return task.ID(), nil
}

// createAcquireDeliverTask creates an atomic ACQUIRE_DELIVER task
// that buys from source market AND delivers to factory in one operation.
// Uses supply-priority selection to avoid overpaying at SCARCE/LIMITED markets.
func (p *PipelinePlanner) createAcquireDeliverTask(
	planCtx *PlanningContext,
	node *goods.SupplyChainNode,
	factorySymbol string,
) (string, error) {
	// Find market to buy from using supply-priority selection
	// Priority: ABUNDANT > HIGH > MODERATE (skips SCARCE/LIMITED to avoid overpaying)
	var sourceMarket string
	if node.WaypointSymbol != "" {
		// Use the market already identified by supply chain resolver
		sourceMarket = node.WaypointSymbol
	} else {
		// Find export market with acceptable supply level
		market, err := p.marketLocator.FindExportMarketBySupplyPriority(
			planCtx.ctx,
			node.Good,
			planCtx.systemSymbol,
			planCtx.playerID,
		)
		if err != nil {
			return "", fmt.Errorf("no market with MODERATE+ supply for %s: %w", node.Good, err)
		}
		sourceMarket = market.WaypointSymbol
	}

	// Create ACQUIRE_DELIVER task: buy from source, deliver to factory
	task := manufacturing.NewAcquireDeliverTask(
		planCtx.pipeline.ID(),
		planCtx.playerID,
		node.Good,
		sourceMarket,    // Where to buy from
		factorySymbol,   // Where to deliver to
		nil,             // No dependencies for raw material acquisition
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

	// Check that there's at least one COLLECT_SELL task (handles both collection and final sale)
	sellCount := 0
	for _, task := range pipeline.Tasks() {
		if task.TaskType() == manufacturing.TaskTypeCollectSell {
			sellCount++
		}
	}
	if sellCount == 0 {
		return fmt.Errorf("pipeline should have at least 1 COLLECT_SELL task")
	}

	// Check that all tasks belong to this pipeline
	for _, task := range pipeline.Tasks() {
		if task.PipelineID() != pipeline.ID() {
			return fmt.Errorf("task %s belongs to different pipeline", task.ID())
		}
	}

	return nil
}
