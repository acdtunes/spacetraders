package services

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// constructionStubTaskRepo embeds the domain interface so only the methods the
// supply monitor exercises need concrete implementations; any unexpected call
// panics on a nil-method deref.
type constructionStubTaskRepo struct {
	manufacturing.TaskRepository

	tasks   []*manufacturing.ManufacturingTask
	updated map[string]manufacturing.TaskStatus
}

func (r *constructionStubTaskRepo) FindByStatus(_ context.Context, _ int, status manufacturing.TaskStatus) ([]*manufacturing.ManufacturingTask, error) {
	var out []*manufacturing.ManufacturingTask
	for _, t := range r.tasks {
		if t.Status() == status {
			out = append(out, t)
		}
	}
	return out, nil
}

func (r *constructionStubTaskRepo) FindByID(_ context.Context, id string) (*manufacturing.ManufacturingTask, error) {
	for _, t := range r.tasks {
		if t.ID() == id {
			return t, nil
		}
	}
	return nil, nil
}

func (r *constructionStubTaskRepo) Update(_ context.Context, task *manufacturing.ManufacturingTask) error {
	if r.updated == nil {
		r.updated = make(map[string]manufacturing.TaskStatus)
	}
	r.updated[task.ID()] = task.Status()
	return nil
}

type constructionStubPipelineRepo struct {
	manufacturing.PipelineRepository

	pipelines map[string]*manufacturing.ManufacturingPipeline
}

func (r *constructionStubPipelineRepo) FindByID(_ context.Context, id string) (*manufacturing.ManufacturingPipeline, error) {
	return r.pipelines[id], nil
}

func newConstructionMonitor(taskRepo *constructionStubTaskRepo, pipelineRepo *constructionStubPipelineRepo, queue *TaskQueue) *SupplyMonitor {
	return NewSupplyMonitor(
		nil, // marketRepo - not needed for construction activation
		manufacturing.NewFactoryStateTracker(),
		nil, // factoryStateRepo
		pipelineRepo,
		queue,
		taskRepo,
		NewSellMarketDistributor(nil, taskRepo),
		nil, // marketLocator
		nil, // storageOpRepo
		nil, // containerReader
		nil, // eventPublisher
		time.Minute,
		1,
	)
}

func newExecutingConstructionPipeline(t *testing.T) *manufacturing.ManufacturingPipeline {
	t.Helper()
	pipeline := manufacturing.NewConstructionPipeline("X1-TEST-I67", 1, 3, 2)
	if err := pipeline.Start(); err != nil {
		t.Fatalf("pipeline.Start: %v", err)
	}
	return pipeline
}

// End-to-end for the execution-layer park (sp-hs2j): a delivery that reached
// execution with no buy source is PARKED back to a deferred PENDING state (via
// ParkForResupply) instead of failing. That parked task must plug straight into
// r900's recovery machinery - the supply monitor re-sources it and marks it READY
// once an EXPORT market clears the supply floor again. This is what turns a supply
// dip into a pause-and-recover instead of a permanent leg death.
func TestPollOnce_ReSourcesParkedConstructionTaskWhenSupplyRegenerates(t *testing.T) {
	pipeline := newExecutingConstructionPipeline(t) // site X1-TEST-I67
	const recoveredMarket = "X1-TEST-D45"

	// A delivery that reached execution with no buy source (the 'no source to
	// acquire from' path during a supply dip) and was parked back to a deferred
	// PENDING state by the executor/worker (EXECUTING -> PENDING, source stays empty).
	parked := manufacturing.NewDeliverToConstructionTask(
		pipeline.ID(), 1, "ADVANCED_CIRCUITRY", "", "", "X1-TEST-I67", []string{},
	)
	if err := parked.MarkReady(); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}
	if err := parked.AssignShip("SHIP-9"); err != nil {
		t.Fatalf("AssignShip: %v", err)
	}
	if err := parked.StartExecution(); err != nil {
		t.Fatalf("StartExecution: %v", err)
	}
	if err := parked.ParkForResupply(); err != nil {
		t.Fatalf("ParkForResupply: %v", err)
	}
	if !parked.IsDeferredConstruction() {
		t.Fatal("precondition: parked task must be deferred (no source, no factory)")
	}

	marketRepo := &plannerStubMarketRepo{
		marketWaypoints: []string{recoveredMarket},
		markets: map[string]*market.Market{
			recoveredMarket: newTradeTypeMarket(t, recoveredMarket, "ADVANCED_CIRCUITRY", "MODERATE", "RESTRICTED", market.TradeTypeExport, 5757),
		},
	}

	taskRepo := &constructionStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{parked}}
	pipelineRepo := &constructionStubPipelineRepo{
		pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline},
	}
	queue := NewTaskQueue()

	monitor := newConstructionMonitorWithMarket(taskRepo, pipelineRepo, queue, marketRepo)
	monitor.PollOnce(context.Background())

	if parked.SourceMarket() != recoveredMarket {
		t.Errorf("expected parked task re-sourced to %s, got %q", recoveredMarket, parked.SourceMarket())
	}
	if parked.Status() != manufacturing.TaskStatusReady {
		t.Fatalf("expected re-sourced task READY, got %s", parked.Status())
	}
	if queue.GetTask(parked.ID()) == nil {
		t.Fatal("expected re-sourced task to be enqueued for dispatch")
	}
}

// A PENDING DELIVER_TO_CONSTRUCTION task with no dependencies in an EXECUTING
// pipeline must be activated (READY + enqueued) by the supply monitor poll.
// This is the bug: construction tasks had no activation path and sat PENDING forever.
func TestPollOnce_ActivatesDependencyFreeConstructionTask(t *testing.T) {
	pipeline := newExecutingConstructionPipeline(t)
	task := manufacturing.NewDeliverToConstructionTask(
		pipeline.ID(), 1, "FAB_MATS", "X1-TEST-F56", "", "X1-TEST-I67", []string{},
	)

	taskRepo := &constructionStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &constructionStubPipelineRepo{
		pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline},
	}
	queue := NewTaskQueue()

	monitor := newConstructionMonitor(taskRepo, pipelineRepo, queue)
	monitor.PollOnce(context.Background())

	if task.Status() != manufacturing.TaskStatusReady {
		t.Fatalf("expected task status READY, got %s", task.Status())
	}
	if queue.GetTask(task.ID()) == nil {
		t.Fatalf("expected task to be enqueued after activation")
	}
	if got := taskRepo.updated[task.ID()]; got != manufacturing.TaskStatusReady {
		t.Fatalf("expected READY status persisted, got %q", got)
	}
}

// A construction task whose dependencies are not yet complete must stay PENDING.
func TestPollOnce_DoesNotActivateConstructionTaskWithIncompleteDependencies(t *testing.T) {
	pipeline := newExecutingConstructionPipeline(t)
	// The dependency is READY (not COMPLETED) - kept out of PENDING so the
	// acquire-deliver activator (which needs a market repo) is not exercised.
	depTask := manufacturing.NewAcquireDeliverTask(pipeline.ID(), 1, "IRON", "X1-TEST-A1", "X1-TEST-B2", nil)
	if err := depTask.MarkReady(); err != nil {
		t.Fatalf("MarkReady dep: %v", err)
	}
	task := manufacturing.NewDeliverToConstructionTask(
		pipeline.ID(), 1, "FAB_MATS", "", "X1-TEST-B2", "X1-TEST-I67", []string{depTask.ID()},
	)

	taskRepo := &constructionStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{depTask, task}}
	pipelineRepo := &constructionStubPipelineRepo{
		pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline},
	}
	queue := NewTaskQueue()

	monitor := newConstructionMonitor(taskRepo, pipelineRepo, queue)
	monitor.PollOnce(context.Background())

	if task.Status() != manufacturing.TaskStatusPending {
		t.Fatalf("expected task to stay PENDING, got %s", task.Status())
	}
	if queue.GetTask(task.ID()) != nil {
		t.Fatalf("task with incomplete dependencies must not be enqueued")
	}
}

func newConstructionMonitorWithMarket(taskRepo *constructionStubTaskRepo, pipelineRepo *constructionStubPipelineRepo, queue *TaskQueue, marketRepo market.MarketRepository) *SupplyMonitor {
	return NewSupplyMonitor(
		marketRepo,
		manufacturing.NewFactoryStateTracker(),
		nil, // factoryStateRepo
		pipelineRepo,
		queue,
		taskRepo,
		NewSellMarketDistributor(nil, taskRepo),
		NewMarketLocator(marketRepo, nil, nil, nil),
		nil, // storageOpRepo
		nil, // containerReader
		nil, // eventPublisher
		time.Minute,
		1,
	)
}

// A DEFERRED construction task (no source, no factory) must NOT be dispatched
// with an empty source. When its material's supply regenerates, the supply
// monitor re-sources it (buy source located) and only then marks it READY -
// mirroring how supply-gated ACQUIRE_DELIVER tasks recover, with no re-invocation
// of the planner.
func TestPollOnce_ReSourcesDeferredConstructionTaskWhenSupplyRegenerates(t *testing.T) {
	pipeline := newExecutingConstructionPipeline(t) // site X1-TEST-I67
	const recoveredMarket = "X1-TEST-D45"

	// Deferred task: no source market, no factory - as staged by the planner.
	deferred := manufacturing.NewDeliverToConstructionTask(
		pipeline.ID(), 1, "ADVANCED_CIRCUITRY", "", "", "X1-TEST-I67", []string{},
	)
	if !deferred.IsDeferredConstruction() {
		t.Fatal("precondition: task must start deferred")
	}

	// Supply regenerated: an EXPORT market now sells ADVANCED_CIRCUITRY at MODERATE.
	marketRepo := &plannerStubMarketRepo{
		marketWaypoints: []string{recoveredMarket},
		markets: map[string]*market.Market{
			recoveredMarket: newTradeTypeMarket(t, recoveredMarket, "ADVANCED_CIRCUITRY", "MODERATE", "RESTRICTED", market.TradeTypeExport, 5757),
		},
	}

	taskRepo := &constructionStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{deferred}}
	pipelineRepo := &constructionStubPipelineRepo{
		pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline},
	}
	queue := NewTaskQueue()

	monitor := newConstructionMonitorWithMarket(taskRepo, pipelineRepo, queue, marketRepo)
	monitor.PollOnce(context.Background())

	if deferred.SourceMarket() != recoveredMarket {
		t.Errorf("expected deferred task re-sourced to %s, got %q", recoveredMarket, deferred.SourceMarket())
	}
	if deferred.Status() != manufacturing.TaskStatusReady {
		t.Fatalf("expected re-sourced task READY, got %s", deferred.Status())
	}
	if queue.GetTask(deferred.ID()) == nil {
		t.Fatal("expected re-sourced task to be enqueued")
	}
	if got := taskRepo.updated[deferred.ID()]; got != manufacturing.TaskStatusReady {
		t.Fatalf("expected READY persisted, got %q", got)
	}
}

// A DEFERRED construction task whose material is still unsourceable (only a
// LIMITED exporter, no import stock) must stay PENDING with no source - it must
// never be dispatched with an empty source.
func TestPollOnce_DeferredConstructionTaskStaysPendingWhenStillUnsourceable(t *testing.T) {
	pipeline := newExecutingConstructionPipeline(t)
	const stillLimited = "X1-TEST-D40"

	deferred := manufacturing.NewDeliverToConstructionTask(
		pipeline.ID(), 1, "ADVANCED_CIRCUITRY", "", "", "X1-TEST-I67", []string{},
	)

	marketRepo := &plannerStubMarketRepo{
		marketWaypoints: []string{stillLimited},
		markets: map[string]*market.Market{
			stillLimited: newTradeTypeMarket(t, stillLimited, "ADVANCED_CIRCUITRY", "LIMITED", "RESTRICTED", market.TradeTypeExport, 5757),
		},
	}

	taskRepo := &constructionStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{deferred}}
	pipelineRepo := &constructionStubPipelineRepo{
		pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline},
	}
	queue := NewTaskQueue()

	monitor := newConstructionMonitorWithMarket(taskRepo, pipelineRepo, queue, marketRepo)
	monitor.PollOnce(context.Background())

	if deferred.Status() != manufacturing.TaskStatusPending {
		t.Fatalf("expected deferred task to stay PENDING while unsourceable, got %s", deferred.Status())
	}
	if deferred.SourceMarket() != "" {
		t.Errorf("expected no source assigned, got %q", deferred.SourceMarket())
	}
	if queue.GetTask(deferred.ID()) != nil {
		t.Fatal("deferred task with no source must not be enqueued")
	}
}

// A construction task from a non-EXECUTING pipeline must not be activated.
func TestPollOnce_DoesNotActivateConstructionTaskFromPlanningPipeline(t *testing.T) {
	pipeline := manufacturing.NewConstructionPipeline("X1-TEST-I67", 1, 3, 2) // stays PLANNING
	task := manufacturing.NewDeliverToConstructionTask(
		pipeline.ID(), 1, "FAB_MATS", "X1-TEST-F56", "", "X1-TEST-I67", []string{},
	)

	taskRepo := &constructionStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &constructionStubPipelineRepo{
		pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline},
	}
	queue := NewTaskQueue()

	monitor := newConstructionMonitor(taskRepo, pipelineRepo, queue)
	monitor.PollOnce(context.Background())

	if task.Status() != manufacturing.TaskStatusPending {
		t.Fatalf("expected task to stay PENDING, got %s", task.Status())
	}
}
