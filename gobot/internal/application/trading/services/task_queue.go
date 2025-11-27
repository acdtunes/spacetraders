package services

import (
	"container/heap"
	"context"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// TaskQueue manages manufacturing tasks with priority ordering.
// It provides efficient access to ready tasks sorted by priority.
// The queue is thread-safe for concurrent access.
type TaskQueue struct {
	mu       sync.RWMutex
	tasks    taskHeap
	taskByID map[string]*manufacturing.ManufacturingTask
}

// NewTaskQueue creates a new task queue
func NewTaskQueue() *TaskQueue {
	tq := &TaskQueue{
		tasks:    make(taskHeap, 0),
		taskByID: make(map[string]*manufacturing.ManufacturingTask),
	}
	heap.Init(&tq.tasks)
	return tq
}

// Enqueue adds a task to the queue
// If a task with the same ID already exists, it is replaced with the new task
// to ensure the queue reflects the current task state from the database.
func (q *TaskQueue) Enqueue(task *manufacturing.ManufacturingTask) {
	q.mu.Lock()
	defer q.mu.Unlock()

	// Only add if task is READY
	if task.Status() != manufacturing.TaskStatusReady {
		return
	}

	// Remove existing task if present to update with new state
	// This ensures the queue reflects the current task state from the database
	if _, exists := q.taskByID[task.ID()]; exists {
		q.removeByIDLocked(task.ID())
	}

	q.taskByID[task.ID()] = task
	heap.Push(&q.tasks, task)
}

// EnqueuePriority adds a high-priority task to the queue
func (q *TaskQueue) EnqueuePriority(task *manufacturing.ManufacturingTask) {
	q.mu.Lock()
	defer q.mu.Unlock()

	// Remove existing if present
	if _, exists := q.taskByID[task.ID()]; exists {
		q.removeByIDLocked(task.ID())
	}

	q.taskByID[task.ID()] = task
	heap.Push(&q.tasks, task)
}

// Dequeue removes and returns the highest-priority ready task
func (q *TaskQueue) Dequeue() *manufacturing.ManufacturingTask {
	q.mu.Lock()
	defer q.mu.Unlock()

	for q.tasks.Len() > 0 {
		task := heap.Pop(&q.tasks).(*manufacturing.ManufacturingTask)
		delete(q.taskByID, task.ID())

		// Verify task is still ready (may have changed since enqueued)
		if task.Status() == manufacturing.TaskStatusReady {
			return task
		}
	}

	return nil
}

// Peek returns the highest-priority task without removing it
func (q *TaskQueue) Peek() *manufacturing.ManufacturingTask {
	q.mu.RLock()
	defer q.mu.RUnlock()

	if q.tasks.Len() == 0 {
		return nil
	}

	return q.tasks[0]
}

// GetReadyTasks returns all ready tasks sorted by effective priority (highest first)
// Note: heap order != sorted order, so we must explicitly sort the results
func (q *TaskQueue) GetReadyTasks() []*manufacturing.ManufacturingTask {
	q.mu.RLock()
	defer q.mu.RUnlock()

	result := make([]*manufacturing.ManufacturingTask, 0, q.tasks.Len())
	for _, task := range q.tasks {
		if task.Status() == manufacturing.TaskStatusReady {
			result = append(result, task)
		}
	}

	// Sort by effective priority (with aging) - highest first
	// Heap order only guarantees index 0 is max, not full sort order
	sortByEffectivePriority(result)

	return result
}

// sortByEffectivePriority sorts tasks by effective priority (base + aging) in descending order
func sortByEffectivePriority(tasks []*manufacturing.ManufacturingTask) {
	// Use same priority calculation as heap Less() function
	effectivePriority := func(task *manufacturing.ManufacturingTask) int {
		basePriority := task.Priority()
		readyAt := task.ReadyAt()
		if readyAt == nil {
			return basePriority
		}
		minutesWaiting := time.Since(*readyAt).Minutes()
		if minutesWaiting < 0 {
			minutesWaiting = 0
		}
		return basePriority + int(minutesWaiting*2)
	}

	// Sort descending by effective priority
	for i := 0; i < len(tasks)-1; i++ {
		for j := i + 1; j < len(tasks); j++ {
			iPriority := effectivePriority(tasks[i])
			jPriority := effectivePriority(tasks[j])
			if jPriority > iPriority {
				tasks[i], tasks[j] = tasks[j], tasks[i]
			} else if jPriority == iPriority {
				// Tiebreaker: earlier ready time
				iReady := tasks[i].ReadyAt()
				jReady := tasks[j].ReadyAt()
				if iReady != nil && jReady != nil && jReady.Before(*iReady) {
					tasks[i], tasks[j] = tasks[j], tasks[i]
				}
			}
		}
	}
}

// GetTask returns a task by ID
func (q *TaskQueue) GetTask(taskID string) *manufacturing.ManufacturingTask {
	q.mu.RLock()
	defer q.mu.RUnlock()

	return q.taskByID[taskID]
}

// Remove removes a task from the queue by ID
func (q *TaskQueue) Remove(taskID string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	return q.removeByIDLocked(taskID)
}

// removeByIDLocked removes a task by ID (must hold lock)
func (q *TaskQueue) removeByIDLocked(taskID string) bool {
	task, exists := q.taskByID[taskID]
	if !exists {
		return false
	}

	delete(q.taskByID, taskID)

	// Find and remove from heap
	for i, t := range q.tasks {
		if t.ID() == taskID {
			heap.Remove(&q.tasks, i)
			break
		}
	}

	_ = task // Suppress unused variable warning
	return true
}

// Size returns the number of tasks in the queue
func (q *TaskQueue) Size() int {
	q.mu.RLock()
	defer q.mu.RUnlock()

	return q.tasks.Len()
}

// Clear removes all tasks from the queue
func (q *TaskQueue) Clear() {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.tasks = make(taskHeap, 0)
	q.taskByID = make(map[string]*manufacturing.ManufacturingTask)
	heap.Init(&q.tasks)
}

// MarkCollectTasksReady marks COLLECT tasks as ready when factory supply reaches HIGH
// This is called by the SupplyMonitor when production is complete
func (q *TaskQueue) MarkCollectTasksReady(factorySymbol string, outputGood string) int {
	q.mu.Lock()
	defer q.mu.Unlock()

	marked := 0
	for _, task := range q.taskByID {
		if task.TaskType() == manufacturing.TaskTypeCollectSell &&
			task.FactorySymbol() == factorySymbol &&
			task.Good() == outputGood &&
			task.Status() == manufacturing.TaskStatusPending {
			if err := task.MarkReady(); err == nil {
				marked++
			}
		}
	}

	return marked
}

// LoadFromRepository loads all ready tasks from the repository
func (q *TaskQueue) LoadFromRepository(ctx context.Context, repo manufacturing.TaskRepository, playerID int) error {
	tasks, err := repo.FindReadyTasks(ctx, playerID)
	if err != nil {
		return err
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	q.tasks = make(taskHeap, 0, len(tasks))
	q.taskByID = make(map[string]*manufacturing.ManufacturingTask)
	heap.Init(&q.tasks)

	for _, task := range tasks {
		q.taskByID[task.ID()] = task
		heap.Push(&q.tasks, task)
	}

	return nil
}

// taskHeap implements heap.Interface for priority-based task ordering
// Higher priority tasks come first
type taskHeap []*manufacturing.ManufacturingTask

func (h taskHeap) Len() int { return len(h) }

func (h taskHeap) Less(i, j int) bool {
	// Calculate effective priority with aging to prevent starvation
	// Formula: effective_priority = base_priority + (minutes_waiting * 2)
	// This allows lower-priority tasks to eventually match higher-priority ones
	iPriority := h.effectivePriority(h[i])
	jPriority := h.effectivePriority(h[j])

	// Higher effective priority comes first (max heap)
	if iPriority != jPriority {
		return iPriority > jPriority
	}

	// Tiebreaker: earlier ready time
	iReady := h[i].ReadyAt()
	jReady := h[j].ReadyAt()
	if iReady != nil && jReady != nil {
		return iReady.Before(*jReady)
	}
	// Fallback: earlier created time
	return h[i].CreatedAt().Before(h[j].CreatedAt())
}

// effectivePriority calculates priority with aging boost
// Tasks waiting longer get priority boost to prevent starvation
// Boost: +2 priority per minute waiting (COLLECT_SELL catches up to ACQUIRE_DELIVER after 5 min)
func (h taskHeap) effectivePriority(task *manufacturing.ManufacturingTask) int {
	basePriority := task.Priority()

	// Calculate aging boost based on time since task became ready
	readyAt := task.ReadyAt()
	if readyAt == nil {
		return basePriority
	}

	minutesWaiting := time.Since(*readyAt).Minutes()
	if minutesWaiting < 0 {
		minutesWaiting = 0
	}

	// +2 priority per minute waiting
	// After 5 minutes, COLLECT_SELL (0) catches up to ACQUIRE_DELIVER (10)
	agingBoost := int(minutesWaiting * 2)

	return basePriority + agingBoost
}

func (h taskHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *taskHeap) Push(x interface{}) {
	*h = append(*h, x.(*manufacturing.ManufacturingTask))
}

func (h *taskHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}
