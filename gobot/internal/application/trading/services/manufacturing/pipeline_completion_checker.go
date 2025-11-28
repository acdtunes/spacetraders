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
