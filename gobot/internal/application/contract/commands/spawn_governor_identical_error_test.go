package commands

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// torwind5Error is the failure signature from the sp-20eyn outage: hull
// TORWIND-5 was unreadable upstream, so every worker spawned for it burned the
// full API retry ladder on ship-state reload and then died with the SAME text.
const torwind5Error = `failed to reload ship state: API error (status 404): {"error":{"code":3000,"message":"Ship TORWIND-5 not found."}}`

// slowDeath drives one full spawn→die cycle for hull with the given error, taking
// FAR longer than the instant-death threshold — the prod shape: 10 API retries at
// exponential backoff capped at 30s, then three in-place ContainerRunner restarts
// at 5s/30s/120s. Every death here is invisible to the timing-shaped breaker.
func slowDeath(gov *spawnGovernor, clock *shared.MockClock, hull, errMsg string) spawnOutcome {
	gov.NoteSpawn(hull)
	clock.Advance(4 * time.Minute)
	return gov.NoteCompletion(hull, false, errMsg)
}

// THE PROD DEFECT. A hull that fails N times in a row with the SAME error is
// quarantined even though every single worker took MINUTES to die — i.e. every
// death was classified "did real work" by the instant-death breaker and reset its
// streak to zero. Before sp-20eyn this loop ran 34,279 times without ever
// quarantining TORWIND-5, and the agent earned nothing for 24h.
//
// This test MUST fail against the unmodified governor.
func TestSpawnGovernor_RepeatedIdenticalErrorSlowDeaths_QuarantineHull(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	gov := newSpawnGovernor(clock)

	var last spawnOutcome
	for i := 0; i < spawnIdenticalErrorThreshold; i++ {
		last = slowDeath(gov, clock, "TORWIND-5", torwind5Error)

		// Every death is slow, so the timing-shaped breaker must stay at zero
		// throughout — proving the quarantine below came from the CONTENT breaker
		// and not accidentally from the pre-existing instant-death path.
		if last.InstantDeath || last.InstantDeaths != 0 {
			t.Fatalf("death %d took 4m and must not register as an instant death, got %+v", i+1, last)
		}
		if last.IdenticalErrors != i+1 {
			t.Fatalf("death %d must advance the identical-error streak to %d, got %d", i+1, i+1, last.IdenticalErrors)
		}
	}

	if !last.JustQuarantined {
		t.Fatalf("the %dth consecutive identical error must quarantine the hull, got %+v", spawnIdenticalErrorThreshold, last)
	}
	if last.Cause != quarantineCauseRepeatedError {
		t.Fatalf("expected cause %q, got %q", quarantineCauseRepeatedError, last.Cause)
	}
	if last.Cooldown != spawnQuarantineCooldownSchedule[0] {
		t.Fatalf("the first quarantine must use the first cooldown rung %s, got %s", spawnQuarantineCooldownSchedule[0], last.Cooldown)
	}
	if !gov.Quarantined("TORWIND-5") || gov.Eligible("TORWIND-5") {
		t.Fatalf("the poison hull must be held out of selection while quarantined")
	}
}

// The quarantine is scoped to the BAD hull, not the fleet: the coordinator keeps
// selecting every other hull and the contract keeps being worked (RULINGS #1).
// A healthy hull that fails once with the same error mid-storm is unaffected —
// the streak is per-hull, not global.
func TestSpawnGovernor_IdenticalErrorQuarantine_IsScopedToTheBadHull(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	gov := newSpawnGovernor(clock)

	for i := 0; i < spawnIdenticalErrorThreshold; i++ {
		slowDeath(gov, clock, "TORWIND-5", torwind5Error)
		// A sibling hull suffers the identical error ONCE in the same period.
		slowDeath(gov, clock, "TORWIND-8", torwind5Error)
		if i == 0 {
			// ...and then delivers, clearing its streak, while TORWIND-5 keeps failing.
			gov.NoteSpawn("TORWIND-8")
			clock.Advance(2 * time.Minute)
			gov.NoteCompletion("TORWIND-8", true, "")
		}
	}

	eligible, held := gov.FilterEligible([]string{"TORWIND-5", "TORWIND-8", "TORWIND-24"})
	if len(held) != 1 || held[0] != "TORWIND-5" {
		t.Fatalf("only the poison hull may be held, got held=%v", held)
	}
	if len(eligible) != 2 || eligible[0] != "TORWIND-8" || eligible[1] != "TORWIND-24" {
		t.Fatalf("every other hull must remain spawnable, got eligible=%v", eligible)
	}
}

// RECOVERY, NOT BLACKLIST. Once the cooldown elapses the hull is released for a
// single re-probe with no human action; a probe that succeeds returns it to full
// service and wipes every scrap of streak state, so its history can never push it
// back toward quarantine.
func TestSpawnGovernor_QuarantineExpires_AndASuccessfulReprobeFullyRestoresTheHull(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	gov := newSpawnGovernor(clock)

	for i := 0; i < spawnIdenticalErrorThreshold; i++ {
		slowDeath(gov, clock, "TORWIND-5", torwind5Error)
	}
	if !gov.Quarantined("TORWIND-5") {
		t.Fatalf("precondition: the hull must be quarantined")
	}

	// Still held one instant before expiry; released the instant it elapses.
	clock.Advance(spawnQuarantineCooldownSchedule[0] - time.Second)
	if gov.Eligible("TORWIND-5") {
		t.Fatalf("the hull must stay held until the full cooldown elapses")
	}
	clock.Advance(time.Second)
	if !gov.Eligible("TORWIND-5") || gov.Quarantined("TORWIND-5") {
		t.Fatalf("an expired quarantine must release the hull for a re-probe — a quarantine is not a blacklist")
	}

	// The re-probe succeeds: the hull was fixed upstream.
	gov.NoteSpawn("TORWIND-5")
	clock.Advance(3 * time.Minute)
	out := gov.NoteCompletion("TORWIND-5", true, "")
	if out.Quarantined || out.JustQuarantined {
		t.Fatalf("a successful re-probe must clear the quarantine outright, got %+v", out)
	}

	// Full restoration: a single fresh failure starts a NEW streak at 1 and the
	// hull is nowhere near quarantine again.
	next := slowDeath(gov, clock, "TORWIND-5", torwind5Error)
	if next.IdenticalErrors != 1 || next.Quarantined {
		t.Fatalf("a recovered hull must start a fresh streak at 1, got %+v", next)
	}
}

// A FAILED re-probe must re-quarantine IMMEDIATELY on the next (longer) cooldown
// rung, not restart the streak from zero. Restarting the streak would mean the
// hull gets N more full worker lifecycles per cooldown forever — a slow
// crash-loop wearing a circuit breaker's clothes.
func TestSpawnGovernor_FailedReprobe_ReQuarantinesImmediatelyAndEscalates(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	gov := newSpawnGovernor(clock)

	for i := 0; i < spawnIdenticalErrorThreshold; i++ {
		slowDeath(gov, clock, "TORWIND-5", torwind5Error)
	}
	clock.Advance(spawnQuarantineCooldownSchedule[0])

	// ONE failing worker — a single death, far short of the threshold — must be
	// enough, because the hull already has a proven record.
	out := slowDeath(gov, clock, "TORWIND-5", torwind5Error)
	if !out.JustQuarantined {
		t.Fatalf("a failed re-probe must re-quarantine on the spot, got %+v", out)
	}
	if out.Cause != quarantineCauseRepeatedError {
		t.Fatalf("a re-quarantine must carry the original cause %q, got %q", quarantineCauseRepeatedError, out.Cause)
	}
	if out.Cooldown != spawnQuarantineCooldownSchedule[1] {
		t.Fatalf("the 2nd quarantine must escalate to %s, got %s", spawnQuarantineCooldownSchedule[1], out.Cooldown)
	}
	if !gov.Quarantined("TORWIND-5") {
		t.Fatalf("the hull must be held again immediately after the failed probe")
	}

	// A third round escalates again, and past the schedule it pins at the longest
	// rung rather than growing without bound.
	for _, want := range []time.Duration{
		spawnQuarantineCooldownSchedule[2],
		spawnQuarantineCooldownSchedule[len(spawnQuarantineCooldownSchedule)-1],
	} {
		clock.Advance(out.Cooldown)
		out = slowDeath(gov, clock, "TORWIND-5", torwind5Error)
		if !out.JustQuarantined || out.Cooldown != want {
			t.Fatalf("expected a re-quarantine with cooldown %s, got %+v", want, out)
		}
	}
}

// The re-probe verdict must NOT depend on the probe reproducing the original
// evidence. A hull fresh out of quarantine that fails its very first worker has
// not demonstrated recovery — whatever it failed with, and whichever breaker put
// it there. Letting a differently-worded (or differently-timed) probe failure
// restart the streak from zero is exactly how a recoverable quarantine decays
// into a slow crash-loop: the hull would earn N more full worker lifetimes per
// cooldown, forever.
//
// This is the test that pins the re-probe branch itself. The
// same-error case cannot: there the accumulated identical-error streak
// re-quarantines the hull on its own, so the branch appears covered while being
// inert.
func TestSpawnGovernor_FailedReprobeWithADifferentError_StillReQuarantines(t *testing.T) {
	cases := []struct {
		name string
		// arrange drives the hull into its FIRST quarantine and returns the cause
		// that quarantine should carry.
		arrange func(gov *spawnGovernor, clock *shared.MockClock) string
	}{
		{
			name: "quarantined for repeated identical errors",
			arrange: func(gov *spawnGovernor, clock *shared.MockClock) string {
				for i := 0; i < spawnIdenticalErrorThreshold; i++ {
					slowDeath(gov, clock, "TORWIND-5", torwind5Error)
				}
				return quarantineCauseRepeatedError
			},
		},
		{
			name: "quarantined for instant deaths",
			arrange: func(gov *spawnGovernor, clock *shared.MockClock) string {
				for i := 0; i < spawnQuarantineThreshold; i++ {
					gov.NoteSpawn("TORWIND-5")
					clock.Advance(time.Second)
					gov.NoteCompletion("TORWIND-5", false, fmt.Sprintf("boom %d", i))
					clock.Advance(spawnBackoffSchedule[len(spawnBackoffSchedule)-1])
				}
				return quarantineCauseInstantDeath
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clock := &shared.MockClock{CurrentTime: time.Now()}
			gov := newSpawnGovernor(clock)

			wantCause := tc.arrange(gov, clock)
			if !gov.Quarantined("TORWIND-5") {
				t.Fatalf("precondition: the hull must be quarantined")
			}
			clock.Advance(spawnQuarantineCooldownSchedule[0])
			if !gov.Eligible("TORWIND-5") {
				t.Fatalf("precondition: the cooldown must have released the hull for a re-probe")
			}

			// The probe fails SLOWLY and with an error nothing has seen before —
			// every streak the governor tracks reads 1 or 0 after this completion.
			out := slowDeath(gov, clock, "TORWIND-5", "some entirely unrelated failure")

			if !out.JustQuarantined {
				t.Fatalf("a failed re-probe must re-quarantine even with a brand-new error, got %+v", out)
			}
			if out.Cause != wantCause {
				t.Fatalf("a re-quarantine must carry the original cause %q, got %q", wantCause, out.Cause)
			}
			if out.Cooldown != spawnQuarantineCooldownSchedule[1] {
				t.Fatalf("the re-quarantine must escalate to %s, got %s", spawnQuarantineCooldownSchedule[1], out.Cooldown)
			}
			if !gov.Quarantined("TORWIND-5") || gov.Eligible("TORWIND-5") {
				t.Fatalf("the hull must be held again immediately after a failed probe")
			}
		})
	}
}

// The breaker is REPEATED-IDENTICAL, not a plain failure counter: a hull
// alternating between two distinct errors is flapping, not stuck, and must never
// be quarantined by this path no matter how long it goes on. Every failure here
// is slow, so the timing-shaped breaker is out of the picture.
func TestSpawnGovernor_AlternatingErrors_NeverTripTheIdenticalBreaker(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	gov := newSpawnGovernor(clock)

	errs := []string{"market closed", "insufficient funds"}
	for i := 0; i < 4*spawnIdenticalErrorThreshold; i++ {
		out := slowDeath(gov, clock, "TORWIND-29", errs[i%len(errs)])
		if out.IdenticalErrors != 1 {
			t.Fatalf("iteration %d: a different error must reset the streak to 1, got %d", i, out.IdenticalErrors)
		}
		if out.Quarantined || out.JustQuarantined {
			t.Fatalf("iteration %d: alternating errors must never quarantine, got %+v", i, out)
		}
	}
}

// A FAILED completion carrying no error text must still feed the streak. Treating
// it the way health.StreakTracker treats "" (a success that resets the count)
// would make the breaker silently blind exactly where the evidence is thinnest —
// silence defaulting to permissive on a guard.
func TestSpawnGovernor_EmptyErrorOnFailure_StillCountsTowardTheStreak(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	gov := newSpawnGovernor(clock)

	var last spawnOutcome
	for i := 0; i < spawnIdenticalErrorThreshold; i++ {
		last = slowDeath(gov, clock, "TORWIND-5", "")
		if last.IdenticalErrors != i+1 {
			t.Fatalf("an unreported error must still advance the streak: want %d, got %d", i+1, last.IdenticalErrors)
		}
	}
	if !last.JustQuarantined || last.Cause != quarantineCauseRepeatedError {
		t.Fatalf("repeated unreported failures must quarantine like any other repeated identical failure, got %+v", last)
	}
}

// A hull's identical-error streak is its own: two hulls failing once each with
// the same text are two isolated blips, never a shared streak.
func TestSpawnGovernor_IdenticalErrorStreak_IsPerHull(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	gov := newSpawnGovernor(clock)

	for i := 0; i < 2*spawnIdenticalErrorThreshold; i++ {
		hull := fmt.Sprintf("TORWIND-%d", i)
		out := slowDeath(gov, clock, hull, torwind5Error)
		if out.IdenticalErrors != 1 || out.Quarantined {
			t.Fatalf("hull %s: each hull tracks its own streak, got %+v", hull, out)
		}
	}
}

// The quarantine counter fires from a path that is ALREADY handling a worker
// failure, so a metrics miss must never panic it (RULINGS #4). With no global
// collector installed — the daemon's own metrics-disabled boot, and every test
// in this package — the shim must be a silent no-op for both cause labels.
func TestRecordHullQuarantine_IsInertWithoutACollector(t *testing.T) {
	for _, cause := range []string{quarantineCauseInstantDeath, quarantineCauseRepeatedError} {
		require.NotPanics(t, func() { metrics.RecordHullQuarantine(cause) }, "cause %q", cause)
	}
}

// The loud captain event distinguishes the two causes, so a reader can tell a
// hull that cannot start work from one the API cannot read — different remedies.
func TestBuildHullQuarantineEvent_DistinguishesRepeatedErrorCause(t *testing.T) {
	event := buildHullQuarantineEvent("fleet-coordinator-1", 7, "TORWIND-5", spawnOutcome{
		IdenticalErrors: spawnIdenticalErrorThreshold,
		Cause:           quarantineCauseRepeatedError,
		Cooldown:        spawnQuarantineCooldownSchedule[0],
	})

	var payload map[string]any
	if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
		t.Fatalf("payload must be valid JSON: %v", err)
	}
	if payload["cause"] != quarantineCauseRepeatedError {
		t.Fatalf("payload must name the cause, got %v", payload["cause"])
	}
	if n, _ := payload["identical_errors"].(float64); int(n) != spawnIdenticalErrorThreshold {
		t.Fatalf("payload must carry the identical-error count, got %v", payload["identical_errors"])
	}
	if secs, _ := payload["cooldown_seconds"].(float64); secs != spawnQuarantineCooldownSchedule[0].Seconds() {
		t.Fatalf("payload must carry the cooldown so nobody reads a quarantine as permanent, got %v", payload["cooldown_seconds"])
	}
	msg, _ := payload["message"].(string)
	if !strings.Contains(msg, "TORWIND-5") || !strings.Contains(msg, "SAME error") {
		t.Fatalf("the message must name the hull and the repeated-error evidence, got %q", msg)
	}
}
