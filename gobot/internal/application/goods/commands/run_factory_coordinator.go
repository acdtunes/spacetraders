package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/contract"
	goodsServices "github.com/andrescamacho/spacetraders-go/internal/application/goods/services"
	goodsTypes "github.com/andrescamacho/spacetraders-go/internal/application/goods/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Type aliases for convenience
type RunFactoryCoordinatorCommand = goodsTypes.RunFactoryCoordinatorCommand
type RunFactoryCoordinatorResponse = goodsTypes.RunFactoryCoordinatorResponse

// RunFactoryCoordinatorHandler orchestrates fleet-based goods production.
// Pattern: Fleet Coordinator (like ContractFleetCoordinator)
//
// Workflow:
// 1. Build dependency tree using SupplyChainResolver
// 2. Flatten tree to get all production nodes
// 3. Discover idle ships for parallel execution
// 4. Execute production sequentially (MVP)
//    TODO: Parallel workers with goroutines and channels
// 5. Return results with quantity acquired
//
// Error Handling:
// - Worker failure → retry with different ship (TODO)
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

	return &RunFactoryCoordinatorHandler{
		mediator:           mediator,
		shipRepo:           shipRepo,
		marketRepo:         marketRepo,
		shipAssignmentRepo: shipAssignmentRepo,
		resolver:           resolver,
		marketLocator:      marketLocator,
		productionExecutor: productionExecutor,
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
		"factory_id":  response.FactoryID,
		"ship_count":  len(idleShips),
		"ship_symbols": idleShipSymbols,
	})

	// Step 4: Execute production (sequential MVP implementation)
	// TODO: Implement parallel worker execution with goroutines and channels
	// TODO: Create worker containers via daemon client
	// TODO: Coordinate completion via unbuffered channels
	// For now, execute sequentially with the first available ship
	if err := h.executeSequentialProduction(ctx, cmd, dependencyTree, idleShips[0], response); err != nil {
		return fmt.Errorf("sequential production failed: %w", err)
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

// executeSequentialProduction executes production sequentially with a single ship (MVP)
func (h *RunFactoryCoordinatorHandler) executeSequentialProduction(
	ctx context.Context,
	cmd *RunFactoryCoordinatorCommand,
	dependencyTree *goods.SupplyChainNode,
	ship *navigation.Ship,
	response *RunFactoryCoordinatorResponse,
) error {
	logger := common.LoggerFromContext(ctx)

	logger.Log("INFO", "Starting sequential production (MVP mode)", map[string]interface{}{
		"factory_id":  response.FactoryID,
		"ship_symbol": ship.ShipSymbol(),
		"target_good": cmd.TargetGood,
	})

	// Execute production for the root node (will recursively produce inputs)
	result, err := h.productionExecutor.ProduceGood(
		ctx,
		ship,
		dependencyTree,
		cmd.SystemSymbol,
		cmd.PlayerID,
	)
	if err != nil {
		return fmt.Errorf("production execution failed: %w", err)
	}

	// Update response with results
	response.QuantityAcquired = result.QuantityAcquired
	response.TotalCost = result.TotalCost
	response.NodesCompleted = response.NodesTotal // All nodes completed in recursive execution
	response.ShipsUsed = 1

	logger.Log("INFO", "Sequential production completed", map[string]interface{}{
		"factory_id":  response.FactoryID,
		"target_good": cmd.TargetGood,
		"quantity":    result.QuantityAcquired,
		"total_cost":  result.TotalCost,
		"waypoint":    result.WaypointSymbol,
	})

	return nil
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

// generateFactoryID generates a unique factory ID
func generateFactoryID() string {
	// TODO: Implement proper ID generation (e.g., UUID or timestamp-based)
	return "factory-placeholder-id"
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
