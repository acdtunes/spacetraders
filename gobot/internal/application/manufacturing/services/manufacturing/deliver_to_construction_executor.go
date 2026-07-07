package manufacturing

import (
	"context"
	"errors"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// ErrDeferToSupply signals that a task cannot proceed because the good it must
// acquire has no buy source available right now - a transient supply gap, not a
// failure. The worker parks such a task as a pending-supply deferral to be
// re-sourced when supply recovers, instead of failing it and burning the retry
// budget (which, once exhausted, terminalized the whole pipeline). This is the
// execution-layer twin of the planner's per-material deferral (sp-hs2j / sp-r900).
var ErrDeferToSupply = errors.New("deferred: no buy source available, awaiting supply recovery")

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
	taskRepo         manufacturing.TaskRepository
}

// NewDeliverToConstructionExecutor creates a new executor for DELIVER_TO_CONSTRUCTION tasks.
// taskRepo is used to enqueue the next delivery task when a supply leaves the site's
// bill for the delivered material unfinished (construction task replenishment).
func NewDeliverToConstructionExecutor(
	navigator Navigator,
	purchaser ConstructionPurchaser,
	constructionRepo manufacturing.ConstructionSiteRepository,
	pipelineRepo manufacturing.PipelineRepository,
	taskRepo manufacturing.TaskRepository,
) *DeliverToConstructionExecutor {
	return &DeliverToConstructionExecutor{
		navigator:        navigator,
		purchaser:        purchaser,
		constructionRepo: constructionRepo,
		pipelineRepo:     pipelineRepo,
		taskRepo:         taskRepo,
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
			// No buy source (never assigned, or the market dried up in a supply dip).
			// Signal a supply deferral so the worker parks this task for re-sourcing
			// rather than failing it toward permanent death (sp-hs2j).
			return fmt.Errorf("%w: DELIVER_TO_CONSTRUCTION %s has no source market or factory", ErrDeferToSupply, task.Good())
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
		// Surface the underlying supply error VERBATIM in the message so it
		// reaches the container log stream (structured map fields are dropped
		// by the renderer). Without this the real cause of a failed delivery
		// (e.g. a 404 route error) never surfaces above "task failed".
		logger.Log("ERROR", fmt.Sprintf("DELIVER_TO_CONSTRUCTION: supply failed: %v", err), map[string]interface{}{
			"task_id":           task.ID(),
			"ship":              params.ShipSymbol,
			"construction_site": constructionSite,
			"good":              task.Good(),
			"units":             cargoUnits,
		})
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

	// --- PHASE 5: Replenish ---
	// One supply delivers a single cargo load. If the site's bill for this good is
	// not yet met, enqueue the next delivery so the pipeline keeps filling without a
	// manual re-plan (sp-b1np). Uses the in-band updated construction state.
	e.enqueueReplenishmentIfNeeded(ctx, task, supplyResult)

	return nil
}

// enqueueReplenishmentIfNeeded creates the next DELIVER_TO_CONSTRUCTION task for the
// delivered good when the construction site still needs more of it. Remaining is read
// from the in-band supply response (the authoritative, just-updated site bill), so no
// extra API call is required. The follow-on task reuses this task's already-resolved
// delivery spec, matching how the planner sizes construction deliveries; it is left
// READY so the coordinator's rescue/assign loop picks it up from the database.
// When the material is complete (remaining <= 0) nothing is queued, so the pipeline
// settles once every material's bill is met.
func (e *DeliverToConstructionExecutor) enqueueReplenishmentIfNeeded(
	ctx context.Context,
	task *manufacturing.ManufacturingTask,
	supplyResult *manufacturing.ConstructionSupplyResult,
) {
	logger := common.LoggerFromContext(ctx)

	if e.taskRepo == nil || supplyResult == nil || supplyResult.Construction == nil {
		return
	}

	remaining := remainingForGood(supplyResult.Construction, task.Good())
	if remaining <= 0 {
		return // material complete - stop cleanly
	}

	next := nextConstructionDeliveryTask(task)
	if err := next.MarkReady(); err != nil {
		logger.Log("WARN", fmt.Sprintf("DELIVER_TO_CONSTRUCTION: failed to ready replenishment task: %v", err), nil)
		return
	}
	if err := e.taskRepo.Create(ctx, next); err != nil {
		logger.Log("WARN", fmt.Sprintf("DELIVER_TO_CONSTRUCTION: failed to enqueue replenishment task: %v", err), nil)
		return
	}

	logger.Log("INFO", "DELIVER_TO_CONSTRUCTION: Replenishment task queued", map[string]interface{}{
		"good":              task.Good(),
		"construction_site": task.ConstructionSite(),
		"remaining":         remaining,
		"next_task":         next.ID(),
	})
}

// remainingForGood returns how many units of good the construction site still needs,
// according to the updated construction state returned in-band by the supply call.
func remainingForGood(site *manufacturing.ConstructionSite, good string) int {
	for _, mat := range site.Materials() {
		if mat.TradeSymbol() == good {
			return mat.Remaining()
		}
	}
	return 0
}

// nextConstructionDeliveryTask builds the follow-on delivery task for a just-completed
// DELIVER_TO_CONSTRUCTION task, reusing its resolved delivery spec (good, source market
// or factory, construction site, pipeline, player) with no dependencies. It funnels
// through the same domain factory the planner uses, so the two paths cannot drift.
func nextConstructionDeliveryTask(completed *manufacturing.ManufacturingTask) *manufacturing.ManufacturingTask {
	return manufacturing.NewDeliverToConstructionTask(
		completed.PipelineID(),
		completed.PlayerID(),
		completed.Good(),
		completed.SourceMarket(),
		completed.FactorySymbol(),
		completed.ConstructionSite(),
		nil,
	)
}
