package manufacturing

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// OrphanedCargoManager handles ships with cargo from interrupted operations
type OrphanedCargoManager interface {
	// HandleShipsWithExistingCargo processes idle ships that have cargo
	HandleShipsWithExistingCargo(ctx context.Context, params OrphanedCargoParams) (map[string]*navigation.Ship, error)

	// FindBestSellMarket finds best market to sell orphaned cargo
	FindBestSellMarket(ctx context.Context, currentLocation, good string, playerID int) (string, error)

	// CreateAdHocSellTask creates COLLECT_SELL task for orphaned cargo
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

		// Try to find matching PENDING COLLECT_SELL task
		var matchingTask *manufacturing.ManufacturingTask
		for _, pipelineID := range pipelineIDs {
			tasks, err := h.taskRepo.FindByPipelineID(ctx, pipelineID)
			if err != nil {
				continue
			}

			for _, task := range tasks {
				if task.TaskType() == manufacturing.TaskTypeCollectSell &&
					task.Status() == manufacturing.TaskStatusPending &&
					task.Good() == primaryCargo {
					matchingTask = task
					break
				}
			}

			if matchingTask != nil {
				break
			}
		}

		if matchingTask == nil {
			// Create ad-hoc sell task
			sellMarket, err := h.FindBestSellMarket(ctx, ship.CurrentLocation().Symbol, primaryCargo, params.PlayerID)
			if err != nil {
				logger.Log("WARN", "Failed to find sell market for orphaned cargo", map[string]interface{}{
					"ship":       shipSymbol,
					"cargo_type": primaryCargo,
					"error":      err.Error(),
				})
				continue
			}

			// Check saturation
			if h.taskAssigner != nil && h.taskAssigner.IsSellMarketSaturated(ctx, sellMarket, primaryCargo, params.PlayerID) {
				logger.Log("INFO", "Sell market saturated - holding orphaned cargo", map[string]interface{}{
					"ship":        shipSymbol,
					"cargo_type":  primaryCargo,
					"sell_market": sellMarket,
				})
				continue
			}

			// Create ad-hoc task
			adHocTask := manufacturing.NewCollectSellTask(
				"", // Empty pipeline ID for ad-hoc tasks
				params.PlayerID,
				primaryCargo,
				ship.CurrentLocation().Symbol,
				sellMarket,
				nil,
			)

			if err := adHocTask.MarkReady(); err != nil {
				continue
			}

			if err := h.taskRepo.Create(ctx, adHocTask); err != nil {
				continue
			}

			logger.Log("INFO", "Created ad-hoc sell task for orphaned cargo", map[string]interface{}{
				"ship":        shipSymbol,
				"task_id":     adHocTask.ID()[:8],
				"cargo_type":  primaryCargo,
				"sell_market": sellMarket,
			})

			matchingTask = adHocTask
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

// CreateAdHocSellTask creates a COLLECT_SELL task for orphaned cargo
func (h *OrphanedCargoHandler) CreateAdHocSellTask(ctx context.Context, ship *navigation.Ship, cargo CargoInfo, playerID int) (*manufacturing.ManufacturingTask, error) {
	sellMarket, err := h.FindBestSellMarket(ctx, ship.CurrentLocation().Symbol, cargo.Good, playerID)
	if err != nil {
		return nil, err
	}

	task := manufacturing.NewCollectSellTask(
		"",
		playerID,
		cargo.Good,
		ship.CurrentLocation().Symbol,
		sellMarket,
		nil,
	)

	if err := task.MarkReady(); err != nil {
		return nil, err
	}

	if h.taskRepo != nil {
		if err := h.taskRepo.Create(ctx, task); err != nil {
			return nil, err
		}
	}

	return task, nil
}
