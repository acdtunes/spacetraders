package services

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// activatorStubTaskRepo embeds the domain interface so only the methods the
// activator exercises need concrete implementations; any unexpected call
// panics on a nil-method deref.
type activatorStubTaskRepo struct {
	manufacturing.TaskRepository

	pending []*manufacturing.ManufacturingTask
	updated []*manufacturing.ManufacturingTask
}

func (r *activatorStubTaskRepo) FindByStatus(_ context.Context, _ int, status manufacturing.TaskStatus) ([]*manufacturing.ManufacturingTask, error) {
	if status != manufacturing.TaskStatusPending {
		return nil, nil
	}
	return r.pending, nil
}

func (r *activatorStubTaskRepo) Update(_ context.Context, task *manufacturing.ManufacturingTask) error {
	r.updated = append(r.updated, task)
	return nil
}

// activatorStubPipelineRepo embeds the domain interface so only FindByID
// needs a concrete implementation.
type activatorStubPipelineRepo struct {
	manufacturing.PipelineRepository

	pipeline *manufacturing.ManufacturingPipeline
}

func (r *activatorStubPipelineRepo) FindByID(_ context.Context, _ string) (*manufacturing.ManufacturingPipeline, error) {
	return r.pipeline, nil
}

// activatorStubTaskQueue embeds the interface so only Enqueue needs a
// concrete implementation.
type activatorStubTaskQueue struct {
	ManufacturingTaskQueue

	enqueued []*manufacturing.ManufacturingTask
}

func (q *activatorStubTaskQueue) Enqueue(task *manufacturing.ManufacturingTask) {
	q.enqueued = append(q.enqueued, task)
}

// newActivatorUnderTest builds a TaskActivator via struct literal - there is
// no exported constructor; production code (supply_monitor.go) builds it the
// same way.
func newActivatorUnderTest(taskRepo *activatorStubTaskRepo, pipelineRepo *activatorStubPipelineRepo, taskQueue *activatorStubTaskQueue, marketLocator *MarketLocator) *TaskActivator {
	return &TaskActivator{
		taskRepo:      taskRepo,
		pipelineRepo:  pipelineRepo,
		taskQueue:     taskQueue,
		marketLocator: marketLocator,
		playerID:      1,
		notifier:      &taskReadyNotifier{},
	}
}

// sp-j2hq: a deferred DELIVER_TO_CONSTRUCTION task recovered by the poll-loop
// must be sourced against the pipeline's persisted --min-supply floor, not a
// hardcoded default MODERATE floor - otherwise a floor set at planning time
// (or on a later resumed `construction start --min-supply X`) is silently
// dropped the moment a material actually needs the recovery path.
func TestActivateConstructionTasks_DeferredTask_UsesPersistedMinSupplyFloor(t *testing.T) {
	const circuitryScarce = "X1-PZ28-D40"

	pipeline := manufacturing.NewConstructionPipeline(plannerTestSite, 1, 3, 5)
	pipeline.SetMinSupply("SCARCE")
	if err := pipeline.Start(); err != nil {
		t.Fatalf("pipeline.Start: %v", err)
	}

	deferredTask := manufacturing.NewDeliverToConstructionTask(
		pipeline.ID(), 1, "ADVANCED_CIRCUITRY", "", "", plannerTestSite, nil,
	)

	marketRepo := &plannerStubMarketRepo{
		marketWaypoints: []string{circuitryScarce},
		markets: map[string]*market.Market{
			circuitryScarce: newTradeTypeMarket(t, circuitryScarce, "ADVANCED_CIRCUITRY", "SCARCE", "RESTRICTED", market.TradeTypeExport, 5757),
		},
	}
	marketLocator := NewMarketLocator(marketRepo, nil, nil, nil)

	taskRepo := &activatorStubTaskRepo{pending: []*manufacturing.ManufacturingTask{deferredTask}}
	pipelineRepo := &activatorStubPipelineRepo{pipeline: pipeline}
	taskQueue := &activatorStubTaskQueue{}
	activator := newActivatorUnderTest(taskRepo, pipelineRepo, taskQueue, marketLocator)

	activated := activator.ActivateConstructionTasks(context.Background())

	if activated != 1 {
		t.Fatalf("expected 1 task activated (SCARCE floor accepts the export), got %d", activated)
	}
	if deferredTask.SourceMarket() != circuitryScarce {
		t.Errorf("expected deferred task sourced from SCARCE exporter %s, got %q", circuitryScarce, deferredTask.SourceMarket())
	}
	if deferredTask.Status() != manufacturing.TaskStatusReady {
		t.Errorf("expected deferred task to become READY, got %s", deferredTask.Status())
	}
	if len(taskQueue.enqueued) != 1 {
		t.Errorf("expected the recovered task to be enqueued, got %d enqueued", len(taskQueue.enqueued))
	}
}

// Regression/pin test: an unset (empty) --min-supply floor must still
// default to MODERATE in the recovery path, so a SCARCE-only exporter is
// correctly left deferred (not silently accepted just because we now read
// the floor from the pipeline instead of a hardcoded literal).
func TestActivateConstructionTasks_DeferredTask_UnsetFloorDefaultsToModerate(t *testing.T) {
	const circuitryScarce = "X1-PZ28-D40"

	pipeline := manufacturing.NewConstructionPipeline(plannerTestSite, 1, 3, 5)
	// MinSupply deliberately left unset ("").
	if err := pipeline.Start(); err != nil {
		t.Fatalf("pipeline.Start: %v", err)
	}

	deferredTask := manufacturing.NewDeliverToConstructionTask(
		pipeline.ID(), 1, "ADVANCED_CIRCUITRY", "", "", plannerTestSite, nil,
	)

	marketRepo := &plannerStubMarketRepo{
		marketWaypoints: []string{circuitryScarce},
		markets: map[string]*market.Market{
			circuitryScarce: newTradeTypeMarket(t, circuitryScarce, "ADVANCED_CIRCUITRY", "SCARCE", "RESTRICTED", market.TradeTypeExport, 5757),
		},
	}
	marketLocator := NewMarketLocator(marketRepo, nil, nil, nil)

	taskRepo := &activatorStubTaskRepo{pending: []*manufacturing.ManufacturingTask{deferredTask}}
	pipelineRepo := &activatorStubPipelineRepo{pipeline: pipeline}
	taskQueue := &activatorStubTaskQueue{}
	activator := newActivatorUnderTest(taskRepo, pipelineRepo, taskQueue, marketLocator)

	activated := activator.ActivateConstructionTasks(context.Background())

	if activated != 0 {
		t.Fatalf("expected 0 tasks activated (SCARCE exporter is below the default MODERATE floor), got %d", activated)
	}
	if deferredTask.SourceMarket() != "" {
		t.Errorf("expected deferred task to remain unsourced, got %q", deferredTask.SourceMarket())
	}
	if deferredTask.Status() != manufacturing.TaskStatusPending {
		t.Errorf("expected deferred task to remain PENDING, got %s", deferredTask.Status())
	}
	if len(taskQueue.enqueued) != 0 {
		t.Errorf("expected nothing enqueued, got %d", len(taskQueue.enqueued))
	}
}
