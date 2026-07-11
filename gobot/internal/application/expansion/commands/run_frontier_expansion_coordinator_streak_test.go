package commands

import (
	"context"
	"errors"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/health"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// frontierStreakRecorder captures the error-loop events the coordinator emits so the
// sp-6wxq streak wiring is observable without a real captain outbox.
type frontierStreakRecorder struct{ events []*captain.Event }

func (r *frontierStreakRecorder) Record(_ context.Context, e *captain.Event) error {
	r.events = append(r.events, e)
	return nil
}

// TestFrontierStreak_ReconcileFailsRepeatedly_EmitsErrorLoopEvent pins the sp-6wxq
// wiring at the frontier-expansion reconcile checkpoint: a pass that fails with the
// identical error for DefaultStreakThreshold consecutive ticks crosses exactly once
// and emits one interrupt-class coordinator error-loop event, while a below-threshold
// run stays silent.
func TestFrontierStreak_ReconcileFailsRepeatedly_EmitsErrorLoopEvent(t *testing.T) {
	rec := &frontierStreakRecorder{}
	h := NewRunFrontierExpansionCoordinatorHandler(nil, nil, nil, &shared.MockClock{})
	h.SetEventRecorder(rec)

	ctx := context.Background()
	cmd := testCmd()
	errMon := health.NewMonitor(health.DefaultStreakThreshold)
	sameErr := errors.New("failed to list posts for frontier coverage: db down")

	for i := 1; i < health.DefaultStreakThreshold; i++ {
		h.noteReconcile(ctx, cmd, errMon, sameErr)
	}
	if len(rec.events) != 0 {
		t.Fatalf("expected no event before the streak threshold, got %d", len(rec.events))
	}

	h.noteReconcile(ctx, cmd, errMon, sameErr)
	if len(rec.events) != 1 {
		t.Fatalf("expected exactly one error-loop event at the threshold, got %d", len(rec.events))
	}
	if got := rec.events[0]; got.Type != captain.EventCoordinatorErrorLoop || got.Ship != cmd.ContainerID {
		t.Fatalf("expected an error-loop event scoped to %q, got type=%q ship=%q", cmd.ContainerID, got.Type, got.Ship)
	}
}

// TestFrontierStreak_SuccessResetsStreak pins reset-on-success: a healthy pass between
// failures restarts the streak, so an intermittent coordinator never falsely escalates.
func TestFrontierStreak_SuccessResetsStreak(t *testing.T) {
	rec := &frontierStreakRecorder{}
	h := NewRunFrontierExpansionCoordinatorHandler(nil, nil, nil, &shared.MockClock{})
	h.SetEventRecorder(rec)

	ctx := context.Background()
	cmd := testCmd()
	errMon := health.NewMonitor(health.DefaultStreakThreshold)
	sameErr := errors.New("failed to list posts for frontier coverage: db down")

	for i := 1; i < health.DefaultStreakThreshold; i++ {
		h.noteReconcile(ctx, cmd, errMon, sameErr)
	}
	h.noteReconcile(ctx, cmd, errMon, nil) // success resets

	for i := 1; i < health.DefaultStreakThreshold; i++ {
		h.noteReconcile(ctx, cmd, errMon, sameErr)
	}
	if len(rec.events) != 0 {
		t.Fatalf("expected the success to have reset the streak (no event yet), got %d", len(rec.events))
	}
	h.noteReconcile(ctx, cmd, errMon, sameErr)
	if len(rec.events) != 1 {
		t.Fatalf("expected exactly one event after the post-reset streak re-crossed, got %d", len(rec.events))
	}
}
