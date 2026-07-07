package manufacturing

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// PipelineCompletionChecker detects and marks complete pipelines.
type PipelineCompletionChecker struct {
	pipelineRepo manufacturing.PipelineRepository
	taskRepo     manufacturing.TaskRepository
	registry     *ActivePipelineRegistry

	// Callback when pipeline completes
	onCompleted func(ctx context.Context)
}

// NewPipelineCompletionChecker creates a new completion checker.
func NewPipelineCompletionChecker(
	pipelineRepo manufacturing.PipelineRepository,
	taskRepo manufacturing.TaskRepository,
	registry *ActivePipelineRegistry,
) *PipelineCompletionChecker {
	return &PipelineCompletionChecker{
		pipelineRepo: pipelineRepo,
		taskRepo:     taskRepo,
		registry:     registry,
	}
}

// SetCompletedCallback sets the callback for pipeline completion.
func (c *PipelineCompletionChecker) SetCompletedCallback(callback func(ctx context.Context)) {
	c.onCompleted = callback
}

// CheckPipelineCompletion checks if a pipeline is complete and updates status.
// Returns true if the pipeline was transitioned to a terminal state (completed or failed).
func (c *PipelineCompletionChecker) CheckPipelineCompletion(
	ctx context.Context,
	pipelineID string,
) (bool, error) {
	logger := common.LoggerFromContext(ctx)

	// Get pipeline from registry or load from DB
	pipeline := c.registry.Get(pipelineID)
	if pipeline == nil {
		// Pipeline not in memory - try to load from database
		if c.pipelineRepo != nil {
			dbPipeline, err := c.pipelineRepo.FindByID(ctx, pipelineID)
			if err == nil && dbPipeline != nil && dbPipeline.Status() == manufacturing.PipelineStatusExecuting {
				pipeline = dbPipeline
				c.registry.Register(pipeline)
				logger.Log("DEBUG", fmt.Sprintf("Loaded pipeline %s from database for completion check", pipelineID[:8]), nil)
			}
		}
		if pipeline == nil {
			return false, nil
		}
	}

	if c.taskRepo == nil {
		return false, nil
	}

	// Get all tasks for this pipeline
	tasks, err := c.taskRepo.FindByPipelineID(ctx, pipelineID)
	if err != nil {
		return false, err
	}

	// Evaluate completion
	result := c.evaluateCompletion(pipeline, tasks)

	if result.ShouldComplete {
		return c.completePipeline(ctx, pipeline, result)
	} else if result.ShouldFail {
		return c.failPipeline(ctx, pipeline, result)
	}

	return false, nil
}

// CheckAllPipelinesForCompletion checks all active pipelines and marks completed ones.
func (c *PipelineCompletionChecker) CheckAllPipelinesForCompletion(ctx context.Context) int {
	logger := common.LoggerFromContext(ctx)

	pipelineIDs := c.registry.GetPipelineIDs()
	completed := 0

	for _, pipelineID := range pipelineIDs {
		wasCompleted, err := c.CheckPipelineCompletion(ctx, pipelineID)
		if err != nil {
			logger.Log("WARN", fmt.Sprintf("Failed to check pipeline %s completion: %v", pipelineID[:8], err), nil)
			continue
		}
		if wasCompleted {
			completed++
		}
	}

	if completed > 0 {
		logger.Log("INFO", fmt.Sprintf("Completed %d pipelines during completion check", completed), nil)
	}

	return completed
}

// completionResult holds the evaluation result.
type completionResult struct {
	ShouldComplete   bool
	ShouldFail       bool
	FinalCollections int
	Reason           string
}

// evaluateCompletion evaluates whether a pipeline should complete or fail.
func (c *PipelineCompletionChecker) evaluateCompletion(
	pipeline *manufacturing.ManufacturingPipeline,
	tasks []*manufacturing.ManufacturingTask,
) completionResult {
	// CONSTRUCTION pipelines deliver materials to a site rather than collect-and-sell
	// a product, so they have no COLLECT_SELL task and the sales-pipeline rule below
	// never fires for them. Evaluate them on delivery-task state instead (sp-b1np).
	if pipeline.PipelineType() == manufacturing.PipelineTypeConstruction {
		return evaluateConstructionCompletion(tasks)
	}

	result := completionResult{}

	// Count completed COLLECT_SELL tasks for the final product
	collectSellCount := 0
	for _, task := range tasks {
		if task.TaskType() == manufacturing.TaskTypeCollectSell {
			collectSellCount++
			goodMatch := task.Good() == pipeline.ProductGood()
			isCompleted := task.Status() == manufacturing.TaskStatusCompleted
			if goodMatch && isCompleted {
				result.FinalCollections++
			}
		}
	}

	// Check for failed tasks
	anyFailed := false
	for _, task := range tasks {
		if task.Status() == manufacturing.TaskStatusFailed && !task.CanRetry() {
			anyFailed = true
			break
		}
	}

	// Pipeline completes when ANY COLLECT_SELL task completes (one trade cycle done)
	if result.FinalCollections >= 1 {
		result.ShouldComplete = true
		result.Reason = "collect_sell_completed"
	} else if anyFailed {
		result.ShouldFail = true
		result.Reason = "one_or_more_tasks_failed"
	}

	return result
}

// evaluateConstructionCompletion decides completion for CONSTRUCTION pipelines.
// The construction bill is complete exactly when no delivery task is still in flight:
// the executor's replenishment loop keeps a delivery queued while the site still needs
// the good (sp-b1np), so "nothing in flight" means every material's bill is delivered.
//
// Leg isolation (sp-hs2j): each material is an independent leg. A permanently-failed
// leg must NOT terminalize the whole pipeline while a sibling leg is still deliverable,
// nor discard a sibling leg that already delivered. A supply-parked leg stays PENDING
// (deferred, re-sourced by the SupplyMonitor), so it counts as in flight and keeps the
// pipeline alive. The pipeline only settles once EVERY leg is terminal: it COMPLETES on
// any delivered material, and FAILS only when every leg is dead and nothing was
// delivered (a genuine total loss).
func evaluateConstructionCompletion(tasks []*manufacturing.ManufacturingTask) completionResult {
	result := completionResult{}

	completedDeliveries := 0
	inFlight := 0
	permanentFailure := false

	for _, task := range tasks {
		switch {
		case task.Status() == manufacturing.TaskStatusCompleted:
			if task.TaskType() == manufacturing.TaskTypeDeliverToConstruction {
				completedDeliveries++
			}
		case task.Status() == manufacturing.TaskStatusFailed && !task.CanRetry():
			permanentFailure = true
		default:
			// PENDING (including a supply-parked deferral) / READY / ASSIGNED /
			// EXECUTING, or a retryable FAILED task awaiting its next attempt -
			// all still in flight, so the pipeline stays alive.
			inFlight++
		}
	}

	// Any leg still in flight - in progress OR parked awaiting supply - keeps the
	// pipeline active. One dead sibling leg does not terminalize it here (sp-hs2j).
	if inFlight > 0 {
		return result
	}

	// Every leg is now terminal (delivered or permanently dead).
	if completedDeliveries > 0 {
		// At least one material was delivered and no leg can progress further. A
		// sibling leg's permanent failure does not fail a pipeline that delivered
		// elsewhere - the healthy leg's work is preserved (sp-hs2j leg isolation).
		result.ShouldComplete = true
		result.Reason = "construction_materials_delivered"
		return result
	}
	if permanentFailure {
		// Nothing delivered and every leg is dead - the construction genuinely failed.
		result.ShouldFail = true
		result.Reason = "construction_delivery_failed"
	}
	return result
}

// completePipeline marks a pipeline as completed.
func (c *PipelineCompletionChecker) completePipeline(
	ctx context.Context,
	pipeline *manufacturing.ManufacturingPipeline,
	result completionResult,
) (bool, error) {
	logger := common.LoggerFromContext(ctx)

	if err := pipeline.Complete(); err != nil {
		logger.Log("ERROR", fmt.Sprintf("Failed to mark pipeline %s as complete: %v (status=%s)",
			pipeline.ID()[:8], err, pipeline.Status()), nil)
		return false, err
	}

	if c.pipelineRepo != nil {
		if updateErr := c.pipelineRepo.Update(ctx, pipeline); updateErr != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to persist pipeline completion: %v", updateErr), nil)
		}
	}

	c.registry.Unregister(pipeline.ID())

	netProfit := pipeline.TotalRevenue() - pipeline.TotalCost()
	metrics.RecordManufacturingPipelineCompletion(
		pipeline.PlayerID(),
		pipeline.ProductGood(),
		"completed",
		pipeline.RuntimeDuration(),
		netProfit,
	)

	logger.Log("INFO", fmt.Sprintf("Pipeline %s completed: %s", pipeline.ID()[:8], result.Reason), map[string]interface{}{
		"final_collections": result.FinalCollections,
		"net_profit":        netProfit,
	})

	if c.onCompleted != nil {
		c.onCompleted(ctx)
	}

	return true, nil
}

// failPipeline marks a pipeline as failed.
func (c *PipelineCompletionChecker) failPipeline(
	ctx context.Context,
	pipeline *manufacturing.ManufacturingPipeline,
	result completionResult,
) (bool, error) {
	logger := common.LoggerFromContext(ctx)

	if err := pipeline.Fail(result.Reason); err != nil {
		return false, err
	}

	if c.pipelineRepo != nil {
		_ = c.pipelineRepo.Update(ctx, pipeline)
	}

	c.registry.Unregister(pipeline.ID())

	metrics.RecordManufacturingPipelineCompletion(
		pipeline.PlayerID(),
		pipeline.ProductGood(),
		"failed",
		pipeline.RuntimeDuration(),
		0,
	)

	logger.Log("WARN", fmt.Sprintf("Pipeline %s failed: %s", pipeline.ID()[:8], result.Reason), nil)

	if c.onCompleted != nil {
		c.onCompleted(ctx)
	}

	return true, nil
}
