package manufacturing

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// MinLiquidateCargoValue is the minimum cargo value (in credits) required to create
// a LIQUIDATE task. If the cargo value is below this threshold, the cargo will be
// jettisoned instead to free up the ship for more valuable work.
const MinLiquidateCargoValue = 10000

// OrphanedCargoManager handles ships with cargo from interrupted operations
type OrphanedCargoManager interface {
	// HandleShipsWithExistingCargo processes idle ships that have cargo
	HandleShipsWithExistingCargo(ctx context.Context, params OrphanedCargoParams) (map[string]*navigation.Ship, error)

	// FindBestSellMarket finds best market to sell orphaned cargo
	FindBestSellMarket(ctx context.Context, currentLocation, good string, playerID int) (string, error)

	// CreateAdHocSellTask creates LIQUIDATE task for orphaned cargo
	CreateAdHocSellTask(ctx context.Context, ship *navigation.Ship, cargo CargoInfo, playerID int) (*manufacturing.ManufacturingTask, error)
}

// OrphanedCargoParams contains parameters for handling orphaned cargo
type OrphanedCargoParams struct {
	IdleShips          map[string]*navigation.Ship
	PlayerID           int
	MaxConcurrentTasks int
}

// CargoInfo contains information about cargo on a ship
type CargoInfo struct {
	Good  string
	Units int
}

// OrphanedCargoHandler implements OrphanedCargoManager
type OrphanedCargoHandler struct {
	taskRepo      manufacturing.TaskRepository
	marketRepo    market.MarketRepository
	workerManager WorkerManager
	taskAssigner  TaskAssigner
	mediator      common.Mediator

	// Function to get active pipelines
	getActivePipelines func() map[string]*manufacturing.ManufacturingPipeline
}

// NewOrphanedCargoHandler creates a new orphaned cargo handler
func NewOrphanedCargoHandler(
	taskRepo manufacturing.TaskRepository,
	marketRepo market.MarketRepository,
) *OrphanedCargoHandler {
	return &OrphanedCargoHandler{
		taskRepo:   taskRepo,
		marketRepo: marketRepo,
	}
}

// SetWorkerManager sets the worker manager dependency
func (h *OrphanedCargoHandler) SetWorkerManager(wm WorkerManager) {
	h.workerManager = wm
}

// SetTaskAssigner sets the task assigner dependency
func (h *OrphanedCargoHandler) SetTaskAssigner(ta TaskAssigner) {
	h.taskAssigner = ta
}

// SetActivePipelinesGetter sets the function to get active pipelines
func (h *OrphanedCargoHandler) SetActivePipelinesGetter(getter func() map[string]*manufacturing.ManufacturingPipeline) {
	h.getActivePipelines = getter
}

// SetMediator sets the mediator for executing commands (like jettison)
func (h *OrphanedCargoHandler) SetMediator(m common.Mediator) {
	h.mediator = m
}

// HandleShipsWithExistingCargo handles ships that have cargo from interrupted operations
func (h *OrphanedCargoHandler) HandleShipsWithExistingCargo(
	ctx context.Context,
	params OrphanedCargoParams,
) (map[string]*navigation.Ship, error) {
	logger := common.LoggerFromContext(ctx)

	if h.taskRepo == nil {
		return params.IdleShips, nil
	}

	// Get current assignment count
	assignedCount := 0
	if h.taskAssigner != nil {
		assignedCount = h.taskAssigner.(*TaskAssignmentManager).GetAssignmentCount()
	}

	if assignedCount >= params.MaxConcurrentTasks {
		return params.IdleShips, nil
	}

	// Find ships with cargo
	shipsWithCargo := make(map[string]*navigation.Ship)
	for symbol, ship := range params.IdleShips {
		if ship.CargoUnits() > 0 {
			shipsWithCargo[symbol] = ship
		}
	}

	if len(shipsWithCargo) == 0 {
		return params.IdleShips, nil
	}

	logger.Log("INFO", "Found idle ships with cargo", map[string]interface{}{
		"ships_with_cargo": len(shipsWithCargo),
	})

	// Get active pipeline IDs
	var pipelineIDs []string
	if h.getActivePipelines != nil {
		pipelines := h.getActivePipelines()
		pipelineIDs = make([]string, 0, len(pipelines))
		for id := range pipelines {
			pipelineIDs = append(pipelineIDs, id)
		}
	}

	// Process each ship with cargo
	for shipSymbol, ship := range shipsWithCargo {
		if assignedCount >= params.MaxConcurrentTasks {
			break
		}

		cargo := ship.Cargo()
		if cargo == nil || len(cargo.Inventory) == 0 {
			continue
		}

		// Get primary cargo type
		var primaryCargo string
		var maxUnits int
		for _, item := range cargo.Inventory {
			if item.Units > maxUnits {
				primaryCargo = item.Symbol
				maxUnits = item.Units
			}
		}

		// Try to find matching task for the cargo this ship already has
		// Priority: ACQUIRE_DELIVER > COLLECT_SELL (delivering to factory is more valuable)
		var matchingTask *manufacturing.ManufacturingTask
		var matchingCollectSell *manufacturing.ManufacturingTask

		for _, pipelineID := range pipelineIDs {
			tasks, err := h.taskRepo.FindByPipelineID(ctx, pipelineID)
			if err != nil {
				continue
			}

			for _, task := range tasks {
				// Only consider PENDING or READY tasks for the same good
				if task.Good() != primaryCargo {
					continue
				}
				if task.Status() != manufacturing.TaskStatusPending &&
					task.Status() != manufacturing.TaskStatusReady {
					continue
				}

				// ACQUIRE_DELIVER: Ship already has cargo, just needs to deliver to factory
				// This is the best match - ship can skip the acquire step
				if task.TaskType() == manufacturing.TaskTypeAcquireDeliver {
					matchingTask = task
					logger.Log("INFO", "Found ACQUIRE_DELIVER task matching ship cargo", map[string]interface{}{
						"ship":    shipSymbol,
						"task_id": task.ID()[:8],
						"good":    primaryCargo,
						"factory": task.FactorySymbol(),
					})
					break
				}

				// COLLECT_SELL: Ship already has cargo, just needs to sell
				// Track as backup if we don't find ACQUIRE_DELIVER
				if task.TaskType() == manufacturing.TaskTypeCollectSell && matchingCollectSell == nil {
					matchingCollectSell = task
				}
			}

			if matchingTask != nil {
				break
			}
		}

		// Use COLLECT_SELL if no ACQUIRE_DELIVER found
		if matchingTask == nil && matchingCollectSell != nil {
			matchingTask = matchingCollectSell
			logger.Log("INFO", "Found COLLECT_SELL task matching ship cargo", map[string]interface{}{
				"ship":        shipSymbol,
				"task_id":     matchingTask.ID()[:8],
				"good":        primaryCargo,
				"sell_market": matchingTask.TargetMarket(),
			})
		}

		if matchingTask == nil {
			// Create ad-hoc sell task - but first check if cargo value is worth it
			sellMarketResult, err := h.findBestSellMarketWithPrice(ctx, ship.CurrentLocation().Symbol, primaryCargo, params.PlayerID)
			if err != nil {
				logger.Log("WARN", "Failed to find sell market for orphaned cargo", map[string]interface{}{
					"ship":       shipSymbol,
					"cargo_type": primaryCargo,
					"error":      err.Error(),
				})
				continue
			}

			// Calculate cargo value
			cargoValue := sellMarketResult.PurchasePrice * maxUnits

			// If cargo value is below threshold, jettison instead of creating task
			if cargoValue < MinLiquidateCargoValue {
				logger.Log("INFO", "Cargo value below threshold - jettisoning", map[string]interface{}{
					"ship":         shipSymbol,
					"cargo_type":   primaryCargo,
					"cargo_units":  maxUnits,
					"price":        sellMarketResult.PurchasePrice,
					"cargo_value":  cargoValue,
					"min_required": MinLiquidateCargoValue,
				})

				// Jettison the cargo
				if err := h.jettisonCargo(ctx, ship, primaryCargo, maxUnits, params.PlayerID); err != nil {
					logger.Log("WARN", "Failed to jettison low-value cargo", map[string]interface{}{
						"ship":       shipSymbol,
						"cargo_type": primaryCargo,
						"error":      err.Error(),
					})
				} else {
					logger.Log("INFO", "Jettisoned low-value cargo - ship released", map[string]interface{}{
						"ship":        shipSymbol,
						"cargo_type":  primaryCargo,
						"cargo_units": maxUnits,
						"cargo_value": cargoValue,
					})
				}
				// Ship is now free (cargo jettisoned), keep it in idle pool
				continue
			}

			sellMarket := sellMarketResult.WaypointSymbol

			// Check saturation
			if h.taskAssigner != nil && h.taskAssigner.IsSellMarketSaturated(ctx, sellMarket, primaryCargo, params.PlayerID) {
				logger.Log("INFO", "Sell market saturated - holding orphaned cargo", map[string]interface{}{
					"ship":        shipSymbol,
					"cargo_type":  primaryCargo,
					"sell_market": sellMarket,
				})
				continue
			}

			// Create LIQUIDATE task for orphaned cargo
			// LIQUIDATE tasks are specifically designed for selling leftover cargo
			// and have empty pipeline_id by design (they don't belong to a pipeline)
			liquidateTask := manufacturing.NewLiquidationTask(
				params.PlayerID,
				shipSymbol,     // Ship already assigned
				primaryCargo,
				maxUnits,
				sellMarket,
			)

			if err := h.taskRepo.Create(ctx, liquidateTask); err != nil {
				continue
			}

			logger.Log("INFO", "Created LIQUIDATE task for orphaned cargo", map[string]interface{}{
				"ship":        shipSymbol,
				"task_id":     liquidateTask.ID()[:8],
				"cargo_type":  primaryCargo,
				"cargo_units": maxUnits,
				"sell_market": sellMarket,
				"cargo_value": cargoValue,
			})

			matchingTask = liquidateTask
		} else {
			// Mark existing task as READY
			if err := matchingTask.MarkReady(); err != nil {
				continue
			}
			if err := h.taskRepo.Update(ctx, matchingTask); err != nil {
				continue
			}
		}

		// Assign task to ship
		if h.workerManager != nil {
			err := h.workerManager.AssignTaskToShip(ctx, AssignTaskParams{
				Task:     matchingTask,
				Ship:     ship,
				PlayerID: params.PlayerID,
			})
			if err != nil {
				logger.Log("WARN", "Failed to assign task to ship with cargo", map[string]interface{}{
					"ship":  shipSymbol,
					"error": err.Error(),
				})
				continue
			}
		}

		logger.Log("INFO", "Assigned task to ship with existing cargo", map[string]interface{}{
			"task_id":     matchingTask.ID()[:8],
			"ship":        shipSymbol,
			"cargo_type":  primaryCargo,
			"cargo_units": maxUnits,
		})

		delete(params.IdleShips, shipSymbol)
		assignedCount++
	}

	return params.IdleShips, nil
}

// FindBestSellMarket finds the best market to sell cargo
func (h *OrphanedCargoHandler) FindBestSellMarket(ctx context.Context, currentLocation, good string, playerID int) (string, error) {
	if h.marketRepo == nil {
		return "", fmt.Errorf("no market repository configured")
	}

	system := extractSystemFromWaypoint(currentLocation)

	result, err := h.marketRepo.FindBestMarketBuying(ctx, good, system, playerID)
	if err != nil {
		return "", err
	}
	if result == nil {
		return "", fmt.Errorf("no market found buying %s in system %s", good, system)
	}

	return result.WaypointSymbol, nil
}

// CreateAdHocSellTask creates a LIQUIDATE task for orphaned cargo
func (h *OrphanedCargoHandler) CreateAdHocSellTask(ctx context.Context, ship *navigation.Ship, cargo CargoInfo, playerID int) (*manufacturing.ManufacturingTask, error) {
	sellMarket, err := h.FindBestSellMarket(ctx, ship.CurrentLocation().Symbol, cargo.Good, playerID)
	if err != nil {
		return nil, err
	}

	// Use LIQUIDATE task type - specifically designed for selling orphaned cargo
	task := manufacturing.NewLiquidationTask(
		playerID,
		ship.ShipSymbol(),
		cargo.Good,
		cargo.Units,
		sellMarket,
	)

	if h.taskRepo != nil {
		if err := h.taskRepo.Create(ctx, task); err != nil {
			return nil, err
		}
	}

	return task, nil
}

// findBestSellMarketWithPrice finds the best market to sell cargo and returns the full result with price info
func (h *OrphanedCargoHandler) findBestSellMarketWithPrice(ctx context.Context, currentLocation, good string, playerID int) (*market.BestMarketBuyingResult, error) {
	if h.marketRepo == nil {
		return nil, fmt.Errorf("no market repository configured")
	}

	system := extractSystemFromWaypoint(currentLocation)

	result, err := h.marketRepo.FindBestMarketBuying(ctx, good, system, playerID)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("no market found buying %s in system %s", good, system)
	}

	return result, nil
}

// jettisonCargo jettisons cargo from a ship using the mediator
func (h *OrphanedCargoHandler) jettisonCargo(ctx context.Context, ship *navigation.Ship, good string, units int, playerID int) error {
	if h.mediator == nil {
		return fmt.Errorf("no mediator configured for jettison")
	}

	cmd := &shipCmd.JettisonCargoCommand{
		ShipSymbol: ship.ShipSymbol(),
		PlayerID:   shared.MustNewPlayerID(playerID),
		GoodSymbol: good,
		Units:      units,
	}

	_, err := h.mediator.Send(ctx, cmd)
	return err
}
