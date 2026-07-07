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
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/google/uuid"
)

// Type aliases for convenience
type RunFactoryCoordinatorCommand = mfgTypes.RunFactoryCoordinatorCommand
type RunFactoryCoordinatorResponse = mfgTypes.RunFactoryCoordinatorResponse

const (
	shipDiscoveryInterval  = 30 * time.Second
	shipPoolCapacityFactor = 2
)

// RunFactoryCoordinatorHandler orchestrates fleet-based goods production.
// Pattern: Fleet Coordinator (like ContractFleetCoordinator)
//
// Workflow:
// 1. Build dependency tree using SupplyChainResolver
// 2. Analyze dependencies to identify parallel execution levels
// 3. Discover idle ships for parallel execution
// 4. Execute production in parallel levels (bottom-up: leaves to root)
//   - Spawn goroutines for independent nodes at each level
//   - Use channels for completion signaling
//   - Wait for level completion before proceeding to next level
//
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
	resolver *mfgServices.SupplyChainResolver,
	marketLocator *mfgServices.MarketLocator,
	clock shared.Clock,
) *RunFactoryCoordinatorHandler {
	// Honour the "nil = use RealClock" wiring convention (main.go) that every
	// sibling coordinator follows (run_parallel_manufacturing_coordinator.go,
	// assign_scouting_fleet.go, ...). Omitting this left h.clock nil and
	// SIGSEGV'd the daemon when the parallel claim path called clock.Now()
	// (sp-bt6o).
	if clock == nil {
		clock = shared.NewRealClock()
	}

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

	// Step 3: Wait for an idle hauler.
	//
	// At launch the factory may momentarily find every hauler
	// coordinator-assigned. Rather than crashing on that transient gap (sp-vmrj —
	// the "impatience crash"), we poll for the next idle hauler the same way the
	// long-lived fleet coordinator waits for ships (run_fleet_coordinator.go):
	// keep re-discovering until a hauler frees or the container's context is
	// cancelled. A factory that holds a market at MODERATE+ is long-lived, so an
	// indefinite wait for the next idle gap is the correct bound.
	playerID := shared.MustNewPlayerID(cmd.PlayerID)
	idleShips, idleShipSymbols, err := h.waitForIdleHaulers(ctx, playerID, response.FactoryID)
	if err != nil {
		return err
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

// waitForIdleHaulers polls for idle light haulers, blocking until at least one
// is available or the context is cancelled.
//
// This is the fix for the "impatience crash" (sp-vmrj): the factory used to
// return a fatal "no idle hauler ships available for production" the instant it
// found every hauler busy at launch, killing an acceleration play on the pad. A
// factory meant to hold a market at MODERATE+ is long-lived, so waiting for the
// next idle gap — exactly what the fleet coordinator does when its pool is
// momentarily empty (run_fleet_coordinator.go) — is the correct behaviour. The
// wait is bounded only by context cancellation (container shutdown), never by a
// timeout, so a slow-to-free fleet can never re-introduce the crash.
func (h *RunFactoryCoordinatorHandler) waitForIdleHaulers(
	ctx context.Context,
	playerID shared.PlayerID,
	factoryID string,
) ([]*navigation.Ship, []string, error) {
	logger := common.LoggerFromContext(ctx)
	waited := false

	for {
		// Honour container shutdown between polls (mirrors the fleet
		// coordinator's top-of-loop cancellation check).
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		default:
		}

		idleShips, idleShipSymbols, err := contract.FindIdleLightHaulers(ctx, playerID, h.shipRepo)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to discover idle ships: %w", err)
		}
		if len(idleShips) > 0 {
			if waited {
				logger.Log("INFO", "Idle hauler became available - resuming production", map[string]interface{}{
					"factory_id":   factoryID,
					"ship_count":   len(idleShips),
					"ship_symbols": idleShipSymbols,
				})
			}
			return idleShips, idleShipSymbols, nil
		}

		waited = true
		logger.Log("INFO", "No idle haulers available yet - waiting for an idle gap before production", map[string]interface{}{
			"factory_id":    factoryID,
			"poll_interval": shipDiscoveryInterval.String(),
		})
		h.clock.Sleep(shipDiscoveryInterval)
	}
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

	// Capacity: 2× initial ships to accommodate dynamic discovery
	shipPool := make(chan *navigation.Ship, len(idleShips)*shipPoolCapacityFactor)
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
		"discovery_interval": shipDiscoveryInterval.String(),
	})

	// Execute each level sequentially, but nodes within each level in parallel
	for levelIdx, level := range levels {
		logger.Log("INFO", "Starting parallel level", map[string]interface{}{
			"factory_id":  response.FactoryID,
			"level":       levelIdx,
			"depth":       level.Depth,
			"nodes_count": len(level.Nodes),
		})

		exec := levelExecution{
			cmd:                  cmd,
			shipPool:             shipPool,
			shipsUsed:            shipsUsed,
			shipsUsedMutex:       &shipsUsedMutex,
			deliveryDestinations: h.mapDeliveryDestinations(ctx, cmd, levels, levelIdx),
			opContext:            opContext,
		}

		// Execute all nodes in this level in parallel
		results, err := h.executeLevelParallel(ctx, exec, level.Nodes)
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

func (h *RunFactoryCoordinatorHandler) mapDeliveryDestinations(
	ctx context.Context,
	cmd *RunFactoryCoordinatorCommand,
	levels []mfgServices.ParallelLevel,
	levelIdx int,
) map[string]string {
	logger := common.LoggerFromContext(ctx)
	deliveryDestinations := make(map[string]string) // good -> import waypoint
	if levelIdx+1 >= len(levels) {
		return deliveryDestinations
	}

	nextLevel := levels[levelIdx+1]
	for _, fabricNode := range nextLevel.Nodes {
		if fabricNode.AcquisitionMethod != goods.AcquisitionFabricate {
			continue
		}
		exportMarket, err := h.marketLocator.FindExportMarket(ctx, fabricNode.Good, cmd.SystemSymbol, cmd.PlayerID)
		if err != nil {
			logger.Log("WARNING", "Could not find export market for fabrication destination", map[string]interface{}{
				"good":  fabricNode.Good,
				"error": err.Error(),
			})
			continue
		}
		for _, child := range fabricNode.Children {
			deliveryDestinations[child.Good] = exportMarket.WaypointSymbol
			logger.Log("INFO", "Mapped delivery destination for input", map[string]interface{}{
				"input_good":         child.Good,
				"destination":        exportMarket.WaypointSymbol,
				"fabrication_target": fabricNode.Good,
			})
		}
	}
	return deliveryDestinations
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
	ticker := time.NewTicker(shipDiscoveryInterval)
	defer ticker.Stop()

	playerIDValue := shared.MustNewPlayerID(playerID)
	discoveryCount := 0

	logger.Log("INFO", "Ship pool refresher started", map[string]interface{}{
		"interval": shipDiscoveryInterval.String(),
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

type levelExecution struct {
	cmd                  *RunFactoryCoordinatorCommand
	shipPool             chan *navigation.Ship
	shipsUsed            map[string]bool
	shipsUsedMutex       *sync.Mutex
	deliveryDestinations map[string]string
	opContext            *shared.OperationContext
}

// executeLevelParallel executes all nodes in a level in parallel using goroutines
func (h *RunFactoryCoordinatorHandler) executeLevelParallel(
	ctx context.Context,
	exec levelExecution,
	nodes []*goods.SupplyChainNode,
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

			ship := <-exec.shipPool
			defer func() { exec.shipPool <- ship }() // Return ship to pool

			result, err := h.runNodeWorker(ctx, exec, n, ship)

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

func (h *RunFactoryCoordinatorHandler) runNodeWorker(
	ctx context.Context,
	exec levelExecution,
	n *goods.SupplyChainNode,
	ship *navigation.Ship,
) (*mfgServices.ProductionResult, error) {
	logger := common.LoggerFromContext(ctx)

	// A worker goroutine must never SIGSEGV the daemon. Guard a nil hull before
	// any deref (detectOnboardCargo/logging touch ship immediately) so a
	// degenerate pool entry fails this node cleanly instead of panicking the
	// whole fleet (sp-bt6o).
	if ship == nil {
		logger.Log("WARNING", fmt.Sprintf("Skipping %s %s - nil ship from pool", n.AcquisitionMethod, n.Good), map[string]interface{}{
			"good":   n.Good,
			"method": n.AcquisitionMethod,
		})
		return nil, fmt.Errorf("nil ship for %s %s", n.AcquisitionMethod, n.Good)
	}

	hasNeededCargo := h.detectOnboardCargo(ctx, n, ship)

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

	if !h.claimShipForFactory(ctx, exec.cmd.ContainerID, ship, exec.shipsUsed, exec.shipsUsedMutex) {
		// The pulled hull was nil or no longer claimable (grabbed by another
		// coordinator since discovery). Fail this node cleanly rather than
		// operating an unclaimed ship; the factory degrades to a relaunchable
		// failure, never a daemon panic (sp-bt6o).
		return nil, fmt.Errorf("no claimable ship for %s %s", n.AcquisitionMethod, n.Good)
	}

	deliveryDest := exec.deliveryDestinations[n.Good]

	if hasNeededCargo && deliveryDest != "" {
		return h.deliverExistingCargo(ctx, ship, n.Good, deliveryDest, exec.cmd.PlayerID)
	}
	return h.produceNodeOnly(ctx, ship, n, exec.cmd.SystemSymbol, exec.cmd.PlayerID, deliveryDest, exec.opContext)
}

func (h *RunFactoryCoordinatorHandler) detectOnboardCargo(ctx context.Context, n *goods.SupplyChainNode, ship *navigation.Ship) bool {
	if !n.IsLeaf() || n.AcquisitionMethod != goods.AcquisitionBuy {
		return false
	}
	logger := common.LoggerFromContext(ctx)
	for _, item := range ship.Cargo().Inventory {
		if item.Symbol == n.Good && item.Units > 0 {
			logger.Log("INFO", fmt.Sprintf("Ship %s already has %d units of %s - will deliver directly", ship.ShipSymbol(), item.Units, n.Good), map[string]interface{}{
				"ship_symbol": ship.ShipSymbol(),
				"good":        n.Good,
				"units":       item.Units,
			})
			return true
		}
	}
	return false
}

// claimShipForFactory attempts to claim a ship for the factory container and
// reports whether the ship is usable by the caller.
//
// It must NEVER panic the daemon. A nil ship, or one that is no longer idle at
// claim time (another coordinator grabbed it since discovery — a stale-snapshot
// TOCTOU), is skipped with a WARNING and reported unclaimable, so a bad claim
// degrades to a skipped node instead of SIGSEGV'ing the whole fleet (sp-bt6o).
func (h *RunFactoryCoordinatorHandler) claimShipForFactory(
	ctx context.Context,
	containerID string,
	ship *navigation.Ship,
	shipsUsed map[string]bool,
	shipsUsedMutex *sync.Mutex,
) bool {
	logger := common.LoggerFromContext(ctx)

	// Defense-in-depth: a nil ship must never reach AssignToContainer.
	if ship == nil {
		logger.Log("WARNING", "Skipping nil ship in factory claim path", nil)
		return false
	}

	shipsUsedMutex.Lock()
	alreadyClaimed := shipsUsed[ship.ShipSymbol()]
	shipsUsedMutex.Unlock()

	// Idempotent across parallel levels: a ship this factory already claimed is
	// still ours to use.
	if alreadyClaimed {
		return true
	}

	// Re-validate claimability at claim time rather than trusting the discovery
	// snapshot. A ship now owned by another container must be skipped, not
	// clobbered (mirrors how the fleet/mfg coordinators reject a non-idle ship).
	if !ship.IsIdle() {
		logger.Log("WARNING", "Skipping ship no longer idle at claim time", map[string]interface{}{
			"ship_symbol":  ship.ShipSymbol(),
			"container_id": ship.ContainerID(),
		})
		return false
	}

	if err := ship.AssignToContainer(containerID, h.clock); err != nil {
		logger.Log("WARNING", "Failed to assign ship to container", map[string]interface{}{
			"ship_symbol": ship.ShipSymbol(),
			"error":       err.Error(),
		})
		return false
	}
	if err := h.shipRepo.Save(ctx, ship); err != nil {
		logger.Log("WARNING", "Failed to persist ship assignment", map[string]interface{}{
			"ship_symbol": ship.ShipSymbol(),
			"error":       err.Error(),
		})
		return false
	}

	shipsUsedMutex.Lock()
	shipsUsed[ship.ShipSymbol()] = true
	shipsUsedMutex.Unlock()

	logger.Log("INFO", "Ship assigned to factory", map[string]interface{}{
		"ship_symbol":  ship.ShipSymbol(),
		"container_id": containerID,
	})
	return true
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

			deliveryResult, err := h.deliverCargo(ctx, ship.ShipSymbol(), node.Good, deliveryDest, playerID)
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
	updatedShip, err := h.productionExecutor.NavigateAndDock(ctx, ship.ShipSymbol(), exportMarket.WaypointSymbol, playerIDValue)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate to manufacturing waypoint: %w", err)
	}

	// Inputs were already bought and delivered to the factory by the child
	// workers of the previous parallel level - only verify completion here.
	totalCost := 0
	for _, child := range node.Children {
		if !child.Completed {
			return nil, fmt.Errorf("child %s not completed before fabrication", child.Good)
		}
	}

	// Poll for production and purchase output
	quantity, cost, err := h.productionExecutor.PollForProduction(ctx, node.Good, exportMarket.WaypointSymbol, updatedShip.ShipSymbol(), playerIDValue, opContext)
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

// releaseAllShipAssignments releases all ship assignments for the factory container
func (h *RunFactoryCoordinatorHandler) releaseAllShipAssignments(
	ctx context.Context,
	containerID string,
	playerID int,
	reason string,
) error {
	logger := common.LoggerFromContext(ctx)

	// Find all ships assigned to this container
	playerIDValue := shared.MustNewPlayerID(playerID)
	assignedShips, err := h.shipRepo.FindByContainer(ctx, containerID, playerIDValue)
	if err != nil {
		logger.Log("ERROR", "Failed to find ship assignments", map[string]interface{}{
			"container_id": containerID,
			"error":        err.Error(),
		})
		return err
	}

	// Release each ship using Ship aggregate pattern
	for _, ship := range assignedShips {
		ship.ForceRelease(reason, h.clock)
		if err := h.shipRepo.Save(ctx, ship); err != nil {
			logger.Log("WARNING", "Failed to release ship assignment", map[string]interface{}{
				"ship_symbol":  ship.ShipSymbol(),
				"container_id": containerID,
				"error":        err.Error(),
			})
		}
	}

	logger.Log("INFO", "Released all ship assignments", map[string]interface{}{
		"container_id": containerID,
		"ships_count":  len(assignedShips),
		"reason":       reason,
	})

	return nil
}
