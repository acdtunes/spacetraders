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
	ShipSymbol     string                           // Ship to use for this task
	Task           *manufacturing.ManufacturingTask // Task to execute
	PlayerID       int                              // Player identifier
	ContainerID    string                           // Container ID for ledger tracking
	CoordinatorID  string                           // Parent coordinator container ID
	PipelineNumber int                              // Sequential pipeline number (1, 2, 3...)
	ProductGood    string                           // Final manufactured product (e.g., LASER_RIFLES)
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

	// Create operation context for transaction tracking
	if cmd.ContainerID != "" {
		opContext := shared.NewOperationContext(cmd.ContainerID, "manufacturing_worker")
		ctx = shared.WithOperationContext(ctx, opContext)
	}

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
	case manufacturing.TaskTypeAcquireDeliver:
		err = h.executeAcquireDeliver(ctx, cmd)
	case manufacturing.TaskTypeCollectSell:
		err = h.executeCollectSell(ctx, cmd)
	case manufacturing.TaskTypeLiquidate:
		err = h.executeLiquidate(ctx, cmd)
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

// executeLiquidate sells orphaned cargo at a demand market to recover investment
func (h *RunManufacturingTaskWorkerHandler) executeLiquidate(
	ctx context.Context,
	cmd *RunManufacturingTaskWorkerCommand,
) error {
	task := cmd.Task
	playerID := shared.MustNewPlayerID(cmd.PlayerID)
	logger := common.LoggerFromContext(ctx)

	logger.Log("DEBUG", "LIQUIDATE: Loading ship state", map[string]interface{}{
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
		logger.Log("INFO", "LIQUIDATE: No cargo to sell (idempotent skip)", map[string]interface{}{
			"good": task.Good(),
		})
		return nil // Already sold
	}

	cargoQty := ship.Cargo().GetItemUnits(task.Good())
	logger.Log("DEBUG", "LIQUIDATE: Have cargo to sell", map[string]interface{}{
		"good":     task.Good(),
		"quantity": cargoQty,
	})

	// Navigate to sell market
	if ship.CurrentLocation().Symbol != task.TargetMarket() {
		logger.Log("INFO", "LIQUIDATE: Navigating to sell market", map[string]interface{}{
			"from": ship.CurrentLocation().Symbol,
			"to":   task.TargetMarket(),
			"good": task.Good(),
		})
		_, err = h.mediator.Send(ctx, &shipCmd.NavigateRouteCommand{
			ShipSymbol:   cmd.ShipSymbol,
			Destination:  task.TargetMarket(),
			PlayerID:     playerID,
			PreferCruise: false,
		})
		if err != nil {
			return fmt.Errorf("failed to navigate to market %s: %w", task.TargetMarket(), err)
		}
		logger.Log("DEBUG", "LIQUIDATE: Navigation complete", map[string]interface{}{
			"waypoint": task.TargetMarket(),
		})
	} else {
		logger.Log("DEBUG", "LIQUIDATE: Already at sell market", map[string]interface{}{
			"waypoint": task.TargetMarket(),
		})
	}

	// Dock
	logger.Log("DEBUG", "LIQUIDATE: Docking at market", nil)
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

	logger.Log("INFO", "LIQUIDATE: Selling goods (recovery)", map[string]interface{}{
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

	logger.Log("INFO", "LIQUIDATE: Sale complete", map[string]interface{}{
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
		Description:       fmt.Sprintf("Manufacturing: Liquidate %d %s (recovery)", resp.UnitsSold, task.Good()),
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

// executeAcquireDeliver is an atomic task that buys from source market AND delivers to factory.
// This prevents the "orphaned cargo" bug where a ship buys goods but a different ship delivers.
func (h *RunManufacturingTaskWorkerHandler) executeAcquireDeliver(
	ctx context.Context,
	cmd *RunManufacturingTaskWorkerCommand,
) error {
	task := cmd.Task
	playerID := shared.MustNewPlayerID(cmd.PlayerID)
	logger := common.LoggerFromContext(ctx)

	logger.Log("INFO", "ACQUIRE_DELIVER: Starting atomic buy-and-deliver", map[string]interface{}{
		"ship":         cmd.ShipSymbol,
		"good":         task.Good(),
		"source":       task.SourceMarket(),
		"factory":      task.FactorySymbol(),
	})

	// Load ship
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, playerID)
	if err != nil {
		return fmt.Errorf("failed to load ship: %w", err)
	}

	// --- PHASE 1: ACQUIRE (buy from source market) ---

	// Idempotent: Check if we already have the cargo (resume after crash)
	alreadyHasCargo := ship.Cargo().HasItem(task.Good(), 1)
	var totalUnitsAdded int
	var totalCost int

	if alreadyHasCargo {
		// We already have cargo - skip acquisition phase
		totalUnitsAdded = ship.Cargo().GetItemUnits(task.Good())
		logger.Log("INFO", "ACQUIRE_DELIVER: Already have cargo (resuming at delivery phase)", map[string]interface{}{
			"good":     task.Good(),
			"quantity": totalUnitsAdded,
		})
	} else {
		// Navigate to source market
		if ship.CurrentLocation().Symbol != task.SourceMarket() {
			logger.Log("INFO", "ACQUIRE_DELIVER: Navigating to source market", map[string]interface{}{
				"from": ship.CurrentLocation().Symbol,
				"to":   task.SourceMarket(),
			})
			_, err = h.mediator.Send(ctx, &shipCmd.NavigateRouteCommand{
				ShipSymbol:   cmd.ShipSymbol,
				Destination:  task.SourceMarket(),
				PlayerID:     playerID,
				PreferCruise: false,
			})
			if err != nil {
				return fmt.Errorf("failed to navigate to %s: %w", task.SourceMarket(), err)
			}
		}

		// Dock at source market
		_, err = h.mediator.Send(ctx, &shipTypes.DockShipCommand{
			ShipSymbol: cmd.ShipSymbol,
			PlayerID:   playerID,
		})
		if err != nil {
			return fmt.Errorf("failed to dock at source: %w", err)
		}

		// Purchase loop (same logic as executeAcquire)
		purchaseCount := 0
		const maxPurchases = 10

		for purchaseCount < maxPurchases {
			// Get fresh market data
			marketData, err := h.marketRepo.GetMarketData(ctx, task.SourceMarket(), cmd.PlayerID)
			if err != nil {
				if purchaseCount == 0 {
					return fmt.Errorf("failed to get market data: %w", err)
				}
				break
			}

			// Find supply level and trade volume
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

			// Reload ship for available cargo
			ship, _ = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, playerID)
			availableCargo := ship.AvailableCargoSpace()
			if availableCargo <= 0 {
				break
			}

			// Determine quantity
			quantity := availableCargo
			if task.Quantity() > 0 {
				remaining := task.Quantity() - totalUnitsAdded
				if remaining <= 0 {
					break
				}
				if remaining < quantity {
					quantity = remaining
				}
			}

			// Apply supply-aware limit
			supplyAwareLimit := calculateSupplyAwareLimit(supplyLevel, tradeVolume)
			if supplyAwareLimit > 0 && supplyAwareLimit < quantity {
				quantity = supplyAwareLimit
			}

			if quantity <= 0 {
				break
			}

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
				break
			}

			resp := purchaseResp.(*shipCmd.PurchaseCargoResponse)
			totalUnitsAdded += resp.UnitsAdded
			totalCost += resp.TotalCost
			purchaseCount++

			pricePerUnit := 0
			if resp.UnitsAdded > 0 {
				pricePerUnit = resp.TotalCost / resp.UnitsAdded
			}

			logger.Log("INFO", "ACQUIRE_DELIVER: Purchased goods", map[string]interface{}{
				"good":           task.Good(),
				"units":          resp.UnitsAdded,
				"cost":           resp.TotalCost,
				"price_per_unit": pricePerUnit,
				"round":          purchaseCount,
			})

			// Record ledger transaction
			_, _ = h.mediator.Send(ctx, &ledgerCmd.RecordTransactionCommand{
				PlayerID:          cmd.PlayerID,
				TransactionType:   string(ledger.TransactionTypePurchaseCargo),
				Amount:            -resp.TotalCost,
				Description:       fmt.Sprintf("Manufacturing: Buy %d %s for delivery to factory", resp.UnitsAdded, task.Good()),
				RelatedEntityType: "manufacturing_task",
				RelatedEntityID:   task.ID(),
				OperationType:     "manufacturing",
				Metadata: map[string]interface{}{
					"task_id":        task.ID(),
					"good":           task.Good(),
					"quantity":       resp.UnitsAdded,
					"price_per_unit": pricePerUnit,
					"source_market":  task.SourceMarket(),
					"factory":        task.FactorySymbol(),
				},
			})

			if resp.UnitsAdded == 0 {
				break
			}
		}

		if totalUnitsAdded == 0 {
			return fmt.Errorf("ACQUIRE_DELIVER: no goods acquired at %s - will retry", task.SourceMarket())
		}
	}

	// --- PHASE 2: DELIVER (sell to factory) ---

	// Reload ship
	ship, _ = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, playerID)

	// Navigate to factory
	if ship.CurrentLocation().Symbol != task.FactorySymbol() {
		logger.Log("INFO", "ACQUIRE_DELIVER: Navigating to factory", map[string]interface{}{
			"from":     ship.CurrentLocation().Symbol,
			"to":       task.FactorySymbol(),
			"carrying": totalUnitsAdded,
		})
		_, err = h.mediator.Send(ctx, &shipCmd.NavigateRouteCommand{
			ShipSymbol:   cmd.ShipSymbol,
			Destination:  task.FactorySymbol(),
			PlayerID:     playerID,
			PreferCruise: false,
		})
		if err != nil {
			return fmt.Errorf("failed to navigate to factory %s: %w", task.FactorySymbol(), err)
		}
	}

	// Dock at factory
	_, err = h.mediator.Send(ctx, &shipTypes.DockShipCommand{
		ShipSymbol: cmd.ShipSymbol,
		PlayerID:   playerID,
	})
	if err != nil {
		return fmt.Errorf("failed to dock at factory: %w", err)
	}

	// Reload and get cargo quantity
	ship, _ = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, playerID)
	deliveryQty := ship.Cargo().GetItemUnits(task.Good())

	logger.Log("INFO", "ACQUIRE_DELIVER: Delivering to factory", map[string]interface{}{
		"good":     task.Good(),
		"quantity": deliveryQty,
		"factory":  task.FactorySymbol(),
	})

	// Sell to factory (delivering inputs)
	sellResp, err := h.mediator.Send(ctx, &shipCmd.SellCargoCommand{
		ShipSymbol: cmd.ShipSymbol,
		GoodSymbol: task.Good(),
		Units:      deliveryQty,
		PlayerID:   playerID,
	})
	if err != nil {
		return fmt.Errorf("failed to deliver %s to factory: %w", task.Good(), err)
	}

	resp := sellResp.(*shipCmd.SellCargoResponse)
	task.SetActualQuantity(resp.UnitsSold)
	task.SetTotalCost(totalCost)
	task.SetTotalRevenue(resp.TotalRevenue)

	pricePerUnit := 0
	if resp.UnitsSold > 0 {
		pricePerUnit = resp.TotalRevenue / resp.UnitsSold
	}

	logger.Log("INFO", "ACQUIRE_DELIVER: Complete", map[string]interface{}{
		"good":           task.Good(),
		"delivered":      resp.UnitsSold,
		"revenue":        resp.TotalRevenue,
		"cost":           totalCost,
		"net":            resp.TotalRevenue - totalCost,
		"price_per_unit": pricePerUnit,
	})

	// Record delivery ledger transaction
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
			"factory":        task.FactorySymbol(),
		},
	})

	return nil
}

// executeCollectSell is an atomic task that collects from factory AND sells at market.
// This prevents the "orphaned cargo" bug where a ship collects but a different ship sells.
func (h *RunManufacturingTaskWorkerHandler) executeCollectSell(
	ctx context.Context,
	cmd *RunManufacturingTaskWorkerCommand,
) error {
	task := cmd.Task
	playerID := shared.MustNewPlayerID(cmd.PlayerID)
	logger := common.LoggerFromContext(ctx)

	logger.Log("INFO", "COLLECT_SELL: Starting atomic collect-and-sell", map[string]interface{}{
		"ship":    cmd.ShipSymbol,
		"good":    task.Good(),
		"factory": task.FactorySymbol(),
		"market":  task.TargetMarket(),
	})

	// Load ship
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, playerID)
	if err != nil {
		return fmt.Errorf("failed to load ship: %w", err)
	}

	// --- PHASE 1: COLLECT (buy from factory when supply is HIGH) ---

	// Idempotent: Check if we already have the cargo (resume after crash)
	alreadyHasCargo := ship.Cargo().HasItem(task.Good(), 1)
	var totalUnitsAdded int
	var totalCost int

	if alreadyHasCargo {
		totalUnitsAdded = ship.Cargo().GetItemUnits(task.Good())
		logger.Log("INFO", "COLLECT_SELL: Already have cargo (resuming at sell phase)", map[string]interface{}{
			"good":     task.Good(),
			"quantity": totalUnitsAdded,
		})
	} else {
		// Navigate to factory
		if ship.CurrentLocation().Symbol != task.FactorySymbol() {
			logger.Log("INFO", "COLLECT_SELL: Navigating to factory", map[string]interface{}{
				"from": ship.CurrentLocation().Symbol,
				"to":   task.FactorySymbol(),
			})
			_, err = h.mediator.Send(ctx, &shipCmd.NavigateRouteCommand{
				ShipSymbol:   cmd.ShipSymbol,
				Destination:  task.FactorySymbol(),
				PlayerID:     playerID,
				PreferCruise: false,
			})
			if err != nil {
				return fmt.Errorf("failed to navigate to factory %s: %w", task.FactorySymbol(), err)
			}
		}

		// Dock at factory
		_, err = h.mediator.Send(ctx, &shipTypes.DockShipCommand{
			ShipSymbol: cmd.ShipSymbol,
			PlayerID:   playerID,
		})
		if err != nil {
			return fmt.Errorf("failed to dock at factory: %w", err)
		}

		// Purchase loop with HIGH supply check (same logic as executeCollect)
		purchaseCount := 0
		const maxPurchases = 10

		for purchaseCount < maxPurchases {
			// Get fresh market data
			marketData, err := h.marketRepo.GetMarketData(ctx, task.FactorySymbol(), cmd.PlayerID)
			if err != nil {
				if purchaseCount == 0 {
					return fmt.Errorf("failed to get market data: %w", err)
				}
				break
			}

			// Find supply level and trade volume
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

			if supplyLevel == "" {
				if purchaseCount == 0 {
					return fmt.Errorf("factory %s does not export %s", task.FactorySymbol(), task.Good())
				}
				break
			}

			// COLLECT requires HIGH or ABUNDANT supply
			if supplyLevel != "HIGH" && supplyLevel != "ABUNDANT" {
				if purchaseCount == 0 {
					logger.Log("WARN", "COLLECT_SELL: Factory supply not ready", map[string]interface{}{
						"factory":  task.FactorySymbol(),
						"good":     task.Good(),
						"supply":   supplyLevel,
						"required": "HIGH or ABUNDANT",
					})
					return fmt.Errorf("factory %s supply is %s, need HIGH or ABUNDANT - will retry", task.FactorySymbol(), supplyLevel)
				}
				break
			}

			// Reload ship for available cargo
			ship, _ = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, playerID)
			availableCargo := ship.AvailableCargoSpace()
			if availableCargo <= 0 {
				break
			}

			// Determine quantity
			quantity := availableCargo
			if task.Quantity() > 0 {
				remaining := task.Quantity() - totalUnitsAdded
				if remaining <= 0 {
					break
				}
				if remaining < quantity {
					quantity = remaining
				}
			}

			// Apply supply-aware limit
			supplyAwareLimit := calculateSupplyAwareLimit(supplyLevel, tradeVolume)
			if supplyAwareLimit > 0 && supplyAwareLimit < quantity {
				quantity = supplyAwareLimit
			}

			if quantity <= 0 {
				break
			}

			// Purchase from factory at export price
			purchaseResp, err := h.mediator.Send(ctx, &shipCmd.PurchaseCargoCommand{
				ShipSymbol: cmd.ShipSymbol,
				GoodSymbol: task.Good(),
				Units:      quantity,
				PlayerID:   playerID,
			})
			if err != nil {
				if purchaseCount == 0 {
					return fmt.Errorf("failed to collect %s from factory: %w", task.Good(), err)
				}
				break
			}

			resp := purchaseResp.(*shipCmd.PurchaseCargoResponse)
			totalUnitsAdded += resp.UnitsAdded
			totalCost += resp.TotalCost
			purchaseCount++

			pricePerUnit := 0
			if resp.UnitsAdded > 0 {
				pricePerUnit = resp.TotalCost / resp.UnitsAdded
			}

			logger.Log("INFO", "COLLECT_SELL: Collected from factory", map[string]interface{}{
				"good":           task.Good(),
				"units":          resp.UnitsAdded,
				"cost":           resp.TotalCost,
				"price_per_unit": pricePerUnit,
				"supply":         supplyLevel,
				"round":          purchaseCount,
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
					"task_id":      task.ID(),
					"good":         task.Good(),
					"quantity":     resp.UnitsAdded,
					"factory":      task.FactorySymbol(),
					"supply_level": supplyLevel,
				},
			})

			if resp.UnitsAdded == 0 {
				break
			}
		}

		if totalUnitsAdded == 0 {
			return fmt.Errorf("COLLECT_SELL: no goods collected from factory - will retry")
		}
	}

	// --- PHASE 2: SELL (sell at target market) ---

	// Reload ship
	ship, _ = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, playerID)

	// Navigate to sell market
	if ship.CurrentLocation().Symbol != task.TargetMarket() {
		logger.Log("INFO", "COLLECT_SELL: Navigating to sell market", map[string]interface{}{
			"from":     ship.CurrentLocation().Symbol,
			"to":       task.TargetMarket(),
			"carrying": totalUnitsAdded,
		})
		_, err = h.mediator.Send(ctx, &shipCmd.NavigateRouteCommand{
			ShipSymbol:   cmd.ShipSymbol,
			Destination:  task.TargetMarket(),
			PlayerID:     playerID,
			PreferCruise: false,
		})
		if err != nil {
			return fmt.Errorf("failed to navigate to market %s: %w", task.TargetMarket(), err)
		}
	}

	// Dock at market
	_, err = h.mediator.Send(ctx, &shipTypes.DockShipCommand{
		ShipSymbol: cmd.ShipSymbol,
		PlayerID:   playerID,
	})
	if err != nil {
		return fmt.Errorf("failed to dock at market: %w", err)
	}

	// Reload and get cargo quantity
	ship, _ = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, playerID)
	sellQty := ship.Cargo().GetItemUnits(task.Good())

	logger.Log("INFO", "COLLECT_SELL: Selling at market", map[string]interface{}{
		"good":     task.Good(),
		"quantity": sellQty,
		"market":   task.TargetMarket(),
	})

	// Sell goods
	sellResp, err := h.mediator.Send(ctx, &shipCmd.SellCargoCommand{
		ShipSymbol: cmd.ShipSymbol,
		GoodSymbol: task.Good(),
		Units:      sellQty,
		PlayerID:   playerID,
	})
	if err != nil {
		return fmt.Errorf("failed to sell %s: %w", task.Good(), err)
	}

	resp := sellResp.(*shipCmd.SellCargoResponse)
	task.SetActualQuantity(resp.UnitsSold)
	task.SetTotalCost(totalCost)
	task.SetTotalRevenue(resp.TotalRevenue)

	pricePerUnit := 0
	if resp.UnitsSold > 0 {
		pricePerUnit = resp.TotalRevenue / resp.UnitsSold
	}

	netProfit := resp.TotalRevenue - totalCost
	logger.Log("INFO", "COLLECT_SELL: Complete", map[string]interface{}{
		"good":           task.Good(),
		"sold":           resp.UnitsSold,
		"revenue":        resp.TotalRevenue,
		"cost":           totalCost,
		"net_profit":     netProfit,
		"price_per_unit": pricePerUnit,
	})

	// Record sell ledger transaction
	_, _ = h.mediator.Send(ctx, &ledgerCmd.RecordTransactionCommand{
		PlayerID:          cmd.PlayerID,
		TransactionType:   string(ledger.TransactionTypeSellCargo),
		Amount:            resp.TotalRevenue,
		Description:       fmt.Sprintf("Manufacturing: Sell %d %s (profit: %d)", resp.UnitsSold, task.Good(), netProfit),
		RelatedEntityType: "manufacturing_task",
		RelatedEntityID:   task.ID(),
		OperationType:     "manufacturing",
		Metadata: map[string]interface{}{
			"task_id":        task.ID(),
			"good":           task.Good(),
			"quantity":       resp.UnitsSold,
			"price_per_unit": pricePerUnit,
			"market":         task.TargetMarket(),
			"net_profit":     netProfit,
		},
	})

	return nil
}