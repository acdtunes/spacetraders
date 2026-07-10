package commands

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// A worker that dies within the instant-death threshold of its spawn extends
// the hull's backoff: the hull is UNSELECTABLE until the (escalating) interval
// elapses, then selectable again. This is the primitive that turns the sp-lybx
// storm (4 spawns in 9s) into at most one spawn per interval.
func TestSpawnGovernor_InstantDeath_HoldsHullForEscalatingBackoff(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	gov := newSpawnGovernor(clock)

	// Death #1: spawn, die 1s later (instant), backoff = schedule[0].
	gov.NoteSpawn("TORWIND-24")
	clock.Advance(1 * time.Second)
	out := gov.NoteCompletion("TORWIND-24", false)
	if !out.InstantDeath || out.InstantDeaths != 1 || out.Quarantined {
		t.Fatalf("first instant death: got %+v, want InstantDeath=true InstantDeaths=1 Quarantined=false", out)
	}
	if gov.Eligible("TORWIND-24") {
		t.Fatalf("hull must be held immediately after an instant death (backoff not yet elapsed)")
	}
	// Just before the backoff elapses it stays held; exactly at the boundary it
	// becomes selectable again.
	clock.Advance(spawnBackoffSchedule[0] - time.Second)
	if gov.Eligible("TORWIND-24") {
		t.Fatalf("hull must stay held until the full schedule[0] backoff elapses")
	}
	clock.Advance(1 * time.Second)
	if !gov.Eligible("TORWIND-24") {
		t.Fatalf("hull must be selectable again once schedule[0] backoff has elapsed")
	}

	// Death #2: the SECOND instant death must hold the hull for the LONGER
	// schedule[1] interval (escalation) - "dies instantly twice → third spawn
	// waits the backoff".
	gov.NoteSpawn("TORWIND-24")
	clock.Advance(1 * time.Second)
	out = gov.NoteCompletion("TORWIND-24", false)
	if out.InstantDeaths != 2 || out.Quarantined {
		t.Fatalf("second instant death: got %+v, want InstantDeaths=2 Quarantined=false", out)
	}
	clock.Advance(spawnBackoffSchedule[0]) // schedule[0] < schedule[1], so still held
	if gov.Eligible("TORWIND-24") {
		t.Fatalf("after the 2nd instant death the hull must wait the longer schedule[1] backoff, not schedule[0]")
	}
	clock.Advance(spawnBackoffSchedule[1] - spawnBackoffSchedule[0])
	if !gov.Eligible("TORWIND-24") {
		t.Fatalf("hull must be selectable once the escalated schedule[1] backoff elapses")
	}
}

// After N instant deaths within the window the hull is quarantined: skipped for
// the rest of the run, the crossing reported exactly once (JustQuarantined), and
// a healthy hull alongside it still proceeds - the contract keeps being worked.
func TestSpawnGovernor_NInstantDeaths_QuarantinesHullAndSparesHealthy(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	gov := newSpawnGovernor(clock)

	var last spawnOutcome
	for i := 0; i < spawnQuarantineThreshold; i++ {
		gov.NoteSpawn("TORWIND-24")
		clock.Advance(1 * time.Second)
		last = gov.NoteCompletion("TORWIND-24", false)
		// Advance past whatever backoff was set so the next spawn is allowed.
		clock.Advance(spawnBackoffSchedule[len(spawnBackoffSchedule)-1])
	}

	if !last.JustQuarantined {
		t.Fatalf("the Nth (N=%d) instant death must report JustQuarantined, got %+v", spawnQuarantineThreshold, last)
	}
	if last.InstantDeaths != spawnQuarantineThreshold {
		t.Fatalf("expected InstantDeaths=%d at the quarantine crossing, got %d", spawnQuarantineThreshold, last.InstantDeaths)
	}
	if !gov.Quarantined("TORWIND-24") || gov.Eligible("TORWIND-24") {
		t.Fatalf("a quarantined hull must be permanently ineligible for the rest of the run")
	}

	// The next selection pass over [poison, healthy] must surface the healthy
	// hull and hold only the poison one - the coordinator moves on, contract
	// still progresses (RULINGS #1).
	eligible, held := gov.FilterEligible([]string{"TORWIND-24", "TORWIND-29"})
	if len(eligible) != 1 || eligible[0] != "TORWIND-29" {
		t.Fatalf("expected the healthy hull TORWIND-29 to remain spawnable, got eligible=%v", eligible)
	}
	if len(held) != 1 || held[0] != "TORWIND-24" {
		t.Fatalf("expected only the quarantined hull TORWIND-24 held, got held=%v", held)
	}
}

// A subsequent completion of an already-quarantined hull must NOT re-report the
// crossing: the loud line + captain event fire exactly once (edge-triggered).
func TestSpawnGovernor_Quarantine_ReportedOnlyOnce(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	gov := newSpawnGovernor(clock)

	for i := 0; i < spawnQuarantineThreshold; i++ {
		gov.NoteSpawn("TORWIND-24")
		clock.Advance(1 * time.Second)
		gov.NoteCompletion("TORWIND-24", false)
		clock.Advance(spawnBackoffSchedule[len(spawnBackoffSchedule)-1])
	}

	// One more instant death on the already-quarantined hull.
	gov.NoteSpawn("TORWIND-24")
	clock.Advance(1 * time.Second)
	out := gov.NoteCompletion("TORWIND-24", false)
	if out.JustQuarantined {
		t.Fatalf("quarantine must be edge-triggered - a repeat death must not re-report JustQuarantined, got %+v", out)
	}
	if !out.Quarantined {
		t.Fatalf("the hull must still read as quarantined on repeat deaths, got %+v", out)
	}
}

// A successful worker completion clears the hull's instant-death streak and any
// backoff: a hull that delivers is healthy, immediately selectable again, and
// its history cannot later push it toward quarantine.
func TestSpawnGovernor_Success_ClearsStreakAndBackoff(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	gov := newSpawnGovernor(clock)

	gov.NoteSpawn("TORWIND-29")
	clock.Advance(1 * time.Second)
	gov.NoteCompletion("TORWIND-29", false) // one instant death → backoff
	clock.Advance(spawnBackoffSchedule[0])  // let it become eligible

	gov.NoteSpawn("TORWIND-29")
	clock.Advance(1 * time.Second)
	out := gov.NoteCompletion("TORWIND-29", true) // success clears everything
	if out.InstantDeath || out.Quarantined {
		t.Fatalf("a success must report neither an instant death nor quarantine, got %+v", out)
	}
	if !gov.Eligible("TORWIND-29") {
		t.Fatalf("a hull that just delivered must be immediately selectable")
	}

	// A fresh instant death now starts a NEW streak at 1 (the prior death did
	// not carry over), so the hull is nowhere near quarantine.
	gov.NoteSpawn("TORWIND-29")
	clock.Advance(1 * time.Second)
	out = gov.NoteCompletion("TORWIND-29", false)
	if out.InstantDeaths != 1 || out.Quarantined {
		t.Fatalf("a success must reset the streak so the next instant death starts at 1, got %+v", out)
	}
}

// Regression / byte-identical healthy flow: a hull with no history, and a hull
// that only ever succeeds, are always eligible and never held - the governor is
// inert for healthy hulls.
func TestSpawnGovernor_HealthyHulls_NeverHeld(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	gov := newSpawnGovernor(clock)

	// No history at all.
	if !gov.Eligible("TORWIND-3") {
		t.Fatalf("a hull the governor has never seen must be eligible")
	}

	// A successful spawn/complete cycle leaves it eligible.
	gov.NoteSpawn("TORWIND-3")
	clock.Advance(5 * time.Minute)
	gov.NoteCompletion("TORWIND-3", true)

	eligible, held := gov.FilterEligible([]string{"TORWIND-3", "TORWIND-29"})
	if len(held) != 0 {
		t.Fatalf("healthy hulls must never be held, got held=%v", held)
	}
	if len(eligible) != 2 {
		t.Fatalf("expected both healthy hulls spawnable, got eligible=%v", eligible)
	}
}

// A worker that runs past the instant-death threshold before failing did real
// work first - it is NOT the hot-respawn signature, so it neither backs the hull
// off nor accumulates toward quarantine, no matter how many times it happens.
func TestSpawnGovernor_SlowDeaths_NeverBackoffOrQuarantine(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	gov := newSpawnGovernor(clock)

	for i := 0; i < spawnQuarantineThreshold+2; i++ {
		gov.NoteSpawn("TORWIND-29")
		clock.Advance(spawnInstantDeathThreshold + time.Second) // ran long enough to be "real work"
		out := gov.NoteCompletion("TORWIND-29", false)
		if out.InstantDeath {
			t.Fatalf("a failure after the instant-death threshold must not count as an instant death, got %+v", out)
		}
		if !gov.Eligible("TORWIND-29") {
			t.Fatalf("a slow death must not put the hull in backoff - it did real work")
		}
	}
	if gov.Quarantined("TORWIND-29") {
		t.Fatalf("slow deaths must never quarantine a hull, no matter how many")
	}
}

// Instant deaths spread wider than the window do not accumulate: quarantine
// reflects a BURST of deaths (the storm), not a slow drip over hours.
func TestSpawnGovernor_DeathsBeyondWindow_StartFreshStreak(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	gov := newSpawnGovernor(clock)

	// Two instant deaths inside the window.
	for i := 0; i < 2; i++ {
		gov.NoteSpawn("TORWIND-24")
		clock.Advance(1 * time.Second)
		gov.NoteCompletion("TORWIND-24", false)
		clock.Advance(spawnBackoffSchedule[len(spawnBackoffSchedule)-1])
	}

	// Now jump well past the window before the next instant death.
	clock.Advance(spawnQuarantineWindow + time.Minute)
	gov.NoteSpawn("TORWIND-24")
	clock.Advance(1 * time.Second)
	out := gov.NoteCompletion("TORWIND-24", false)
	if out.InstantDeaths != 1 {
		t.Fatalf("an instant death beyond the window must start a fresh streak at 1, got %d", out.InstantDeaths)
	}
	if out.Quarantined {
		t.Fatalf("stale out-of-window deaths must not push a hull into quarantine, got %+v", out)
	}
}

// A completion with no matching spawn (e.g. a re-adopted restart worker the
// governor never launched) is a no-op: the governor only judges hulls it spawned.
func TestSpawnGovernor_CompletionWithoutSpawn_IsNoOp(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	gov := newSpawnGovernor(clock)

	out := gov.NoteCompletion("TORWIND-99", false)
	if out.InstantDeath || out.Quarantined || out.JustQuarantined {
		t.Fatalf("a completion for an unspawned hull must be inert, got %+v", out)
	}
	if !gov.Eligible("TORWIND-99") {
		t.Fatalf("an unspawned hull must remain eligible")
	}
}

// The one loud captain event carries the hull, the count, a human message, and
// the interrupt-class coordinator.error_loop type (Ship stays container-scoped).
func TestBuildHullQuarantineEvent_CarriesHullCountAndMessage(t *testing.T) {
	event := buildHullQuarantineEvent("fleet-coordinator-1", 7, "TORWIND-24", 3)

	if event.Type != captain.EventCoordinatorErrorLoop {
		t.Fatalf("expected interrupt-class coordinator.error_loop type, got %q", event.Type)
	}
	if event.Ship != "fleet-coordinator-1" {
		t.Fatalf("Ship must stay container-scoped (the coordinator's own id), got %q", event.Ship)
	}
	if event.PlayerID != 7 {
		t.Fatalf("expected player 7, got %d", event.PlayerID)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
		t.Fatalf("payload must be valid JSON: %v", err)
	}
	if payload["hull"] != "TORWIND-24" {
		t.Fatalf("payload must name the hull, got %v", payload["hull"])
	}
	if payload["checkpoint"] != "hull_quarantine" {
		t.Fatalf("payload must tag the checkpoint, got %v", payload["checkpoint"])
	}
	if deaths, _ := payload["instant_deaths"].(float64); int(deaths) != 3 {
		t.Fatalf("payload must carry the instant-death count, got %v", payload["instant_deaths"])
	}
	msg, _ := payload["message"].(string)
	if !strings.Contains(msg, "TORWIND-24") || !strings.Contains(msg, "quarantined") {
		t.Fatalf("payload message must name the hull and the quarantine, got %q", msg)
	}
}
