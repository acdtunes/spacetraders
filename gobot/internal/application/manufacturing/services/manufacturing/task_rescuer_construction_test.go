package manufacturing

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// rescuerStubTaskRepo embeds the domain interface so only the methods the
// rescuer exercises need concrete implementations.
type rescuerStubTaskRepo struct {
	manufacturing.TaskRepository

	readyTasks []*manufacturing.ManufacturingTask
}

func (r *rescuerStubTaskRepo) FindReadyWithActivePipeline(_ context.Context, _ int) ([]*manufacturing.ManufacturingTask, error) {
	return r.readyTasks, nil
}

func (r *rescuerStubTaskRepo) Update(_ context.Context, _ *manufacturing.ManufacturingTask) error {
	return nil
}

// A READY DELIVER_TO_CONSTRUCTION task must be re-enqueued by the rescuer.
// This is the bug: the rescuer's type switch had no construction case, so
// orphaned READY construction tasks were silently dropped forever.
func TestRescueReadyTasks_RescuesDeliverToConstructionTask(t *testing.T) {
	task := manufacturing.NewDeliverToConstructionTask(
		"pipeline-1", 1, "FAB_MATS", "X1-TEST-F56", "", "X1-TEST-I67", []string{},
	)
	if err := task.MarkReady(); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}

	queue := services.NewTaskQueue()
	rescuer := NewTaskRescuer(
		&rescuerStubTaskRepo{readyTasks: []*manufacturing.ManufacturingTask{task}},
		queue,
		nil, // no market condition checker needed for construction deliveries
	)

	result := rescuer.RescueReadyTasks(context.Background(), 1)

	if result.TotalRescued() != 1 {
		t.Fatalf("expected 1 rescued task, got %d", result.TotalRescued())
	}
	if queue.GetTask(task.ID()) == nil {
		t.Fatalf("expected DELIVER_TO_CONSTRUCTION task to be re-enqueued")
	}
	if task.Status() != manufacturing.TaskStatusReady {
		t.Fatalf("expected task to remain READY, got %s", task.Status())
	}
}
