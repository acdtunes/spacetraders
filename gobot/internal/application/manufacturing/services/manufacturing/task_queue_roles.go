package manufacturing

import "github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"

type TaskEnqueuer interface {
	Enqueue(task *manufacturing.ManufacturingTask)
}

type TaskRemover interface {
	Remove(taskID string) bool
}

type ReadyTaskReader interface {
	GetReadyTasks() []*manufacturing.ManufacturingTask
	HasReadyTasksByType(taskType manufacturing.TaskType) bool
}

type TaskCounter interface {
	Size() int
}

type TaskEnqueueRemover interface {
	TaskEnqueuer
	TaskRemover
}

type ReadyTaskAssignmentQueue interface {
	ReadyTaskReader
	TaskRemover
}

type RecoveryTaskQueue interface {
	TaskEnqueuer
	TaskCounter
}
