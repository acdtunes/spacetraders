package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	goodsServices "github.com/andrescamacho/spacetraders-go/internal/application/goods/services"
	goodsTypes "github.com/andrescamacho/spacetraders-go/internal/application/goods/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Type aliases for convenience
type RunFactoryWorkerCommand = goodsTypes.RunFactoryWorkerCommand
type RunFactoryWorkerResponse = goodsTypes.RunFactoryWorkerResponse
type WorkerResult = goodsTypes.WorkerResult

// RunFactoryWorkerHandler executes the goods production workflow for a single node.
// Pattern: Single Workflow (like ContractWorkflow)
//
// Workflow:
// 1. Receive assigned production node from command
// 2. Check acquisition method (BUY or FABRICATE)
// 3. For BUY:
//    a. Find best market selling the good
//    b. Navigate to market and dock
//    c. Purchase whatever quantity is available
// 4. For FABRICATE:
//    a. Recursively produce all required inputs
//    b. Find manufacturing waypoint (imports this good)
//    c. Deliver all inputs to waypoint
//    d. Poll database until output good appears in exports
//    e. Purchase output good
// 5. Signal completion to coordinator (if channel provided)
// 6. Return with cargo containing produced good
type RunFactoryWorkerHandler struct {
	shipRepo          navigation.ShipRepository
	marketRepo        market.MarketRepository
	productionExecutor *goodsServices.ProductionExecutor
	clock             shared.Clock
}

// NewRunFactoryWorkerHandler creates a new factory worker handler
func NewRunFactoryWorkerHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	marketRepo market.MarketRepository,
	marketLocator *goodsServices.MarketLocator,
	clock shared.Clock,
) *RunFactoryWorkerHandler {
	productionExecutor := goodsServices.NewProductionExecutor(
		mediator,
		shipRepo,
		marketRepo,
		marketLocator,
		clock,
	)

	return &RunFactoryWorkerHandler{
		shipRepo:          shipRepo,
		marketRepo:        marketRepo,
		productionExecutor: productionExecutor,
		clock:             clock,
	}
}

// Handle executes the factory worker command
func (h *RunFactoryWorkerHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*RunFactoryWorkerCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	response := &RunFactoryWorkerResponse{
		FactoryID:        cmd.FactoryID,
		Good:             cmd.ProductionNode.Good,
		QuantityAcquired: 0,
		TotalCost:        0,
		Completed:        false,
		Error:            "",
	}

	// Execute production workflow
	if err := h.executeProduction(ctx, cmd, response); err != nil {
		response.Error = err.Error()
		return response, err
	}

	response.Completed = true

	// Signal completion to coordinator if channel provided
	if cmd.CompletionChan != nil {
		select {
		case cmd.CompletionChan <- WorkerResult{
			FactoryID:        cmd.FactoryID,
			Good:             cmd.ProductionNode.Good,
			QuantityAcquired: response.QuantityAcquired,
			TotalCost:        response.TotalCost,
			Error:            nil,
		}:
			// Successfully signaled
		case <-ctx.Done():
			// Context cancelled, skip signaling
		}
	}

	return response, nil
}

// executeProduction orchestrates the complete production workflow
func (h *RunFactoryWorkerHandler) executeProduction(
	ctx context.Context,
	cmd *RunFactoryWorkerCommand,
	response *RunFactoryWorkerResponse,
) error {
	logger := common.LoggerFromContext(ctx)

	logger.Log("INFO", "Starting factory worker", map[string]interface{}{
		"factory_id":         cmd.FactoryID,
		"ship_symbol":        cmd.ShipSymbol,
		"good":               cmd.ProductionNode.Good,
		"acquisition_method": cmd.ProductionNode.AcquisitionMethod,
	})

	// Get ship entity
	playerID := shared.MustNewPlayerID(cmd.PlayerID)
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, playerID)
	if err != nil {
		return fmt.Errorf("failed to find ship: %w", err)
	}

	// Execute production using ProductionExecutor
	// Note: Factory worker doesn't have container context, so opContext is nil
	result, err := h.productionExecutor.ProduceGood(
		ctx,
		ship,
		cmd.ProductionNode,
		cmd.SystemSymbol,
		cmd.PlayerID,
		nil, // No operation context for standalone worker
	)
	if err != nil {
		return fmt.Errorf("production failed: %w", err)
	}

	// Update response with results
	response.QuantityAcquired = result.QuantityAcquired
	response.TotalCost = result.TotalCost

	logger.Log("INFO", "Factory worker completed", map[string]interface{}{
		"factory_id":       cmd.FactoryID,
		"good":             cmd.ProductionNode.Good,
		"quantity_acquired": result.QuantityAcquired,
		"total_cost":       result.TotalCost,
		"waypoint":         result.WaypointSymbol,
	})

	return nil
}
