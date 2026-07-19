package health

// EffectTracker detects a coordinator that keeps DETECTING actionable
// candidates (there is work to do) yet takes ZERO effect-actions, sustained
// over a threshold of consecutive ticks — the "dry-run survived a day"
// pathology: the loop is RUNNING and error-free, so the error-streak monitor
// stays quiet, but it never reaches its EFFECT. It is the inert-loop sibling of
// StreakTracker's stuck-erroring loop; the two together cover both ways a
// coordinator can look alive while doing nothing.
//
// It differs from StreakTracker in two deliberate ways:
//
//   - the bad condition is candidates-but-zero-effect, not a repeated error.
//     "desired" is the count of actions the coordinator DECIDED to take this
//     tick (net of legitimate holds — cooldowns, caps, satisfied portfolio);
//     "effected" is how many it actually carried out. desired>0 && effected==0
//     is the pathology. Passing the post-hold "desired" (not raw candidates)
//     is what keeps the check off a correctly-idle loop whose candidates are
//     all legitimately blocked.
//
//   - it fires ONCE per continuous no-effect episode (state-change dedup),
//     re-arming only after an intervening productive or idle tick — so a
//     genuinely stuck coordinator alarms once, not every N ticks. A recovery
//     re-arms the one-shot, so a later fresh inert episode alarms again.
type EffectTracker struct {
	threshold int
	streak    int
	fired     bool
}

// NewEffectTracker returns a tracker that warns once per no-effect episode
// after threshold consecutive candidates-but-zero-effect ticks. threshold <= 0
// disables warning entirely (the RULINGS #5 disable escape) — Observe still
// tracks the streak, but warn is always false.
func NewEffectTracker(threshold int) *EffectTracker {
	return &EffectTracker{threshold: threshold}
}

// Observe records one tick's (desired, effected) tallies and reports the
// current no-effect streak and whether THIS call should emit the single
// per-episode WARN. warn is true exactly once per continuous no-effect
// episode, on the tick the streak first reaches the threshold.
//
// A productive tick (effected > 0) or an idle tick (desired == 0 — nothing
// actionable, a healthy coordinator) resets the streak to zero and re-arms the
// one-shot, closing the episode.
func (t *EffectTracker) Observe(desired, effected int) (streak int, warn bool) {
	noEffect := desired > 0 && effected == 0
	if !noEffect {
		t.streak = 0
		t.fired = false
		return 0, false
	}
	t.streak++
	warn = t.threshold > 0 && t.streak >= t.threshold && !t.fired
	if warn {
		t.fired = true
	}
	return t.streak, warn
}
