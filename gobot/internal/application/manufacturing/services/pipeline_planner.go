package services

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
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
	marketLocator   *MarketLocator
	storageSources  *StorageSourceFinder  // Optional: enables STORAGE_ACQUIRE_DELIVER tasks
	containerReader ContainerStatusReader // Optional: gates STORAGE_ACQUIRE_DELIVER on coordinator liveness (sp-86yb)
}

// NewPipelinePlanner creates a new pipeline planner.
// storageOpRepo is optional - if nil, only ACQUIRE_DELIVER tasks will be created.
// containerReader is optional - if nil, storage sources are trusted on row status
// alone (pre-sp-86yb behavior); wiring it adds a coordinator-liveness gate so a
// storage source whose coordinator container already died/stopped is skipped even
// if its storage_operations row is still (stale-)RUNNING.
func NewPipelinePlanner(
	marketLocator *MarketLocator,
	storageOpRepo storage.StorageOperationRepository,
	containerReader ContainerStatusReader,
) *PipelinePlanner {
	return &PipelinePlanner{
		marketLocator:   marketLocator,
		storageSources:  NewStorageSourceFinder(storageOpRepo, containerReader),
		containerReader: containerReader,
	}
}

// MarketLocator returns the market locator used by this planner.
// This allows other services (like SupplyMonitor) to share the same locator.
func (p *PipelinePlanner) MarketLocator() *MarketLocator {
	return p.marketLocator
}

// SetStorageOperationRepository sets the storage operation repository.
// This enables STORAGE_ACQUIRE_DELIVER tasks for goods available from
// running storage operations (e.g., gas siphoning for LIQUID_HYDROGEN).
func (p *PipelinePlanner) SetStorageOperationRepository(repo storage.StorageOperationRepository) {
	p.storageSources = NewStorageSourceFinder(repo, p.containerReader)
}

// PlanningContext holds state during pipeline planning
type PlanningContext struct {
	ctx           context.Context
	systemSymbol  string
	playerID      int
	pipeline      *manufacturing.ManufacturingPipeline
	tasks         []*manufacturing.ManufacturingTask
	factoryStates []*manufacturing.FactoryState
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

	planCtx := &PlanningContext{
		ctx:           ctx,
		systemSymbol:  systemSymbol,
		playerID:      playerID,
		pipeline:      pipeline,
		tasks:         make([]*manufacturing.ManufacturingTask, 0),
		factoryStates: make([]*manufacturing.FactoryState, 0),
	}

	// The root node can be:
	// - FABRICATE: produces the final good via manufacturing (needs factory state)
	// - BUY: direct arbitrage (HIGH/ABUNDANT source, just buy and sell)
	// Its destination is the sell market (not another factory)
	sellMarket := opp.SellMarket().Symbol

	// Walk the dependency tree and create atomic tasks
	// Pass the sell market as the destination for the final product
	if _, err := p.createTasksFromTreeAtomic(planCtx, opp.DependencyTree(), sellMarket, true); err != nil {
		return nil, nil, nil, fmt.Errorf("failed to create tasks from tree: %w", err)
	}

	// Add all tasks to pipeline
	for _, task := range planCtx.tasks {
		if err := pipeline.AddTask(task); err != nil {
			return nil, nil, nil, fmt.Errorf("failed to add task to pipeline: %w", err)
		}
	}

	return pipeline, planCtx.tasks, planCtx.factoryStates, nil
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
		node.Good,      // Output good (factory must SELL this)
		requiredInputs, // Input goods (factory must BUY these)
		planCtx.systemSymbol,
		planCtx.playerID,
	)
	if err != nil {
		return "", fmt.Errorf("failed to find factory for %s with inputs %v: %w", node.Good, requiredInputs, err)
	}

	// Process all children - they deliver their goods to THIS factory
	for _, child := range node.Children {
		// Child delivers to THIS factory (not the final destination)
		if _, err := p.createTasksFromTreeAtomic(planCtx, child, factoryMarket.WaypointSymbol, false); err != nil {
			return "", err
		}
	}

	// Create factory state for tracking production
	factoryState := manufacturing.NewFactoryState(
		factoryMarket.WaypointSymbol,
		node.Good,
		planCtx.pipeline.ID(),
		planCtx.playerID,
		requiredInputs,
	)
	planCtx.factoryStates = append(planCtx.factoryStates, factoryState)

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
			destination,                  // Where to sell to (market)
			[]string{},                   // No structural dependencies - gated by supply monitor
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
			destination,                  // Where to deliver to (next factory)
			[]string{},                   // No structural dependencies - gated by supply monitor
		)
	}

	planCtx.tasks = append(planCtx.tasks, task)

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
		sourceMarket, // Where to buy from (HIGH/ABUNDANT source)
		destination,  // Where to sell to (market)
		[]string{},   // No dependencies - direct arbitrage is ready immediately
	)
	planCtx.tasks = append(planCtx.tasks, task)

	return task.ID(), nil
}

// createAcquireDeliverTask creates an atomic ACQUIRE_DELIVER or STORAGE_ACQUIRE_DELIVER task
// that acquires the good AND delivers to factory in one operation.
//
// Priority:
//  1. Check for running storage operation (e.g., gas siphoning) - creates STORAGE_ACQUIRE_DELIVER
//  2. Fall back to market acquisition with supply-priority selection - creates ACQUIRE_DELIVER
func (p *PipelinePlanner) createAcquireDeliverTask(
	planCtx *PlanningContext,
	node *goods.SupplyChainNode,
	factorySymbol string,
) (string, error) {
	logger := common.LoggerFromContext(planCtx.ctx)

	// First, check if there's a running storage operation for this good (e.g., gas siphoning)
	// If so, create STORAGE_ACQUIRE_DELIVER instead of ACQUIRE_DELIVER
	if storageOp := p.storageSources.FindRunningOperationForGood(planCtx.ctx, planCtx.playerID, node.Good); storageOp != nil {
		logger.Log("INFO", "Using storage operation for acquisition task", map[string]interface{}{
			"good":        node.Good,
			"storage_op":  storageOp.ID(),
			"waypoint":    storageOp.WaypointSymbol(),
			"factory":     factorySymbol,
			"pipeline_id": planCtx.pipeline.ID(),
		})

		task := manufacturing.NewStorageAcquireDeliverTask(
			planCtx.pipeline.ID(),
			planCtx.playerID,
			node.Good,
			storageOp.ID(),             // Storage operation to acquire from
			storageOp.WaypointSymbol(), // Where storage ships are located
			factorySymbol,              // Where to deliver to
			nil,                        // No dependencies for raw material acquisition
		)
		planCtx.tasks = append(planCtx.tasks, task)
		return task.ID(), nil
	}

	// No storage operation found - fall back to market acquisition
	// ALWAYS use supply-filtered selection to avoid buying from SCARCE/LIMITED markets
	// This overrides any WaypointSymbol pre-set by the supply chain resolver,
	// which may have selected a market without considering supply levels.
	// Priority: HIGH > ABUNDANT only (strict filtering to maximize profit margins)
	market, err := p.marketLocator.FindExportMarketWithGoodSupply(
		planCtx.ctx,
		node.Good,
		planCtx.systemSymbol,
		planCtx.playerID,
	)
	if err != nil {
		return "", fmt.Errorf("no market with HIGH/ABUNDANT supply for %s: %w", node.Good, err)
	}
	if market == nil {
		return "", fmt.Errorf("no market with HIGH/ABUNDANT supply for %s (supply too low)", node.Good)
	}
	sourceMarket := market.WaypointSymbol

	// Create ACQUIRE_DELIVER task: buy from source, deliver to factory
	task := manufacturing.NewAcquireDeliverTask(
		planCtx.pipeline.ID(),
		planCtx.playerID,
		node.Good,
		sourceMarket,  // Where to buy from
		factorySymbol, // Where to deliver to
		nil,           // No dependencies for raw material acquisition
	)
	planCtx.tasks = append(planCtx.tasks, task)

	return task.ID(), nil
}
