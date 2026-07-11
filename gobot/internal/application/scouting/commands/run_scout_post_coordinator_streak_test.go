package commands

import (
	"context"
	"errors"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/health"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

func scoutStreakCmd() *RunScoutPostCoordinatorCommand {
	return &RunScoutPostCoordinatorCommand{PlayerID: shared.MustNewPlayerID(1), ContainerID: "scout-post-coordinator-1"}
}

// TestScoutPostStreak_ReconcileFailsRepeatedly_EmitsErrorLoopEvent pins the sp-6wxq
// wiring at the scout-post reconcile checkpoint: a pass that fails with the identical
// error for DefaultStreakThreshold consecutive ticks crosses exactly once and emits one
// interrupt-class coordinator error-loop event through the already-wired eventStore,
// while a below-threshold run stays silent.
func TestScoutPostStreak_ReconcileFailsRepeatedly_EmitsErrorLoopEvent(t *testing.T) {
	store := &fakeScoutEventStore{}
	h := &RunScoutPostCoordinatorHandler{eventStore: store}

	ctx := context.Background()
	cmd := scoutStreakCmd()
	errMon := health.NewMonitor(health.DefaultStreakThreshold)
	sameErr := errors.New("failed to list scout posts: db down")

	for i := 1; i < health.DefaultStreakThreshold; i++ {
		h.noteReconcile(ctx, cmd, errMon, sameErr)
	}
	if len(store.recorded) != 0 {
		t.Fatalf("expected no event before the streak threshold, got %d", len(store.recorded))
	}

	h.noteReconcile(ctx, cmd, errMon, sameErr)
	// The scout-post store may also carry other event types; count only error-loop events.
	loops := 0
	var last *captain.Event
	for _, e := range store.recorded {
		if e.Type == captain.EventCoordinatorErrorLoop {
			loops++
			last = e
		}
	}
	if loops != 1 {
		t.Fatalf("expected exactly one error-loop event at the threshold, got %d", loops)
	}
	if last.Ship != cmd.ContainerID {
		t.Fatalf("expected the event scoped to the coordinator container %q, got %q", cmd.ContainerID, last.Ship)
	}
}

// TestScoutPostStreak_SuccessResetsStreak pins reset-on-success: a healthy pass between
// failures restarts the streak, so an intermittent coordinator never falsely escalates.
func TestScoutPostStreak_SuccessResetsStreak(t *testing.T) {
	store := &fakeScoutEventStore{}
	h := &RunScoutPostCoordinatorHandler{eventStore: store}

	ctx := context.Background()
	cmd := scoutStreakCmd()
	errMon := health.NewMonitor(health.DefaultStreakThreshold)
	sameErr := errors.New("failed to list scout posts: db down")

	for i := 1; i < health.DefaultStreakThreshold; i++ {
		h.noteReconcile(ctx, cmd, errMon, sameErr)
	}
	h.noteReconcile(ctx, cmd, errMon, nil) // success resets

	for i := 1; i < health.DefaultStreakThreshold; i++ {
		h.noteReconcile(ctx, cmd, errMon, sameErr)
	}
	if countErrorLoops(store) != 0 {
		t.Fatalf("expected the success to have reset the streak (no event yet), got %d", countErrorLoops(store))
	}
	h.noteReconcile(ctx, cmd, errMon, sameErr)
	if countErrorLoops(store) != 1 {
		t.Fatalf("expected exactly one event after the post-reset streak re-crossed, got %d", countErrorLoops(store))
	}
}

func countErrorLoops(store *fakeScoutEventStore) int {
	n := 0
	for _, e := range store.recorded {
		if e.Type == captain.EventCoordinatorErrorLoop {
			n++
		}
	}
	return n
}
