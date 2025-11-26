package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	ledgerCmd "github.com/andrescamacho/spacetraders-go/internal/application/ledger/commands"
	shipCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// RunManufacturingTaskWorkerCommand executes a single manufacturing task
type RunManufacturingTaskWorkerCommand struct {
	ShipSymbol    string                           // Ship to use for this task
	Task          *manufacturing.ManufacturingTask // Task to execute
	PlayerID      int                              // Player identifier
	ContainerID   string                           // Container ID for ledger tracking
	CoordinatorID string                           // Parent coordinator container ID
}

// RunManufacturingTaskWorkerResponse contains the results of task execution
type RunManufacturingTaskWorkerResponse struct {
	Success        bool   // Whether execution succeeded
	TaskID         string // Task ID
	TaskType       string // Task type
	Good           string // Trade good handled
	ActualQuantity int    // Actual quantity handled
	TotalCost      int    // Cost incurred
	TotalRevenue   int    // Revenue earned
	NetProfit      int    // Net profit (revenue - cost)
	DurationMs     int64  // Execution duration in milliseconds
	Error          string // Error message if failed
}

// RunManufacturingTaskWorkerHandler executes a single manufacturing task
type RunManufacturingTaskWorkerHandler struct {
	shipRepo   navigation.ShipRepository
	marketRepo market.MarketRepository
	taskRepo   manufacturing.TaskRepository
	mediator   common.Mediator
}

// NewRunManufacturingTaskWorkerHandler creates a new handler
func NewRunManufacturingTaskWorkerHandler(
	shipRepo navigation.ShipRepository,
	marketRepo market.MarketRepository,
	taskRepo manufacturing.TaskRepository,
	mediator common.Mediator,
) *RunManufacturingTaskWorkerHandler {
	return &RunManufacturingTaskWorkerHandler{
		shipRepo:   shipRepo,
		marketRepo: marketRepo,
		taskRepo:   taskRepo,
		mediator:   mediator,
	}
}

// Handle executes the command
func (h *RunManufacturingTaskWorkerHandler) Handle(
	ctx context.Context,
	request common.Request,
) (common.Response, error) {
	cmd, ok := request.(*RunManufacturingTaskWorkerCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	logger := common.LoggerFromContext(ctx)
	startTime := time.Now()
	task := cmd.Task

	logger.Log("INFO", "Starting manufacturing task", map[string]interface{}{
		"task_id": task.ID()[:8],
		"type":    task.TaskType(),
		"good":    task.Good(),
		"ship":    cmd.ShipSymbol,
	})

	// Mark task as executing
	if err := task.StartExecution(); err != nil {
		return h.failResponse(task, startTime, fmt.Sprintf("failed to start execution: %v", err)), nil
	}

	// Execute based on task type
	var err error
	switch task.TaskType() {
	case manufacturing.TaskTypeAcquire:
		err = h.executeAcquire(ctx, cmd)
	case manufacturing.TaskTypeDeliver:
		err = h.executeDeliver(ctx, cmd)
	case manufacturing.TaskTypeCollect:
		err = h.executeCollect(ctx, cmd)
	case manufacturing.TaskTypeSell, manufacturing.TaskTypeLiquidate:
		err = h.executeSell(ctx, cmd)
	default:
		err = fmt.Errorf("unknown task type: %s", task.TaskType())
	}

	if err != nil {
		logger.Log("ERROR", "Manufacturing task failed", map[string]interface{}{
			"task_id": task.ID()[:8],
			"type":    task.TaskType(),
			"error":   err.Error(),
		})
		task.Fail(err.Error())
		// Persist failed task state
		if h.taskRepo != nil {
			_ = h.taskRepo.Update(ctx, task)
		}

		// Record failed task metrics
		metrics.RecordManufacturingTaskCompletion(cmd.PlayerID, string(task.TaskType()), "failed", time.Since(startTime))
		metrics.RecordManufacturingTaskRetry(cmd.PlayerID, string(task.TaskType()))

		return h.failResponse(task, startTime, err.Error()), nil
	}

	// Mark task as complete
	if err := task.Complete(); err != nil {
		return h.failResponse(task, startTime, fmt.Sprintf("failed to complete task: %v", err)), nil
	}

	// Persist completed task state
	if h.taskRepo != nil {
		if err := h.taskRepo.Update(ctx, task); err != nil {
			logger.Log("WARN", fmt.Sprintf("Failed to persist task completion: %v", err), nil)
		}
	}

	duration := time.Since(startTime)
	logger.Log("INFO", "Manufacturing task completed", map[string]interface{}{
		"task_id":     task.ID()[:8],
		"type":        task.TaskType(),
		"good":        task.Good(),
		"quantity":    task.ActualQuantity(),
		"cost":        task.TotalCost(),
		"revenue":     task.TotalRevenue(),
		"duration_ms": duration.Milliseconds(),
	})

	// Record successful task metrics
	metrics.RecordManufacturingTaskCompletion(cmd.PlayerID, string(task.TaskType()), "completed", duration)

	// Record cost and revenue metrics
	if task.TotalCost() > 0 {
		metrics.RecordManufacturingCost(cmd.PlayerID, string(task.TaskType()), task.TotalCost())
	}
	if task.TotalRevenue() > 0 {
		metrics.RecordManufacturingRevenue(cmd.PlayerID, task.TotalRevenue())
	}

	return &RunManufacturingTaskWorkerResponse{
		Success:        true,
		TaskID:         task.ID(),
		TaskType:       string(task.TaskType()),
		Good:           task.Good(),
		ActualQuantity: task.ActualQuantity(),
		TotalCost:      task.TotalCost(),
		TotalRevenue:   task.TotalRevenue(),
		NetProfit:      task.NetProfit(),
		DurationMs:     duration.Milliseconds(),
	}, nil
}

// failResponse creates a failure response
func (h *RunManufacturingTaskWorkerHandler) failResponse(
	task *manufacturing.ManufacturingTask,
	startTime time.Time,
	errMsg string,
) *RunManufacturingTaskWorkerResponse {
	return &RunManufacturingTaskWorkerResponse{
		Success:    false,
		TaskID:     task.ID(),
		TaskType:   string(task.TaskType()),
		Good:       task.Good(),
		DurationMs: time.Since(startTime).Milliseconds(),
		Error:      errMsg,
	}
}

// executeAcquire buys goods from an export market
func (h *RunManufacturingTaskWorkerHandler) executeAcquire(
	ctx context.Context,
	cmd *RunManufacturingTaskWorkerCommand,
) error {
	task := cmd.Task
	playerID := shared.MustNewPlayerID(cmd.PlayerID)
	logger := common.LoggerFromContext(ctx)

	logger.Log("DEBUG", "ACQUIRE: Loading ship state", map[string]interface{}{
		"ship": cmd.ShipSymbol,
		"good": task.Good(),
	})

	// Load ship
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, playerID)
	if err != nil {
		return fmt.Errorf("failed to load ship: %w", err)
	}

	// Idempotent: Check if we already have the cargo
	if ship.Cargo().HasItem(task.Good(), 1) {
		qty := ship.Cargo().GetItemUnits(task.Good())
		task.SetActualQuantity(qty)
		logger.Log("INFO", "ACQUIRE: Already have cargo (idempotent skip)", map[string]interface{}{
			"good":     task.Good(),
			"quantity": qty,
		})
		return nil // Already acquired
	}

	// Navigate to source market
	if ship.CurrentLocation().Symbol != task.SourceMarket() {
		logger.Log("INFO", "ACQUIRE: Navigating to source market", map[string]interface{}{
			"from":   ship.CurrentLocation().Symbol,
			"to":     task.SourceMarket(),
			"good":   task.Good(),
		})
		_, err = h.mediator.Send(ctx, &shipCmd.NavigateRouteCommand{
			ShipSymbol:   cmd.ShipSymbol,
			Destination:  task.SourceMarket(),
			PlayerID:     playerID,
			PreferCruise: true,
		})
		if err != nil {
			return fmt.Errorf("failed to navigate to %s: %w", task.SourceMarket(), err)
		}
		logger.Log("DEBUG", "ACQUIRE: Navigation complete", map[string]interface{}{
			"waypoint": task.SourceMarket(),
		})
	} else {
		logger.Log("DEBUG", "ACQUIRE: Already at source market", map[string]interface{}{
			"waypoint": task.SourceMarket(),
		})
	}

	// Dock
	logger.Log("DEBUG", "ACQUIRE: Docking at market", nil)
	_, err = h.mediator.Send(ctx, &shipTypes.DockShipCommand{
		ShipSymbol: cmd.ShipSymbol,
		PlayerID:   playerID,
	})
	if err != nil {
		return fmt.Errorf("failed to dock: %w", err)
	}

	// Purchase loop: keep buying until supply drops below HIGH or cargo is full
	// This maximizes acquisition while supply is abundant
	var totalUnitsAdded int
	var totalCost int
	purchaseCount := 0
	const maxPurchases = 10 // Safety limit to prevent infinite loops

	for purchaseCount < maxPurchases {
		// Get fresh market data for current supply level
		marketData, err := h.marketRepo.GetMarketData(ctx, task.SourceMarket(), cmd.PlayerID)
		if err != nil {
			if purchaseCount == 0 {
				return fmt.Errorf("failed to get market data for %s: %w", task.SourceMarket(), err)
			}
			// If we already purchased some, just log and break
			logger.Log("WARN", "ACQUIRE: Failed to refresh market data, stopping", map[string]interface{}{
				"error":           err.Error(),
				"purchases_so_far": purchaseCount,
			})
			break
		}

		// Find the trade good at this market
		var supplyLevel string
		var tradeVolume int
		for _, good := range marketData.TradeGoods() {
			if good.Symbol() == task.Good() {
				if good.Supply() != nil {
					supplyLevel = *good.Supply()
				}
				tradeVolume = good.TradeVolume()
				break
			}
		}

		// Check if supply has dropped below HIGH - stop purchasing
		if supplyLevel != "HIGH" && supplyLevel != "ABUNDANT" {
			logger.Log("INFO", "ACQUIRE: Supply dropped below HIGH, stopping purchases", map[string]interface{}{
				"supply":           supplyLevel,
				"purchases_so_far": purchaseCount,
				"total_units":      totalUnitsAdded,
			})
			break
		}

		// Reload ship for available cargo
		ship, _ = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, playerID)
		availableCargo := ship.AvailableCargoSpace()
		if availableCargo <= 0 {
			logger.Log("INFO", "ACQUIRE: Cargo full, stopping purchases", map[string]interface{}{
				"purchases_so_far": purchaseCount,
				"total_units":      totalUnitsAdded,
			})
			break
		}

		// Determine quantity to purchase this round
		quantity := availableCargo
		if task.Quantity() > 0 {
			remaining := task.Quantity() - totalUnitsAdded
			if remaining <= 0 {
				break // Already acquired target quantity
			}
			if remaining < quantity {
				quantity = remaining
			}
		}

		// Apply supply-aware limit per purchase (to not crash supply in one buy)
		supplyAwareLimit := calculateSupplyAwareLimit(supplyLevel, tradeVolume)
		if supplyAwareLimit > 0 && supplyAwareLimit < quantity {
			quantity = supplyAwareLimit
		}

		if quantity <= 0 {
			break
		}

		logger.Log("INFO", "ACQUIRE: Purchasing goods", map[string]interface{}{
			"good":               task.Good(),
			"quantity":           quantity,
			"available_cargo":    availableCargo,
			"supply":             supplyLevel,
			"trade_volume":       tradeVolume,
			"supply_aware_limit": supplyAwareLimit,
			"waypoint":           task.SourceMarket(),
			"purchase_round":     purchaseCount + 1,
		})

		// Purchase goods
		purchaseResp, err := h.mediator.Send(ctx, &shipCmd.PurchaseCargoCommand{
			ShipSymbol: cmd.ShipSymbol,
			GoodSymbol: task.Good(),
			Units:      quantity,
			PlayerID:   playerID,
		})
		if err != nil {
			if purchaseCount == 0 {
				return fmt.Errorf("failed to purchase %s: %w", task.Good(), err)
			}
			// If we already purchased some, just log and break
			logger.Log("WARN", "ACQUIRE: Purchase failed, stopping", map[string]interface{}{
				"error":            err.Error(),
				"purchases_so_far": purchaseCount,
			})
			break
		}

		resp := purchaseResp.(*shipCmd.PurchaseCargoResponse)
		totalUnitsAdded += resp.UnitsAdded
		totalCost += resp.TotalCost
		purchaseCount++

		// Calculate price per unit for metadata
		pricePerUnit := 0
		if resp.UnitsAdded > 0 {
			pricePerUnit = resp.TotalCost / resp.UnitsAdded
		}

		logger.Log("INFO", "ACQUIRE: Purchase round complete", map[string]interface{}{
			"good":             task.Good(),
			"units_added":      resp.UnitsAdded,
			"total_cost":       resp.TotalCost,
			"price_per_unit":   pricePerUnit,
			"purchase_round":   purchaseCount,
			"cumulative_units": totalUnitsAdded,
			"cumulative_cost":  totalCost,
		})

		// Record ledger transaction for this purchase
		_, _ = h.mediator.Send(ctx, &ledgerCmd.RecordTransactionCommand{
			PlayerID:          cmd.PlayerID,
			TransactionType:   string(ledger.TransactionTypePurchaseCargo),
			Amount:            -resp.TotalCost,
			Description:       fmt.Sprintf("Manufacturing: Buy %d %s (round %d)", resp.UnitsAdded, task.Good(), purchaseCount),
			RelatedEntityType: "manufacturing_task",
			RelatedEntityID:   task.ID(),
			OperationType:     "manufacturing",
			Metadata: map[string]interface{}{
				"task_id":        task.ID(),
				"good":           task.Good(),
				"quantity":       resp.UnitsAdded,
				"price_per_unit": pricePerUnit,
				"waypoint":       task.SourceMarket(),
				"purchase_round": purchaseCount,
			},
		})

		// If we didn't get any units, break to avoid infinite loop
		if resp.UnitsAdded == 0 {
			logger.Log("WARN", "ACQUIRE: No units added, stopping", nil)
			break
		}
	}

	// Update task with final totals
	task.SetActualQuantity(totalUnitsAdded)
	task.SetTotalCost(totalCost)

	logger.Log("INFO", "ACQUIRE: All purchases complete", map[string]interface{}{
		"good":           task.Good(),
		"total_units":    totalUnitsAdded,
		"total_cost":     totalCost,
		"purchase_count": purchaseCount,
	})

	return nil
}

// executeDeliver delivers goods to a factory
func (h *RunManufacturingTaskWorkerHandler) executeDeliver(
	ctx context.Context,
	cmd *RunManufacturingTaskWorkerCommand,
) error {
	task := cmd.Task
	playerID := shared.MustNewPlayerID(cmd.PlayerID)
	logger := common.LoggerFromContext(ctx)

	logger.Log("DEBUG", "DELIVER: Loading ship state", map[string]interface{}{
		"ship": cmd.ShipSymbol,
		"good": task.Good(),
	})

	// Load ship
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, playerID)
	if err != nil {
		return fmt.Errorf("failed to load ship: %w", err)
	}

	// Idempotent: Check if cargo is empty (already delivered)
	if !ship.Cargo().HasItem(task.Good(), 1) {
		logger.Log("INFO", "DELIVER: No cargo to deliver (idempotent skip)", map[string]interface{}{
			"good": task.Good(),
		})
		return nil // Already delivered
	}

	cargoQty := ship.Cargo().GetItemUnits(task.Good())
	logger.Log("DEBUG", "DELIVER: Have cargo to deliver", map[string]interface{}{
		"good":     task.Good(),
		"quantity": cargoQty,
	})

	// Navigate to factory
	if ship.CurrentLocation().Symbol != task.TargetMarket() {
		logger.Log("INFO", "DELIVER: Navigating to factory", map[string]interface{}{
			"from":    ship.CurrentLocation().Symbol,
			"to":      task.TargetMarket(),
			"good":    task.Good(),
		})
		_, err = h.mediator.Send(ctx, &shipCmd.NavigateRouteCommand{
			ShipSymbol:   cmd.ShipSymbol,
			Destination:  task.TargetMarket(),
			PlayerID:     playerID,
			PreferCruise: true,
		})
		if err != nil {
			return fmt.Errorf("failed to navigate to factory %s: %w", task.TargetMarket(), err)
		}
		logger.Log("DEBUG", "DELIVER: Navigation complete", map[string]interface{}{
			"waypoint": task.TargetMarket(),
		})
	} else {
		logger.Log("DEBUG", "DELIVER: Already at factory", map[string]interface{}{
			"waypoint": task.TargetMarket(),
		})
	}

	// Dock
	logger.Log("DEBUG", "DELIVER: Docking at factory", nil)
	_, err = h.mediator.Send(ctx, &shipTypes.DockShipCommand{
		ShipSymbol: cmd.ShipSymbol,
		PlayerID:   playerID,
	})
	if err != nil {
		return fmt.Errorf("failed to dock: %w", err)
	}

	// Reload ship to get cargo quantity
	ship, _ = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, playerID)
	quantity := ship.Cargo().GetItemUnits(task.Good())

	logger.Log("INFO", "DELIVER: Selling to factory (delivering inputs)", map[string]interface{}{
		"good":     task.Good(),
		"quantity": quantity,
		"factory":  task.TargetMarket(),
	})

	// Sell to factory (delivering inputs to factory = selling at import price)
	sellResp, err := h.mediator.Send(ctx, &shipCmd.SellCargoCommand{
		ShipSymbol: cmd.ShipSymbol,
		GoodSymbol: task.Good(),
		Units:      quantity,
		PlayerID:   playerID,
	})
	if err != nil {
		return fmt.Errorf("failed to deliver %s to factory: %w", task.Good(), err)
	}

	resp := sellResp.(*shipCmd.SellCargoResponse)
	task.SetActualQuantity(resp.UnitsSold)
	task.SetTotalRevenue(resp.TotalRevenue) // Factory pays for inputs

	// Calculate price per unit for metadata
	pricePerUnit := 0
	if resp.UnitsSold > 0 {
		pricePerUnit = resp.TotalRevenue / resp.UnitsSold
	}

	logger.Log("INFO", "DELIVER: Delivery complete", map[string]interface{}{
		"good":           task.Good(),
		"units_sold":     resp.UnitsSold,
		"total_revenue":  resp.TotalRevenue,
		"price_per_unit": pricePerUnit,
	})

	// Record ledger transaction
	_, _ = h.mediator.Send(ctx, &ledgerCmd.RecordTransactionCommand{
		PlayerID:          cmd.PlayerID,
		TransactionType:   string(ledger.TransactionTypeSellCargo),
		Amount:            resp.TotalRevenue,
		Description:       fmt.Sprintf("Manufacturing: Deliver %d %s to factory", resp.UnitsSold, task.Good()),
		RelatedEntityType: "manufacturing_task",
		RelatedEntityID:   task.ID(),
		OperationType:     "manufacturing",
		Metadata: map[string]interface{}{
			"task_id":        task.ID(),
			"good":           task.Good(),
			"quantity":       resp.UnitsSold,
			"price_per_unit": pricePerUnit,
			"waypoint":       task.TargetMarket(),
		},
	})

	return nil
}

// executeCollect buys produced goods from a factory ONLY when supply is HIGH or ABUNDANT
// This ensures we buy at factory export prices (low) not retail import prices (high)
func (h *RunManufacturingTaskWorkerHandler) executeCollect(
	ctx context.Context,
	cmd *RunManufacturingTaskWorkerCommand,
) error {
	task := cmd.Task
	playerID := shared.MustNewPlayerID(cmd.PlayerID)
	logger := common.LoggerFromContext(ctx)

	logger.Log("DEBUG", "COLLECT: Loading ship state", map[string]interface{}{
		"ship":    cmd.ShipSymbol,
		"good":    task.Good(),
		"factory": task.FactorySymbol(),
	})

	// Load ship
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, playerID)
	if err != nil {
		return fmt.Errorf("failed to load ship: %w", err)
	}

	// Idempotent: Check if we already have the cargo
	if ship.Cargo().HasItem(task.Good(), 1) {
		qty := ship.Cargo().GetItemUnits(task.Good())
		task.SetActualQuantity(qty)
		logger.Log("INFO", "COLLECT: Already have cargo (idempotent skip)", map[string]interface{}{
			"good":     task.Good(),
			"quantity": qty,
		})
		return nil // Already collected
	}

	// Navigate to factory
	if ship.CurrentLocation().Symbol != task.FactorySymbol() {
		logger.Log("INFO", "COLLECT: Navigating to factory", map[string]interface{}{
			"from":    ship.CurrentLocation().Symbol,
			"to":      task.FactorySymbol(),
			"good":    task.Good(),
		})
		_, err = h.mediator.Send(ctx, &shipCmd.NavigateRouteCommand{
			ShipSymbol:   cmd.ShipSymbol,
			Destination:  task.FactorySymbol(),
			PlayerID:     playerID,
			PreferCruise: true,
		})
		if err != nil {
			return fmt.Errorf("failed to navigate to factory %s: %w", task.FactorySymbol(), err)
		}
		logger.Log("DEBUG", "COLLECT: Navigation complete", map[string]interface{}{
			"waypoint": task.FactorySymbol(),
		})
	} else {
		logger.Log("DEBUG", "COLLECT: Already at factory", map[string]interface{}{
			"waypoint": task.FactorySymbol(),
		})
	}

	// Dock
	logger.Log("DEBUG", "COLLECT: Docking at factory", nil)
	_, err = h.mediator.Send(ctx, &shipTypes.DockShipCommand{
		ShipSymbol: cmd.ShipSymbol,
		PlayerID:   playerID,
	})
	if err != nil {
		return fmt.Errorf("failed to dock: %w", err)
	}

	// CRITICAL: Check supply level at factory before buying
	// We only buy when supply is HIGH or ABUNDANT (stable low prices)
	// This is the core of the manufacturing strategy - see docs/PARALLEL_MANUFACTURING_SYSTEM_DESIGN.md
	marketData, err := h.marketRepo.GetMarketData(ctx, task.FactorySymbol(), cmd.PlayerID)
	if err != nil {
		return fmt.Errorf("failed to get market data for factory %s: %w", task.FactorySymbol(), err)
	}

	// Find the trade good at this market
	var supplyLevel string
	var activityLevel string
	var tradeVolume int
	for _, good := range marketData.TradeGoods() {
		if good.Symbol() == task.Good() {
			if good.Supply() != nil {
				supplyLevel = *good.Supply()
			}
			if good.Activity() != nil {
				activityLevel = *good.Activity()
			}
			tradeVolume = good.TradeVolume()
			break
		}
	}

	if supplyLevel == "" {
		return fmt.Errorf("factory %s does not export %s - wrong factory selected", task.FactorySymbol(), task.Good())
	}

	// Only proceed if supply is HIGH or ABUNDANT
	// These are the stable supply levels where prices are predictable (2.9% drift)
	if supplyLevel != "HIGH" && supplyLevel != "ABUNDANT" {
		logger.Log("WARN", "Factory supply not ready for collection", map[string]interface{}{
			"factory":      task.FactorySymbol(),
			"good":         task.Good(),
			"supply":       supplyLevel,
			"required":     "HIGH or ABUNDANT",
			"will_retry":   true,
		})
		return fmt.Errorf("factory %s supply is %s, need HIGH or ABUNDANT - will retry", task.FactorySymbol(), supplyLevel)
	}

	// NOTE: Activity filter NOT applied to COLLECT tasks
	// Activity at factories (EXPORT markets) reflects production rate:
	// - GROWING = more goods being produced (good for collection)
	// - STRONG = high production (good for collection)
	// Activity filtering is only needed for SELL tasks where price volatility matters

	logger.Log("INFO", "Factory supply ready for collection", map[string]interface{}{
		"factory":      task.FactorySymbol(),
		"good":         task.Good(),
		"supply":       supplyLevel,
		"activity":     activityLevel,
		"trade_volume": tradeVolume,
	})

	// Reload ship to get available cargo space
	ship, _ = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, playerID)
	availableCargo := ship.AvailableCargoSpace()
	quantity := availableCargo
	if task.Quantity() > 0 && task.Quantity() < quantity {
		quantity = task.Quantity()
	}

	// Apply supply-aware limit to maintain stable supply
	// Uses calculateSupplyAwareLimit helper function
	supplyAwareLimit := calculateSupplyAwareLimit(supplyLevel, tradeVolume)
	if supplyAwareLimit > 0 && supplyAwareLimit < quantity {
		quantity = supplyAwareLimit
	}

	logger.Log("INFO", "COLLECT: Purchasing goods from factory", map[string]interface{}{
		"good":               task.Good(),
		"quantity":           quantity,
		"available_cargo":    availableCargo,
		"supply":             supplyLevel,
		"trade_volume":       tradeVolume,
		"supply_aware_limit": supplyAwareLimit,
		"factory":            task.FactorySymbol(),
	})

	// Purchase produced goods from factory at export price
	purchaseResp, err := h.mediator.Send(ctx, &shipCmd.PurchaseCargoCommand{
		ShipSymbol: cmd.ShipSymbol,
		GoodSymbol: task.Good(),
		Units:      quantity,
		PlayerID:   playerID,
	})
	if err != nil {
		return fmt.Errorf("failed to collect %s from factory: %w", task.Good(), err)
	}

	resp := purchaseResp.(*shipCmd.PurchaseCargoResponse)
	task.SetActualQuantity(resp.UnitsAdded)
	task.SetTotalCost(resp.TotalCost)

	// Calculate price per unit for metadata
	pricePerUnit := 0
	if resp.UnitsAdded > 0 {
		pricePerUnit = resp.TotalCost / resp.UnitsAdded
	}

	logger.Log("INFO", "Collected goods from factory at export price", map[string]interface{}{
		"factory":        task.FactorySymbol(),
		"good":           task.Good(),
		"quantity":       resp.UnitsAdded,
		"price_per_unit": pricePerUnit,
		"total_cost":     resp.TotalCost,
		"supply":         supplyLevel,
	})

	// Record ledger transaction
	_, _ = h.mediator.Send(ctx, &ledgerCmd.RecordTransactionCommand{
		PlayerID:          cmd.PlayerID,
		TransactionType:   string(ledger.TransactionTypePurchaseCargo),
		Amount:            -resp.TotalCost,
		Description:       fmt.Sprintf("Manufacturing: Collect %d %s from factory (supply=%s)", resp.UnitsAdded, task.Good(), supplyLevel),
		RelatedEntityType: "manufacturing_task",
		RelatedEntityID:   task.ID(),
		OperationType:     "manufacturing",
		Metadata: map[string]interface{}{
			"task_id":        task.ID(),
			"good":           task.Good(),
			"quantity":       resp.UnitsAdded,
			"price_per_unit": pricePerUnit,
			"waypoint":       task.FactorySymbol(),
			"supply_level":   supplyLevel,
		},
	})

	return nil
}

// executeSell sells goods at a demand market
func (h *RunManufacturingTaskWorkerHandler) executeSell(
	ctx context.Context,
	cmd *RunManufacturingTaskWorkerCommand,
) error {
	task := cmd.Task
	playerID := shared.MustNewPlayerID(cmd.PlayerID)
	logger := common.LoggerFromContext(ctx)

	taskType := "SELL"
	if task.TaskType() == manufacturing.TaskTypeLiquidate {
		taskType = "LIQUIDATE"
	}

	logger.Log("DEBUG", fmt.Sprintf("%s: Loading ship state", taskType), map[string]interface{}{
		"ship": cmd.ShipSymbol,
		"good": task.Good(),
	})

	// Load ship
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, playerID)
	if err != nil {
		return fmt.Errorf("failed to load ship: %w", err)
	}

	// Idempotent: Check if cargo is empty (already sold)
	if !ship.Cargo().HasItem(task.Good(), 1) {
		logger.Log("INFO", fmt.Sprintf("%s: No cargo to sell (idempotent skip)", taskType), map[string]interface{}{
			"good": task.Good(),
		})
		return nil // Already sold
	}

	cargoQty := ship.Cargo().GetItemUnits(task.Good())
	logger.Log("DEBUG", fmt.Sprintf("%s: Have cargo to sell", taskType), map[string]interface{}{
		"good":     task.Good(),
		"quantity": cargoQty,
	})

	// Navigate to sell market
	if ship.CurrentLocation().Symbol != task.TargetMarket() {
		logger.Log("INFO", fmt.Sprintf("%s: Navigating to sell market", taskType), map[string]interface{}{
			"from": ship.CurrentLocation().Symbol,
			"to":   task.TargetMarket(),
			"good": task.Good(),
		})
		_, err = h.mediator.Send(ctx, &shipCmd.NavigateRouteCommand{
			ShipSymbol:   cmd.ShipSymbol,
			Destination:  task.TargetMarket(),
			PlayerID:     playerID,
			PreferCruise: true,
		})
		if err != nil {
			return fmt.Errorf("failed to navigate to market %s: %w", task.TargetMarket(), err)
		}
		logger.Log("DEBUG", fmt.Sprintf("%s: Navigation complete", taskType), map[string]interface{}{
			"waypoint": task.TargetMarket(),
		})
	} else {
		logger.Log("DEBUG", fmt.Sprintf("%s: Already at sell market", taskType), map[string]interface{}{
			"waypoint": task.TargetMarket(),
		})
	}

	// Dock
	logger.Log("DEBUG", fmt.Sprintf("%s: Docking at market", taskType), nil)
	_, err = h.mediator.Send(ctx, &shipTypes.DockShipCommand{
		ShipSymbol: cmd.ShipSymbol,
		PlayerID:   playerID,
	})
	if err != nil {
		return fmt.Errorf("failed to dock: %w", err)
	}

	// Reload ship to get cargo quantity
	ship, _ = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, playerID)
	quantity := ship.Cargo().GetItemUnits(task.Good())

	logger.Log("INFO", fmt.Sprintf("%s: Selling goods", taskType), map[string]interface{}{
		"good":     task.Good(),
		"quantity": quantity,
		"market":   task.TargetMarket(),
	})

	// Sell goods
	sellResp, err := h.mediator.Send(ctx, &shipCmd.SellCargoCommand{
		ShipSymbol: cmd.ShipSymbol,
		GoodSymbol: task.Good(),
		Units:      quantity,
		PlayerID:   playerID,
	})
	if err != nil {
		return fmt.Errorf("failed to sell %s: %w", task.Good(), err)
	}

	resp := sellResp.(*shipCmd.SellCargoResponse)
	task.SetActualQuantity(resp.UnitsSold)
	task.SetTotalRevenue(resp.TotalRevenue)

	// Calculate price per unit for metadata
	pricePerUnit := 0
	if resp.UnitsSold > 0 {
		pricePerUnit = resp.TotalRevenue / resp.UnitsSold
	}

	logger.Log("INFO", fmt.Sprintf("%s: Sale complete", taskType), map[string]interface{}{
		"good":           task.Good(),
		"units_sold":     resp.UnitsSold,
		"total_revenue":  resp.TotalRevenue,
		"price_per_unit": pricePerUnit,
	})

	// Record ledger transaction
	transactionType := string(ledger.TransactionTypeSellCargo)
	description := fmt.Sprintf("Manufacturing: Sell %d %s", resp.UnitsSold, task.Good())
	if task.TaskType() == manufacturing.TaskTypeLiquidate {
		description = fmt.Sprintf("Manufacturing: Liquidate %d %s (recovery)", resp.UnitsSold, task.Good())
	}

	_, _ = h.mediator.Send(ctx, &ledgerCmd.RecordTransactionCommand{
		PlayerID:          cmd.PlayerID,
		TransactionType:   transactionType,
		Amount:            resp.TotalRevenue,
		Description:       description,
		RelatedEntityType: "manufacturing_task",
		RelatedEntityID:   task.ID(),
		OperationType:     "manufacturing",
		Metadata: map[string]interface{}{
			"task_id":        task.ID(),
			"good":           task.Good(),
			"quantity":       resp.UnitsSold,
			"price_per_unit": pricePerUnit,
			"waypoint":       task.TargetMarket(),
		},
	})

	return nil
}

// calculateSupplyAwareLimit determines safe purchase quantity based on current supply level
// See docs/PARALLEL_MANUFACTURING_SYSTEM_DESIGN.md - Trade Size Calculation
// This prevents crashing supply by limiting purchase based on supply scarcity
func calculateSupplyAwareLimit(supply string, tradeVolume int) int {
	if tradeVolume <= 0 {
		return 0 // No limit if trade volume unknown
	}

	// Supply-aware multipliers from design doc
	multipliers := map[string]float64{
		"ABUNDANT": 0.80, // Plenty of buffer
		"HIGH":     0.60, // Sweet spot - maintain stability
		"MODERATE": 0.40, // Careful - could drop to LIMITED
		"LIMITED":  0.20, // Very careful - critical supply
		"SCARCE":   0.10, // Minimal - supply nearly depleted
	}

	multiplier, ok := multipliers[supply]
	if !ok {
		multiplier = 0.40 // Default to conservative (MODERATE)
	}

	return int(float64(tradeVolume) * multiplier)
}