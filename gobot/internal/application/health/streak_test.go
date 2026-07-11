package health

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

// These tests pin sp-e2l1: a coordinator retry loop that repeats the exact
// same error forever must become observable after N consecutive occurrences,
// exactly once per streak-length (edge-triggered) — not once per iteration,
// and never falsely on an intermittent/recovering loop. See the 2026-07-05
// negotiate-nil incident report: 18h of identical errors, zero events.

// TestErrorStreakTracker_BelowThreshold_NeverCrosses pins that a streak
// shorter than the threshold never reports a crossing — no premature alarm.
func TestErrorStreakTracker_BelowThreshold_NeverCrosses(t *testing.T) {
	tr := NewStreakTracker(3)
	for i := 1; i <= 2; i++ {
		streak, crossed := tr.Note("boom")
		if crossed {
			t.Fatalf("iteration %d: must not cross before threshold, got crossed=true", i)
		}
		if streak != i {
			t.Fatalf("iteration %d: expected streak %d, got %d", i, i, streak)
		}
	}
}

// TestErrorStreakTracker_AtThreshold_CrossesExactlyOnce pins the core
// edge-triggered contract: of N consecutive identical errors, only the Nth
// call reports crossed=true.
func TestErrorStreakTracker_AtThreshold_CrossesExactlyOnce(t *testing.T) {
	const threshold = 3
	tr := NewStreakTracker(threshold)

	var crossings int
	for i := 1; i <= threshold; i++ {
		streak, crossed := tr.Note("boom")
		if streak != i {
			t.Fatalf("iteration %d: expected streak %d, got %d", i, i, streak)
		}
		wantCrossed := i == threshold
		if crossed != wantCrossed {
			t.Fatalf("iteration %d: expected crossed=%v, got %v", i, wantCrossed, crossed)
		}
		if crossed {
			crossings++
		}
	}
	if crossings != 1 {
		t.Fatalf("expected exactly one crossing at threshold, got %d", crossings)
	}
}

// TestErrorStreakTracker_PeriodicReCross_AtEveryMultiple pins that a streak
// still stuck after the first alarm re-alarms at every further multiple of
// the threshold (2N, 3N, ...), so a coordinator that never recovers keeps
// resurfacing instead of going silent again after the first event.
func TestErrorStreakTracker_PeriodicReCross_AtEveryMultiple(t *testing.T) {
	const threshold = 3
	tr := NewStreakTracker(threshold)

	var crossedAt []int
	for i := 1; i <= threshold*3; i++ {
		_, crossed := tr.Note("boom")
		if crossed {
			crossedAt = append(crossedAt, i)
		}
	}
	want := []int{threshold, threshold * 2, threshold * 3}
	if len(crossedAt) != len(want) {
		t.Fatalf("expected crossings at %v, got %v", want, crossedAt)
	}
	for i, w := range want {
		if crossedAt[i] != w {
			t.Fatalf("expected crossings at %v, got %v", want, crossedAt)
		}
	}
}

// TestErrorStreakTracker_DifferentError_ResetsStreak pins that a change in
// error text — not just any failure — restarts the streak: two different
// bugs firing alternately must not sum into one false alarm.
func TestErrorStreakTracker_DifferentError_ResetsStreak(t *testing.T) {
	tr := NewStreakTracker(3)
	tr.Note("error A")
	tr.Note("error A")

	streak, crossed := tr.Note("error B")
	if streak != 1 {
		t.Fatalf("a different error must restart the streak at 1, got %d", streak)
	}
	if crossed {
		t.Fatalf("a freshly restarted streak must not cross")
	}
}

// TestErrorStreakTracker_Success_ResetsStreak pins "an intermittent/
// recovering loop does not falsely alarm": a success between two occurrences
// of the identical error must prevent them from being counted as
// consecutive, even if the loop later fails with the same message again.
func TestErrorStreakTracker_Success_ResetsStreak(t *testing.T) {
	const threshold = 3
	tr := NewStreakTracker(threshold)
	tr.Note("boom")
	tr.Note("boom")

	streak, crossed := tr.Note("") // success
	if streak != 0 || crossed {
		t.Fatalf("success must reset streak to 0 with no crossing, got streak=%d crossed=%v", streak, crossed)
	}

	// The same error recurring after a recovery starts a fresh streak, not
	// a continuation of the pre-recovery count.
	streak, crossed = tr.Note("boom")
	if streak != 1 {
		t.Fatalf("error after a recovery must restart streak at 1, got %d", streak)
	}
	if crossed {
		t.Fatalf("a freshly restarted streak must not cross")
	}
}

// TestErrorStreakTracker_ZeroThreshold_NeverCrosses pins that a non-positive
// threshold disables alarming entirely (still tracks the streak count, but
// never reports a crossing) rather than crossing on every call.
func TestErrorStreakTracker_ZeroThreshold_NeverCrosses(t *testing.T) {
	tr := NewStreakTracker(0)
	for i := 1; i <= 10; i++ {
		if _, crossed := tr.Note("boom"); crossed {
			t.Fatalf("iteration %d: zero threshold must never cross", i)
		}
	}
}

// TestCoordinatorErrorMonitor_IndependentSites pins that each named
// checkpoint gets its own independent streak: a busy checkpoint crossing its
// threshold must not be masked or contaminated by a different checkpoint's
// successes or failures within the same loop iteration.
func TestCoordinatorErrorMonitor_IndependentSites(t *testing.T) {
	const threshold = 3
	mon := NewMonitor(threshold)

	// Site A fails every iteration; site B succeeds every iteration in
	// between. Interleave them the way a single Handle() loop iteration
	// would: both checkpoints get Noted every pass.
	var aCrossed, bCrossed int
	for i := 1; i <= threshold; i++ {
		if _, crossed := mon.Note("site_a", "boom"); crossed {
			aCrossed++
		}
		if _, crossed := mon.Note("site_b", ""); crossed {
			bCrossed++
		}
	}

	if aCrossed != 1 {
		t.Fatalf("expected site_a to cross exactly once, got %d", aCrossed)
	}
	if bCrossed != 0 {
		t.Fatalf("expected site_b (always succeeding) to never cross, got %d", bCrossed)
	}
}

// TestCoordinatorErrorMonitor_SuccessAtOneSiteDoesNotMaskAnother pins that
// resetting one checkpoint on success has no effect on a different
// checkpoint's in-progress streak — they must not share state.
func TestCoordinatorErrorMonitor_SuccessAtOneSiteDoesNotMaskAnother(t *testing.T) {
	const threshold = 3
	mon := NewMonitor(threshold)

	mon.Note("negotiate_contract", "boom")
	mon.Note("negotiate_contract", "boom")
	// An unrelated checkpoint succeeding must not touch negotiate_contract's streak.
	mon.Note("find_idle_haulers", "")

	streak, crossed := mon.Note("negotiate_contract", "boom")
	if streak != 3 {
		t.Fatalf("expected negotiate_contract streak to reach 3 uninterrupted, got %d", streak)
	}
	if !crossed {
		t.Fatalf("expected negotiate_contract to cross at streak 3")
	}
}

// TestBuildErrorLoopEvent_PopulatesFields pins the shape of the captain
// event recorded when a checkpoint's error streak crosses: it must be
// interrupt-visible (EventCoordinatorErrorLoop), scoped to the coordinator's
// own container, and carry enough payload detail (which checkpoint, the
// exact error, the streak length) for the watchkeeper/captain to act on
// without re-deriving it from logs.
func TestBuildErrorLoopEvent_PopulatesFields(t *testing.T) {
	cause := errors.New("failed to negotiate: API returned nil result or contract")
	event := NewErrorLoopEvent("contract_fleet_coordinator-player-1-abc123", 42, "negotiate_contract", cause, 5)

	if event.Type != captain.EventCoordinatorErrorLoop {
		t.Fatalf("expected type %q, got %q", captain.EventCoordinatorErrorLoop, event.Type)
	}
	if event.Ship != "contract_fleet_coordinator-player-1-abc123" {
		t.Fatalf("expected Ship to carry the container id, got %q", event.Ship)
	}
	if event.PlayerID != 42 {
		t.Fatalf("expected PlayerID 42, got %d", event.PlayerID)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
		t.Fatalf("payload must be valid JSON: %v", err)
	}
	if payload["checkpoint"] != "negotiate_contract" {
		t.Fatalf("expected payload checkpoint=negotiate_contract, got %v", payload["checkpoint"])
	}
	if payload["error"] != cause.Error() {
		t.Fatalf("expected payload error=%q, got %v", cause.Error(), payload["error"])
	}
	if streak, ok := payload["streak"].(float64); !ok || streak != 5 {
		t.Fatalf("expected payload streak=5, got %v", payload["streak"])
	}
	if payload["container_id"] != "contract_fleet_coordinator-player-1-abc123" {
		t.Fatalf("expected payload container_id to match, got %v", payload["container_id"])
	}
}
