package manufacturing

import (
	"sync"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// AssignmentTracker maintains in-memory tracking of task assignments to ships.
// This provides fast access to assignment state without database queries.
type AssignmentTracker struct {
	mu             sync.RWMutex
	assignedTasks  map[string]string                   // taskID -> shipSymbol
	taskContainers map[string]string                   // taskID -> containerID
	tasksByShip    map[string]string                   // shipSymbol -> taskID
	tasksByType    map[manufacturing.TaskType]int      // taskType -> count
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

// UntrackWithType removes a task assignment and decrements the type count.
func (t *AssignmentTracker) UntrackWithType(taskID string, taskType manufacturing.TaskType) {
	t.mu.Lock()
	defer t.mu.Unlock()

	shipSymbol := t.assignedTasks[taskID]
	delete(t.assignedTasks, taskID)
	delete(t.taskContainers, taskID)

	if shipSymbol != "" {
		if t.tasksByShip[shipSymbol] == taskID {
			delete(t.tasksByShip, shipSymbol)
		}
	}

	if t.tasksByType[taskType] > 0 {
		t.tasksByType[taskType]--
	}
}

// GetAssignmentCount returns the total number of assigned tasks.
func (t *AssignmentTracker) GetAssignmentCount() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.assignedTasks)
}

// GetCount is an alias for GetAssignmentCount.
func (t *AssignmentTracker) GetCount() int {
	return t.GetAssignmentCount()
}

// GetAssignedTasks returns a copy of the task-to-ship mapping.
func (t *AssignmentTracker) GetAssignedTasks() map[string]string {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make(map[string]string, len(t.assignedTasks))
	for k, v := range t.assignedTasks {
		result[k] = v
	}
	return result
}

// GetTaskContainers returns a copy of the task-to-container mapping.
func (t *AssignmentTracker) GetTaskContainers() map[string]string {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make(map[string]string, len(t.taskContainers))
	for k, v := range t.taskContainers {
		result[k] = v
	}
	return result
}

// IsTaskAssigned returns true if the task is currently assigned.
func (t *AssignmentTracker) IsTaskAssigned(taskID string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	_, exists := t.assignedTasks[taskID]
	return exists
}

// IsTaskTracked is an alias for IsTaskAssigned.
func (t *AssignmentTracker) IsTaskTracked(taskID string) bool {
	return t.IsTaskAssigned(taskID)
}

// IsShipAssigned returns true if the ship is currently assigned to a task.
func (t *AssignmentTracker) IsShipAssigned(shipSymbol string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	_, exists := t.tasksByShip[shipSymbol]
	return exists
}

// GetShipForTask returns the ship assigned to a task.
func (t *AssignmentTracker) GetShipForTask(taskID string) string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.assignedTasks[taskID]
}

// GetTaskForShip returns the task assigned to a ship.
func (t *AssignmentTracker) GetTaskForShip(shipSymbol string) string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.tasksByShip[shipSymbol]
}

// GetContainerForTask returns the container ID for a task.
func (t *AssignmentTracker) GetContainerForTask(taskID string) string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.taskContainers[taskID]
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

// UpdateTypeCounts updates the type counts (for use with external count sources).
func (t *AssignmentTracker) UpdateTypeCounts(collectSell, acquireDeliver int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.tasksByType[manufacturing.TaskTypeCollectSell] = collectSell
	t.tasksByType[manufacturing.TaskTypeAcquireDeliver] = acquireDeliver
}

// Clear removes all tracked assignments.
func (t *AssignmentTracker) Clear() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.assignedTasks = make(map[string]string)
	t.taskContainers = make(map[string]string)
	t.tasksByShip = make(map[string]string)
	t.tasksByType = make(map[manufacturing.TaskType]int)
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

// LoadFromMaps loads assignments from existing maps (for state recovery).
func (t *AssignmentTracker) LoadFromMaps(tasks map[string]string, containers map[string]string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for taskID, ship := range tasks {
		t.assignedTasks[taskID] = ship
		t.tasksByShip[ship] = taskID
	}

	for taskID, containerID := range containers {
		t.taskContainers[taskID] = containerID
	}
}
