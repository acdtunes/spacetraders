package services

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
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
		nil, // marketLocator
		nil, // storageOpRepo
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
