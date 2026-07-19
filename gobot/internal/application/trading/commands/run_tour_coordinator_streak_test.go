package commands

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/health"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

// TestTourStreak_BudgetUnreadableRepeatedly_EmitsErrorLoopEvent pins the wiring at
// the tour's dynamic-budget resolve checkpoint (the fail-closed pause+continue — the
// one unbounded silent retry in this worker-shaped coordinator):
// an unreadable live treasury for DefaultStreakThreshold consecutive iterations
// crosses exactly once and emits one interrupt-class coordinator error-loop event,
// while a below-threshold run stays silent.
func TestTourStreak_BudgetUnreadableRepeatedly_EmitsErrorLoopEvent(t *testing.T) {
	rec := &streakCaptureRecorder{}
	h := &RunTourCoordinatorHandler{captainEvents: rec}

	ctx := context.Background()
	cmd := &RunTourCoordinatorCommand{PlayerID: 1, ContainerID: "tour-run-abc"}
	budgetMon := health.NewMonitor(health.DefaultStreakThreshold)

	for i := 1; i < health.DefaultStreakThreshold; i++ {
		h.noteTourBudget(ctx, cmd, budgetMon, true)
	}
	if len(rec.events) != 0 {
		t.Fatalf("expected no event before the streak threshold, got %d", len(rec.events))
	}

	h.noteTourBudget(ctx, cmd, budgetMon, true)
	if len(rec.events) != 1 {
		t.Fatalf("expected exactly one error-loop event at the threshold, got %d", len(rec.events))
	}
	if got := rec.events[0]; got.Type != captain.EventCoordinatorErrorLoop || got.Ship != cmd.ContainerID {
		t.Fatalf("expected an error-loop event scoped to %q, got type=%q ship=%q", cmd.ContainerID, got.Type, got.Ship)
	}
}

// TestTourStreak_ReadableResolveResetsStreak pins reset-on-success: a readable
// dynamic-budget resolve between unreadable iterations restarts the streak, so a
// treasury that only flickers unreadable never falsely escalates to a captain event.
func TestTourStreak_ReadableResolveResetsStreak(t *testing.T) {
	rec := &streakCaptureRecorder{}
	h := &RunTourCoordinatorHandler{captainEvents: rec}

	ctx := context.Background()
	cmd := &RunTourCoordinatorCommand{PlayerID: 1, ContainerID: "tour-run-abc"}
	budgetMon := health.NewMonitor(health.DefaultStreakThreshold)

	for i := 1; i < health.DefaultStreakThreshold; i++ {
		h.noteTourBudget(ctx, cmd, budgetMon, true)
	}
	h.noteTourBudget(ctx, cmd, budgetMon, false) // readable resolve resets

	for i := 1; i < health.DefaultStreakThreshold; i++ {
		h.noteTourBudget(ctx, cmd, budgetMon, true)
	}
	if len(rec.events) != 0 {
		t.Fatalf("expected the readable resolve to have reset the streak (no event yet), got %d", len(rec.events))
	}
	h.noteTourBudget(ctx, cmd, budgetMon, true)
	if len(rec.events) != 1 {
		t.Fatalf("expected exactly one event after the post-reset streak re-crossed, got %d", len(rec.events))
	}
}
