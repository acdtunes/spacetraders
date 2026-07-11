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
	sup.promAlertsURL = "" // isolate the suite from any Prometheus on the dev box (sp-y0f6)
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
	sup.promAlertsURL = "" // isolate the suite from any Prometheus on the dev box (sp-y0f6)
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

// --- sp-o8wi: nudge coalescing (cooldown) + interrupt bypass + persisted clock ---
//
// Bug: firstWake fired a mail+nudge on EVERY poll tick that saw any newly
// arrived event, so deploy churn (20-24 events queued across consecutive
// ticks) nudged the captain seconds apart and stalled the session. The fix
// enforces nudgeCooldown between successive non-interrupt firstWake nudges:
// events arriving inside the window stay unmailed and ride the next allowed
// firstWake as one accumulated batch. A never-mailed interrupt bypasses the
// cooldown, and the clock is persisted so a restart mid-window does not reset
// it and re-storm on boot.

func deployEvent(id int64, at time.Time) *captain.Event {
	return &captain.Event{ID: id, Type: captain.EventDeployCompleted, Ship: "S", CreatedAt: at}
}

func TestBridgeCoalescesNonInterruptStormIntoOneNudgePerCooldown(t *testing.T) {
	sup, _, gw := newBridgeSupervisor(t)
	ctx := context.Background()
	t0 := time.Now()

	// Tick 0 of the storm: the first deploy events fire one firstWake mail+nudge.
	ran, err := sup.bridgeWake(ctx, t0, []*captain.Event{deployEvent(1, t0), deployEvent(2, t0)}, WakePolicy{})
	require.NoError(t, err)
	require.True(t, ran)
	require.Len(t, gw.mails, 1)
	require.Len(t, gw.nudges, 1)

	// Consecutive ticks inside the cooldown window: a new deploy event arrives
	// each tick, yet NO new nudge is delivered — they accumulate unmailed.
	for i, dt := range []time.Duration{30 * time.Second, 60 * time.Second, 90 * time.Second, 120 * time.Second} {
		batch := []*captain.Event{deployEvent(1, t0), deployEvent(2, t0)}
		for j := int64(0); j <= int64(i); j++ {
			batch = append(batch, deployEvent(3+j, t0.Add(dt))) // events 3,4,5,6 accumulate
		}
		ran, err = sup.bridgeWake(ctx, t0.Add(dt), batch, WakePolicy{})
		require.NoError(t, err)
		require.False(t, ran, "a non-interrupt batch inside the cooldown window must not nudge")
	}
	require.Len(t, gw.nudges, 1, "no per-tick nudging inside the cooldown window")

	// Once the cooldown elapses: exactly one more nudge, carrying the WHOLE
	// accumulated batch (events 1..6), not one nudge per queued event.
	full := []*captain.Event{
		deployEvent(1, t0), deployEvent(2, t0), deployEvent(3, t0), deployEvent(4, t0), deployEvent(5, t0), deployEvent(6, t0),
	}
	ran, err = sup.bridgeWake(ctx, t0.Add(nudgeCooldown+time.Second), full, WakePolicy{})
	require.NoError(t, err)
	require.True(t, ran)
	require.Len(t, gw.nudges, 2, "one coalesced nudge after the cooldown elapses")
	require.Equal(t, "wake: 6 events", gw.mails[1][1], "the coalesced wake carries the whole accumulated batch")
}

func TestBridgeInterruptBypassesNudgeCooldown(t *testing.T) {
	sup, _, gw := newBridgeSupervisor(t)
	ctx := context.Background()
	t0 := time.Now()

	// A non-interrupt firstWake stamps the cooldown clock.
	ran, err := sup.bridgeWake(ctx, t0, []*captain.Event{deployEvent(1, t0)}, WakePolicy{})
	require.NoError(t, err)
	require.True(t, ran)
	require.Len(t, gw.nudges, 1)

	// A bare non-interrupt event 15s later is deferred (proves the window is open)...
	ran, err = sup.bridgeWake(ctx, t0.Add(15*time.Second),
		[]*captain.Event{deployEvent(1, t0), deployEvent(2, t0.Add(15 * time.Second))}, WakePolicy{})
	require.NoError(t, err)
	require.False(t, ran, "a non-interrupt event inside the window is deferred")
	require.Len(t, gw.nudges, 1)

	// ...but a brand-new INTERRUPT event well inside the same window nudges NOW.
	ran, err = sup.bridgeWake(ctx, t0.Add(20*time.Second), []*captain.Event{
		deployEvent(1, t0),
		{ID: 3, Type: captain.EventWorkflowFailed, Ship: "S", CreatedAt: t0.Add(20 * time.Second)},
	}, WakePolicy{})
	require.NoError(t, err)
	require.True(t, ran, "a never-mailed interrupt must bypass the cooldown")
	require.Len(t, gw.nudges, 2, "interrupt nudges immediately despite the open cooldown window")
}

func TestNudgeCooldownClockSurvivesRestartAndDoesNotReStorm(t *testing.T) {
	db, playerID, store := setupDB(t)
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "state"), 0o755))
	ctx := context.Background()
	t0 := time.Now()

	// Pre-restart: a non-interrupt firstWake stamps + persists lastNudge.
	sup1, gw1 := reopenBridgeSupervisor(t, db, playerID, store, dir)
	ran, err := sup1.bridgeWake(ctx, t0, []*captain.Event{deployEvent(1, t0)}, WakePolicy{})
	require.NoError(t, err)
	require.True(t, ran)
	require.Len(t, gw1.nudges, 1)

	// Restart inside the cooldown window: the persisted clock reloads, so a new
	// deploy event on boot is deferred — no re-storm.
	sup2, gw2 := reopenBridgeSupervisor(t, db, playerID, store, dir)
	require.False(t, sup2.lastNudge.IsZero(), "lastNudge must reload from disk after restart")
	ran, err = sup2.bridgeWake(ctx, t0.Add(30*time.Second),
		[]*captain.Event{deployEvent(1, t0), deployEvent(2, t0.Add(30 * time.Second))}, WakePolicy{})
	require.NoError(t, err)
	require.False(t, ran, "a restart inside the cooldown window must not re-nudge on boot")
	require.Empty(t, gw2.nudges)

	// Past the cooldown post-restart: the accumulated event finally nudges once.
	sup3, gw3 := reopenBridgeSupervisor(t, db, playerID, store, dir)
	ran, err = sup3.bridgeWake(ctx, t0.Add(nudgeCooldown+time.Second),
		[]*captain.Event{deployEvent(1, t0), deployEvent(2, t0.Add(30 * time.Second))}, WakePolicy{})
	require.NoError(t, err)
	require.True(t, ran, "once the persisted cooldown elapses the coalesced wake fires")
	require.Len(t, gw3.nudges, 1)
}

func TestSupervisorStateWithoutLastNudgeFieldLoadsCleanly(t *testing.T) {
	db, playerID, store := setupDB(t)
	dir := t.TempDir()
	statePath := NewWorkspace(dir).StatePath()
	require.NoError(t, os.MkdirAll(filepath.Dir(statePath), 0o755))

	// A state file written before last_nudge existed (backward compatibility).
	require.NoError(t, os.WriteFile(statePath,
		[]byte(`{"last_session":"2020-01-01T00:00:00Z","last_surveyor_nudge":"2020-01-01T00:00:00Z"}`), 0o644))

	sup, gw := reopenBridgeSupervisor(t, db, playerID, store, dir)
	require.True(t, sup.lastNudge.IsZero(), "an absent last_nudge loads as zero (fire the first wake immediately)")

	// With no persisted cooldown, the first non-interrupt event fires at once.
	ran, err := sup.bridgeWake(context.Background(), time.Now(),
		[]*captain.Event{deployEvent(1, time.Now())}, WakePolicy{})
	require.NoError(t, err)
	require.True(t, ran, "a zero cooldown clock means the first wake fires immediately")
	require.Len(t, gw.nudges, 1)
}

// TestTickCoalescesDeployStormBehindStandingInterrupt is the faithful incident
// repro at the Tick level: a mailed interrupt keeps the wake GATE open every
// tick (evaluateWakeGate wakes on any interrupt in the batch, mailed or not),
// while deploy-churn events accumulate. Before the fix firstWake fired every
// tick; after it, the non-interrupt accumulation rides one coalesced wake.
func TestTickCoalescesDeployStormBehindStandingInterrupt(t *testing.T) {
	sup, s, gw := newBridgeSupervisor(t)
	ctx := context.Background()
	t0 := time.Now()
	sup.lastSession = t0 // heartbeat cadence nowhere near due

	// A standing interrupt: fires once, then keeps the gate open on later ticks.
	recordEvent(t, s, captain.EventWorkflowFailed)
	ran, err := sup.Tick(ctx, t0)
	require.NoError(t, err)
	require.True(t, ran)
	require.Len(t, gw.nudges, 1, "the interrupt fires the initial wake")

	// Deploy churn: a non-interrupt event arrives each tick inside the cooldown.
	for i, dt := range []time.Duration{30 * time.Second, 60 * time.Second, 90 * time.Second} {
		recordEvent(t, s, captain.EventDeployCompleted)
		_, err := sup.Tick(ctx, t0.Add(dt))
		require.NoError(t, err)
		require.Len(t, gw.nudges, 1, "tick %d inside the cooldown must not add a nudge", i+1)
	}

	// Once the cooldown elapses the accumulated deploy events ride one wake.
	_, err = sup.Tick(ctx, t0.Add(nudgeCooldown+time.Second))
	require.NoError(t, err)
	require.Len(t, gw.nudges, 2, "the deploy storm coalesces into exactly one further nudge")
}
