package commands

import (
	"context"
	"testing"
)

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
