package commands

import (
	"context"
	"errors"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/health"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

// TestRebalancerStreak_ReconcileFailsRepeatedly_EmitsErrorLoopEvent pins the
// sp-6wxq wiring at the worker-rebalancer reconcile checkpoint: a pass that fails
// with the identical error for DefaultStreakThreshold consecutive ticks crosses
// exactly once and emits one interrupt-class coordinator error-loop event, while a
// below-threshold run stays silent.
func TestRebalancerStreak_ReconcileFailsRepeatedly_EmitsErrorLoopEvent(t *testing.T) {
	rec := &streakCaptureRecorder{}
	h := NewRunWorkerRebalancerCoordinatorHandler(nil, nil, nil, nil, clockAt(0))
	h.SetEventRecorder(rec)

	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	cmd := rebalancerTestCmd()
	errMon := health.NewMonitor(health.DefaultStreakThreshold)
	sameErr := errors.New("worker rebalancer: no gate graph wired")

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

// TestRebalancerStreak_SuccessResetsStreak pins reset-on-success: a healthy pass
// between failures restarts the streak, so an intermittent coordinator never
// falsely escalates to a captain event.
func TestRebalancerStreak_SuccessResetsStreak(t *testing.T) {
	rec := &streakCaptureRecorder{}
	h := NewRunWorkerRebalancerCoordinatorHandler(nil, nil, nil, nil, clockAt(0))
	h.SetEventRecorder(rec)

	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	cmd := rebalancerTestCmd()
	errMon := health.NewMonitor(health.DefaultStreakThreshold)
	sameErr := errors.New("failed to list ships for rebalance: db down")

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
