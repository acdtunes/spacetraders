package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// RunArbitrageWorkerCommand executes a single arbitrage run for a ship
type RunArbitrageWorkerCommand struct {
	ShipSymbol  string                         // Ship to use for arbitrage
	Opportunity *trading.ArbitrageOpportunity  // Opportunity to execute
	PlayerID    int                            // Player identifier
	ContainerID string                         // Container ID for ledger tracking
}

// RunArbitrageWorkerResponse contains the results of the arbitrage run
type RunArbitrageWorkerResponse struct {
	Success         bool   // Whether execution succeeded
	Good            string // Trade good symbol
	NetProfit       int    // Net profit (revenue - costs - fuel)
	DurationSeconds int    // Execution duration
	Error           string // Error message if failed
}

// RunArbitrageWorkerHandler executes a single arbitrage run
type RunArbitrageWorkerHandler struct {
	executor *services.ArbitrageExecutor
	shipRepo navigation.ShipRepository
}

// NewRunArbitrageWorkerHandler creates a new handler
func NewRunArbitrageWorkerHandler(
	executor *services.ArbitrageExecutor,
	shipRepo navigation.ShipRepository,
) *RunArbitrageWorkerHandler {
	return &RunArbitrageWorkerHandler{
		executor: executor,
		shipRepo: shipRepo,
	}
}

// Handle executes the command
func (h *RunArbitrageWorkerHandler) Handle(
	ctx context.Context,
	request common.Request,
) (common.Response, error) {
	cmd, ok := request.(*RunArbitrageWorkerCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	logger := common.LoggerFromContext(ctx)

	playerIDValue := shared.MustNewPlayerID(cmd.PlayerID)

	// Step 1: Load ship
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, playerIDValue)
	if err != nil {
		errMsg := fmt.Sprintf("ship not found: %v", err)
		logger.Log("ERROR", errMsg, map[string]interface{}{
			"ship": cmd.ShipSymbol,
		})
		return &RunArbitrageWorkerResponse{
			Success: false,
			Good:    cmd.Opportunity.Good(),
			Error:   errMsg,
		}, nil
	}

	logger.Log("INFO", "Starting arbitrage worker", map[string]interface{}{
		"ship":       cmd.ShipSymbol,
		"good":       cmd.Opportunity.Good(),
		"buy_market": cmd.Opportunity.BuyMarket().Symbol,
		"sell_market": cmd.Opportunity.SellMarket().Symbol,
		"margin":     fmt.Sprintf("%.1f%%", cmd.Opportunity.ProfitMargin()),
	})

	// Step 2: Execute arbitrage run
	result, err := h.executor.Execute(
		ctx,
		ship,
		cmd.Opportunity,
		cmd.PlayerID,
		cmd.ContainerID,
	)
	if err != nil {
		errMsg := fmt.Sprintf("execution failed: %v", err)
		logger.Log("ERROR", errMsg, map[string]interface{}{
			"ship": cmd.ShipSymbol,
			"good": cmd.Opportunity.Good(),
		})
		return &RunArbitrageWorkerResponse{
			Success: false,
			Good:    cmd.Opportunity.Good(),
			Error:   errMsg,
		}, nil
	}

	// Step 3: Return results
	logger.Log("INFO", "Arbitrage worker completed", map[string]interface{}{
		"ship":       cmd.ShipSymbol,
		"good":       result.Good,
		"net_profit": result.NetProfit,
		"duration":   result.DurationSeconds,
	})

	return &RunArbitrageWorkerResponse{
		Success:         true,
		Good:            result.Good,
		NetProfit:       result.NetProfit,
		DurationSeconds: result.DurationSeconds,
	}, nil
}
