package manufacturing

import (
	"sync"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// AssignmentTracker maintains in-memory tracking of task assignments to ships.
// This provides fast access to assignment state without database queries.
type AssignmentTracker struct {
	mu             sync.RWMutex
	assignedTasks  map[string]string              // taskID -> shipSymbol
	taskContainers map[string]string              // taskID -> containerID
	tasksByShip    map[string]string              // shipSymbol -> taskID
	tasksByType    map[manufacturing.TaskType]int // taskType -> count
}

// NewAssignmentTracker creates a new assignment tracker.
func NewAssignmentTracker() *AssignmentTracker {
	return &AssignmentTracker{
		assignedTasks:  make(map[string]string),
		taskContainers: make(map[string]string),
		tasksByShip:    make(map[string]string),
		tasksByType:    make(map[manufacturing.TaskType]int),
	}
}

// Track records a task assignment.
func (t *AssignmentTracker) Track(taskID, shipSymbol, containerID string, taskType manufacturing.TaskType) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.assignedTasks[taskID] = shipSymbol
	t.taskContainers[taskID] = containerID
	t.tasksByShip[shipSymbol] = taskID
	t.tasksByType[taskType]++
}

// Untrack removes a task assignment.
func (t *AssignmentTracker) Untrack(taskID string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Get ship and type before removing
	shipSymbol := t.assignedTasks[taskID]
	delete(t.assignedTasks, taskID)
	delete(t.taskContainers, taskID)

	if shipSymbol != "" {
		// Check if this was the current task for this ship
		if t.tasksByShip[shipSymbol] == taskID {
			delete(t.tasksByShip, shipSymbol)
		}
	}
}

// GetAssignmentCount returns the total number of assigned tasks.
func (t *AssignmentTracker) GetAssignmentCount() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.assignedTasks)
}

// IsTaskAssigned returns true if the task is currently assigned.
func (t *AssignmentTracker) IsTaskAssigned(taskID string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	_, exists := t.assignedTasks[taskID]
	return exists
}

// GetAllocations returns the current allocation counts for reservation policy.
func (t *AssignmentTracker) GetAllocations() manufacturing.TaskTypeAllocations {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return manufacturing.TaskTypeAllocations{
		CollectSellCount:    t.tasksByType[manufacturing.TaskTypeCollectSell],
		AcquireDeliverCount: t.tasksByType[manufacturing.TaskTypeAcquireDeliver],
		TotalWorkers:        len(t.assignedTasks),
	}
}

// GetTaskIDs returns all currently tracked task IDs.
func (t *AssignmentTracker) GetTaskIDs() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make([]string, 0, len(t.assignedTasks))
	for taskID := range t.assignedTasks {
		result = append(result, taskID)
	}
	return result
}
