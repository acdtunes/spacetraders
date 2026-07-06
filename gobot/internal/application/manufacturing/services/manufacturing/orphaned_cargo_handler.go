package manufacturing

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
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
}

// OrphanedCargoParams contains parameters for handling orphaned cargo
type OrphanedCargoParams struct {
	IdleShips          map[string]*navigation.Ship
	PlayerID           int
	MaxConcurrentTasks int
}

// OrphanedCargoHandler implements OrphanedCargoManager
type OrphanedCargoHandler struct {
	taskRepo      manufacturing.TaskRepository
	marketRepo    market.MarketRepository
	shipRepo      navigation.ShipRepository
	workerManager WorkerManager
	taskAssigner  TaskAssigner
	mediator      common.Mediator
}

// NewOrphanedCargoHandler creates a new orphaned cargo handler with all dependencies
func NewOrphanedCargoHandler(
	taskRepo manufacturing.TaskRepository,
	marketRepo market.MarketRepository,
	shipRepo navigation.ShipRepository,
	workerManager WorkerManager,
	taskAssigner TaskAssigner,
	mediator common.Mediator,
) *OrphanedCargoHandler {
	return &OrphanedCargoHandler{
		taskRepo:      taskRepo,
		marketRepo:    marketRepo,
		shipRepo:      shipRepo,
		workerManager: workerManager,
		taskAssigner:  taskAssigner,
		mediator:      mediator,
	}
}

// SetTaskAssigner sets the task assigner for circular dependency resolution.
// This is the only setter needed - call after TaskAssignmentManager is created.
func (h *OrphanedCargoHandler) SetTaskAssigner(ta TaskAssigner) {
	h.taskAssigner = ta
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
		assignedCount = h.taskAssigner.GetAssignmentCount()
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

	// Process each ship with cargo
	for shipSymbol, ship := range shipsWithCargo {
		if assignedCount >= params.MaxConcurrentTasks {
			break
		}

		cargo := ship.Cargo()
		if cargo == nil || len(cargo.Inventory) == 0 {
			continue
		}

		primaryCargo, maxUnits := largestCargoItem(cargo)

		matchingTask := h.findMatchingTaskForCargo(ctx, shipSymbol, primaryCargo, params.PlayerID)

		if matchingTask == nil {
			liquidateTask, verifiedUnits, ok := h.createLiquidateTaskForOrphanedCargo(ctx, ship, shipSymbol, primaryCargo, maxUnits, params.PlayerID)
			if !ok {
				continue
			}
			maxUnits = verifiedUnits
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

func largestCargoItem(cargo *shared.Cargo) (string, int) {
	var primaryCargo string
	var maxUnits int
	for _, item := range cargo.Inventory {
		if item.Units > maxUnits {
			primaryCargo = item.Symbol
			maxUnits = item.Units
		}
	}
	return primaryCargo, maxUnits
}

func (h *OrphanedCargoHandler) findMatchingTaskForCargo(ctx context.Context, shipSymbol, good string, playerID int) *manufacturing.ManufacturingTask {
	logger := common.LoggerFromContext(ctx)

	availableTasks, err := h.taskRepo.FindAvailableByGood(ctx, playerID, good)
	if err != nil {
		logger.Log("WARN", "Failed to find available tasks for cargo", map[string]interface{}{
			"ship":       shipSymbol,
			"cargo_type": good,
			"error":      err.Error(),
		})
	}

	var matchingCollectSell *manufacturing.ManufacturingTask

	for _, task := range availableTasks {
		// ACQUIRE_DELIVER: Ship already has cargo, just needs to deliver to factory
		// This is the best match - ship can skip the acquire step
		if task.TaskType() == manufacturing.TaskTypeAcquireDeliver {
			logger.Log("INFO", "Found ACQUIRE_DELIVER task matching ship cargo", map[string]interface{}{
				"ship":    shipSymbol,
				"task_id": task.ID()[:8],
				"good":    good,
				"factory": task.FactorySymbol(),
			})
			return task
		}

		// COLLECT_SELL: Ship already has cargo, just needs to sell
		// Track as backup if we don't find ACQUIRE_DELIVER
		if task.TaskType() == manufacturing.TaskTypeCollectSell && matchingCollectSell == nil {
			matchingCollectSell = task
		}
	}

	if matchingCollectSell != nil {
		logger.Log("INFO", "Found COLLECT_SELL task matching ship cargo", map[string]interface{}{
			"ship":        shipSymbol,
			"task_id":     matchingCollectSell.ID()[:8],
			"good":        good,
			"sell_market": matchingCollectSell.TargetMarket(),
		})
	}

	return matchingCollectSell
}

func (h *OrphanedCargoHandler) createLiquidateTaskForOrphanedCargo(
	ctx context.Context,
	ship *navigation.Ship,
	shipSymbol, good string,
	units, playerID int,
) (*manufacturing.ManufacturingTask, int, bool) {
	logger := common.LoggerFromContext(ctx)

	sellMarketResult, err := h.findBestSellMarketWithPrice(ctx, ship.CurrentLocation().Symbol, good, playerID)
	if err != nil {
		logger.Log("WARN", "Failed to find sell market for orphaned cargo", map[string]interface{}{
			"ship":       shipSymbol,
			"cargo_type": good,
			"error":      err.Error(),
		})
		return nil, units, false
	}

	cargoValue := sellMarketResult.PurchasePrice * units

	// If cargo value is below threshold, jettison instead of creating task
	if cargoValue < MinLiquidateCargoValue {
		h.jettisonLowValueCargo(ctx, ship, shipSymbol, good, units, cargoValue, sellMarketResult.PurchasePrice, playerID)
		// Ship is now free (cargo jettisoned), keep it in idle pool
		return nil, units, false
	}

	sellMarket := sellMarketResult.WaypointSymbol

	if h.taskAssigner != nil && h.taskAssigner.IsSellMarketSaturated(ctx, sellMarket, good, playerID) {
		logger.Log("INFO", "Sell market saturated - holding orphaned cargo", map[string]interface{}{
			"ship":        shipSymbol,
			"cargo_type":  good,
			"sell_market": sellMarket,
		})
		return nil, units, false
	}

	// DEDUPLICATION: Check if LIQUIDATE task already exists for this ship+good
	exists, err := h.taskRepo.ExistsLiquidateForShipAndGood(ctx, shipSymbol, good, playerID)
	if err != nil {
		logger.Log("WARN", "Failed to check existing LIQUIDATE task", map[string]interface{}{
			"ship":       shipSymbol,
			"cargo_type": good,
			"error":      err.Error(),
		})
		return nil, units, false
	}
	if exists {
		logger.Log("DEBUG", "LIQUIDATE task already exists for ship+good - skipping", map[string]interface{}{
			"ship":       shipSymbol,
			"cargo_type": good,
		})
		return nil, units, false
	}

	units, ok := h.verifyCargoAgainstAPI(ctx, shipSymbol, good, units, playerID)
	if !ok {
		return nil, units, false
	}

	// LIQUIDATE tasks are specifically designed for selling leftover cargo
	// and have empty pipeline_id by design (they don't belong to a pipeline)
	liquidateTask := manufacturing.NewLiquidationTask(
		playerID,
		shipSymbol, // Ship already assigned
		good,
		units,
		sellMarket,
	)

	if err := h.taskRepo.Create(ctx, liquidateTask); err != nil {
		return nil, units, false
	}

	logger.Log("INFO", "Created LIQUIDATE task for orphaned cargo", map[string]interface{}{
		"ship":        shipSymbol,
		"task_id":     liquidateTask.ID()[:8],
		"cargo_type":  good,
		"cargo_units": units,
		"sell_market": sellMarket,
		"cargo_value": cargoValue,
	})

	return liquidateTask, units, true
}

func (h *OrphanedCargoHandler) jettisonLowValueCargo(
	ctx context.Context,
	ship *navigation.Ship,
	shipSymbol, good string,
	units, cargoValue, pricePerUnit, playerID int,
) {
	logger := common.LoggerFromContext(ctx)

	logger.Log("INFO", "Cargo value below threshold - jettisoning", map[string]interface{}{
		"ship":         shipSymbol,
		"cargo_type":   good,
		"cargo_units":  units,
		"price":        pricePerUnit,
		"cargo_value":  cargoValue,
		"min_required": MinLiquidateCargoValue,
	})

	if err := h.jettisonCargo(ctx, ship, good, units, playerID); err != nil {
		logger.Log("WARN", "Failed to jettison low-value cargo", map[string]interface{}{
			"ship":       shipSymbol,
			"cargo_type": good,
			"error":      err.Error(),
		})
	} else {
		logger.Log("INFO", "Jettisoned low-value cargo - ship released", map[string]interface{}{
			"ship":        shipSymbol,
			"cargo_type":  good,
			"cargo_units": units,
			"cargo_value": cargoValue,
		})
	}
}

func (h *OrphanedCargoHandler) verifyCargoAgainstAPI(ctx context.Context, shipSymbol, good string, units, playerID int) (int, bool) {
	logger := common.LoggerFromContext(ctx)

	if h.shipRepo == nil {
		return units, true
	}

	freshShip, syncErr := h.shipRepo.SyncShipFromAPI(ctx, shipSymbol, shared.MustNewPlayerID(playerID))
	if syncErr != nil {
		logger.Log("WARN", "Failed to sync ship from API for cargo verification", map[string]interface{}{
			"ship":  shipSymbol,
			"error": syncErr.Error(),
		})
		// Continue anyway - better to try than skip entirely
		return units, true
	}

	freshCargoQty := freshShip.Cargo().GetItemUnits(good)
	if freshCargoQty == 0 {
		logger.Log("INFO", "Stale cargo detected - API shows no cargo, skipping LIQUIDATE", map[string]interface{}{
			"ship":       shipSymbol,
			"cargo_type": good,
			"db_units":   units,
			"api_units":  0,
		})
		return units, false
	}

	if freshCargoQty != units {
		logger.Log("DEBUG", "Cargo quantity updated from API", map[string]interface{}{
			"ship":       shipSymbol,
			"cargo_type": good,
			"db_units":   units,
			"api_units":  freshCargoQty,
		})
	}
	return freshCargoQty, true
}

// findBestSellMarketWithPrice finds the best market to sell cargo and returns the full result with price info
func (h *OrphanedCargoHandler) findBestSellMarketWithPrice(ctx context.Context, currentLocation, good string, playerID int) (*market.BestMarketBuyingResult, error) {
	if h.marketRepo == nil {
		return nil, fmt.Errorf("no market repository configured")
	}

	system := extractSystemFromWaypointSymbol(currentLocation)

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

	cmd := &shipCargo.JettisonCargoCommand{
		ShipSymbol: ship.ShipSymbol(),
		PlayerID:   shared.MustNewPlayerID(playerID),
		GoodSymbol: good,
		Units:      units,
	}

	_, err := h.mediator.Send(ctx, cmd)
	return err
}
