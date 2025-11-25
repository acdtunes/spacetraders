package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	goodsServices "github.com/andrescamacho/spacetraders-go/internal/application/goods/services"
	shipCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// RunManufacturingWorkerCommand executes a single manufacturing arbitrage run for a ship.
// The worker manufactures goods based on demand (high import prices) and sells them.
type RunManufacturingWorkerCommand struct {
	ShipSymbol    string                             // Ship to use for manufacturing
	Opportunity   *trading.ManufacturingOpportunity  // Opportunity to execute
	PlayerID      int                                // Player identifier
	ContainerID   string                             // Container ID for ledger tracking
	CoordinatorID string                             // Parent coordinator container ID
	SystemSymbol  string                             // System symbol for market lookups
}

// RunManufacturingWorkerResponse contains the results of the manufacturing run
type RunManufacturingWorkerResponse struct {
	Success           bool    // Whether execution succeeded
	Good              string  // Trade good symbol
	QuantityProduced  int     // Units manufactured and sold
	ProductionCost    int     // Total cost to produce (inputs + fabrication)
	SaleRevenue       int     // Revenue from selling manufactured goods
	NetProfit         int     // Net profit (revenue - costs)
	DurationSeconds   int     // Execution duration
	Error             string  // Error message if failed
}

// RunManufacturingWorkerHandler executes a single manufacturing arbitrage run
type RunManufacturingWorkerHandler struct {
	productionExecutor *goodsServices.ProductionExecutor
	shipRepo           navigation.ShipRepository
	marketRepo         market.MarketRepository
	mediator           common.Mediator
	clock              shared.Clock
}

// NewRunManufacturingWorkerHandler creates a new handler
func NewRunManufacturingWorkerHandler(
	productionExecutor *goodsServices.ProductionExecutor,
	shipRepo navigation.ShipRepository,
	marketRepo market.MarketRepository,
	mediator common.Mediator,
	clock shared.Clock,
) *RunManufacturingWorkerHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &RunManufacturingWorkerHandler{
		productionExecutor: productionExecutor,
		shipRepo:           shipRepo,
		marketRepo:         marketRepo,
		mediator:           mediator,
		clock:              clock,
	}
}

// Handle executes the command
func (h *RunManufacturingWorkerHandler) Handle(
	ctx context.Context,
	request common.Request,
) (common.Response, error) {
	cmd, ok := request.(*RunManufacturingWorkerCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	logger := common.LoggerFromContext(ctx)
	startTime := h.clock.Now()

	playerIDValue := shared.MustNewPlayerID(cmd.PlayerID)

	// Step 1: Load ship
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, playerIDValue)
	if err != nil {
		errMsg := fmt.Sprintf("ship not found: %v", err)
		logger.Log("ERROR", errMsg, map[string]interface{}{
			"ship": cmd.ShipSymbol,
		})
		return &RunManufacturingWorkerResponse{
			Success: false,
			Good:    cmd.Opportunity.Good(),
			Error:   errMsg,
		}, nil
	}

	// Step 2: Handle existing cargo (from interrupted operation)
	if ship.CargoUnits() > 0 {
		if err := h.recoverExistingCargo(ctx, ship, cmd, playerIDValue); err != nil {
			logger.Log("WARN", fmt.Sprintf("Cargo recovery failed: %v", err), map[string]interface{}{
				"ship": cmd.ShipSymbol,
			})
			// Continue anyway - cargo may have been jettisoned
		}
		// Reload ship after recovery
		ship, err = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, playerIDValue)
		if err != nil {
			return nil, fmt.Errorf("failed to reload ship after recovery: %w", err)
		}
	}

	logger.Log("INFO", "Starting manufacturing worker", map[string]interface{}{
		"ship":        cmd.ShipSymbol,
		"good":        cmd.Opportunity.Good(),
		"sell_market": cmd.Opportunity.SellMarket().Symbol,
		"tree_depth":  cmd.Opportunity.TreeDepth(),
		"score":       fmt.Sprintf("%.1f", cmd.Opportunity.Score()),
	})

	// Step 3: Produce the good using the dependency tree
	opContext := &shared.OperationContext{
		ContainerID: cmd.ContainerID,
		OperationType: "manufacturing_arbitrage",
	}

	productionResult, err := h.productionExecutor.ProduceGood(
		ctx,
		ship,
		cmd.Opportunity.DependencyTree(),
		cmd.SystemSymbol,
		cmd.PlayerID,
		opContext,
	)
	if err != nil {
		errMsg := fmt.Sprintf("production failed: %v", err)
		logger.Log("ERROR", errMsg, map[string]interface{}{
			"ship": cmd.ShipSymbol,
			"good": cmd.Opportunity.Good(),
		})
		return &RunManufacturingWorkerResponse{
			Success:         false,
			Good:            cmd.Opportunity.Good(),
			DurationSeconds: int(h.clock.Now().Sub(startTime).Seconds()),
			Error:           errMsg,
		}, nil
	}

	logger.Log("INFO", "Production complete, navigating to sell market", map[string]interface{}{
		"ship":             cmd.ShipSymbol,
		"good":             cmd.Opportunity.Good(),
		"quantity":         productionResult.QuantityAcquired,
		"production_cost":  productionResult.TotalCost,
		"sell_market":      cmd.Opportunity.SellMarket().Symbol,
	})

	// Step 4: Navigate to sell market and dock
	sellMarket := cmd.Opportunity.SellMarket().Symbol
	_, err = h.mediator.Send(ctx, &shipCmd.NavigateRouteCommand{
		ShipSymbol:  cmd.ShipSymbol,
		Destination: sellMarket,
		PlayerID:    playerIDValue,
	})
	if err != nil {
		errMsg := fmt.Sprintf("failed to navigate to sell market: %v", err)
		logger.Log("ERROR", errMsg, map[string]interface{}{
			"ship":        cmd.ShipSymbol,
			"sell_market": sellMarket,
		})
		return &RunManufacturingWorkerResponse{
			Success:          false,
			Good:             cmd.Opportunity.Good(),
			QuantityProduced: productionResult.QuantityAcquired,
			ProductionCost:   productionResult.TotalCost,
			DurationSeconds:  int(h.clock.Now().Sub(startTime).Seconds()),
			Error:            errMsg,
		}, nil
	}

	// Dock at sell market
	_, err = h.mediator.Send(ctx, &shipTypes.DockShipCommand{
		ShipSymbol: cmd.ShipSymbol,
		PlayerID:   playerIDValue,
	})
	if err != nil {
		errMsg := fmt.Sprintf("failed to dock at sell market: %v", err)
		logger.Log("ERROR", errMsg, map[string]interface{}{
			"ship":        cmd.ShipSymbol,
			"sell_market": sellMarket,
		})
		return &RunManufacturingWorkerResponse{
			Success:          false,
			Good:             cmd.Opportunity.Good(),
			QuantityProduced: productionResult.QuantityAcquired,
			ProductionCost:   productionResult.TotalCost,
			DurationSeconds:  int(h.clock.Now().Sub(startTime).Seconds()),
			Error:            errMsg,
		}, nil
	}

	// Step 5: Sell manufactured goods
	// Reload ship to get current cargo
	ship, err = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, playerIDValue)
	if err != nil {
		return nil, fmt.Errorf("failed to reload ship before selling: %w", err)
	}

	totalRevenue := 0
	quantitySold := 0

	// Sell all cargo (should be the manufactured good)
	for _, item := range ship.Cargo().Inventory {
		sellResp, err := h.mediator.Send(ctx, &shipCmd.SellCargoCommand{
			ShipSymbol: cmd.ShipSymbol,
			GoodSymbol: item.Symbol,
			Units:      item.Units,
			PlayerID:   playerIDValue,
		})
		if err != nil {
			logger.Log("WARN", fmt.Sprintf("Failed to sell %s: %v", item.Symbol, err), nil)
			continue
		}

		response, ok := sellResp.(*shipCmd.SellCargoResponse)
		if !ok {
			logger.Log("WARN", "Unexpected response type from sell command", nil)
			continue
		}

		if item.Symbol == cmd.Opportunity.Good() {
			quantitySold += response.UnitsSold
		}
		totalRevenue += response.TotalRevenue

		logger.Log("INFO", fmt.Sprintf("Sold %d units of %s for %d credits",
			response.UnitsSold, item.Symbol, response.TotalRevenue), map[string]interface{}{
			"good":     item.Symbol,
			"units":    response.UnitsSold,
			"revenue":  response.TotalRevenue,
		})
	}

	// Calculate net profit
	netProfit := totalRevenue - productionResult.TotalCost
	duration := int(h.clock.Now().Sub(startTime).Seconds())

	logger.Log("INFO", "Manufacturing worker completed", map[string]interface{}{
		"ship":            cmd.ShipSymbol,
		"good":            cmd.Opportunity.Good(),
		"quantity":        quantitySold,
		"production_cost": productionResult.TotalCost,
		"sale_revenue":    totalRevenue,
		"net_profit":      netProfit,
		"duration":        duration,
	})

	return &RunManufacturingWorkerResponse{
		Success:          true,
		Good:             cmd.Opportunity.Good(),
		QuantityProduced: quantitySold,
		ProductionCost:   productionResult.TotalCost,
		SaleRevenue:      totalRevenue,
		NetProfit:        netProfit,
		DurationSeconds:  duration,
	}, nil
}

// manufacturingCargoEval holds evaluation data for a cargo item
type manufacturingCargoEval struct {
	good       string
	units      int
	bestMarket string
	bestPrice  int
	value      int
}

// recoverExistingCargo handles cargo from an interrupted manufacturing run.
// If total cargo value >= MinCargoValueForRecovery, sells at best market.
// Otherwise, jettisons the cargo to free up space.
func (h *RunManufacturingWorkerHandler) recoverExistingCargo(
	ctx context.Context,
	ship *navigation.Ship,
	cmd *RunManufacturingWorkerCommand,
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
	var evaluations []manufacturingCargoEval

	systemSymbol := cmd.SystemSymbol
	if systemSymbol == "" {
		systemSymbol = ship.CurrentLocation().SystemSymbol
	}

	for _, item := range cargoItems {
		bestMarket, err := h.marketRepo.FindBestMarketBuying(ctx, item.Symbol, systemSymbol, cmd.PlayerID)
		if err != nil {
			logger.Log("WARN", fmt.Sprintf("Could not find market for %s: %v", item.Symbol, err), nil)
			evaluations = append(evaluations, manufacturingCargoEval{
				good:  item.Symbol,
				units: item.Units,
			})
			continue
		}

		itemValue := bestMarket.PurchasePrice * item.Units
		totalValue += itemValue
		evaluations = append(evaluations, manufacturingCargoEval{
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
func (h *RunManufacturingWorkerHandler) sellRecoveredCargo(
	ctx context.Context,
	cmd *RunManufacturingWorkerCommand,
	playerID shared.PlayerID,
	evaluations []manufacturingCargoEval,
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
			ShipSymbol:  cmd.ShipSymbol,
			Destination: eval.bestMarket,
			PlayerID:    playerID,
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
func (h *RunManufacturingWorkerHandler) jettisonAllCargo(
	ctx context.Context,
	cmd *RunManufacturingWorkerCommand,
	playerID shared.PlayerID,
	evaluations []manufacturingCargoEval,
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
