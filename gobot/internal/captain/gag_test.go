package watchkeeper

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

// gagEvents returns the supervisor-gag audit events currently queued for the
// player, so tests can assert the edge-triggered event was recorded (and how
// many times) independent of any other synthetic events a tick might emit.
func gagEvents(t *testing.T, s *captainStores) []*captain.Event {
	t.Helper()
	all, err := s.store.FindUnprocessed(context.Background(), s.playerID, 100)
	require.NoError(t, err)
	out := make([]*captain.Event, 0, len(all))
	for _, e := range all {
		if e.Type == captain.EventSupervisorGagged {
			out = append(out, e)
		}
	}
	return out
}

// Acceptance (outer loop): the SAME running supervisor stands down the instant
// the gag config says gag, and resumes the instant it clears — driven by two
// ticks around a LIVE config flip written to the very state file the supervisor
// re-reads each tick, with NO restart in between. This is the core requirement:
// runtime-togglable, re-read live each tick, effective within one tick.
func TestGagStandsDownThenResumesOnLiveConfigFlip(t *testing.T) {
	sup, s, gw := newBridgeSupervisor(t)
	now := time.Now()
	sup.lastSession = now // heartbeat nowhere near due; the event drives the wake
	recordEvent(t, s, captain.EventWorkflowFailed)

	// GAG ON — written to disk, re-read by the supervisor on its next tick.
	require.NoError(t, SaveGagPolicy(sup.statePath, GagPolicy{Gagged: true, GagReason: "admiral halt"}))

	ran, err := sup.Tick(context.Background(), now)
	require.NoError(t, err)
	require.False(t, ran, "gagged: the wake-eval loop stands down, no session this tick")
	require.Empty(t, gw.mails, "gagged: spawns no captain session")
	require.Empty(t, gw.nudges, "gagged: takes no corrective action")
	require.False(t, sup.ws.Disabled(), "the soft gag never trips the DISABLED hard halt — the process stays live")

	left, err := s.store.FindUnprocessed(context.Background(), s.playerID, 10)
	require.NoError(t, err)
	require.NotEmpty(t, left, "the wake-worthy event is not consumed while gagged; it waits for resume")

	// GAG OFF — same process, no restart. The next tick must resume normally.
	require.NoError(t, SaveGagPolicy(sup.statePath, GagPolicy{Gagged: false}))

	ran, err = sup.Tick(context.Background(), now.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, ran, "ungagged: normal wake-eval resumes on the very next tick, no restart")
	require.Len(t, gw.mails, 1, "the queued event is delivered once the gag clears")
	require.Len(t, gw.nudges, 1)
}

// Unit (mutation anchor): with the gag on, a tick that WOULD otherwise act (an
// overdue heartbeat AND a queued interrupt event — two independent reasons to
// wake) takes no action at all, yet the supervisor stays live (no DISABLED, no
// error). Removing the one-line gag check in Tick makes this tick nudge/mail —
// this test then fails, which is the required mutation signal.
func TestGaggedTickTakesNoActionButStaysLive(t *testing.T) {
	sup, s, gw := newBridgeSupervisor(t)
	now := time.Now()
	sup.lastSession = now.Add(-2 * time.Hour) // heartbeat overdue: an ungagged tick would nudge
	recordEvent(t, s, captain.EventWorkflowFailed)
	require.NoError(t, SaveGagPolicy(sup.statePath, GagPolicy{Gagged: true}))

	ran, err := sup.Tick(context.Background(), now)
	require.NoError(t, err, "the gagged supervisor keeps running — standing down is not an error")
	require.False(t, ran)
	require.Empty(t, gw.mails, "no session spawned while gagged")
	require.Empty(t, gw.nudges, "no heartbeat nudge, no corrective action while gagged")
	require.False(t, sup.ws.Disabled(), "gag is a soft pause: it never writes the DISABLED sentinel")
}

// Unit (distinctness, arm 1): captain/DISABLED remains the HARD halt regardless
// of the gag. A DISABLED file present halts the tick even when the gag is off —
// the gag mechanism neither replaces nor weakens the hard kill switch.
func TestDisabledStillHardHaltsIndependentlyOfGag(t *testing.T) {
	sup, s, gw := newBridgeSupervisor(t)
	now := time.Now()
	sup.lastSession = now.Add(-2 * time.Hour)
	recordEvent(t, s, captain.EventWorkflowFailed)

	// Gag explicitly OFF, but the Admiral's hard switch is set.
	require.NoError(t, SaveGagPolicy(sup.statePath, GagPolicy{Gagged: false}))
	require.NoError(t, os.WriteFile(filepath.Join(s.dir, "DISABLED"), []byte("admiral\n"), 0o644))

	ran, err := sup.Tick(context.Background(), now)
	require.NoError(t, err)
	require.False(t, ran, "DISABLED hard-halts the tick, gag off or not")
	require.Empty(t, gw.mails)
	require.Empty(t, gw.nudges)
}

// Unit (distinctness, arm 2): the universe-reset detector's DISABLED-touch is
// UNAFFECTED by the gag. With the gag on and a live reset mismatch, the Tier-3
// safety rail still runs (the gag check sits AFTER it) and still hard-halts the
// fleet — proving the soft pause never shadows the reset kill-switch rail.
func TestGagLeavesUniverseResetRailIntact(t *testing.T) {
	sup, db, playerID, gw, dir := newUniverseSupervisor(t)
	seedEra(t, db, playerID, "torwind", "2026-07-05")
	sup.SetUniverseWatch(&scriptedStatus{status: &api.ServerStatus{ResetDate: "2026-07-06"}},
		persistence.NewEraRepository(db))
	require.NoError(t, SaveGagPolicy(sup.statePath, GagPolicy{Gagged: true, GagReason: "operator pause"}))

	ran, err := sup.Tick(context.Background(), time.Now())
	require.NoError(t, err)
	require.False(t, ran)

	data, err := os.ReadFile(filepath.Join(dir, "DISABLED"))
	require.NoError(t, err, "the universe-reset rail must still touch DISABLED even while gagged")
	require.Contains(t, string(data), "torwind")
	require.Equal(t, 1, mailsTo(gw, "human"), "and still mail the Admiral once")
}

// Unit (observability): entering and exiting the gag is logged and audited
// EXACTLY once per transition — edge-triggered, never per tick. Two consecutive
// gagged ticks log the entry once; clearing the gag logs the exit once. A
// captain event is recorded per edge so the stand-down is visible on the next
// wake.
func TestGagEnterExitLoggedAndEventedOncePerEdge(t *testing.T) {
	sup, s, _ := newBridgeSupervisor(t)
	t0 := time.Now()
	require.NoError(t, SaveGagPolicy(sup.statePath, GagPolicy{Gagged: true, GagReason: "deploy freeze"}))

	out := captureOutput(t, func() {
		_, err := sup.Tick(context.Background(), t0) // rising edge: log + event once
		require.NoError(t, err)
		_, err = sup.Tick(context.Background(), t0.Add(30*time.Second)) // still gagged: silent
		require.NoError(t, err)
		require.NoError(t, SaveGagPolicy(sup.statePath, GagPolicy{Gagged: false}))
		_, err = sup.Tick(context.Background(), t0.Add(60*time.Second)) // falling edge: log + event once
		require.NoError(t, err)
	})

	require.Equal(t, 1, strings.Count(out, "GAG ENGAGED"),
		"entering the gag logs once across consecutive gagged ticks, not per tick")
	require.Equal(t, 1, strings.Count(out, "GAG CLEARED"),
		"exiting the gag logs once")

	evs := gagEvents(t, s)
	require.Len(t, evs, 2, "one audit event per edge: enter and exit")
	joined := evs[0].Payload + evs[1].Payload
	require.Contains(t, joined, "gagged")
	require.Contains(t, joined, "ungagged")
	require.Contains(t, joined, "deploy freeze", "the operator reason is captured for the wake mail")
}
