package commands

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/contract"
	mfgServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	mfgTypes "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/types"
	appShipCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/google/uuid"
)

// Type aliases for convenience
type RunFactoryCoordinatorCommand = mfgTypes.RunFactoryCoordinatorCommand
type RunFactoryCoordinatorResponse = mfgTypes.RunFactoryCoordinatorResponse

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
	resolver           *mfgServices.SupplyChainResolver
	marketLocator      *mfgServices.MarketLocator
	productionExecutor *mfgServices.ProductionExecutor
	dependencyAnalyzer *mfgServices.DependencyAnalyzer
	clock              shared.Clock
}

// NewRunFactoryCoordinatorHandler creates a new factory coordinator handler
func NewRunFactoryCoordinatorHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	marketRepo market.MarketRepository,
	shipAssignmentRepo container.ShipAssignmentRepository,
	resolver *mfgServices.SupplyChainResolver,
	marketLocator *mfgServices.MarketLocator,
	clock shared.Clock,
) *RunFactoryCoordinatorHandler {
	productionExecutor := mfgServices.NewProductionExecutor(
		mediator,
		shipRepo,
		marketRepo,
		marketLocator,
		clock,
	)

	dependencyAnalyzer := mfgServices.NewDependencyAnalyzer()

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
	resolver *mfgServices.SupplyChainResolver,
	marketLocator *mfgServices.MarketLocator,
	dependencyAnalyzer *mfgServices.DependencyAnalyzer,
	productionExecutor *mfgServices.ProductionExecutor,
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

	// Create operation context for transaction linking and add to context
	if cmd.ContainerID != "" {
		opContext := shared.NewOperationContext(cmd.ContainerID, "factory_workflow")
		ctx = shared.WithOperationContext(ctx, opContext)
	}

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
		// Release all ship assignments on error
		h.releaseAllShipAssignments(ctx, cmd.ContainerID, cmd.PlayerID, "production_failed")
		return fmt.Errorf("parallel production failed: %w", err)
	}

	// Step 6: Release all ship assignments on successful completion
	if err := h.releaseAllShipAssignments(ctx, cmd.ContainerID, cmd.PlayerID, "factory_completed"); err != nil {
		logger.Log("WARNING", "Failed to release ship assignments", map[string]interface{}{
			"error": err.Error(),
		})
	}

	logger.Log("INFO", "Factory coordinator completed", map[string]interface{}{
		"factory_id":      response.FactoryID,
		"target_good":     cmd.TargetGood,
		"quantity":        response.QuantityAcquired,
		"total_cost":      response.TotalCost,
		"nodes_completed": response.NodesCompleted,
	})

	return nil
}

// executeParallelProduction executes production levels in parallel
// Operation context is retrieved from ctx using shared.OperationContextFromContext if present
func (h *RunFactoryCoordinatorHandler) executeParallelProduction(
	ctx context.Context,
	cmd *RunFactoryCoordinatorCommand,
	levels []mfgServices.ParallelLevel,
	idleShips []*navigation.Ship,
	response *RunFactoryCoordinatorResponse,
) error {
	// Get operation context from context
	opContext := shared.OperationContextFromContext(ctx)
	logger := common.LoggerFromContext(ctx)
	shipsUsed := make(map[string]bool)
	var shipsUsedMutex sync.Mutex // Protect concurrent map access
	totalCost := 0
	nodesCompleted := 0

	// Create a ship pool for parallel workers
	// Capacity: 2× initial ships to accommodate dynamic discovery
	shipPool := make(chan *navigation.Ship, len(idleShips)*2)
	for _, ship := range idleShips {
		shipPool <- ship
	}

	// Launch background ship discoverer
	discoveryCtx, cancelDiscovery := context.WithCancel(ctx)
	defer cancelDiscovery()

	go h.shipPoolRefresher(discoveryCtx, cmd.PlayerID, shipPool, shipsUsed, &shipsUsedMutex)

	logger.Log("INFO", "Starting parallel production", map[string]interface{}{
		"factory_id":         response.FactoryID,
		"levels":             len(levels),
		"available_ships":    len(idleShips),
		"discovery_enabled":  true,
		"discovery_interval": "30s",
	})

	// Execute each level sequentially, but nodes within each level in parallel
	for levelIdx, level := range levels {
		logger.Log("INFO", "Starting parallel level", map[string]interface{}{
			"factory_id":  response.FactoryID,
			"level":       levelIdx,
			"depth":       level.Depth,
			"nodes_count": len(level.Nodes),
		})

		// Build delivery destinations map for this level's BUY nodes
		// If next level has FABRICATE nodes, BUY nodes should deliver there
		deliveryDestinations := make(map[string]string) // good -> import waypoint
		if levelIdx+1 < len(levels) {
			nextLevel := levels[levelIdx+1]
			for _, fabricNode := range nextLevel.Nodes {
				if fabricNode.AcquisitionMethod == goods.AcquisitionFabricate {
					// Find export market for this fabrication node (markets that manufacture it)
					// These markets will IMPORT the raw materials needed for production
					exportMarket, err := h.marketLocator.FindExportMarket(ctx, fabricNode.Good, cmd.SystemSymbol, cmd.PlayerID)
					if err != nil {
						logger.Log("WARNING", "Could not find export market for fabrication destination", map[string]interface{}{
							"good":  fabricNode.Good,
							"error": err.Error(),
						})
						continue
					}
					// All children of this fabrication node should deliver to this manufacturing waypoint
					for _, child := range fabricNode.Children {
						deliveryDestinations[child.Good] = exportMarket.WaypointSymbol
						logger.Log("INFO", "Mapped delivery destination for input", map[string]interface{}{
							"input_good":         child.Good,
							"destination":        exportMarket.WaypointSymbol,
							"fabrication_target": fabricNode.Good,
						})
					}
				}
			}
		}

		// Execute all nodes in this level in parallel
		results, err := h.executeLevelParallel(ctx, cmd, level.Nodes, shipPool, shipsUsed, &shipsUsedMutex, deliveryDestinations, opContext)
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
		"factory_id":       response.FactoryID,
		"total_cost":       totalCost,
		"ships_used":       len(shipsUsed),
		"ships_discovered": len(shipsUsed) - len(idleShips),
		"nodes_completed":  nodesCompleted,
	})

	return nil
}

// shipPoolRefresher runs a background goroutine that periodically discovers new idle ships
// and adds them to the ship pool, allowing blocked workers to acquire ships mid-execution.
//
// Discovery process:
// 1. Poll every 30 seconds for newly idle ships
// 2. Filter out ships already in the pool (via shipsUsed map)
// 3. Attempt non-blocking send to shipPool (skip if full)
// 4. Log newly added ships for observability
// 5. Exit gracefully on context cancellation
//
// Thread safety:
// - shipsUsed map: protected by mutex for concurrent access
// - shipPool channel: Go channels are concurrency-safe
func (h *RunFactoryCoordinatorHandler) shipPoolRefresher(
	ctx context.Context,
	playerID int,
	shipPool chan *navigation.Ship,
	shipsUsed map[string]bool,
	shipsUsedMutex *sync.Mutex,
) {
	logger := common.LoggerFromContext(ctx)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	playerIDValue := shared.MustNewPlayerID(playerID)
	discoveryCount := 0

	logger.Log("INFO", "Ship pool refresher started", map[string]interface{}{
		"interval": "30s",
	})

	for {
		select {
		case <-ctx.Done():
			logger.Log("INFO", "Ship pool refresher stopped", map[string]interface{}{
				"total_discoveries": discoveryCount,
			})
			return

		case <-ticker.C:
			// Re-discover idle ships
			newIdleShips, _, err := contract.FindIdleLightHaulers(
				ctx,
				playerIDValue,
				h.shipRepo,
				h.shipAssignmentRepo,
			)
			if err != nil {
				logger.Log("WARNING", "Failed to refresh ship pool", map[string]interface{}{
					"error": err.Error(),
				})
				continue
			}

			// Add newly discovered ships to pool (non-blocking)
			addedCount := 0
			addedShips := make([]string, 0)

			for _, ship := range newIdleShips {
				// Skip if ship already in use by this factory (check with lock)
				shipsUsedMutex.Lock()
				alreadyUsed := shipsUsed[ship.ShipSymbol()]
				shipsUsedMutex.Unlock()

				if alreadyUsed {
					continue
				}

				// Attempt non-blocking send to pool
				select {
				case shipPool <- ship:
					shipsUsedMutex.Lock()
					shipsUsed[ship.ShipSymbol()] = true
					shipsUsedMutex.Unlock()
					addedShips = append(addedShips, ship.ShipSymbol())
					addedCount++
				default:
					// Channel full, skip this ship
					// Will retry on next tick if ship still idle
				}
			}

			if addedCount > 0 {
				discoveryCount += addedCount
				logger.Log("INFO", "Added new ships to pool", map[string]interface{}{
					"added_count":        addedCount,
					"added_ships":        addedShips,
					"total_discoveries":  discoveryCount,
					"pool_capacity_used": fmt.Sprintf("%d/%d", len(shipsUsed), cap(shipPool)),
				})
			}
		}
	}
}

// executeLevelParallel executes all nodes in a level in parallel using goroutines
func (h *RunFactoryCoordinatorHandler) executeLevelParallel(
	ctx context.Context,
	cmd *RunFactoryCoordinatorCommand,
	nodes []*goods.SupplyChainNode,
	shipPool chan *navigation.Ship,
	shipsUsed map[string]bool,
	shipsUsedMutex *sync.Mutex,
	deliveryDestinations map[string]string,
	opContext *shared.OperationContext, // Operation context for transaction linking
) ([]*mfgServices.ProductionResult, error) {
	logger := common.LoggerFromContext(ctx)

	// Result channel for workers
	type workerResult struct {
		result *mfgServices.ProductionResult
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

			// Check if this ship already has the needed cargo (for BUY nodes)
			hasNeededCargo := false
			if n.IsLeaf() && n.AcquisitionMethod == goods.AcquisitionBuy {
				for _, item := range ship.Cargo().Inventory {
					if item.Symbol == n.Good && item.Units > 0 {
						hasNeededCargo = true
						logger.Log("INFO", fmt.Sprintf("Ship %s already has %d units of %s - will deliver directly", ship.ShipSymbol(), item.Units, n.Good), map[string]interface{}{
							"ship_symbol": ship.ShipSymbol(),
							"good":        n.Good,
							"units":       item.Units,
						})
						break
					}
				}
			}

			if !hasNeededCargo {
				logger.Log("INFO", fmt.Sprintf("Worker starting: %s %s using ship %s", n.AcquisitionMethod, n.Good, ship.ShipSymbol()), map[string]interface{}{
					"good":        n.Good,
					"ship_symbol": ship.ShipSymbol(),
					"method":      n.AcquisitionMethod,
				})
			} else {
				logger.Log("INFO", fmt.Sprintf("Worker starting: DELIVER %s using ship %s (cargo already onboard)", n.Good, ship.ShipSymbol()), map[string]interface{}{
					"good":        n.Good,
					"ship_symbol": ship.ShipSymbol(),
					"method":      "DELIVER",
				})
			}

			// Track ship usage and persist assignment
			shipsUsedMutex.Lock()
			alreadyAssigned := shipsUsed[ship.ShipSymbol()]
			if !alreadyAssigned {
				shipsUsed[ship.ShipSymbol()] = true
			}
			shipsUsedMutex.Unlock()

			// Persist ship assignment if this is the first time using this ship
			if !alreadyAssigned {
				assignment := container.NewShipAssignment(
					ship.ShipSymbol(),
					cmd.PlayerID,
					cmd.ContainerID,
					h.clock,
				)
				if err := h.shipAssignmentRepo.Assign(ctx, assignment); err != nil {
					logger.Log("WARNING", "Failed to persist ship assignment", map[string]interface{}{
						"ship_symbol": ship.ShipSymbol(),
						"error":       err.Error(),
					})
				} else {
					logger.Log("INFO", "Ship assigned to factory", map[string]interface{}{
						"ship_symbol":  ship.ShipSymbol(),
						"container_id": cmd.ContainerID,
					})
				}
			}

			// Get delivery destination for this good (if any)
			deliveryDest := deliveryDestinations[n.Good]

			// Execute production for this node only (non-recursive)
			var result *mfgServices.ProductionResult
			var err error

			if hasNeededCargo && deliveryDest != "" {
				// Ship already has cargo - just deliver it
				result, err = h.deliverExistingCargo(ctx, ship, n.Good, deliveryDest, cmd.PlayerID, opContext)
			} else {
				// Normal production (buy or fabricate)
				result, err = h.produceNodeOnly(ctx, ship, n, cmd.SystemSymbol, cmd.PlayerID, deliveryDest, opContext)
			}

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
				logger.Log("INFO", fmt.Sprintf("Worker completed: %s %s (%d units, %d credits)", n.AcquisitionMethod, n.Good, result.QuantityAcquired, result.TotalCost), map[string]interface{}{
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
	results := make([]*mfgServices.ProductionResult, 0, len(nodes))
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
	deliveryDest string,
	opContext *shared.OperationContext, // Operation context for transaction linking
) (*mfgServices.ProductionResult, error) {
	logger := common.LoggerFromContext(ctx)

	// For leaf nodes (BUY), purchase and optionally deliver
	if node.IsLeaf() {
		// First, buy the goods
		result, err := h.productionExecutor.ProduceGood(ctx, ship, node, systemSymbol, playerID, opContext)
		if err != nil {
			return nil, err
		}

		// If there's a delivery destination, deliver the cargo there
		if deliveryDest != "" {
			logger.Log("INFO", fmt.Sprintf("Delivering %d units of %s to %s", result.QuantityAcquired, node.Good, deliveryDest), map[string]interface{}{
				"good":        node.Good,
				"quantity":    result.QuantityAcquired,
				"destination": deliveryDest,
			})

			deliveryResult, err := h.deliverCargo(ctx, ship.ShipSymbol(), node.Good, deliveryDest, playerID, opContext)
			if err != nil {
				return nil, fmt.Errorf("failed to deliver %s to %s: %w", node.Good, deliveryDest, err)
			}

			// Update result with delivery revenue (negative cost)
			result.TotalCost -= deliveryResult.TotalRevenue
			result.WaypointSymbol = deliveryDest

			logger.Log("INFO", fmt.Sprintf("Delivered %d units of %s for %d credits revenue", deliveryResult.UnitsSold, node.Good, deliveryResult.TotalRevenue), map[string]interface{}{
				"good":     node.Good,
				"units":    deliveryResult.UnitsSold,
				"revenue":  deliveryResult.TotalRevenue,
				"location": deliveryDest,
			})
		}

		return result, nil
	}

	// For fabrication nodes, we assume children are already completed
	// Just deliver inputs and purchase output
	playerIDValue := shared.MustNewPlayerID(playerID)

	// Find manufacturing waypoint (market that exports/produces this good)
	exportMarket, err := h.marketLocator.FindExportMarket(ctx, node.Good, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find export market for %s: %w", node.Good, err)
	}

	logger.Log("INFO", "Found manufacturing waypoint for parallel node", map[string]interface{}{
		"good":     node.Good,
		"waypoint": exportMarket.WaypointSymbol,
	})

	// Navigate and dock at manufacturing waypoint
	updatedShip, err := h.navigateAndDock(ctx, ship.ShipSymbol(), exportMarket.WaypointSymbol, playerIDValue)
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
		result, err := h.productionExecutor.ProduceGood(ctx, updatedShip, child, systemSymbol, playerID, opContext)
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
	quantity, cost, err := h.pollForProduction(ctx, node.Good, exportMarket.WaypointSymbol, updatedShip, playerIDValue, opContext)
	if err != nil {
		return nil, err
	}

	totalCost += cost

	return &mfgServices.ProductionResult{
		QuantityAcquired: quantity,
		TotalCost:        totalCost,
		WaypointSymbol:   exportMarket.WaypointSymbol,
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
	opContext *shared.OperationContext, // Operation context for transaction linking
) (int, int, error) {
	return h.productionExecutor.PollForProduction(ctx, good, waypointSymbol, ship.ShipSymbol(), playerID, opContext)
}

// sumCosts sums the total cost from a list of production results
func sumCosts(results []*mfgServices.ProductionResult) int {
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

// deliverCargo navigates to destination, docks, and sells the specified good
func (h *RunFactoryCoordinatorHandler) deliverCargo(
	ctx context.Context,
	shipSymbol string,
	good string,
	destination string,
	playerID int,
	opContext *shared.OperationContext, // Operation context for transaction linking
) (*appShipCmd.SellCargoResponse, error) {
	playerIDValue := shared.MustNewPlayerID(playerID)

	// Navigate and dock at destination
	ship, err := h.navigateAndDock(ctx, shipSymbol, destination, playerIDValue)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate to delivery destination: %w", err)
	}

	// Find the cargo item and sell it
	var unitsToSell int
	for _, item := range ship.Cargo().Inventory {
		if item.Symbol == good {
			unitsToSell = item.Units
			break
		}
	}

	if unitsToSell == 0 {
		return nil, fmt.Errorf("ship %s has no %s in cargo to deliver", shipSymbol, good)
	}

	// Sell the cargo
	sellCmd := &appShipCmd.SellCargoCommand{
		ShipSymbol: shipSymbol,
		GoodSymbol: good,
		Units:      unitsToSell,
		PlayerID:   playerIDValue,
	}

	sellResp, err := h.mediator.Send(ctx, sellCmd)
	if err != nil {
		return nil, fmt.Errorf("failed to sell cargo: %w", err)
	}

	response, ok := sellResp.(*appShipCmd.SellCargoResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type from sell command")
	}

	return response, nil
}

// deliverExistingCargo delivers cargo that's already on the ship (skips buying)
func (h *RunFactoryCoordinatorHandler) deliverExistingCargo(
	ctx context.Context,
	ship *navigation.Ship,
	good string,
	destination string,
	playerID int,
	opContext *shared.OperationContext, // Operation context for transaction linking
) (*mfgServices.ProductionResult, error) {
	logger := common.LoggerFromContext(ctx)

	// Find quantity in cargo
	var quantity int
	for _, item := range ship.Cargo().Inventory {
		if item.Symbol == good {
			quantity = item.Units
			break
		}
	}

	if quantity == 0 {
		return nil, fmt.Errorf("ship %s has no %s in cargo", ship.ShipSymbol(), good)
	}

	logger.Log("INFO", fmt.Sprintf("Delivering existing cargo: %d units of %s to %s", quantity, good, destination), map[string]interface{}{
		"ship":        ship.ShipSymbol(),
		"good":        good,
		"quantity":    quantity,
		"destination": destination,
	})

	// Deliver to destination
	deliveryResult, err := h.deliverCargo(ctx, ship.ShipSymbol(), good, destination, playerID, opContext)
	if err != nil {
		return nil, err
	}

	logger.Log("INFO", fmt.Sprintf("Existing cargo delivered: %d units of %s for %d credits", deliveryResult.UnitsSold, good, deliveryResult.TotalRevenue), map[string]interface{}{
		"good":     good,
		"units":    deliveryResult.UnitsSold,
		"revenue":  deliveryResult.TotalRevenue,
		"location": destination,
	})

	// Return result with negative cost (revenue from selling)
	return &mfgServices.ProductionResult{
		QuantityAcquired: deliveryResult.UnitsSold,
		TotalCost:        -deliveryResult.TotalRevenue, // Negative because we earned money
		WaypointSymbol:   destination,
	}, nil
}

// releaseAllShipAssignments releases all ship assignments for the factory container
func (h *RunFactoryCoordinatorHandler) releaseAllShipAssignments(
	ctx context.Context,
	containerID string,
	playerID int,
	reason string,
) error {
	logger := common.LoggerFromContext(ctx)

	if err := h.shipAssignmentRepo.ReleaseByContainer(ctx, containerID, playerID, reason); err != nil {
		logger.Log("ERROR", "Failed to release ship assignments", map[string]interface{}{
			"container_id": containerID,
			"error":        err.Error(),
		})
		return err
	}

	logger.Log("INFO", "Released all ship assignments", map[string]interface{}{
		"container_id": containerID,
		"reason":       reason,
	})

	return nil
}
