package services

import (
	"context"
	"sync"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// DualTaskQueue manages separate queues for fabrication (ACQUIRE_DELIVER) and
// collection (COLLECT_SELL) tasks. This separation enables:
// 1. Fabrication pipelines to be limited by max_pipelines parameter
// 2. Collection pipelines to be unlimited for revenue generation
// 3. Independent queue management for each task type
type DualTaskQueue struct {
	mu               sync.RWMutex
	fabricationQueue *TaskQueue // ACQUIRE_DELIVER tasks (for fabrication pipelines)
	collectionQueue  *TaskQueue // COLLECT_SELL tasks (for collection pipelines)
}

// NewDualTaskQueue creates a new dual task queue with separate fabrication and collection queues
func NewDualTaskQueue() *DualTaskQueue {
	return &DualTaskQueue{
		fabricationQueue: NewTaskQueue(),
		collectionQueue:  NewTaskQueue(),
	}
}

// Enqueue adds a task to the appropriate queue based on task type.
// ACQUIRE_DELIVER tasks go to fabrication queue.
// COLLECT_SELL tasks go to collection queue.
func (q *DualTaskQueue) Enqueue(task *manufacturing.ManufacturingTask) {
	q.mu.Lock()
	defer q.mu.Unlock()

	switch task.TaskType() {
	case manufacturing.TaskTypeAcquireDeliver:
		q.fabricationQueue.Enqueue(task)
	case manufacturing.TaskTypeCollectSell:
		q.collectionQueue.Enqueue(task)
	default:
		// Unknown task type - default to fabrication queue
		q.fabricationQueue.Enqueue(task)
	}
}

// EnqueuePriority adds a high-priority task to the appropriate queue
func (q *DualTaskQueue) EnqueuePriority(task *manufacturing.ManufacturingTask) {
	q.mu.Lock()
	defer q.mu.Unlock()

	switch task.TaskType() {
	case manufacturing.TaskTypeAcquireDeliver:
		q.fabricationQueue.EnqueuePriority(task)
	case manufacturing.TaskTypeCollectSell:
		q.collectionQueue.EnqueuePriority(task)
	default:
		q.fabricationQueue.EnqueuePriority(task)
	}
}

// GetReadyFabricationTasks returns all ready ACQUIRE_DELIVER tasks sorted by priority
func (q *DualTaskQueue) GetReadyFabricationTasks() []*manufacturing.ManufacturingTask {
	q.mu.RLock()
	defer q.mu.RUnlock()

	return q.fabricationQueue.GetReadyTasks()
}

// GetReadyCollectionTasks returns all ready COLLECT_SELL tasks sorted by priority
func (q *DualTaskQueue) GetReadyCollectionTasks() []*manufacturing.ManufacturingTask {
	q.mu.RLock()
	defer q.mu.RUnlock()

	return q.collectionQueue.GetReadyTasks()
}

// GetReadyTasks returns all ready tasks from both queues combined, sorted by priority.
// This provides backward compatibility with code expecting a single queue.
// Collection tasks have higher priority to generate revenue first.
func (q *DualTaskQueue) GetReadyTasks() []*manufacturing.ManufacturingTask {
	q.mu.RLock()
	defer q.mu.RUnlock()

	// Get tasks from both queues
	fabricationTasks := q.fabricationQueue.GetReadyTasks()
	collectionTasks := q.collectionQueue.GetReadyTasks()

	// Combine: collection tasks first (revenue generation priority), then fabrication
	result := make([]*manufacturing.ManufacturingTask, 0, len(fabricationTasks)+len(collectionTasks))
	result = append(result, collectionTasks...)
	result = append(result, fabricationTasks...)

	return result
}

// DequeueFabrication removes and returns the highest-priority fabrication task
func (q *DualTaskQueue) DequeueFabrication() *manufacturing.ManufacturingTask {
	q.mu.Lock()
	defer q.mu.Unlock()

	return q.fabricationQueue.Dequeue()
}

// DequeueCollection removes and returns the highest-priority collection task
func (q *DualTaskQueue) DequeueCollection() *manufacturing.ManufacturingTask {
	q.mu.Lock()
	defer q.mu.Unlock()

	return q.collectionQueue.Dequeue()
}

// Dequeue removes and returns the highest-priority task from either queue.
// Prioritizes collection tasks over fabrication tasks.
func (q *DualTaskQueue) Dequeue() *manufacturing.ManufacturingTask {
	q.mu.Lock()
	defer q.mu.Unlock()

	// Prioritize collection tasks (revenue generation)
	if task := q.collectionQueue.Dequeue(); task != nil {
		return task
	}

	return q.fabricationQueue.Dequeue()
}

// GetTask returns a task by ID from either queue
func (q *DualTaskQueue) GetTask(taskID string) *manufacturing.ManufacturingTask {
	q.mu.RLock()
	defer q.mu.RUnlock()

	if task := q.fabricationQueue.GetTask(taskID); task != nil {
		return task
	}
	return q.collectionQueue.GetTask(taskID)
}

// Remove removes a task from either queue by ID
func (q *DualTaskQueue) Remove(taskID string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	// Try both queues
	if q.fabricationQueue.Remove(taskID) {
		return true
	}
	return q.collectionQueue.Remove(taskID)
}

// Size returns the total number of tasks in both queues
func (q *DualTaskQueue) Size() int {
	q.mu.RLock()
	defer q.mu.RUnlock()

	return q.fabricationQueue.Size() + q.collectionQueue.Size()
}

// FabricationSize returns the number of fabrication tasks in queue
func (q *DualTaskQueue) FabricationSize() int {
	q.mu.RLock()
	defer q.mu.RUnlock()

	return q.fabricationQueue.Size()
}

// CollectionSize returns the number of collection tasks in queue
func (q *DualTaskQueue) CollectionSize() int {
	q.mu.RLock()
	defer q.mu.RUnlock()

	return q.collectionQueue.Size()
}

// CountByType returns counts of ready tasks by task type across both queues
func (q *DualTaskQueue) CountByType() map[manufacturing.TaskType]int {
	q.mu.RLock()
	defer q.mu.RUnlock()

	counts := make(map[manufacturing.TaskType]int)

	// Get counts from fabrication queue
	for taskType, count := range q.fabricationQueue.CountByType() {
		counts[taskType] = count
	}

	// Get counts from collection queue
	for taskType, count := range q.collectionQueue.CountByType() {
		counts[taskType] += count
	}

	return counts
}

// HasReadyFabricationTasks returns true if there are ready fabrication tasks
func (q *DualTaskQueue) HasReadyFabricationTasks() bool {
	q.mu.RLock()
	defer q.mu.RUnlock()

	return q.fabricationQueue.Size() > 0
}

// HasReadyCollectionTasks returns true if there are ready collection tasks
func (q *DualTaskQueue) HasReadyCollectionTasks() bool {
	q.mu.RLock()
	defer q.mu.RUnlock()

	return q.collectionQueue.Size() > 0
}

// HasReadyTasks returns true if there are ready tasks in either queue
func (q *DualTaskQueue) HasReadyTasks() bool {
	q.mu.RLock()
	defer q.mu.RUnlock()

	return q.fabricationQueue.Size() > 0 || q.collectionQueue.Size() > 0
}

// HasReadyTasksByType returns true if there are ready tasks of the specified type
// Provides backward compatibility with code using the single TaskQueue
func (q *DualTaskQueue) HasReadyTasksByType(taskType manufacturing.TaskType) bool {
	q.mu.RLock()
	defer q.mu.RUnlock()

	switch taskType {
	case manufacturing.TaskTypeCollectSell:
		return q.collectionQueue.Size() > 0
	case manufacturing.TaskTypeAcquireDeliver:
		return q.fabricationQueue.Size() > 0
	default:
		// Check both queues for unknown types
		return q.fabricationQueue.HasReadyTasksByType(taskType) || q.collectionQueue.HasReadyTasksByType(taskType)
	}
}

// GetReadyTasksByType returns ready tasks filtered by type, sorted by priority
// Provides backward compatibility with code using the single TaskQueue
func (q *DualTaskQueue) GetReadyTasksByType(taskType manufacturing.TaskType) []*manufacturing.ManufacturingTask {
	q.mu.RLock()
	defer q.mu.RUnlock()

	switch taskType {
	case manufacturing.TaskTypeCollectSell:
		return q.collectionQueue.GetReadyTasks()
	case manufacturing.TaskTypeAcquireDeliver:
		return q.fabricationQueue.GetReadyTasks()
	default:
		// Check both queues for unknown types
		result := q.fabricationQueue.GetReadyTasksByType(taskType)
		result = append(result, q.collectionQueue.GetReadyTasksByType(taskType)...)
		return result
	}
}

// MarkCollectTasksReady marks COLLECT tasks as ready when factory supply reaches HIGH.
// Delegates to the collection queue.
func (q *DualTaskQueue) MarkCollectTasksReady(factorySymbol string, outputGood string) int {
	q.mu.Lock()
	defer q.mu.Unlock()

	return q.collectionQueue.MarkCollectTasksReady(factorySymbol, outputGood)
}

// Clear removes all tasks from both queues
func (q *DualTaskQueue) Clear() {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.fabricationQueue.Clear()
	q.collectionQueue.Clear()
}

// LoadFromRepository loads all ready tasks from the repository into appropriate queues
func (q *DualTaskQueue) LoadFromRepository(ctx context.Context, repo manufacturing.TaskRepository, playerID int) error {
	tasks, err := repo.FindReadyTasks(ctx, playerID)
	if err != nil {
		return err
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	// Clear both queues
	q.fabricationQueue.Clear()
	q.collectionQueue.Clear()

	// Route tasks to appropriate queues
	for _, task := range tasks {
		switch task.TaskType() {
		case manufacturing.TaskTypeCollectSell:
			q.collectionQueue.Enqueue(task)
		default:
			q.fabricationQueue.Enqueue(task)
		}
	}

	return nil
}

// FabricationQueue returns the underlying fabrication queue (for backward compatibility)
func (q *DualTaskQueue) FabricationQueue() *TaskQueue {
	return q.fabricationQueue
}

// CollectionQueue returns the underlying collection queue (for backward compatibility)
func (q *DualTaskQueue) CollectionQueue() *TaskQueue {
	return q.collectionQueue
}
