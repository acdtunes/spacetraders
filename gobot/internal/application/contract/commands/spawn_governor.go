package commands

import (
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/health"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// The spawn governor is the coordinator-level guard against worker-spawn storms. The main loop
// spawns a worker, blocks on its completion, and on a failure event immediately re-selects the SAME
// idle hull and respawns, with nothing between the death and the respawn — so one poison hull
// hot-loops the coordinator and stalls the whole contract chain. An escalating per-hull backoff
// spaces out respawns, and after N deaths the hull is quarantined so the coordinator moves on to a
// healthy hull; the CONTRACT keeps being worked (RULINGS #1).
//
// TWO BREAKERS, BECAUSE TIMING ALONE IS BLIND. An instant-death breaker counting only deaths faster
// than a threshold has its streak RESET by anything slower, and a hull can fail the same way for
// minutes at a time — API retries with exponential backoff, then in-place container restarts — so
// it is never quarantined and, because nothing is ever "down", no recovery path fires. The second
// breaker is content-shaped: it counts consecutive IDENTICAL errors with no reference to elapsed
// time at all, because a hull failing the same way N times running is poison whether it dies in a
// second or ten minutes.
//
// QUARANTINE EXPIRES; IT IS A CIRCUIT BREAKER, NOT A BLACKLIST. A quarantine sticky for the
// coordinator's whole run needs a recreate to clear, so a hull whose upstream problem resolves
// never comes back. Both causes quarantine for an EXPIRING cooldown, after which the hull is
// re-probed with a single real worker: a probe that succeeds clears every scrap of streak state
// (full recovery, no human action), and a probe that fails re-quarantines IMMEDIATELY on the
// escalated cooldown rather than restarting the streak from zero — otherwise the recoverable
// quarantine is just a slower crash-loop.

// spawnIdenticalErrorThreshold is how many CONSECUTIVE completions carrying the
// SAME error message quarantine a hull, independent of how long each worker
// took to die. Three, matching spawnQuarantineThreshold so the governor has one
// number to reason about, and because identical evidence is far stronger than a
// bare failure count: two hulls failing for two different reasons is noise,
// while one hull reproducing one error three times running is a fact. It is
// deliberately LOWER than health.DefaultStreakThreshold (5) — that tracker
// watches a 10s retry loop where 5 crosses in under a minute, whereas one
// observation HERE costs a whole worker lifecycle (spawn → API retry storm →
// in-place restarts → death), minutes of a hull's earning time apiece.
const spawnIdenticalErrorThreshold = 3

// spawnQuarantineCooldownSchedule is how long a hull stays quarantined after its
// k-th quarantine (index 0 = the first). Escalating for the same reason the
// spawn backoff escalates: a hull that fails its re-probe has PROVEN it is still
// broken, so probing it again at the same rate would rebuild the crash-loop at a
// lower frequency instead of ending it. 15m is short enough that a hull fixed
// upstream (server-side state repaired, cargo cleared, pin removed) is back in
// service within one contract cycle unattended, and long enough that a still-
// broken hull costs ~1 wasted worker per 15m instead of the observed ~1400/h.
// Quarantines past the last entry reuse the last (longest) interval; a single
// successful worker resets the count so a recovered hull never inherits a long
// cooldown from its past.
var spawnQuarantineCooldownSchedule = []time.Duration{
	15 * time.Minute,
	30 * time.Minute,
	60 * time.Minute,
}

// The two reasons a hull is quarantined. These are the Prometheus `cause` label
// values AND the captain payload field, so the two very different failure
// signatures stay distinguishable downstream: an instant-death quarantine points
// at a hull that cannot even start work (bad class, bad local state), while a
// repeated-identical-error quarantine points at a hull that starts, grinds, and
// dies the same way every time (the TORWIND-5 unreadable-upstream signature).
const (
	quarantineCauseInstantDeath  = "instant_death"
	quarantineCauseRepeatedError = "repeated_identical_error"
)

// unspecifiedWorkerError is the streak key for a FAILED completion that carries
// no error text. It must NOT be treated as health.StreakTracker treats "" (a
// success that resets the streak): that would make the identical-error breaker
// silently blind exactly where the evidence is thinnest, and silence must never
// default to permissive on a guard. Two failures that both report nothing are,
// on the only evidence available, the same failure.
const unspecifiedWorkerError = "<unspecified>"

// spawnInstantDeathThreshold is how soon after its spawn a worker must fail to
// count as an "instant death". A worker that dies this fast never got far
// enough to do real work (a hull crashing on the first delivery check, a hull
// in a bad server/cache state) — the poison-hull signature. A worker that runs
// longer and then fails did real work first and is NOT the storm signature, so
// it resets the hull's instant-death streak rather than adding to it.
const spawnInstantDeathThreshold = 30 * time.Second

// spawnQuarantineThreshold is how many instant deaths one hull may suffer within
// spawnQuarantineWindow before it is quarantined: skipped until spawnQuarantineCooldownSchedule
// elapses, then re-probed. Low enough to shut down a genuine crash-loop fast, high enough to
// tolerate a hull that flaps once or twice for a transient reason.
const spawnQuarantineThreshold = 3

// spawnQuarantineWindow bounds how far apart instant deaths may be and still
// accumulate toward quarantine. Deaths spread wider than this are treated as a
// fresh streak — a hull that insta-died once, was skipped, and much later
// insta-dies again for an unrelated reason should not be quarantined off a
// stale count. Quarantine reflects a BURST of deaths (the storm), not a slow
// drip over hours.
const spawnQuarantineWindow = 10 * time.Minute

// spawnBackoffSchedule is how long a hull is held out of worker selection after its k-th
// consecutive instant death (index 0 = after the 1st). Escalating, so a flapping hull is retried
// with progressively more breathing room instead of hot-looping: even the first entries turn a
// burst of respawns into at most one spawn per interval. Deaths past the last entry reuse the last,
// longest interval; in practice quarantine caps the streak before the schedule is exhausted.
var spawnBackoffSchedule = []time.Duration{
	5 * time.Second,
	15 * time.Second,
	45 * time.Second,
}

// spawnGovernorConfig carries the governor's tunables so tests can drive it
// with short, deterministic durations while production uses the named-constant
// defaults (defaultSpawnGovernorConfig). Mirrors how health.NewMonitor
// takes its threshold as a parameter with health.DefaultStreakThreshold as
// the wired-in default.
type spawnGovernorConfig struct {
	InstantDeathThreshold   time.Duration
	QuarantineThreshold     int
	QuarantineWindow        time.Duration
	Backoff                 []time.Duration
	IdenticalErrorThreshold int
	QuarantineCooldown      []time.Duration
}

// defaultSpawnGovernorConfig returns the production configuration built from the
// named schedule constants above.
func defaultSpawnGovernorConfig() spawnGovernorConfig {
	return spawnGovernorConfig{
		InstantDeathThreshold:   spawnInstantDeathThreshold,
		QuarantineThreshold:     spawnQuarantineThreshold,
		QuarantineWindow:        spawnQuarantineWindow,
		Backoff:                 spawnBackoffSchedule,
		IdenticalErrorThreshold: spawnIdenticalErrorThreshold,
		QuarantineCooldown:      spawnQuarantineCooldownSchedule,
	}
}

// hullSpawnState is one hull's spawn/death history within a single coordinator
// run. All timing is measured against the injected clock.
type hullSpawnState struct {
	// spawnedAt / hasPending track the most recent worker spawned for this hull
	// so a later completion can be classified as instant-or-not by elapsed time.
	spawnedAt  time.Time
	hasPending bool

	// instantDeaths counts consecutive instant deaths in the current window;
	// windowStart is when that window opened (the first instant death of the
	// streak). A success or a non-instant death clears both.
	instantDeaths int
	windowStart   time.Time

	// errStreak counts CONSECUTIVE identical worker error messages with no reference to elapsed
	// time — the breaker that catches a hull dying slowly and identically forever. It reuses
	// health.StreakTracker rather than a third hand-rolled counter, because the semantics needed
	// here (identical increments, a different error resets to 1, a success resets to 0) are exactly
	// what it implements. Only its streak LENGTH is consumed: its `crossed` return re-fires on
	// every multiple of the threshold, which is right for re-alarming a stuck loop but wrong for a
	// LATCHING quarantine whose re-entry is governed by the cooldown and re-probe rules below.
	errStreak *health.StreakTracker

	// eligibleAt is the earliest time this hull may be spawned again (post-death
	// backoff). Zero means eligible now.
	eligibleAt time.Time

	// quarantineUntil is when the current quarantine expires; the hull is skipped by every
	// selection pass before it. Zero (or in the past) means not quarantined. EXPIRING rather than
	// sticky-for-the-run: a hull whose upstream problem clears must return to service with no
	// human action, so a quarantine is a circuit breaker, not a blacklist.
	//
	// quarantineCount is how many times this hull has been quarantined without an
	// intervening success — it indexes the escalating cooldown schedule so a hull
	// that keeps failing its re-probe is probed ever less often.
	//
	// quarantineCause is the last quarantine's cause (one of the
	// quarantineCause* constants), carried into the metric label, the captain
	// payload, and any re-quarantine from a failed re-probe.
	quarantineUntil time.Time
	quarantineCount int
	quarantineCause string

	// probing marks the ONE worker spawned for this hull immediately after a
	// quarantine expired. Its failure is not the start of a fresh streak — it is
	// positive proof the hull is still broken — so it re-quarantines on the spot.
	probing bool
}

// spawnGovernor tracks per-hull spawn/death history for one coordinator run and
// decides which hulls may be spawned now. It is NOT safe for concurrent use;
// the coordinator's main loop is single-goroutine, calling NoteSpawn /
// NoteCompletion / FilterEligible in sequence.
type spawnGovernor struct {
	clock shared.Clock
	cfg   spawnGovernorConfig
	hulls map[string]*hullSpawnState
}

// newSpawnGovernor returns a governor wired to the production defaults.
func newSpawnGovernor(clock shared.Clock) *spawnGovernor {
	return newSpawnGovernorWithConfig(clock, defaultSpawnGovernorConfig())
}

// newSpawnGovernorWithConfig returns a governor with an explicit config, for
// tests that need short durations.
func newSpawnGovernorWithConfig(clock shared.Clock, cfg spawnGovernorConfig) *spawnGovernor {
	return &spawnGovernor{
		clock: clock,
		cfg:   cfg,
		hulls: make(map[string]*hullSpawnState),
	}
}

// spawnOutcome reports what a NoteCompletion call concluded, so the coordinator
// can emit the one loud quarantine event exactly on the crossing.
type spawnOutcome struct {
	// InstantDeath is true when the completed worker failed within the
	// instant-death threshold of its spawn.
	InstantDeath bool
	// InstantDeaths is the hull's current consecutive instant-death count within
	// the window (after this completion is applied).
	InstantDeaths int
	// IdenticalErrors is the hull's current consecutive identical-error streak
	// (after this completion is applied), independent of elapsed time.
	IdenticalErrors int
	// Quarantined is true when the hull is quarantined (whether it crossed on
	// this call or was already quarantined).
	Quarantined bool
	// JustQuarantined is true only on the exact completion that crossed the hull
	// into quarantine — the coordinator emits its loud event on this edge. A
	// failed re-probe after a cooldown is a NEW edge and reports true again, so a
	// hull that is still broken resurfaces instead of alarming once and going
	// quiet; the escalating cooldown is what bounds that event rate.
	JustQuarantined bool
	// Cause names why the hull is quarantined (one of the quarantineCause*
	// constants), so the metric label and captain payload can tell an
	// instant-death poison hull from a slow, identically-failing one.
	Cause string
	// Cooldown is how long the quarantine just applied lasts — reported so the
	// loud line can state when the hull comes back rather than implying it is
	// gone for good.
	Cooldown time.Duration
}

// NoteSpawn records that a worker was just spawned for hull, timestamping it so
// the matching completion can be classified as instant-or-not. Called once per
// successful main-loop spawn.
func (g *spawnGovernor) NoteSpawn(hull string) {
	st := g.stateFor(hull)
	now := g.clock.Now()

	// RE-PROBE: the first worker spawned for the hull since a quarantine expired is a probe of a
	// hull we have POSITIVE evidence was broken, not an ordinary spawn. Clearing quarantineUntil
	// HERE rather than on expiry keeps the probe atomic with the release — exactly one worker gets
	// through per cooldown — and quarantineCount survives so a failed probe escalates instead of
	// restarting the ladder.
	if !st.quarantineUntil.IsZero() && !now.Before(st.quarantineUntil) {
		st.probing = true
		st.quarantineUntil = time.Time{}
	}

	st.spawnedAt = now
	st.hasPending = true
}

// NoteCompletion records the outcome of the worker most recently spawned for
// hull and updates the hull's backoff/quarantine state. errMsg is the failed
// worker's error text (WorkerCompletedEvent.Error), ignored on success.
//
//   - success: the hull is healthy — clear EVERY scrap of failure state, streaks
//     and quarantine ladder alike. A hull that delivers is not a poison hull, and
//     this is the recovery path that keeps quarantine from being a blacklist.
//   - failure on a post-quarantine re-probe: the hull is still broken — re-quarantine at once on
//     the escalated cooldown, never restart the streak from zero.
//   - failure within the instant-death threshold: an instant death — extend the
//     hull's backoff (escalating) and, if this is the Nth within the window,
//     quarantine it.
//   - failure after the threshold: the worker did real work before failing — not
//     the storm signature, so clear the instant-death streak (but do not grant a
//     backoff-free retry beyond the normal flow).
//
// The identical-error streak advances on EVERY failure regardless of which branch applies, because
// it is the breaker that catches what all the timing-shaped logic above is blind to: a hull that
// takes minutes to die, the same way, forever.
//
// A completion with no matching NoteSpawn (e.g. a re-adopted restart worker the
// governor never spawned) is a no-op: the governor only judges hulls it launched.
func (g *spawnGovernor) NoteCompletion(hull string, success bool, errMsg string) spawnOutcome {
	st := g.stateFor(hull)
	now := g.clock.Now()

	// Only classify completions for a worker this governor actually spawned.
	if !st.hasPending {
		return spawnOutcome{
			Quarantined:   g.isQuarantined(st, now),
			InstantDeaths: st.instantDeaths,
		}
	}
	elapsed := now.Sub(st.spawnedAt)
	st.hasPending = false
	probing := st.probing
	st.probing = false

	if success {
		// RECOVERY, NOT PAROLE: a delivered contract is proof the hull works, so
		// the quarantine ladder resets too. Without this a hull that recovered on
		// its 3rd probe would still carry a 60m cooldown into its next unrelated
		// hiccup.
		st.instantDeaths = 0
		st.windowStart = time.Time{}
		st.eligibleAt = time.Time{}
		st.errStreak.Note("")
		st.quarantineUntil = time.Time{}
		st.quarantineCount = 0
		st.quarantineCause = ""
		return spawnOutcome{}
	}

	// Content-shaped breaker, evaluated on every failure and deliberately BEFORE
	// any timing test — this is the one that had to work while elapsed time said
	// "this worker did real work".
	identicalErrors, _ := st.errStreak.Note(errStreakKey(errMsg))

	// Timing-shaped breaker: unchanged sp-lybx semantics.
	instantDeath := elapsed < g.cfg.InstantDeathThreshold
	if instantDeath {
		if st.instantDeaths == 0 || now.Sub(st.windowStart) > g.cfg.QuarantineWindow {
			st.windowStart = now
			st.instantDeaths = 1
		} else {
			st.instantDeaths++
		}
		st.eligibleAt = now.Add(g.backoffFor(st.instantDeaths))
	} else {
		// A worker that ran long enough to do real work before failing is not the
		// hot-respawn signature — reset the streak so slow, unrelated failures
		// never accrue toward THIS breaker (the identical-error one above still
		// sees them).
		st.instantDeaths = 0
		st.windowStart = time.Time{}
	}

	justQuarantined := false
	switch {
	case probing:
		// The re-probe failed: the hull is still broken. Re-quarantine on the spot
		// at the next rung of the cooldown ladder — restarting the streak from zero
		// here is precisely how a "recoverable" quarantine degrades into a slower
		// crash-loop. Carry the original cause: a failed probe is the same illness,
		// not a new one.
		g.quarantine(st, now, st.quarantineCause)
		justQuarantined = true
	case g.isQuarantined(st, now):
		// A death while still quarantined (a worker the governor would not have
		// selected) neither re-alarms nor extends the cooldown.
	case instantDeath && st.instantDeaths >= g.cfg.QuarantineThreshold:
		g.quarantine(st, now, quarantineCauseInstantDeath)
		justQuarantined = true
	case g.cfg.IdenticalErrorThreshold > 0 && identicalErrors >= g.cfg.IdenticalErrorThreshold:
		g.quarantine(st, now, quarantineCauseRepeatedError)
		justQuarantined = true
	}

	out := spawnOutcome{
		InstantDeath:    instantDeath,
		InstantDeaths:   st.instantDeaths,
		IdenticalErrors: identicalErrors,
		Quarantined:     g.isQuarantined(st, now),
		JustQuarantined: justQuarantined,
	}
	if justQuarantined {
		out.Cause = st.quarantineCause
		out.Cooldown = st.quarantineUntil.Sub(now)
	}
	return out
}

// errStreakKey maps a failed worker's error text onto the key the identical-error
// streak counts. Exact match, no normalisation: collapsing distinct errors that
// merely look alike would quarantine a hull that is actually flapping between
// unrelated transients, whereas failing to collapse two spellings of one error
// only DELAYS quarantine — and the instant-death breaker is the other net.
func errStreakKey(errMsg string) string {
	if errMsg == "" {
		return unspecifiedWorkerError
	}
	return errMsg
}

// quarantine latches the hull out of selection until the k-th rung of the
// cooldown ladder elapses. cause is carried for the metric label and the captain
// payload; it is structurally non-empty at every call site (a re-quarantine
// inherits the cause of the quarantine that produced its probe), and the fallback
// only exists so a future caller can never emit a blank metric label.
func (g *spawnGovernor) quarantine(st *hullSpawnState, now time.Time, cause string) {
	if cause == "" {
		cause = quarantineCauseRepeatedError
	}
	st.quarantineCount++
	st.quarantineCause = cause
	st.quarantineUntil = now.Add(g.cooldownFor(st.quarantineCount))
}

// isQuarantined reports whether the hull's quarantine is still in force at now.
// An expired quarantine reads false: the hull is released for its re-probe.
func (g *spawnGovernor) isQuarantined(st *hullSpawnState, now time.Time) bool {
	return !st.quarantineUntil.IsZero() && now.Before(st.quarantineUntil)
}

// Eligible reports whether hull may be spawned right now: not quarantined and
// past any post-death backoff. A hull with no history is eligible.
func (g *spawnGovernor) Eligible(hull string) bool {
	st, ok := g.hulls[hull]
	if !ok {
		return true
	}
	now := g.clock.Now()
	if g.isQuarantined(st, now) {
		return false
	}
	return !now.Before(st.eligibleAt)
}

// FilterEligible partitions candidate symbols into those spawnable now and
// those currently held (in backoff or quarantined), preserving order. The held
// list lets the caller log honestly why a candidate was skipped.
func (g *spawnGovernor) FilterEligible(symbols []string) (eligible, held []string) {
	for _, s := range symbols {
		if g.Eligible(s) {
			eligible = append(eligible, s)
		} else {
			held = append(held, s)
		}
	}
	return eligible, held
}

// Quarantined reports whether hull is currently quarantined. False once the
// cooldown expires — at which point the hull is released for a single re-probe.
func (g *spawnGovernor) Quarantined(hull string) bool {
	st, ok := g.hulls[hull]
	return ok && g.isQuarantined(st, g.clock.Now())
}

// backoffFor returns the backoff interval after the streak-th consecutive
// instant death (streak counts from 1). Streaks past the schedule reuse the
// last (longest) interval.
func (g *spawnGovernor) backoffFor(streak int) time.Duration {
	if len(g.cfg.Backoff) == 0 {
		return 0
	}
	idx := streak - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(g.cfg.Backoff) {
		idx = len(g.cfg.Backoff) - 1
	}
	return g.cfg.Backoff[idx]
}

// cooldownFor returns how long the count-th quarantine of a hull lasts (count
// from 1). Mirrors backoffFor exactly — same clamp, same "past the schedule
// reuses the longest rung" rule — so the governor has one shape of escalating
// ladder, not two. An empty schedule yields 0, which makes a quarantine expire
// immediately; that is a config error, not a production path.
func (g *spawnGovernor) cooldownFor(count int) time.Duration {
	if len(g.cfg.QuarantineCooldown) == 0 {
		return 0
	}
	idx := count - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(g.cfg.QuarantineCooldown) {
		idx = len(g.cfg.QuarantineCooldown) - 1
	}
	return g.cfg.QuarantineCooldown[idx]
}

func (g *spawnGovernor) stateFor(hull string) *hullSpawnState {
	st, ok := g.hulls[hull]
	if !ok {
		st = &hullSpawnState{errStreak: health.NewStreakTracker(g.cfg.IdenticalErrorThreshold)}
		g.hulls[hull] = st
	}
	return st
}
