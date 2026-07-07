package manufacturing

import (
	"context"
	"testing"
	"time"

	domain "github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// completionFakeTaskRepo returns a fixed task list for a pipeline so the completion
// checker evaluates against a known set of task states.
type completionFakeTaskRepo struct {
	domain.TaskRepository
	tasks []*domain.ManufacturingTask
}

func (r *completionFakeTaskRepo) FindByPipelineID(_ context.Context, _ string) ([]*domain.ManufacturingTask, error) {
	return r.tasks, nil
}

// completionFakePipelineRepo records the pipeline persisted on completion.
type completionFakePipelineRepo struct {
	domain.PipelineRepository
	updated *domain.ManufacturingPipeline
}

func (r *completionFakePipelineRepo) Update(_ context.Context, p *domain.ManufacturingPipeline) error {
	r.updated = p
	return nil
}

func completedConstructionTask(t *testing.T, pipelineID, good string) *domain.ManufacturingTask {
	t.Helper()
	task := domain.NewDeliverToConstructionTask(pipelineID, 1, good, "X1-TEST-F56", "", "X1-TEST-I67", nil)
	if err := task.MarkReady(); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}
	if err := task.AssignShip("SHIP-1"); err != nil {
		t.Fatalf("AssignShip: %v", err)
	}
	if err := task.StartExecution(); err != nil {
		t.Fatalf("StartExecution: %v", err)
	}
	if err := task.Complete(); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	return task
}

func executingConstructionTask(t *testing.T, pipelineID, good string) *domain.ManufacturingTask {
	t.Helper()
	task := domain.NewDeliverToConstructionTask(pipelineID, 1, good, "X1-TEST-F56", "", "X1-TEST-I67", nil)
	if err := task.MarkReady(); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}
	if err := task.AssignShip("SHIP-2"); err != nil {
		t.Fatalf("AssignShip: %v", err)
	}
	if err := task.StartExecution(); err != nil {
		t.Fatalf("StartExecution: %v", err)
	}
	return task
}

// A construction pipeline has no COLLECT_SELL tasks, so the generic completion rule
// (complete when a COLLECT_SELL for the product finishes) never fires for it - it
// would idle EXECUTING forever after full delivery. Once every DELIVER_TO_CONSTRUCTION
// task is terminal and at least one delivery completed, the pipeline must reach
// COMPLETED. Part of sp-b1np: full delivery must settle the pipeline lifecycle.
func TestCheckPipelineCompletion_ConstructionCompletesWhenAllDelivered(t *testing.T) {
	pipeline := domain.NewConstructionPipeline("X1-TEST-I67", 1, 3, 5)
	if err := pipeline.AddMaterial(domain.NewConstructionMaterialTarget("FAB_MATS", 40)); err != nil {
		t.Fatalf("AddMaterial: %v", err)
	}
	if err := pipeline.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	tasks := []*domain.ManufacturingTask{
		completedConstructionTask(t, pipeline.ID(), "FAB_MATS"),
	}

	registry := NewActivePipelineRegistry()
	registry.Register(pipeline)
	checker := NewPipelineCompletionChecker(&completionFakePipelineRepo{}, &completionFakeTaskRepo{tasks: tasks}, registry)

	completed, err := checker.CheckPipelineCompletion(context.Background(), pipeline.ID())
	if err != nil {
		t.Fatalf("CheckPipelineCompletion: %v", err)
	}
	if !completed {
		t.Fatalf("expected construction pipeline to complete once every delivery finished")
	}
	if pipeline.Status() != domain.PipelineStatusCompleted {
		t.Fatalf("expected pipeline status COMPLETED, got %s", pipeline.Status())
	}
}

// While a construction delivery is still in flight (e.g. a replenishment task the
// executor just enqueued), the pipeline must NOT be marked complete - the bill is
// not yet fully delivered. Part of sp-b1np.
func TestEvaluateCompletion_ConstructionStaysActiveWhileTaskInFlight(t *testing.T) {
	pipeline := domain.NewConstructionPipeline("X1-TEST-I67", 1, 3, 5)
	if err := pipeline.AddMaterial(domain.NewConstructionMaterialTarget("FAB_MATS", 80)); err != nil {
		t.Fatalf("AddMaterial: %v", err)
	}
	if err := pipeline.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	tasks := []*domain.ManufacturingTask{
		completedConstructionTask(t, pipeline.ID(), "FAB_MATS"),
		executingConstructionTask(t, pipeline.ID(), "FAB_MATS"),
	}

	checker := &PipelineCompletionChecker{}
	result := checker.evaluateCompletion(pipeline, tasks)
	if result.ShouldComplete {
		t.Fatalf("expected construction pipeline NOT to complete while a delivery is still in flight")
	}
	if result.ShouldFail {
		t.Fatalf("did not expect failure while a delivery is still in flight")
	}
}

// A permanently failed construction delivery (retries exhausted) must fail the
// pipeline rather than silently completing it. Preserves the existing fail behavior
// under the new construction-specific completion branch. Part of sp-b1np.
func TestEvaluateCompletion_ConstructionFailsOnPermanentDeliveryFailure(t *testing.T) {
	pipeline := domain.NewConstructionPipeline("X1-TEST-I67", 1, 3, 5)
	if err := pipeline.AddMaterial(domain.NewConstructionMaterialTarget("FAB_MATS", 80)); err != nil {
		t.Fatalf("AddMaterial: %v", err)
	}
	if err := pipeline.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// retryCount == maxRetries -> CanRetry() is false -> permanent failure.
	failed := domain.ReconstituteTask(
		"task-failed", pipeline.ID(), 1,
		domain.TaskTypeDeliverToConstruction, domain.TaskStatusFailed,
		"FAB_MATS", 0, 0,
		"X1-TEST-F56", "", "", "", "", "X1-TEST-I67",
		nil, "SHIP-1", domain.PriorityDeliverToConstruction,
		3, 3,
		0, 0, "boom",
		time.Now(), nil, nil, nil,
		false, false, nil,
	)

	checker := &PipelineCompletionChecker{}
	result := checker.evaluateCompletion(pipeline, []*domain.ManufacturingTask{failed})
	if !result.ShouldFail {
		t.Fatalf("expected construction pipeline to fail when a delivery permanently failed")
	}
	if result.ShouldComplete {
		t.Fatalf("did not expect completion when a delivery permanently failed")
	}
}

// permanentlyFailedConstructionTask builds a DELIVER_TO_CONSTRUCTION task whose
// retries are exhausted (retryCount == maxRetries -> CanRetry() is false): a
// genuinely dead leg, distinct from a supply-parked (PENDING) leg.
func permanentlyFailedConstructionTask(pipelineID, good string) *domain.ManufacturingTask {
	return domain.ReconstituteTask(
		"task-dead-"+good, pipelineID, 1,
		domain.TaskTypeDeliverToConstruction, domain.TaskStatusFailed,
		good, 0, 0,
		"X1-TEST-F56", "", "", "", "", "X1-TEST-I67",
		nil, "SHIP-X", domain.PriorityDeliverToConstruction,
		3, 3,
		0, 0, "boom",
		time.Now(), nil, nil, nil,
		false, false, nil,
	)
}

// Leg isolation (sp-hs2j): when one construction leg permanently fails but a sibling
// leg delivered its material and nothing is left in flight, the pipeline must COMPLETE
// on the delivered work rather than being failed by the dead leg. Previously a single
// permanent failure terminalized the whole pipeline, killing the healthy leg with it
// (the 11:10:48 incident: FAB_MATS died on a supply dip and took the circuitry leg down).
func TestEvaluateCompletion_ConstructionCompletesDespiteOneDeadLegWhenSiblingDelivered(t *testing.T) {
	pipeline := domain.NewConstructionPipeline("X1-TEST-I67", 1, 3, 5)
	if err := pipeline.AddMaterial(domain.NewConstructionMaterialTarget("FAB_MATS", 40)); err != nil {
		t.Fatalf("AddMaterial: %v", err)
	}
	if err := pipeline.AddMaterial(domain.NewConstructionMaterialTarget("ADVANCED_CIRCUITRY", 40)); err != nil {
		t.Fatalf("AddMaterial: %v", err)
	}
	if err := pipeline.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	delivered := completedConstructionTask(t, pipeline.ID(), "FAB_MATS")
	deadLeg := permanentlyFailedConstructionTask(pipeline.ID(), "ADVANCED_CIRCUITRY")

	checker := &PipelineCompletionChecker{}
	result := checker.evaluateCompletion(pipeline, []*domain.ManufacturingTask{delivered, deadLeg})

	if result.ShouldFail {
		t.Fatalf("one dead leg must not fail a pipeline whose sibling leg delivered")
	}
	if !result.ShouldComplete {
		t.Fatalf("expected the pipeline to complete on the delivered leg once nothing is in flight")
	}
}

// Leg isolation (sp-hs2j): while a sibling leg is still in flight, a permanently
// failed leg must NOT terminalize the pipeline - it stays active so the live leg
// (and any re-sourced parked leg) keeps flowing.
func TestEvaluateCompletion_ConstructionStaysActiveWhenDeadLegHasInFlightSibling(t *testing.T) {
	pipeline := domain.NewConstructionPipeline("X1-TEST-I67", 1, 3, 5)
	if err := pipeline.AddMaterial(domain.NewConstructionMaterialTarget("FAB_MATS", 80)); err != nil {
		t.Fatalf("AddMaterial: %v", err)
	}
	if err := pipeline.AddMaterial(domain.NewConstructionMaterialTarget("ADVANCED_CIRCUITRY", 40)); err != nil {
		t.Fatalf("AddMaterial: %v", err)
	}
	if err := pipeline.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	inFlight := executingConstructionTask(t, pipeline.ID(), "FAB_MATS")
	deadLeg := permanentlyFailedConstructionTask(pipeline.ID(), "ADVANCED_CIRCUITRY")

	checker := &PipelineCompletionChecker{}
	result := checker.evaluateCompletion(pipeline, []*domain.ManufacturingTask{inFlight, deadLeg})

	if result.ShouldFail {
		t.Fatalf("pipeline must not fail while a sibling leg is still in flight")
	}
	if result.ShouldComplete {
		t.Fatalf("pipeline must not complete while a sibling leg is still in flight")
	}
}

// When EVERY leg is permanently dead and nothing was delivered, the construction
// genuinely failed and the pipeline must fail (all-legs-dead terminal case). This
// preserves the sp-b1np fail-on-permanent-failure contract for the total-loss case.
func TestEvaluateCompletion_ConstructionFailsWhenAllLegsDead(t *testing.T) {
	pipeline := domain.NewConstructionPipeline("X1-TEST-I67", 1, 3, 5)
	if err := pipeline.AddMaterial(domain.NewConstructionMaterialTarget("FAB_MATS", 40)); err != nil {
		t.Fatalf("AddMaterial: %v", err)
	}
	if err := pipeline.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	deadA := permanentlyFailedConstructionTask(pipeline.ID(), "FAB_MATS")
	deadB := permanentlyFailedConstructionTask(pipeline.ID(), "ADVANCED_CIRCUITRY")

	checker := &PipelineCompletionChecker{}
	result := checker.evaluateCompletion(pipeline, []*domain.ManufacturingTask{deadA, deadB})

	if !result.ShouldFail {
		t.Fatalf("expected pipeline to fail when every leg is permanently dead")
	}
	if result.ShouldComplete {
		t.Fatalf("did not expect completion when every leg is dead")
	}
}
