package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	"github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// MinCargoValueForRecovery is the minimum cargo value (in credits) to warrant selling vs jettisoning
const MinCargoValueForRecovery = 10000

// RunArbitrageWorkerCommand executes a single arbitrage run for a ship
type RunArbitrageWorkerCommand struct {
	ShipSymbol    string                        // Ship to use for arbitrage
	Opportunity   *trading.ArbitrageOpportunity // Opportunity to execute
	PlayerID      int                           // Player identifier
	ContainerID   string                        // Container ID for ledger tracking
	CoordinatorID string                        // Parent coordinator container ID
	MinBalance    int                           // Minimum credit balance to maintain (0 = no limit)
	SystemSymbol  string                        // System symbol for market lookups (recovery)
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
	executor   *services.ArbitrageExecutor
	shipRepo   navigation.ShipRepository
	marketRepo market.MarketRepository
	mediator   common.Mediator
}

// NewRunArbitrageWorkerHandler creates a new handler
func NewRunArbitrageWorkerHandler(
	executor *services.ArbitrageExecutor,
	shipRepo navigation.ShipRepository,
	marketRepo market.MarketRepository,
	mediator common.Mediator,
) *RunArbitrageWorkerHandler {
	return &RunArbitrageWorkerHandler{
		executor:   executor,
		shipRepo:   shipRepo,
		marketRepo: marketRepo,
		mediator:   mediator,
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

	// Step 2: Handle existing cargo (from interrupted trade)
	if ship.CargoUnits() > 0 {
		if err := h.recoverExistingCargo(ctx, ship, cmd, playerIDValue); err != nil {
			logger.Log("WARN", fmt.Sprintf("Cargo recovery failed: %v", err), map[string]interface{}{
				"ship": cmd.ShipSymbol,
			})
			// Continue with trade anyway - ship may have had cargo jettisoned
		}
		// Reload ship after recovery
		ship, err = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, playerIDValue)
		if err != nil {
			return nil, fmt.Errorf("failed to reload ship after recovery: %w", err)
		}
	}

	logger.Log("INFO", "Starting arbitrage worker", map[string]interface{}{
		"ship":        cmd.ShipSymbol,
		"good":        cmd.Opportunity.Good(),
		"buy_market":  cmd.Opportunity.BuyMarket().Symbol,
		"sell_market": cmd.Opportunity.SellMarket().Symbol,
		"margin":      fmt.Sprintf("%.1f%%", cmd.Opportunity.ProfitMargin()),
	})

	// Step 3: Execute arbitrage run
	result, err := h.executor.Execute(
		ctx,
		ship,
		cmd.Opportunity,
		cmd.PlayerID,
		cmd.ContainerID,
		cmd.MinBalance,
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

	// Step 4: Return results
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

// recoverExistingCargo handles cargo from an interrupted trade.
// If total cargo value >= MinCargoValueForRecovery, sells at best market.
// Otherwise, jettisons the cargo to free up space.
func (h *RunArbitrageWorkerHandler) recoverExistingCargo(
	ctx context.Context,
	ship *navigation.Ship,
	cmd *RunArbitrageWorkerCommand,
	playerID shared.PlayerID,
) error {
	logger := common.LoggerFromContext(ctx)

	cargoItems := ship.Cargo().Inventory
	if len(cargoItems) == 0 {
		return nil
	}

	logger.Log("INFO", "Found existing cargo, evaluating for recovery", map[string]interface{}{
		"ship":        cmd.ShipSymbol,
		"cargo_units": ship.CargoUnits(),
	})

	// Calculate total cargo value using best market prices
	totalValue := 0
	var evaluations []cargoEvaluation

	systemSymbol := cmd.SystemSymbol
	if systemSymbol == "" {
		systemSymbol = ship.CurrentLocation().SystemSymbol
	}

	for _, item := range cargoItems {
		bestMarket, err := h.marketRepo.FindBestMarketBuying(ctx, item.Symbol, systemSymbol, cmd.PlayerID)
		if err != nil {
			logger.Log("WARN", fmt.Sprintf("Could not find market for %s: %v", item.Symbol, err), nil)
			// No market found - value is 0
			evaluations = append(evaluations, cargoEvaluation{
				good:  item.Symbol,
				units: item.Units,
			})
			continue
		}

		itemValue := bestMarket.PurchasePrice * item.Units
		totalValue += itemValue
		evaluations = append(evaluations, cargoEvaluation{
			good:       item.Symbol,
			units:      item.Units,
			bestMarket: bestMarket.WaypointSymbol,
			bestPrice:  bestMarket.PurchasePrice,
			value:      itemValue,
		})
	}

	logger.Log("INFO", fmt.Sprintf("Cargo evaluation complete: total value=%d, threshold=%d",
		totalValue, MinCargoValueForRecovery), map[string]interface{}{
		"ship":        cmd.ShipSymbol,
		"total_value": totalValue,
		"threshold":   MinCargoValueForRecovery,
	})

	if totalValue >= MinCargoValueForRecovery {
		// Sell cargo at best markets
		return h.sellRecoveredCargo(ctx, cmd, playerID, evaluations)
	}

	// Jettison cargo - not worth the trip
	return h.jettisonAllCargo(ctx, cmd, playerID, evaluations)
}

// sellRecoveredCargo navigates to best market and sells cargo
func (h *RunArbitrageWorkerHandler) sellRecoveredCargo(
	ctx context.Context,
	cmd *RunArbitrageWorkerCommand,
	playerID shared.PlayerID,
	evaluations []cargoEvaluation,
) error {
	logger := common.LoggerFromContext(ctx)

	for _, eval := range evaluations {
		if eval.bestMarket == "" {
			// No market - jettison this item
			logger.Log("INFO", fmt.Sprintf("No market for %s, jettisoning %d units", eval.good, eval.units), nil)
			_, err := h.mediator.Send(ctx, &shipCmd.JettisonCargoCommand{
				ShipSymbol: cmd.ShipSymbol,
				PlayerID:   playerID,
				GoodSymbol: eval.good,
				Units:      eval.units,
			})
			if err != nil {
				return fmt.Errorf("failed to jettison %s: %w", eval.good, err)
			}
			continue
		}

		logger.Log("INFO", fmt.Sprintf("Recovering cargo: selling %d %s at %s for %d credits",
			eval.units, eval.good, eval.bestMarket, eval.value), map[string]interface{}{
			"good":        eval.good,
			"units":       eval.units,
			"market":      eval.bestMarket,
			"total_value": eval.value,
		})

		// Navigate to sell market
		_, err := h.mediator.Send(ctx, &shipCmd.NavigateRouteCommand{
			ShipSymbol:   cmd.ShipSymbol,
			Destination:  eval.bestMarket,
			PlayerID:     playerID,
			PreferCruise: false,
		})
		if err != nil {
			return fmt.Errorf("failed to navigate to sell market %s: %w", eval.bestMarket, err)
		}

		// Dock
		_, err = h.mediator.Send(ctx, &shipTypes.DockShipCommand{
			ShipSymbol: cmd.ShipSymbol,
			PlayerID:   playerID,
		})
		if err != nil {
			return fmt.Errorf("failed to dock at %s: %w", eval.bestMarket, err)
		}

		// Sell
		_, err = h.mediator.Send(ctx, &shipCmd.SellCargoCommand{
			ShipSymbol: cmd.ShipSymbol,
			GoodSymbol: eval.good,
			Units:      eval.units,
			PlayerID:   playerID,
		})
		if err != nil {
			return fmt.Errorf("failed to sell %s: %w", eval.good, err)
		}

		logger.Log("INFO", fmt.Sprintf("Recovered cargo sold: %d %s at %s",
			eval.units, eval.good, eval.bestMarket), nil)
	}

	return nil
}

// jettisonAllCargo jettisons all cargo items
func (h *RunArbitrageWorkerHandler) jettisonAllCargo(
	ctx context.Context,
	cmd *RunArbitrageWorkerCommand,
	playerID shared.PlayerID,
	evaluations []cargoEvaluation,
) error {
	logger := common.LoggerFromContext(ctx)

	for _, eval := range evaluations {
		logger.Log("INFO", fmt.Sprintf("Jettisoning cargo: %d %s (value below threshold)",
			eval.units, eval.good), map[string]interface{}{
			"good":  eval.good,
			"units": eval.units,
			"value": eval.value,
		})

		_, err := h.mediator.Send(ctx, &shipCmd.JettisonCargoCommand{
			ShipSymbol: cmd.ShipSymbol,
			PlayerID:   playerID,
			GoodSymbol: eval.good,
			Units:      eval.units,
		})
		if err != nil {
			return fmt.Errorf("failed to jettison %s: %w", eval.good, err)
		}
	}

	logger.Log("INFO", "All cargo jettisoned", map[string]interface{}{
		"ship": cmd.ShipSymbol,
	})

	return nil
}

// cargoEvaluation holds evaluation data for a cargo item
type cargoEvaluation struct {
	good       string
	units      int
	bestMarket string
	bestPrice  int
	value      int
}
