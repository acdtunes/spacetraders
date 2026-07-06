package commands

import (
	"context"
	"testing"

	mfgServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services/manufacturing"
)

// fakeWorkerManager is a test double for mfgServices.WorkerManager that returns a
// preconfigured completion and records failure handling.
type fakeWorkerManager struct {
	completion *mfgServices.TaskCompletion
}

func (f *fakeWorkerManager) AssignTaskToShip(ctx context.Context, params mfgServices.AssignTaskParams) error {
	return nil
}

func (f *fakeWorkerManager) HandleWorkerCompletion(ctx context.Context, shipSymbol string) (*mfgServices.TaskCompletion, error) {
	return f.completion, nil
}

func (f *fakeWorkerManager) HandleTaskFailure(ctx context.Context, completion mfgServices.TaskCompletion) error {
	return nil
}

// recordingTaskAssigner is a test double for mfgServices.TaskAssigner that captures
// the AssignParams passed to AssignTasks.
type recordingTaskAssigner struct {
	lastParams mfgServices.AssignParams
	called     bool
}

func (r *recordingTaskAssigner) AssignTasks(ctx context.Context, params mfgServices.AssignParams) (int, error) {
	r.lastParams = params
	r.called = true
	return 0, nil
}

func (r *recordingTaskAssigner) IsSellMarketSaturated(ctx context.Context, sellMarket, good string, playerID int) bool {
	return false
}

func (r *recordingTaskAssigner) GetAssignmentCount() int {
	return 0
}

// TestHandleWorkerCompletion_PassesCoordinatorIDToAssignTasks pins that the task
// reassignment triggered after a worker completes carries the coordinator's container
// ID (CoordinatorID). Every other AssignTasks call in the coordinator sets this so
// worker containers can be cascade-stopped with their parent; this path must too.
func TestHandleWorkerCompletion_PassesCoordinatorIDToAssignTasks(t *testing.T) {
	handler := NewRunParallelManufacturingCoordinatorHandler(
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)

	worker := &fakeWorkerManager{
		completion: &mfgServices.TaskCompletion{
			TaskID:     "task-abcdef123456",
			ShipSymbol: "SHIP-1",
			Success:    false,
		},
	}
	assigner := &recordingTaskAssigner{}
	handler.workerManager = worker
	handler.taskAssigner = assigner

	cmd := &RunParallelManufacturingCoordinatorCommand{
		PlayerID:    7,
		ContainerID: "coordinator-container-42",
	}
	config := coordinatorConfig{maxConcurrentTasks: 5}

	handler.handleWorkerCompletion(context.Background(), cmd, "SHIP-1", config)

	if !assigner.called {
		t.Fatal("expected handleWorkerCompletion to call AssignTasks")
	}
	if assigner.lastParams.CoordinatorID != cmd.ContainerID {
		t.Fatalf("expected AssignTasks CoordinatorID %q, got %q", cmd.ContainerID, assigner.lastParams.CoordinatorID)
	}
}

// TestHandle_UnwiredEventBus_ReturnsErrorInsteadOfPanicking reproduces the daemon
// restart-loop bug: the parallel manufacturing coordinator was constructed in main.go
// without SetEventSubscriber/SetEventPublisher, so Handle nil-dereferenced the event
// bus on the container's naked goroutine and took down the whole daemon.
//
// Handle must instead fail fast with an error so a mis-wired coordinator only fails
// its OWN container, leaving the daemon and other containers healthy.
func TestHandle_UnwiredEventBus_ReturnsErrorInsteadOfPanicking(t *testing.T) {
	// Construct the handler exactly as it would be if the wiring call were missing:
	// the event bus is never set via SetEventSubscriber/SetEventPublisher.
	handler := NewRunParallelManufacturingCoordinatorHandler(
		nil, // demandFinder
		nil, // collectionOpportunityFinder
		nil, // pipelinePlanner
		nil, // taskQueue
		nil, // factoryTracker
		nil, // shipRepo
		nil, // pipelineRepo
		nil, // taskRepo
		nil, // factoryStateRepo
		nil, // marketRepo
		nil, // containerRemover
		nil, // mediator
		nil, // daemonClient
		nil, // clock -> defaults to RealClock
		nil, // waypointProvider
	)

	cmd := &RunParallelManufacturingCoordinatorCommand{
		SystemSymbol:       "X1-PZ28",
		PlayerID:           1,
		ContainerID:        "test-container",
		MaxPipelines:       1,
		MaxConcurrentTasks: 1,
	}

	var (
		err      error
		panicked interface{}
	)
	func() {
		defer func() { panicked = recover() }()
		_, err = handler.Handle(context.Background(), cmd)
	}()

	if panicked != nil {
		t.Fatalf("Handle panicked on an unwired event bus instead of returning an error: %v", panicked)
	}
	if err == nil {
		t.Fatal("expected Handle to return an error when the event bus is not wired, got nil")
	}
}
