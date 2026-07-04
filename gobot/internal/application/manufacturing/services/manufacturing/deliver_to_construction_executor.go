package manufacturing

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// ConstructionPurchaser executes the market purchase loop for the acquire phase.
// Satisfied by *ManufacturingPurchaser; narrowed to an interface for testability.
type ConstructionPurchaser interface {
	ExecutePurchaseLoop(ctx context.Context, params PurchaseLoopParams) (*PurchaseResult, error)
}

// DeliverToConstructionExecutor executes DELIVER_TO_CONSTRUCTION tasks.
// This task acquires goods (from the ship's cargo, or by purchasing at the
// task's source market / factory) and delivers them to a construction site
// using the construction supply API.
//
// Workflow:
//  1. Acquire goods if cargo is empty (purchase at source market or factory)
//  2. Navigate to construction site
//  3. Dock at construction site
//  4. Call construction supply API to deliver goods
//  5. Update pipeline with delivered quantity
type DeliverToConstructionExecutor struct {
	navigator        Navigator
	purchaser        ConstructionPurchaser
	constructionRepo manufacturing.ConstructionSiteRepository
	pipelineRepo     manufacturing.PipelineRepository
}

// NewDeliverToConstructionExecutor creates a new executor for DELIVER_TO_CONSTRUCTION tasks.
func NewDeliverToConstructionExecutor(
	navigator Navigator,
	purchaser ConstructionPurchaser,
	constructionRepo manufacturing.ConstructionSiteRepository,
	pipelineRepo manufacturing.PipelineRepository,
) *DeliverToConstructionExecutor {
	return &DeliverToConstructionExecutor{
		navigator:        navigator,
		purchaser:        purchaser,
		constructionRepo: constructionRepo,
		pipelineRepo:     pipelineRepo,
	}
}

// TaskType returns the task type this executor handles.
func (e *DeliverToConstructionExecutor) TaskType() manufacturing.TaskType {
	return manufacturing.TaskTypeDeliverToConstruction
}

// Execute runs the DELIVER_TO_CONSTRUCTION task workflow:
// Phase 1: Navigate to construction site
// Phase 2: Dock at construction site
// Phase 3: Deliver goods via construction supply API
func (e *DeliverToConstructionExecutor) Execute(ctx context.Context, params TaskExecutionParams) error {
	task := params.Task
	logger := common.LoggerFromContext(ctx)

	constructionSite := task.ConstructionSite()
	if constructionSite == "" {
		return fmt.Errorf("DELIVER_TO_CONSTRUCTION: task %s has no construction site", task.ID())
	}

	logger.Log("INFO", "DELIVER_TO_CONSTRUCTION: Starting delivery", map[string]interface{}{
		"ship":              params.ShipSymbol,
		"good":              task.Good(),
		"construction_site": constructionSite,
	})

	// Load ship to check current cargo
	ship, err := e.navigator.ReloadShip(ctx, params.ShipSymbol, params.PlayerID)
	if err != nil {
		return err
	}

	// Check if we already have cargo to deliver (idempotent resume after crash)
	cargoUnits := ship.Cargo().GetItemUnits(task.Good())
	if cargoUnits == 0 {
		// --- PHASE 0: ACQUIRE (purchase at source market or factory) ---
		source := task.SourceMarket()
		if source == "" {
			source = task.FactorySymbol()
		}
		if source == "" {
			return fmt.Errorf("DELIVER_TO_CONSTRUCTION: no %s in cargo and no source to acquire from", task.Good())
		}
		if e.purchaser == nil {
			return fmt.Errorf("DELIVER_TO_CONSTRUCTION: no %s in cargo and no purchaser configured", task.Good())
		}

		logger.Log("INFO", "DELIVER_TO_CONSTRUCTION: Acquiring goods", map[string]interface{}{
			"ship":   params.ShipSymbol,
			"good":   task.Good(),
			"source": source,
		})

		if _, err := e.navigator.NavigateAndDock(ctx, params.ShipSymbol, source, params.PlayerID); err != nil {
			return fmt.Errorf("failed to navigate to acquisition source: %w", err)
		}

		purchaseResult, err := e.purchaser.ExecutePurchaseLoop(ctx, PurchaseLoopParams{
			ShipSymbol:        params.ShipSymbol,
			PlayerID:          params.PlayerID,
			Good:              task.Good(),
			TaskID:            task.ID(),
			DesiredQty:        task.Quantity(),
			Market:            source,
			Factory:           constructionSite,
			RequireHighSupply: false,
		})
		if err != nil {
			return err
		}
		if purchaseResult.TotalUnitsAdded == 0 {
			return fmt.Errorf("DELIVER_TO_CONSTRUCTION: no goods acquired at %s - will retry", source)
		}

		cargoUnits = purchaseResult.TotalUnitsAdded
	}

	logger.Log("INFO", "DELIVER_TO_CONSTRUCTION: Cargo to deliver", map[string]interface{}{
		"good":     task.Good(),
		"quantity": cargoUnits,
	})

	// --- PHASE 1 & 2: Navigate to construction site and dock ---
	ship, err = e.navigator.NavigateAndDock(ctx, params.ShipSymbol, constructionSite, params.PlayerID)
	if err != nil {
		return fmt.Errorf("failed to navigate to construction site: %w", err)
	}

	// --- PHASE 3: Supply construction site ---
	logger.Log("INFO", "DELIVER_TO_CONSTRUCTION: Supplying construction site", map[string]interface{}{
		"ship":              params.ShipSymbol,
		"construction_site": constructionSite,
		"good":              task.Good(),
		"units":             cargoUnits,
	})

	supplyResult, err := e.constructionRepo.SupplyMaterial(
		ctx,
		params.ShipSymbol,
		constructionSite,
		task.Good(),
		cargoUnits,
		params.PlayerID.Value(),
	)
	if err != nil {
		return fmt.Errorf("failed to supply construction: %w", err)
	}

	logger.Log("INFO", "DELIVER_TO_CONSTRUCTION: Supply successful", map[string]interface{}{
		"good":            task.Good(),
		"units_delivered": supplyResult.UnitsDelivered,
		"is_complete":     supplyResult.Construction.IsComplete(),
	})

	// --- PHASE 4: Update pipeline progress ---
	if task.PipelineID() != "" {
		pipeline, err := e.pipelineRepo.FindByID(ctx, task.PipelineID())
		if err != nil {
			logger.Log("WARN", "DELIVER_TO_CONSTRUCTION: Failed to load pipeline for progress update", map[string]interface{}{
				"pipeline_id": task.PipelineID(),
				"error":       err.Error(),
			})
			// Don't fail the task - supply was successful
		} else if pipeline != nil {
			// Record the delivery in pipeline
			if err := pipeline.RecordMaterialDelivery(task.Good(), cargoUnits); err != nil {
				logger.Log("WARN", "DELIVER_TO_CONSTRUCTION: Failed to record delivery", map[string]interface{}{
					"pipeline_id": task.PipelineID(),
					"error":       err.Error(),
				})
			} else {
				// Save pipeline with updated progress
				if err := e.pipelineRepo.Update(ctx, pipeline); err != nil {
					logger.Log("WARN", "DELIVER_TO_CONSTRUCTION: Failed to save pipeline progress", map[string]interface{}{
						"pipeline_id": task.PipelineID(),
						"error":       err.Error(),
					})
				} else {
					logger.Log("INFO", "DELIVER_TO_CONSTRUCTION: Pipeline progress updated", map[string]interface{}{
						"pipeline_id": task.PipelineID(),
						"good":        task.Good(),
						"progress":    fmt.Sprintf("%.1f%%", pipeline.ConstructionProgress()),
					})
				}
			}
		}
	}

	return nil
}
