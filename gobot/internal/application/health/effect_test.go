package health

import "testing"

// These tests pin the effect-tracking contract: a coordinator that keeps
// DETECTING actionable candidates (there IS work to do) yet takes ZERO
// effect-actions, sustained over N consecutive ticks, is effectively inert but
// looks alive — the "dry-run survived a day" class (verified RUNNING at
// deploy, never at its EFFECT). It must WARN exactly ONCE per continuous
// no-effect episode, never on a healthy loop that is simply idle (nothing to
// do) or productive.

// TestEffectTracker_NoEffectOverThreshold_WarnsExactlyOnce pins the core
// edge-triggered contract: of N consecutive candidates-but-zero-effect ticks,
// only the Nth reports warn=true, and a still-inert loop past N never repeats
// within the same episode (state-change dedup, NOT a per-tick or every-multiple
// re-alarm).
func TestEffectTracker_NoEffectOverThreshold_WarnsExactlyOnce(t *testing.T) {
	const threshold = 3
	tr := NewEffectTracker(threshold)

	var warns int
	for i := 1; i <= threshold*3; i++ {
		streak, warn := tr.Observe(2, 0) // 2 actionable candidates, 0 effect-actions
		if streak != i {
			t.Fatalf("tick %d: expected streak %d, got %d", i, i, streak)
		}
		if warn {
			warns++
			if i != threshold {
				t.Fatalf("expected the single warn on tick %d, got it on tick %d", threshold, i)
			}
		}
	}
	if warns != 1 {
		t.Fatalf("a sustained no-effect episode must WARN exactly once, got %d", warns)
	}
}

// TestEffectTracker_EffectPresent_NeverWarns pins that a coordinator taking
// real effect-actions every tick — even with a large candidate backlog — is
// healthy and never warns.
func TestEffectTracker_EffectPresent_NeverWarns(t *testing.T) {
	tr := NewEffectTracker(3)
	for i := 1; i <= 10; i++ {
		if streak, warn := tr.Observe(5, 1); warn || streak != 0 {
			t.Fatalf("tick %d: a productive tick must reset (streak 0) and never warn, got streak=%d warn=%v", i, streak, warn)
		}
	}
}

// TestEffectTracker_IdleTickNeverWarns pins that having NOTHING actionable
// (desired==0) is a healthy idle, not the pathology: zero candidates never
// counts toward the no-effect streak, no matter how long it persists. This is
// what keeps the check off a correctly-quiet coordinator (all candidates
// legitimately cooling down / capped / satisfied).
func TestEffectTracker_IdleTickNeverWarns(t *testing.T) {
	tr := NewEffectTracker(3)
	for i := 1; i <= 10; i++ {
		if streak, warn := tr.Observe(0, 0); warn || streak != 0 {
			t.Fatalf("tick %d: an idle tick (no candidates) must not warn, got streak=%d warn=%v", i, streak, warn)
		}
	}
}

// TestEffectTracker_BelowThreshold_NeverWarns pins no premature alarm: a
// no-effect streak shorter than the threshold stays silent.
func TestEffectTracker_BelowThreshold_NeverWarns(t *testing.T) {
	const threshold = 5
	tr := NewEffectTracker(threshold)
	for i := 1; i < threshold; i++ {
		if _, warn := tr.Observe(1, 0); warn {
			t.Fatalf("tick %d: must not warn before threshold %d", i, threshold)
		}
	}
}

// TestEffectTracker_ProductiveTickResetsAndRearms pins the two-sided reset:
// a productive tick mid-streak restarts the count (so a briefly-inert then
// recovering loop never alarms), AND after a full episode HAS warned, a
// productive tick re-arms the one-shot so a genuinely NEW inert episode later
// warns again (never silent forever after the first).
func TestEffectTracker_ProductiveTickResetsAndRearms(t *testing.T) {
	const threshold = 3
	tr := NewEffectTracker(threshold)

	// Two inert ticks (below threshold), then a productive tick resets.
	tr.Observe(1, 0)
	tr.Observe(1, 0)
	if streak, warn := tr.Observe(1, 2); warn || streak != 0 {
		t.Fatalf("a productive tick must reset the streak, got streak=%d warn=%v", streak, warn)
	}

	// A fresh no-effect episode counts from 1 and warns once at the threshold.
	var warns int
	for i := 1; i <= threshold; i++ {
		if _, warn := tr.Observe(1, 0); warn {
			warns++
		}
	}
	if warns != 1 {
		t.Fatalf("a fresh episode after a reset must warn once, got %d", warns)
	}

	// Recovery re-arms: another productive tick, then another sustained inert
	// episode must warn AGAIN — the check never goes permanently silent.
	tr.Observe(1, 3)
	warns = 0
	for i := 1; i <= threshold; i++ {
		if _, warn := tr.Observe(1, 0); warn {
			warns++
		}
	}
	if warns != 1 {
		t.Fatalf("a second inert episode after recovery must warn again, got %d", warns)
	}
}

// TestEffectTracker_AlreadyWarnedEpisode_NoRepeatUntilReset pins state-change
// dedup at the boundary: once an episode has warned, it stays silent across
// every subsequent inert tick until an intervening productive/idle tick
// closes the episode — it does not re-warn at 2N, 3N like the error streak.
func TestEffectTracker_AlreadyWarnedEpisode_NoRepeatUntilReset(t *testing.T) {
	const threshold = 2
	tr := NewEffectTracker(threshold)

	warnsBeforeReset := 0
	for i := 1; i <= threshold*4; i++ {
		if _, warn := tr.Observe(1, 0); warn {
			warnsBeforeReset++
		}
	}
	if warnsBeforeReset != 1 {
		t.Fatalf("an unbroken inert episode must warn once, not at every multiple, got %d", warnsBeforeReset)
	}
}

// TestEffectTracker_ZeroThreshold_Disabled pins the disable escape (RULINGS
// #5): a non-positive threshold never warns, however long the loop stays
// inert.
func TestEffectTracker_ZeroThreshold_Disabled(t *testing.T) {
	tr := NewEffectTracker(0)
	for i := 1; i <= 10; i++ {
		if _, warn := tr.Observe(3, 0); warn {
			t.Fatalf("tick %d: a zero threshold must disable warning entirely", i)
		}
	}
}
