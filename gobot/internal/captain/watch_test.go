package watchkeeper

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

// armWatch appends a watch to the persisted policy for dir's workspace,
// mirroring the CLI's additive `wake watch add` so calling it twice arms two.
func armWatch(t *testing.T, dir string, w Watch) {
	t.Helper()
	path := NewWorkspace(dir).StatePath()
	wp, err := LoadWatchPolicy(path)
	require.NoError(t, err)
	wp.Watches = append(wp.Watches, w)
	require.NoError(t, SaveWatchPolicy(path, wp))
}

func loadWatches(t *testing.T, dir string) []Watch {
	t.Helper()
	wp, err := LoadWatchPolicy(NewWorkspace(dir).StatePath())
	require.NoError(t, err)
	return wp.Watches
}

// --- sp-oyer: watch spec parsing ---

func TestParseWatchSpecShipArrival(t *testing.T) {
	w, err := ParseWatchSpec("ship:TORWIND-E:arrival")
	require.NoError(t, err)
	require.Equal(t, WatchSubjectShip, w.Subject)
	require.Equal(t, "TORWIND-E", w.ID)
	require.Equal(t, WatchPredicateArrival, w.Predicate)
}

func TestParseWatchSpecContainerTerminal(t *testing.T) {
	w, err := ParseWatchSpec("container:c-9f2a:terminal")
	require.NoError(t, err)
	require.Equal(t, WatchSubjectContainer, w.Subject)
	require.Equal(t, "c-9f2a", w.ID)
	require.Equal(t, WatchPredicateTerminal, w.Predicate)
}

func TestParseWatchSpecIDWithColonsIsRejoined(t *testing.T) {
	// Subject is the first segment and predicate the last, so an id containing
	// colons survives intact rather than being mis-split.
	w, err := ParseWatchSpec("container:ns:sub:id:terminal")
	require.NoError(t, err)
	require.Equal(t, "ns:sub:id", w.ID)
}

func TestParseWatchSpecRejectsWrongPredicateForSubject(t *testing.T) {
	_, err := ParseWatchSpec("ship:TORWIND-E:terminal")
	require.Error(t, err)
}

func TestParseWatchSpecRejectsUnknownSubject(t *testing.T) {
	_, err := ParseWatchSpec("fleet:X:arrival")
	require.Error(t, err)
}

func TestParseWatchSpecRejectsTooFewSegments(t *testing.T) {
	_, err := ParseWatchSpec("ship:arrival")
	require.Error(t, err)
}

func TestParseWatchSpecRejectsEmptyID(t *testing.T) {
	_, err := ParseWatchSpec("ship::arrival")
	require.Error(t, err)
}

// --- sp-oyer: match semantics ---

func TestWatchMatchesShipArrivalByShipField(t *testing.T) {
	w := Watch{Subject: WatchSubjectShip, ID: "TORWIND-E", Predicate: WatchPredicateArrival}
	require.True(t, w.matches(&captain.Event{Type: captain.EventWorkflowFinished, Ship: "TORWIND-E"}))
	require.True(t, w.matches(&captain.Event{Type: captain.EventWorkflowFailed, Ship: "TORWIND-E"}),
		"a failed terminal state still ends the wait and fires the watch")
	require.False(t, w.matches(&captain.Event{Type: captain.EventWorkflowFinished, Ship: "OTHER-1"}))
}

func TestWatchDoesNotMatchNonTerminalEventForSameShip(t *testing.T) {
	w := Watch{Subject: WatchSubjectShip, ID: "TORWIND-E", Predicate: WatchPredicateArrival}
	require.False(t, w.matches(&captain.Event{Type: captain.EventShipIdle, Ship: "TORWIND-E"}),
		"only a terminal-workflow event counts as an arrival, not e.g. ship.idle")
}

func TestWatchMatchesContainerTerminalByPayloadContainerID(t *testing.T) {
	w := Watch{Subject: WatchSubjectContainer, ID: "c-42", Predicate: WatchPredicateTerminal}
	require.True(t, w.matches(&captain.Event{Type: captain.EventWorkflowFinished, Payload: `{"container_id":"c-42"}`}))
	require.False(t, w.matches(&captain.Event{Type: captain.EventWorkflowFinished, Payload: `{"container_id":"c-99"}`}))
	require.False(t, w.matches(&captain.Event{Type: captain.EventWorkflowFinished, Payload: `not json`}))
}

// --- sp-oyer: wake.watch is always interrupt class ---

func TestWakeWatchIsInterruptEvenUnderCustomInterruptOverride(t *testing.T) {
	// A custom --interrupt-types override REPLACES the default set; a fired
	// watch must still be interrupt class regardless.
	events := []*captain.Event{{Type: captain.EventWakeWatch}}
	interrupts, deferred := partitionEvents(events, []string{"workflow.failed"})
	require.Len(t, interrupts, 1)
	require.Empty(t, deferred)
}

// --- sp-oyer acceptance: supervisor-tick behavior ---

// Acceptance (2): an arrival event for a watched ship wakes the captain within
// one supervisor tick, and the watch auto-clears — even though workflow.finished
// is a deferred type that never wakes on its own.
func TestWatchFiresOnShipArrivalAndAutoClears(t *testing.T) {
	sup, s, gw := newBridgeSupervisor(t)
	sup.lastSession = time.Now() // cadence nowhere near due: only the watch can wake

	armWatch(t, s.dir, Watch{
		Subject: WatchSubjectShip, ID: "TORWIND-E", Predicate: WatchPredicateArrival,
		Deadline: time.Now().Add(time.Hour), ArmedAt: time.Now(),
	})
	require.NoError(t, s.store.Record(context.Background(),
		&captain.Event{Type: captain.EventWorkflowFinished, Ship: "TORWIND-E", PlayerID: s.playerID}))

	ran, err := sup.Tick(context.Background(), time.Now())
	require.NoError(t, err)
	require.True(t, ran, "the watched ship's arrival must wake the captain in one tick")
	require.Len(t, gw.mails, 1)
	require.Contains(t, gw.mails[0][2], "ship:TORWIND-E:arrival matched")

	require.Empty(t, loadWatches(t, s.dir), "a fired watch auto-disarms")
}

func TestWatchFiresOnContainerTerminal(t *testing.T) {
	sup, s, gw := newBridgeSupervisor(t)
	sup.lastSession = time.Now()

	armWatch(t, s.dir, Watch{
		Subject: WatchSubjectContainer, ID: "c-42", Predicate: WatchPredicateTerminal,
		Deadline: time.Now().Add(time.Hour), ArmedAt: time.Now(),
	})
	require.NoError(t, s.store.Record(context.Background(),
		&captain.Event{Type: captain.EventWorkflowFinished, Ship: "ANY-1", PlayerID: s.playerID,
			Payload: `{"container_id":"c-42"}`}))

	ran, err := sup.Tick(context.Background(), time.Now())
	require.NoError(t, err)
	require.True(t, ran)
	require.Len(t, gw.mails, 1)
	require.Contains(t, gw.mails[0][2], "container:c-42:terminal matched")
	require.Empty(t, loadWatches(t, s.dir))
}

// Acceptance (3): a LOST arrival event (none ever arrives) must not strand the
// wake — the deadline fires it, tagged deadline-fired, and the watch clears.
func TestWatchDeadlineFiresWhenEventLostAndAutoClears(t *testing.T) {
	sup, s, gw := newBridgeSupervisor(t)
	sup.lastSession = time.Now()
	now := time.Now()

	armWatch(t, s.dir, Watch{
		Subject: WatchSubjectShip, ID: "GHOST-1", Predicate: WatchPredicateArrival,
		Deadline: now.Add(-time.Minute), ArmedAt: now.Add(-30 * time.Minute),
	})

	ran, err := sup.Tick(context.Background(), now)
	require.NoError(t, err)
	require.True(t, ran, "a passed deadline fires the wake even with no matching event")
	require.Len(t, gw.mails, 1)
	require.Contains(t, gw.mails[0][2], "ship:GHOST-1:arrival deadline-fired")

	require.Empty(t, loadWatches(t, s.dir))
}

// A watch armed for one ship must not fire on a different ship's arrival, and
// with the cadence not due that unrelated deferred arrival must not wake at all.
func TestWatchDoesNotFireOnUnwatchedShipArrival(t *testing.T) {
	sup, s, gw := newBridgeSupervisor(t)
	sup.lastSession = time.Now()

	armWatch(t, s.dir, Watch{
		Subject: WatchSubjectShip, ID: "SHIP-A", Predicate: WatchPredicateArrival,
		Deadline: time.Now().Add(time.Hour), ArmedAt: time.Now(),
	})
	require.NoError(t, s.store.Record(context.Background(),
		&captain.Event{Type: captain.EventWorkflowFinished, Ship: "SHIP-B", PlayerID: s.playerID}))

	ran, err := sup.Tick(context.Background(), time.Now())
	require.NoError(t, err)
	require.False(t, ran, "an unwatched ship's deferred arrival must not wake")
	require.Empty(t, gw.mails)
	require.Len(t, loadWatches(t, s.dir), 1, "the watch for SHIP-A stays armed")
}

// Acceptance (4): a watch add/fire leaves the standing wake policy untouched,
// and multiple watches coexist independently (firing one leaves the others).
func TestWatchFireLeavesStandingPolicyUntouchedAndOthersArmed(t *testing.T) {
	sup, s, gw := newBridgeSupervisor(t)
	path := NewWorkspace(s.dir).StatePath()

	credits := 500000
	require.NoError(t, SaveWakePolicy(path, WakePolicy{CreditsAbove: &credits, DeclaredAt: time.Now()}))

	armWatch(t, s.dir, Watch{Subject: WatchSubjectShip, ID: "SHIP-A", Predicate: WatchPredicateArrival,
		Deadline: time.Now().Add(time.Hour), ArmedAt: time.Now()})
	armWatch(t, s.dir, Watch{Subject: WatchSubjectShip, ID: "SHIP-B", Predicate: WatchPredicateArrival,
		Deadline: time.Now().Add(time.Hour), ArmedAt: time.Now()})

	// Keep the credits gate from waking on its own so we isolate the watch fire.
	sup.lastCredits = 0
	sup.lastSession = time.Now()
	require.NoError(t, s.store.Record(context.Background(),
		&captain.Event{Type: captain.EventWorkflowFinished, Ship: "SHIP-A", PlayerID: s.playerID}))

	ran, err := sup.Tick(context.Background(), time.Now())
	require.NoError(t, err)
	require.True(t, ran)
	require.Len(t, gw.mails, 1)

	// Standing wake policy survived the watch fire untouched.
	wake, err := LoadWakePolicy(path)
	require.NoError(t, err)
	require.NotNil(t, wake.CreditsAbove)
	require.Equal(t, 500000, *wake.CreditsAbove)

	// SHIP-A fired and cleared; SHIP-B is still armed (watches coexist).
	remaining := loadWatches(t, s.dir)
	require.Len(t, remaining, 1)
	require.Equal(t, "SHIP-B", remaining[0].ID)
}

// Restart persistence (RULINGS #2): an armed watch survives a watchkeeper
// restart, and its deadline is evaluated against the wall clock so a restart
// neither strands nor double-fires it.
func TestWatchSurvivesRestartAndDeadlineHonoredAcrossRestart(t *testing.T) {
	db, playerID, store := setupDB(t)
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "state"), 0o755))
	path := NewWorkspace(dir).StatePath()

	now := time.Now()
	require.NoError(t, SaveWatchPolicy(path, WatchPolicy{Watches: []Watch{{
		Subject: WatchSubjectShip, ID: "TORWIND-E", Predicate: WatchPredicateArrival,
		Deadline: now.Add(-time.Minute), ArmedAt: now.Add(-time.Hour),
	}}}))

	// Simulated restart: a brand-new supervisor reloads the armed watch.
	wp, err := LoadWatchPolicy(path)
	require.NoError(t, err)
	require.Len(t, wp.Watches, 1, "an armed watch survives a restart")

	sup, gw := reopenBridgeSupervisor(t, db, playerID, store, dir)
	sup.lastSession = now
	ran, err := sup.Tick(context.Background(), now)
	require.NoError(t, err)
	require.True(t, ran)
	require.Len(t, gw.mails, 1)
	require.Contains(t, gw.mails[0][2], "deadline-fired")

	wp, err = LoadWatchPolicy(path)
	require.NoError(t, err)
	require.Empty(t, wp.Watches, "the deadline-fired watch cleared across the restart")
}
