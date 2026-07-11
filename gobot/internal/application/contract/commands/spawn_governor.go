package commands

import (
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// The spawn governor is the coordinator-level guard against worker-spawn
// storms (sp-lybx): the contract coordinator persisted 4 worker containers for
// one hull within 9 seconds, each dying instantly ('deliveries not complete' —
// a 0-cargo probe can never deliver). The main loop spawns a worker, blocks on
// its completion, and on a failure event immediately re-selects the SAME idle
// hull and respawns — with nothing between the death and the respawn. A single
// poison hull therefore hot-loops the coordinator, stalling the whole contract
// chain (income.stalled caught the 2026-07-10 incident 30+ min in).
//
// Fix A (hull-class exclusion in FindIdleShipsByFleet) stops the SPECIFIC
// 0-cargo cause at discovery. This governor is the generic net for ANY
// instant-death cause on a cargo-carrying hull: an escalating per-hull backoff
// spaces out respawns, and after N instant deaths within a window the hull is
// quarantined for the rest of the run so the coordinator moves on to a healthy
// hull (the CONTRACT keeps being worked — RULINGS #1).

// spawnInstantDeathThreshold is how soon after its spawn a worker must fail to
// count as an "instant death". A worker that dies this fast never got far
// enough to do real work (a hull crashing on the first delivery check, a hull
// in a bad server/cache state) — the poison-hull signature. A worker that runs
// longer and then fails did real work first and is NOT the storm signature, so
// it resets the hull's instant-death streak rather than adding to it.
const spawnInstantDeathThreshold = 30 * time.Second

// spawnQuarantineThreshold is how many instant deaths one hull may suffer
// within spawnQuarantineWindow before it is quarantined (skipped for the rest
// of the coordinator run). Three tolerates a hull that flaps once or twice for
// a transient reason while still shutting down a genuine crash-loop fast.
const spawnQuarantineThreshold = 3

// spawnQuarantineWindow bounds how far apart instant deaths may be and still
// accumulate toward quarantine. Deaths spread wider than this are treated as a
// fresh streak — a hull that insta-died once, was skipped, and much later
// insta-dies again for an unrelated reason should not be quarantined off a
// stale count. Quarantine reflects a BURST of deaths (the storm), not a slow
// drip over hours.
const spawnQuarantineWindow = 10 * time.Minute

// spawnBackoffSchedule is how long a hull is held out of worker selection after
// its k-th consecutive instant death (index 0 = after the 1st death). Escalating
// so a flapping hull is retried with progressively more breathing room instead
// of hot-looping — the 5s→15s spacing alone turns the sp-lybx "4 spawns in 9s"
// into at most one spawn per interval. Deaths past the last entry reuse the last
// (longest) interval; in practice quarantine caps the streak at
// spawnQuarantineThreshold before the schedule is exhausted.
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
	InstantDeathThreshold time.Duration
	QuarantineThreshold   int
	QuarantineWindow      time.Duration
	Backoff               []time.Duration
}

// defaultSpawnGovernorConfig returns the production configuration built from the
// named schedule constants above.
func defaultSpawnGovernorConfig() spawnGovernorConfig {
	return spawnGovernorConfig{
		InstantDeathThreshold: spawnInstantDeathThreshold,
		QuarantineThreshold:   spawnQuarantineThreshold,
		QuarantineWindow:      spawnQuarantineWindow,
		Backoff:               spawnBackoffSchedule,
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

	// eligibleAt is the earliest time this hull may be spawned again (post-death
	// backoff). Zero means eligible now.
	eligibleAt time.Time

	// quarantined is sticky for the coordinator run once set: the hull is skipped
	// for every remaining selection pass. A coordinator recreate/restart builds a
	// fresh governor (this state is intentionally in-memory only), which clears
	// the quarantine — acceptable because the hull may have been fixed
	// (reclassified, repaired, unpinned) in the meantime, and re-observing the
	// storm re-quarantines it cheaply.
	quarantined bool
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
	// Quarantined is true when the hull is quarantined (whether it crossed on
	// this call or was already quarantined).
	Quarantined bool
	// JustQuarantined is true only on the exact completion that crossed the hull
	// into quarantine — the coordinator emits its single loud event on this edge.
	JustQuarantined bool
}

// NoteSpawn records that a worker was just spawned for hull, timestamping it so
// the matching completion can be classified as instant-or-not. Called once per
// successful main-loop spawn.
func (g *spawnGovernor) NoteSpawn(hull string) {
	st := g.stateFor(hull)
	st.spawnedAt = g.clock.Now()
	st.hasPending = true
}

// NoteCompletion records the outcome of the worker most recently spawned for
// hull and updates the hull's backoff/quarantine state.
//
//   - success: the hull is healthy — clear its instant-death streak and any
//     backoff. A hull that delivers is not a poison hull.
//   - failure within the instant-death threshold: an instant death — extend the
//     hull's backoff (escalating) and, if this is the Nth within the window,
//     quarantine it.
//   - failure after the threshold: the worker did real work before failing —
//     not the storm signature, so clear the instant-death streak (but do not
//     grant a backoff-free retry beyond the normal flow).
//
// A completion with no matching NoteSpawn (e.g. a re-adopted restart worker the
// governor never spawned) is a no-op: the governor only judges hulls it launched.
func (g *spawnGovernor) NoteCompletion(hull string, success bool) spawnOutcome {
	st := g.stateFor(hull)

	// Only classify completions for a worker this governor actually spawned.
	if !st.hasPending {
		return spawnOutcome{Quarantined: st.quarantined, InstantDeaths: st.instantDeaths}
	}
	elapsed := g.clock.Now().Sub(st.spawnedAt)
	st.hasPending = false

	if success {
		st.instantDeaths = 0
		st.windowStart = time.Time{}
		st.eligibleAt = time.Time{}
		return spawnOutcome{}
	}

	if elapsed >= g.cfg.InstantDeathThreshold {
		// A worker that ran long enough to do real work before failing is not the
		// hot-respawn signature — reset the streak so slow, unrelated failures
		// never accrue toward quarantine.
		st.instantDeaths = 0
		st.windowStart = time.Time{}
		return spawnOutcome{}
	}

	now := g.clock.Now()
	if st.instantDeaths == 0 || now.Sub(st.windowStart) > g.cfg.QuarantineWindow {
		st.windowStart = now
		st.instantDeaths = 1
	} else {
		st.instantDeaths++
	}
	st.eligibleAt = now.Add(g.backoffFor(st.instantDeaths))

	justQuarantined := false
	if !st.quarantined && st.instantDeaths >= g.cfg.QuarantineThreshold {
		st.quarantined = true
		justQuarantined = true
	}

	return spawnOutcome{
		InstantDeath:    true,
		InstantDeaths:   st.instantDeaths,
		Quarantined:     st.quarantined,
		JustQuarantined: justQuarantined,
	}
}

// Eligible reports whether hull may be spawned right now: not quarantined and
// past any post-death backoff. A hull with no history is eligible.
func (g *spawnGovernor) Eligible(hull string) bool {
	st, ok := g.hulls[hull]
	if !ok {
		return true
	}
	if st.quarantined {
		return false
	}
	return !g.clock.Now().Before(st.eligibleAt)
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

// Quarantined reports whether hull is quarantined for the rest of this run.
func (g *spawnGovernor) Quarantined(hull string) bool {
	st, ok := g.hulls[hull]
	return ok && st.quarantined
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

func (g *spawnGovernor) stateFor(hull string) *hullSpawnState {
	st, ok := g.hulls[hull]
	if !ok {
		st = &hullSpawnState{}
		g.hulls[hull] = st
	}
	return st
}
