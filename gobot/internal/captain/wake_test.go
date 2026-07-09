package watchkeeper

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

type fakeGateway struct {
	mails, nudges [][]string
	alive         map[string]bool
}

func (f *fakeGateway) SendMail(_ context.Context, to, subject, body string) error {
	f.mails = append(f.mails, []string{to, subject, body})
	return nil
}

func (f *fakeGateway) Nudge(_ context.Context, alias, text string) error {
	f.nudges = append(f.nudges, []string{alias, text})
	return nil
}

func (f *fakeGateway) SessionAlive(_ context.Context, alias string) (bool, error) {
	if f.alive == nil {
		return true, nil
	}
	return f.alive[alias], nil
}

func newBridgeSupervisor(t *testing.T) (*Supervisor, *captainStores, *fakeGateway) {
	t.Helper()
	db, playerID, store := setupDB(t)
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "state"), 0o755))
	cfg := config.CaptainConfig{
		Enabled: true, PlayerID: playerID, WorkspaceDir: dir,
		PollIntervalSeconds: 30, HeartbeatMinutes: 45, MaxSessionsPerHour: 6,
		SessionTimeoutMinutes: 10, ShipIdleMinutes: 30, StaleHeartbeatMinutes: 5,
		EngineMode: "bridge", CaptainAgent: "captain", AdmiralAlias: "human",
		AckTimeoutMinutes: 10, EscalateAfterRenudges: 3,
	}
	gw := &fakeGateway{}
	sup, err := NewSupervisor(db, store, NewWorkspace(dir), cfg)
	require.NoError(t, err)
	sup.gw = gw
	return sup, &captainStores{store: store, playerID: playerID, dir: dir}, gw
}

// reopenBridgeSupervisor simulates a process restart: it constructs a brand
// new Supervisor against the same db/store/workspace dir a prior one used,
// so any durable state that prior Supervisor persisted is picked back up
// exactly as a real restart would (NewSupervisor loads it from disk).
func reopenBridgeSupervisor(t *testing.T, db *gorm.DB, playerID int, store captain.EventStore, dir string) (*Supervisor, *fakeGateway) {
	t.Helper()
	cfg := config.CaptainConfig{
		Enabled: true, PlayerID: playerID, WorkspaceDir: dir,
		PollIntervalSeconds: 30, HeartbeatMinutes: 45, MaxSessionsPerHour: 6,
		SessionTimeoutMinutes: 10, ShipIdleMinutes: 30, StaleHeartbeatMinutes: 5,
		EngineMode: "bridge", CaptainAgent: "captain", AdmiralAlias: "human",
		AckTimeoutMinutes: 10, EscalateAfterRenudges: 3,
	}
	gw := &fakeGateway{}
	sup, err := NewSupervisor(db, store, NewWorkspace(dir), cfg)
	require.NoError(t, err)
	sup.gw = gw
	return sup, gw
}

func recordEvent(t *testing.T, s *captainStores, typ captain.EventType) {
	t.Helper()
	require.NoError(t, s.store.Record(context.Background(),
		&captain.Event{Type: typ, Ship: "S", PlayerID: s.playerID}))
}

func mailsTo(gw *fakeGateway, to string) int {
	n := 0
	for _, m := range gw.mails {
		if m[0] == to {
			n++
		}
	}
	return n
}

func TestBridgeWakeSendsMailAndNudgeForEvents(t *testing.T) {
	sup, s, gw := newBridgeSupervisor(t)
	sup.lastSession = time.Now() // events, not heartbeat, drive this
	recordEvent(t, s, captain.EventWorkflowFailed)
	recordEvent(t, s, captain.EventShipIdle)

	ran, err := sup.Tick(context.Background(), time.Now())
	require.NoError(t, err)
	require.True(t, ran)

	require.Len(t, gw.mails, 1)
	require.Len(t, gw.nudges, 1)
	require.Equal(t, "captain", gw.mails[0][0])
	require.Equal(t, "wake: 2 events", gw.mails[0][1])
	require.Contains(t, gw.mails[0][2], "spacetraders captain events ack")
	require.Contains(t, gw.mails[0][2], "--player-id")
	require.Contains(t, gw.nudges[0][1], "check mail")

	left, err := s.store.FindUnprocessed(context.Background(), s.playerID, 10)
	require.NoError(t, err)
	require.Len(t, left, 2, "bridge wake must not ack events itself")
}

func TestBridgeHeartbeatNudgesWithoutMail(t *testing.T) {
	sup, _, gw := newBridgeSupervisor(t)
	sup.lastSession = time.Now().Add(-2 * time.Hour) // heartbeat due, zero events

	ran, err := sup.Tick(context.Background(), time.Now())
	require.NoError(t, err)
	require.True(t, ran)
	require.Empty(t, gw.mails)
	require.Len(t, gw.nudges, 1)
	require.Contains(t, gw.nudges[0][1], "heartbeat")
}

func TestBridgeRenudgesUnackedAfterTimeout(t *testing.T) {
	sup, s, gw := newBridgeSupervisor(t)
	recordEvent(t, s, captain.EventWorkflowFailed)

	t0 := time.Now()
	sup.lastSession = t0
	ran, err := sup.Tick(context.Background(), t0)
	require.NoError(t, err)
	require.True(t, ran)
	require.Len(t, gw.mails, 1)
	require.Len(t, gw.nudges, 1)

	// Still unacked, before the ack timeout: no repeat.
	_, err = sup.Tick(context.Background(), t0.Add(5*time.Minute))
	require.NoError(t, err)
	require.Len(t, gw.nudges, 1, "no re-nudge before ack timeout")

	// Past the ack timeout: one re-nudge, still exactly one mail.
	_, err = sup.Tick(context.Background(), t0.Add(11*time.Minute))
	require.NoError(t, err)
	require.Len(t, gw.mails, 1, "re-nudge sends no duplicate mail")
	require.Len(t, gw.nudges, 2)
	require.Contains(t, gw.nudges[1][1], "unacked")
}

func TestBridgeEscalatesToAdmiralAfterMaxRenudges(t *testing.T) {
	sup, s, gw := newBridgeSupervisor(t)
	recordEvent(t, s, captain.EventWorkflowFailed)
	left, err := s.store.FindUnprocessed(context.Background(), s.playerID, 10)
	require.NoError(t, err)
	require.Len(t, left, 1)
	id := left[0].ID

	// Already mailed and re-nudged the maximum number of times.
	sup.renudges = map[int64]int{id: 3}
	sup.escalated = map[int64]bool{}
	t0 := time.Now()
	sup.lastSession = t0

	_, err = sup.Tick(context.Background(), t0.Add(11*time.Minute))
	require.NoError(t, err)
	require.Equal(t, 1, mailsTo(gw, "human"), "escalates to Admiral once")

	// Further ticks do not re-escalate.
	_, err = sup.Tick(context.Background(), t0.Add(30*time.Minute))
	require.NoError(t, err)
	require.Equal(t, 1, mailsTo(gw, "human"), "escalation fires at most once per event")
}

func TestBridgeWakeRespectsKillSwitch(t *testing.T) {
	sup, s, gw := newBridgeSupervisor(t)
	sup.lastSession = time.Now().Add(-2 * time.Hour)
	recordEvent(t, s, captain.EventWorkflowFailed)
	require.NoError(t, os.WriteFile(filepath.Join(s.dir, "DISABLED"), nil, 0o644))

	ran, err := sup.Tick(context.Background(), time.Now())
	require.NoError(t, err)
	require.False(t, ran)
	require.Empty(t, gw.mails)
	require.Empty(t, gw.nudges)
}

// --- Durable scheduling state across process restarts ---
//
// Bug: a fresh Supervisor left lastSession/lastSurveyorNudge at time.Time's
// zero value, so now.Sub(zeroValue) is enormous and every "due" check
// evaluated true immediately after construction. Every process start (three
// manual `captain --once` runs plus one launchd service start) fired an
// immediate heartbeat wake and survey nudge. The fix persists scheduling
// state to <workspace_dir>/state/supervisor-state.json and arms fresh
// cadences one full interval out from now instead of due-at-zero.

func TestFreshSupervisorSchedulesHeartbeatOneIntervalOutNotImmediately(t *testing.T) {
	sup, _, gw := newBridgeSupervisor(t)

	ran, err := sup.Tick(context.Background(), time.Now())
	require.NoError(t, err)
	require.False(t, ran, "a brand-new process must not treat the heartbeat as immediately due")
	require.Empty(t, gw.mails)
	require.Empty(t, gw.nudges)
}

func TestSupervisorFiresHeartbeatWhenPersistedTimestampIsPastDue(t *testing.T) {
	db, playerID, store := setupDB(t)
	dir := t.TempDir()
	require.NoError(t, saveSupervisorState(NewWorkspace(dir).StatePath(), supervisorState{
		LastSession: time.Now().Add(-2 * time.Hour),
	}))

	sup, gw := reopenBridgeSupervisor(t, db, playerID, store, dir)
	ran, err := sup.Tick(context.Background(), time.Now())
	require.NoError(t, err)
	require.True(t, ran, "a persisted timestamp from 2h ago (> 45m heartbeat_minutes) must be due right after restart")
	require.Len(t, gw.nudges, 1)
	require.Contains(t, gw.nudges[0][1], "heartbeat")
}

func TestSupervisorRestartRoundTripsHeartbeatStateAndDoesNotRefire(t *testing.T) {
	db, playerID, store := setupDB(t)
	dir := t.TempDir()

	sup1, gw1 := reopenBridgeSupervisor(t, db, playerID, store, dir)
	sup1.lastSession = time.Now().Add(-2 * time.Hour)
	ran, err := sup1.Tick(context.Background(), time.Now())
	require.NoError(t, err)
	require.True(t, ran, "heartbeat due before restart")
	require.Len(t, gw1.nudges, 1)

	sup2, gw2 := reopenBridgeSupervisor(t, db, playerID, store, dir)
	ran, err = sup2.Tick(context.Background(), time.Now())
	require.NoError(t, err)
	require.False(t, ran, "restart must not re-fire a heartbeat that was just sent moments before")
	require.Empty(t, gw2.nudges)
	require.Empty(t, gw2.mails)
}

func TestRenudgeStateSurvivesRestartAndDoesNotResendInitialMail(t *testing.T) {
	db, playerID, store := setupDB(t)
	dir := t.TempDir()

	sup1, gw1 := reopenBridgeSupervisor(t, db, playerID, store, dir)
	require.NoError(t, store.Record(context.Background(),
		&captain.Event{Type: captain.EventWorkflowFailed, Ship: "S", PlayerID: playerID}))

	t0 := time.Now()
	sup1.lastSession = t0
	ran, err := sup1.Tick(context.Background(), t0)
	require.NoError(t, err)
	require.True(t, ran)
	require.Len(t, gw1.mails, 1, "first wake sends exactly one mail")

	// Restart shortly after: still within the ack timeout, and the event was
	// already mailed pre-restart, so this must be a no-op — not a duplicate
	// full wake mail (which is what an unpersisted, reset-to-nil renudges
	// map would cause via hasUnmailedEvents).
	sup2, gw2 := reopenBridgeSupervisor(t, db, playerID, store, dir)
	ran, err = sup2.Tick(context.Background(), t0.Add(2*time.Minute))
	require.NoError(t, err)
	require.False(t, ran, "event already wake-mailed pre-restart and still within ack timeout")
	require.Empty(t, gw2.mails, "restart must not re-send the initial wake mail")
	require.Empty(t, gw2.nudges)

	// Past the ack timeout post-restart: a re-nudge, not a fresh full wake.
	sup3, gw3 := reopenBridgeSupervisor(t, db, playerID, store, dir)
	ran, err = sup3.Tick(context.Background(), t0.Add(11*time.Minute))
	require.NoError(t, err)
	require.True(t, ran)
	require.Empty(t, gw3.mails, "still a re-nudge, not a duplicate wake mail, after restart")
	require.Len(t, gw3.nudges, 1)
	require.Contains(t, gw3.nudges[0][1], "unacked")
}

// --- sp-sk68 wake model: captain-declared wake policy, Tick-level ---
//
// The wake GATE (evaluateWakeGate) is unit-tested exhaustively as a pure
// function in wakegate_test.go. These tests prove it is actually wired into
// Tick: interrupt events still force an immediate wake under the default
// policy (no regression), a declared CreditsAbove/CreditsBelow threshold can
// force a wake with zero events queued, and — critically — a policy change
// takes effect on the very next Tick without restarting the process, because
// Tick re-reads the policy from disk every time rather than caching it at
// construction.

func TestBridgeWakesImmediatelyForInterruptEventEvenWhenCadenceNotDue(t *testing.T) {
	sup, s, gw := newBridgeSupervisor(t)
	sup.lastSession = time.Now() // heartbeat cadence nowhere near due
	recordEvent(t, s, captain.EventContainerCrashLoop)

	ran, err := sup.Tick(context.Background(), time.Now())
	require.NoError(t, err)
	require.True(t, ran, "an interrupt-type event must force a wake regardless of cadence")
	require.Len(t, gw.mails, 1)
	require.Len(t, gw.nudges, 1)
}

func TestBridgeWakesWhenDeclaredCreditsAboveThresholdIsCrossed(t *testing.T) {
	sup, s, gw := newBridgeSupervisor(t)
	sup.lastSession = time.Now() // heartbeat cadence nowhere near due

	require.NoError(t, sup.db.Create(&persistence.TransactionModel{
		ID: "t-1", PlayerID: s.playerID, Timestamp: time.Now(), TransactionType: "SELL_CARGO",
		Category: "TRADING_REVENUE", Amount: 5000, BalanceBefore: 400000, BalanceAfter: 500000,
	}).Error)

	above := 500000
	require.NoError(t, SaveWakePolicy(NewWorkspace(s.dir).StatePath(), WakePolicy{CreditsAbove: &above}))

	ran, err := sup.Tick(context.Background(), time.Now())
	require.NoError(t, err)
	require.True(t, ran, "credits at/above the declared CreditsAbove threshold must force a wake")
	require.Empty(t, gw.mails, "zero events queued: a credits-triggered wake is a heartbeat-style nudge, not a mail")
	require.Len(t, gw.nudges, 1)
}

func TestBridgeWakePolicyTakesEffectNextTickWithoutRestart(t *testing.T) {
	sup, _, gw := newBridgeSupervisor(t)
	sup.lastSession = time.Now() // heartbeat cadence nowhere near due

	// No policy declared yet, no events, cadence not due: no wake.
	ran, err := sup.Tick(context.Background(), time.Now())
	require.NoError(t, err)
	require.False(t, ran)

	// The captain (in reality, a separate `spacetraders captain wake set`
	// CLI invocation) declares a NextWakeAt policy directly on disk against
	// the SAME running Supervisor — no restart, no reconstruction.
	declared := time.Now()
	require.NoError(t, SaveWakePolicy(sup.statePath, WakePolicy{NextWakeAt: &declared}))

	ran, err = sup.Tick(context.Background(), declared)
	require.NoError(t, err)
	require.True(t, ran, "the supervisor must re-read the wake policy from disk on every Tick, not just at construction")
	require.Len(t, gw.nudges, 1)
}

func TestLastCreditsSurvivesRestart(t *testing.T) {
	db, playerID, store := setupDB(t)
	dir := t.TempDir()

	sup1, _ := reopenBridgeSupervisor(t, db, playerID, store, dir)
	sup1.lastCredits = 777000
	sup1.saveState()

	sup2, _ := reopenBridgeSupervisor(t, db, playerID, store, dir)
	require.Equal(t, 777000, sup2.lastCredits, "LastCredits must survive a process restart")
}
