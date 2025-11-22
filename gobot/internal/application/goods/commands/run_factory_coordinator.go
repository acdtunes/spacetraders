package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	goodsServices "github.com/andrescamacho/spacetraders-go/internal/application/goods/services"
	goodsTypes "github.com/andrescamacho/spacetraders-go/internal/application/goods/types"
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
// 4. Create and start worker containers for each node
// 5. Wait on completion channels
// 6. Loop until target good is acquired
// 7. Cleanup: release ships, mark factory completed
//
// Error Handling:
// - Worker failure → retry with different ship
// - No idle ships → wait with timeout
// - Market unavailable → backoff and retry
type RunFactoryCoordinatorHandler struct {
	mediator      common.Mediator
	shipRepo      navigation.ShipRepository
	marketRepo    market.MarketRepository
	resolver      *goodsServices.SupplyChainResolver
	marketLocator *goodsServices.MarketLocator
	clock         shared.Clock
}

// NewRunFactoryCoordinatorHandler creates a new factory coordinator handler
func NewRunFactoryCoordinatorHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	marketRepo market.MarketRepository,
	resolver *goodsServices.SupplyChainResolver,
	marketLocator *goodsServices.MarketLocator,
	clock shared.Clock,
) *RunFactoryCoordinatorHandler {
	return &RunFactoryCoordinatorHandler{
		mediator:      mediator,
		shipRepo:      shipRepo,
		marketRepo:    marketRepo,
		resolver:      resolver,
		marketLocator: marketLocator,
		clock:         clock,
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

	// TODO: Step 3: Discover idle ships for parallel execution
	// TODO: Step 4: Create worker containers for production nodes
	// TODO: Step 5: Start workers in goroutines with completion channels
	// TODO: Step 6: Wait on completion channels and track progress
	// TODO: Step 7: Loop until target good is acquired
	// TODO: Step 8: Cleanup: release ships, update factory status

	// For now, return success with tree built
	logger.Log("INFO", "Factory coordinator workflow placeholder", map[string]interface{}{
		"factory_id":  response.FactoryID,
		"target_good": cmd.TargetGood,
		"note":        "Full fleet coordination not yet implemented",
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
