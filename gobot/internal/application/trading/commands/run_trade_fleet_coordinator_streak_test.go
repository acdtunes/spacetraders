package commands

import (
	"context"
	"errors"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/health"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

// streakCaptureRecorder captures the error-loop events a coordinator emits so the
// streak-breach rollout (sp-6wxq) is observable without a real captain outbox.
type streakCaptureRecorder struct{ events []*captain.Event }

func (r *streakCaptureRecorder) Record(_ context.Context, e *captain.Event) error {
	r.events = append(r.events, e)
	return nil
}

// TestTradeFleetStreak_ReconcileFailsRepeatedly_EmitsErrorLoopEvent pins the
// sp-6wxq wiring at the trade-fleet reconcile checkpoint: a pass that fails with
// the identical error for DefaultStreakThreshold consecutive ticks crosses the
// streak exactly once and emits one interrupt-class coordinator error-loop event
// (the s88 silent-stuck class — e.g. a launcher never wired — made visible),
// while a below-threshold run stays silent.
func TestTradeFleetStreak_ReconcileFailsRepeatedly_EmitsErrorLoopEvent(t *testing.T) {
	rec := &streakCaptureRecorder{}
	h := newTradeHandler(&fakeTradeShipRepo{}, nil, clockAt(0))
	h.SetEventRecorder(rec)

	ctx := tradeCtx(&tradeCaptureLogger{})
	cmd := tradeCmd()
	errMon := health.NewMonitor(health.DefaultStreakThreshold)
	sameErr := errors.New("trade fleet coordinator: no tour launcher wired (call SetTourLauncher at startup)")

	// One short of the threshold: no event yet.
	for i := 1; i < health.DefaultStreakThreshold; i++ {
		h.noteReconcile(ctx, cmd, errMon, sameErr)
	}
	if len(rec.events) != 0 {
		t.Fatalf("expected no event before the streak threshold, got %d", len(rec.events))
	}

	// The threshold-th identical failure crosses and emits exactly one event.
	h.noteReconcile(ctx, cmd, errMon, sameErr)
	if len(rec.events) != 1 {
		t.Fatalf("expected exactly one error-loop event at the threshold, got %d", len(rec.events))
	}
	got := rec.events[0]
	if got.Type != captain.EventCoordinatorErrorLoop {
		t.Fatalf("expected type %q, got %q", captain.EventCoordinatorErrorLoop, got.Type)
	}
	if got.Ship != cmd.ContainerID {
		t.Fatalf("expected the event scoped to the coordinator container %q, got %q", cmd.ContainerID, got.Ship)
	}
}

// TestTradeFleetStreak_SuccessResetsStreak pins reset-on-success: a healthy
// (non-error) reconcile pass between failures restarts the streak, so an
// intermittent coordinator never falsely escalates to a captain event.
func TestTradeFleetStreak_SuccessResetsStreak(t *testing.T) {
	rec := &streakCaptureRecorder{}
	h := newTradeHandler(&fakeTradeShipRepo{}, nil, clockAt(0))
	h.SetEventRecorder(rec)

	ctx := tradeCtx(&tradeCaptureLogger{})
	cmd := tradeCmd()
	errMon := health.NewMonitor(health.DefaultStreakThreshold)
	sameErr := errors.New("failed to list ships for trade fleet reconcile: db down")

	// Accumulate one short of the threshold, then a success (nil err) resets it.
	for i := 1; i < health.DefaultStreakThreshold; i++ {
		h.noteReconcile(ctx, cmd, errMon, sameErr)
	}
	h.noteReconcile(ctx, cmd, errMon, nil)

	// A fresh run of failures must again reach the full threshold before emitting.
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
