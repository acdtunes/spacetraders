package commands

import (
	"context"
	"fmt"
	"sync"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/contract"
	goodsServices "github.com/andrescamacho/spacetraders-go/internal/application/goods/services"
	goodsTypes "github.com/andrescamacho/spacetraders-go/internal/application/goods/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/google/uuid"
)

// Type aliases for convenience
type RunFactoryCoordinatorCommand = goodsTypes.RunFactoryCoordinatorCommand
type RunFactoryCoordinatorResponse = goodsTypes.RunFactoryCoordinatorResponse

// RunFactoryCoordinatorHandler orchestrates fleet-based goods production.
// Pattern: Fleet Coordinator (like ContractFleetCoordinator)
//
// Workflow:
// 1. Build dependency tree using SupplyChainResolver
// 2. Analyze dependencies to identify parallel execution levels
// 3. Discover idle ships for parallel execution
// 4. Execute production in parallel levels (bottom-up: leaves to root)
//    - Spawn goroutines for independent nodes at each level
//    - Use channels for completion signaling
//    - Wait for level completion before proceeding to next level
// 5. Return aggregated results
//
// Error Handling:
// - Worker failure → propagate error, cancel remaining workers
// - No idle ships → return error
// - Market unavailable → backoff and retry (handled by ProductionExecutor)
type RunFactoryCoordinatorHandler struct {
	mediator           common.Mediator
	shipRepo           navigation.ShipRepository
	marketRepo         market.MarketRepository
	shipAssignmentRepo container.ShipAssignmentRepository
	resolver           *goodsServices.SupplyChainResolver
	marketLocator      *goodsServices.MarketLocator
	productionExecutor *goodsServices.ProductionExecutor
	dependencyAnalyzer *goodsServices.DependencyAnalyzer
	clock              shared.Clock
}

// NewRunFactoryCoordinatorHandler creates a new factory coordinator handler
func NewRunFactoryCoordinatorHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	marketRepo market.MarketRepository,
	shipAssignmentRepo container.ShipAssignmentRepository,
	resolver *goodsServices.SupplyChainResolver,
	marketLocator *goodsServices.MarketLocator,
	clock shared.Clock,
) *RunFactoryCoordinatorHandler {
	productionExecutor := goodsServices.NewProductionExecutor(
		mediator,
		shipRepo,
		marketRepo,
		marketLocator,
		clock,
	)

	dependencyAnalyzer := goodsServices.NewDependencyAnalyzer()

	return &RunFactoryCoordinatorHandler{
		mediator:           mediator,
		shipRepo:           shipRepo,
		marketRepo:         marketRepo,
		shipAssignmentRepo: shipAssignmentRepo,
		resolver:           resolver,
		marketLocator:      marketLocator,
		productionExecutor: productionExecutor,
		dependencyAnalyzer: dependencyAnalyzer,
		clock:              clock,
	}
}

// NewRunFactoryCoordinatorHandlerWithConfig creates a coordinator with external dependencies and config
func NewRunFactoryCoordinatorHandlerWithConfig(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	marketRepo market.MarketRepository,
	shipAssignmentRepo container.ShipAssignmentRepository,
	resolver *goodsServices.SupplyChainResolver,
	marketLocator *goodsServices.MarketLocator,
	dependencyAnalyzer *goodsServices.DependencyAnalyzer,
	productionExecutor *goodsServices.ProductionExecutor,
	clock shared.Clock,
	cfg interface{}, // Interface to avoid circular import with config package
) *RunFactoryCoordinatorHandler {
	return &RunFactoryCoordinatorHandler{
		mediator:           mediator,
		shipRepo:           shipRepo,
		marketRepo:         marketRepo,
		shipAssignmentRepo: shipAssignmentRepo,
		resolver:           resolver,
		marketLocator:      marketLocator,
		productionExecutor: productionExecutor,
		dependencyAnalyzer: dependencyAnalyzer,
		clock:              clock,
	}
}

// Handle executes the factory coordinator command
func (h *RunFactoryCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*RunFactoryCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	response := &RunFactoryCoordinatorResponse{
		FactoryID:        generateFactoryID(),
		TargetGood:       cmd.TargetGood,
		QuantityAcquired: 0,
		TotalCost:        0,
		NodesCompleted:   0,
		NodesTotal:       0,
		ShipsUsed:        0,
		Completed:        false,
		Error:            "",
	}

	// Execute coordination workflow
	if err := h.executeCoordination(ctx, cmd, response); err != nil {
		response.Error = err.Error()
		return response, err
	}

	response.Completed = true
	return response, nil
}

// executeCoordination orchestrates the complete fleet-based production workflow
func (h *RunFactoryCoordinatorHandler) executeCoordination(
	ctx context.Context,
	cmd *RunFactoryCoordinatorCommand,
	response *RunFactoryCoordinatorResponse,
) error {
	logger := common.LoggerFromContext(ctx)

	logger.Log("INFO", "Starting factory coordinator", map[string]interface{}{
		"factory_id":    response.FactoryID,
		"target_good":   cmd.TargetGood,
		"system_symbol": cmd.SystemSymbol,
	})

	// Step 1: Build dependency tree
	dependencyTree, err := h.buildDependencyTree(ctx, cmd)
	if err != nil {
		return fmt.Errorf("failed to build dependency tree: %w", err)
	}

	// Step 2: Flatten tree to get production nodes
	nodes := dependencyTree.FlattenToList()
	response.NodesTotal = len(nodes)

	logger.Log("INFO", "Dependency tree built", map[string]interface{}{
		"factory_id":      response.FactoryID,
		"target_good":     cmd.TargetGood,
		"total_nodes":     response.NodesTotal,
		"tree_depth":      dependencyTree.TotalDepth(),
		"buy_nodes":       countNodesByMethod(nodes, goods.AcquisitionBuy),
		"fabricate_nodes": countNodesByMethod(nodes, goods.AcquisitionFabricate),
	})

	// Step 3: Discover idle ships
	playerID := shared.MustNewPlayerID(cmd.PlayerID)
	idleShips, idleShipSymbols, err := contract.FindIdleLightHaulers(
		ctx,
		playerID,
		h.shipRepo,
		h.shipAssignmentRepo,
	)
	if err != nil {
		return fmt.Errorf("failed to discover idle ships: %w", err)
	}

	if len(idleShips) == 0 {
		return fmt.Errorf("no idle hauler ships available for production")
	}

	logger.Log("INFO", "Discovered idle ships for production", map[string]interface{}{
		"factory_id":   response.FactoryID,
		"ship_count":   len(idleShips),
		"ship_symbols": idleShipSymbols,
	})

	// Step 4: Analyze dependencies for parallel execution
	parallelLevels := h.dependencyAnalyzer.IdentifyParallelLevels(dependencyTree)
	speedup := h.dependencyAnalyzer.EstimateParallelSpeedup(parallelLevels)

	logger.Log("INFO", "Parallel execution plan created", map[string]interface{}{
		"factory_id":        response.FactoryID,
		"parallel_levels":   len(parallelLevels),
		"estimated_speedup": speedup,
	})

	// Step 5: Execute production in parallel levels
	if err := h.executeParallelProduction(ctx, cmd, parallelLevels, idleShips, response); err != nil {
		return fmt.Errorf("parallel production failed: %w", err)
	}

	logger.Log("INFO", "Factory coordinator completed", map[string]interface{}{
		"factory_id":       response.FactoryID,
		"target_good":      cmd.TargetGood,
		"quantity":         response.QuantityAcquired,
		"total_cost":       response.TotalCost,
		"nodes_completed":  response.NodesCompleted,
	})

	return nil
}

// executeParallelProduction executes production levels in parallel
func (h *RunFactoryCoordinatorHandler) executeParallelProduction(
	ctx context.Context,
	cmd *RunFactoryCoordinatorCommand,
	levels []goodsServices.ParallelLevel,
	idleShips []*navigation.Ship,
	response *RunFactoryCoordinatorResponse,
) error {
	logger := common.LoggerFromContext(ctx)
	shipsUsed := make(map[string]bool)
	totalCost := 0
	nodesCompleted := 0

	// Create a ship pool for parallel workers
	shipPool := make(chan *navigation.Ship, len(idleShips))
	for _, ship := range idleShips {
		shipPool <- ship
	}

	logger.Log("INFO", "Starting parallel production", map[string]interface{}{
		"factory_id":      response.FactoryID,
		"levels":          len(levels),
		"available_ships": len(idleShips),
	})

	// Execute each level sequentially, but nodes within each level in parallel
	for levelIdx, level := range levels {
		logger.Log("INFO", "Starting parallel level", map[string]interface{}{
			"factory_id":  response.FactoryID,
			"level":       levelIdx,
			"depth":       level.Depth,
			"nodes_count": len(level.Nodes),
		})

		// Execute all nodes in this level in parallel
		results, err := h.executeLevelParallel(ctx, cmd, level.Nodes, shipPool, shipsUsed)
		if err != nil {
			return fmt.Errorf("level %d execution failed: %w", levelIdx, err)
		}

		// Aggregate results
		for _, result := range results {
			totalCost += result.TotalCost
			nodesCompleted++
		}

		logger.Log("INFO", "Parallel level completed", map[string]interface{}{
			"factory_id":      response.FactoryID,
			"level":           levelIdx,
			"nodes_completed": len(results),
			"level_cost":      sumCosts(results),
		})
	}

	// Update response
	response.TotalCost = totalCost
	response.NodesCompleted = nodesCompleted
	response.ShipsUsed = len(shipsUsed)

	// Find the root node's quantity
	if len(levels) > 0 {
		rootLevel := levels[len(levels)-1]
		if len(rootLevel.Nodes) > 0 {
			response.QuantityAcquired = rootLevel.Nodes[0].QuantityAcquired
		}
	}

	logger.Log("INFO", "Parallel production completed", map[string]interface{}{
		"factory_id":      response.FactoryID,
		"total_cost":      totalCost,
		"ships_used":      len(shipsUsed),
		"nodes_completed": nodesCompleted,
	})

	return nil
}

// executeLevelParallel executes all nodes in a level in parallel using goroutines
func (h *RunFactoryCoordinatorHandler) executeLevelParallel(
	ctx context.Context,
	cmd *RunFactoryCoordinatorCommand,
	nodes []*goods.SupplyChainNode,
	shipPool chan *navigation.Ship,
	shipsUsed map[string]bool,
) ([]*goodsServices.ProductionResult, error) {
	logger := common.LoggerFromContext(ctx)

	// Result channel for workers
	type workerResult struct {
		result *goodsServices.ProductionResult
		err    error
		node   *goods.SupplyChainNode
	}
	resultChan := make(chan workerResult, len(nodes))

	// WaitGroup to track goroutine completion
	var wg sync.WaitGroup

	// Launch a worker for each node
	for _, node := range nodes {
		wg.Add(1)
		go func(n *goods.SupplyChainNode) {
			defer wg.Done()

			// Acquire a ship from the pool
			ship := <-shipPool
			defer func() { shipPool <- ship }() // Return ship to pool

			logger.Log("INFO", "Worker starting production", map[string]interface{}{
				"good":        n.Good,
				"ship_symbol": ship.ShipSymbol(),
				"method":      n.AcquisitionMethod,
			})

			// Track ship usage
			shipsUsed[ship.ShipSymbol()] = true

			// Execute production for this node only (non-recursive)
			result, err := h.produceNodeOnly(ctx, ship, n, cmd.SystemSymbol, cmd.PlayerID)

			// Send result back
			resultChan <- workerResult{
				result: result,
				err:    err,
				node:   n,
			}

			if err != nil {
				logger.Log("ERROR", "Worker failed", map[string]interface{}{
					"good":  n.Good,
					"error": err.Error(),
				})
			} else {
				logger.Log("INFO", "Worker completed production", map[string]interface{}{
					"good":     n.Good,
					"quantity": result.QuantityAcquired,
					"cost":     result.TotalCost,
				})
			}
		}(node)
	}

	// Wait for all workers to complete
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// Collect results
	results := make([]*goodsServices.ProductionResult, 0, len(nodes))
	var firstError error

	for wr := range resultChan {
		if wr.err != nil && firstError == nil {
			firstError = wr.err
		}
		if wr.result != nil {
			results = append(results, wr.result)
			// Mark node as completed
			wr.node.MarkCompleted(wr.result.QuantityAcquired)
		}
	}

	if firstError != nil {
		return nil, firstError
	}

	return results, nil
}

// produceNodeOnly produces a single node without recursing into children
// This is used for parallel execution where children are already produced
func (h *RunFactoryCoordinatorHandler) produceNodeOnly(
	ctx context.Context,
	ship *navigation.Ship,
	node *goods.SupplyChainNode,
	systemSymbol string,
	playerID int,
) (*goodsServices.ProductionResult, error) {
	// For leaf nodes (BUY), just purchase directly
	if node.IsLeaf() {
		return h.productionExecutor.ProduceGood(ctx, ship, node, systemSymbol, playerID)
	}

	// For fabrication nodes, we assume children are already completed
	// Just deliver inputs and purchase output
	logger := common.LoggerFromContext(ctx)
	playerIDValue := shared.MustNewPlayerID(playerID)

	// Find manufacturing waypoint
	importMarket, err := h.marketLocator.FindImportMarket(ctx, node.Good, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find import market for %s: %w", node.Good, err)
	}

	logger.Log("INFO", "Found manufacturing waypoint for parallel node", map[string]interface{}{
		"good":     node.Good,
		"waypoint": importMarket.WaypointSymbol,
	})

	// Navigate and dock at manufacturing waypoint
	updatedShip, err := h.navigateAndDock(ctx, ship.ShipSymbol(), importMarket.WaypointSymbol, playerIDValue)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate to manufacturing waypoint: %w", err)
	}

	// Check if we have inputs in cargo (from children that already completed)
	// In parallel mode, we need to gather inputs first
	totalCost := 0
	for _, child := range node.Children {
		if !child.Completed {
			return nil, fmt.Errorf("child %s not completed before fabrication", child.Good)
		}
		// In a real implementation, we'd transfer cargo from child workers
		// For now, we'll produce inputs inline
		result, err := h.productionExecutor.ProduceGood(ctx, updatedShip, child, systemSymbol, playerID)
		if err != nil {
			return nil, fmt.Errorf("failed to produce input %s: %w", child.Good, err)
		}
		totalCost += result.TotalCost
	}

	// Reload ship after producing inputs
	updatedShip, err = h.shipRepo.FindBySymbol(ctx, updatedShip.ShipSymbol(), playerIDValue)
	if err != nil {
		return nil, fmt.Errorf("failed to reload ship: %w", err)
	}

	// Poll for production and purchase output
	quantity, cost, err := h.pollForProduction(ctx, node.Good, importMarket.WaypointSymbol, updatedShip, playerIDValue)
	if err != nil {
		return nil, err
	}

	totalCost += cost

	return &goodsServices.ProductionResult{
		QuantityAcquired: quantity,
		TotalCost:        totalCost,
		WaypointSymbol:   importMarket.WaypointSymbol,
	}, nil
}

// navigateAndDock is a helper that navigates to a waypoint and docks
func (h *RunFactoryCoordinatorHandler) navigateAndDock(
	ctx context.Context,
	shipSymbol string,
	destination string,
	playerID shared.PlayerID,
) (*navigation.Ship, error) {
	return h.productionExecutor.NavigateAndDock(ctx, shipSymbol, destination, playerID)
}

// pollForProduction polls for manufactured goods
func (h *RunFactoryCoordinatorHandler) pollForProduction(
	ctx context.Context,
	good string,
	waypointSymbol string,
	ship *navigation.Ship,
	playerID shared.PlayerID,
) (int, int, error) {
	return h.productionExecutor.PollForProduction(ctx, good, waypointSymbol, ship.ShipSymbol(), playerID)
}

// sumCosts sums the total cost from a list of production results
func sumCosts(results []*goodsServices.ProductionResult) int {
	total := 0
	for _, r := range results {
		total += r.TotalCost
	}
	return total
}

// buildDependencyTree builds the supply chain dependency tree
func (h *RunFactoryCoordinatorHandler) buildDependencyTree(
	ctx context.Context,
	cmd *RunFactoryCoordinatorCommand,
) (*goods.SupplyChainNode, error) {
	playerID := cmd.PlayerID
	systemSymbol := cmd.SystemSymbol

	// Use SupplyChainResolver to build tree
	tree, err := h.resolver.BuildDependencyTree(
		ctx,
		cmd.TargetGood,
		systemSymbol,
		playerID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve supply chain: %w", err)
	}

	return tree, nil
}

// generateFactoryID generates a unique factory ID using UUID
func generateFactoryID() string {
	return "factory-" + uuid.New().String()
}

// countNodesByMethod counts nodes by acquisition method
func countNodesByMethod(nodes []*goods.SupplyChainNode, method goods.AcquisitionMethod) int {
	count := 0
	for _, node := range nodes {
		if node.AcquisitionMethod == method {
			count++
		}
	}
	return count
}
