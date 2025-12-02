package manufacturing

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// AssignmentReconciler synchronizes in-memory assignment state with the database.
// This ensures the tracker matches database state after restarts or crashes.
type AssignmentReconciler struct {
	taskRepo manufacturing.TaskRepository
	tracker  *AssignmentTracker
}

// NewAssignmentReconciler creates a new assignment reconciler.
func NewAssignmentReconciler(
	taskRepo manufacturing.TaskRepository,
	tracker *AssignmentTracker,
) *AssignmentReconciler {
	return &AssignmentReconciler{
		taskRepo: taskRepo,
		tracker:  tracker,
	}
}

// Reconcile syncs in-memory state with database.
// Steps:
// 1. Load ASSIGNED tasks from DB into memory (handles coordinator restarts)
// 2. Load EXECUTING tasks from DB into memory
// 3. Remove stale entries that no longer exist or have completed
func (r *AssignmentReconciler) Reconcile(ctx context.Context, playerID int) {
	if r.taskRepo == nil || r.tracker == nil {
		return
	}

	logger := common.LoggerFromContext(ctx)

	// Step 1: Load ASSIGNED tasks from DB into memory
	addedAssigned := r.loadTasksFromDB(ctx, playerID, manufacturing.TaskStatusAssigned)
	if addedAssigned > 0 {
		logger.Log("DEBUG", "Reconciled: loaded ASSIGNED tasks from DB", map[string]interface{}{
			"count": addedAssigned,
		})
	}

	// Step 2: Load EXECUTING tasks from DB into memory
	addedExecuting := r.loadTasksFromDB(ctx, playerID, manufacturing.TaskStatusExecuting)
	if addedExecuting > 0 {
		logger.Log("DEBUG", "Reconciled: loaded EXECUTING tasks from DB", map[string]interface{}{
			"count": addedExecuting,
		})
	}

	// Step 3: Remove stale entries
	staleCount := r.removeStaleEntries(ctx)
	if staleCount > 0 {
		logger.Log("DEBUG", "Reconciled: removed stale entries", map[string]interface{}{
			"count": staleCount,
		})
	}
}

// loadTasksFromDB loads tasks with a given status from database into the tracker.
func (r *AssignmentReconciler) loadTasksFromDB(
	ctx context.Context,
	playerID int,
	status manufacturing.TaskStatus,
) int {
	tasks, err := r.taskRepo.FindByStatus(ctx, playerID, status)
	if err != nil {
		return 0
	}

	added := 0
	for _, task := range tasks {
		if task.AssignedShip() != "" {
			if !r.tracker.IsTaskAssigned(task.ID()) {
				r.tracker.Track(task.ID(), task.AssignedShip(), "", task.TaskType())
				added++
			}
		}
	}
	return added
}

// removeStaleEntries removes tracked tasks that no longer exist or have completed.
func (r *AssignmentReconciler) removeStaleEntries(ctx context.Context) int {
	taskIDs := r.tracker.GetTaskIDs()
	if len(taskIDs) == 0 {
		return 0
	}

	staleTaskIDs := make([]string, 0)
	for _, taskID := range taskIDs {
		task, err := r.taskRepo.FindByID(ctx, taskID)
		if err != nil || task == nil {
			staleTaskIDs = append(staleTaskIDs, taskID)
			continue
		}

		// Remove if task has completed/failed
		if task.Status() != manufacturing.TaskStatusAssigned &&
			task.Status() != manufacturing.TaskStatusExecuting {
			staleTaskIDs = append(staleTaskIDs, taskID)
		}
	}

	for _, taskID := range staleTaskIDs {
		r.tracker.Untrack(taskID)
	}

	return len(staleTaskIDs)
}

// CountAssignedByType returns counts of currently assigned/executing tasks by type.
func (r *AssignmentReconciler) CountAssignedByType(
	ctx context.Context,
	playerID int,
) map[manufacturing.TaskType]int {
	counts := make(map[manufacturing.TaskType]int)

	if r.taskRepo == nil {
		return counts
	}

	// Count ASSIGNED tasks
	assignedTasks, err := r.taskRepo.FindByStatus(ctx, playerID, manufacturing.TaskStatusAssigned)
	if err == nil {
		for _, task := range assignedTasks {
			counts[task.TaskType()]++
		}
	}

	// Count EXECUTING tasks (also count as assigned for reservation purposes)
	executingTasks, err := r.taskRepo.FindByStatus(ctx, playerID, manufacturing.TaskStatusExecuting)
	if err == nil {
		for _, task := range executingTasks {
			counts[task.TaskType()]++
		}
	}

	return counts
}

// GetTracker returns the underlying assignment tracker.
func (r *AssignmentReconciler) GetTracker() *AssignmentTracker {
	return r.tracker
}
