package captainsup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

type stubRunner struct {
	prompts []string
	err     error
}

func (s *stubRunner) Run(_ context.Context, prompt string) error {
	s.prompts = append(s.prompts, prompt)
	return s.err
}

func newTestSupervisor(t *testing.T, runner SessionRunner) (*Supervisor, *captainStores) {
	t.Helper()
	db, playerID, store := setupDB(t)
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "state"), 0o755))
	cfg := config.CaptainConfig{
		Enabled: true, PlayerID: playerID, WorkspaceDir: dir,
		PollIntervalSeconds: 30, HeartbeatMinutes: 45, MaxSessionsPerHour: 6,
		SessionTimeoutMinutes: 10, ShipIdleMinutes: 30, StaleHeartbeatMinutes: 5,
	}
	sup := NewSupervisor(db, store, runner, NewWorkspace(dir), cfg)
	return sup, &captainStores{store: store, playerID: playerID, dir: dir}
}

type captainStores struct {
	store    captain.EventStore
	playerID int
	dir      string
}

func TestTickNoTriggerNoSession(t *testing.T) {
	runner := &stubRunner{}
	sup, _ := newTestSupervisor(t, runner)
	sup.lastSession = time.Now() // heartbeat not due
	require.NoError(t, MarkMetaReviewDone(sup.ws, time.Now()))

	ran, err := sup.Tick(context.Background(), time.Now())
	require.NoError(t, err)
	require.False(t, ran)
	require.Empty(t, runner.prompts)
}

func TestTickRunsOnEventAndMarksProcessed(t *testing.T) {
	runner := &stubRunner{}
	sup, s := newTestSupervisor(t, runner)
	sup.lastSession = time.Now()
	require.NoError(t, s.store.Record(context.Background(),
		&captain.Event{Type: captain.EventWorkflowFailed, Ship: "S", PlayerID: s.playerID, Payload: `{"error":"x"}`}))

	ran, err := sup.Tick(context.Background(), time.Now())
	require.NoError(t, err)
	require.True(t, ran)
	require.Len(t, runner.prompts, 1)
	require.Contains(t, runner.prompts[0], "workflow.failed")

	left, err := s.store.FindUnprocessed(context.Background(), s.playerID, 10)
	require.NoError(t, err)
	require.Empty(t, left, "events must be marked processed after a successful session")
}

func TestTickHeartbeatTriggersWithoutEvents(t *testing.T) {
	runner := &stubRunner{}
	sup, _ := newTestSupervisor(t, runner)
	sup.lastSession = time.Now().Add(-2 * time.Hour)

	ran, err := sup.Tick(context.Background(), time.Now())
	require.NoError(t, err)
	require.True(t, ran)
	require.Contains(t, runner.prompts[0], "heartbeat review")
}

func TestTickFailedSessionLeavesEventsUnprocessed(t *testing.T) {
	runner := &stubRunner{err: ErrUsageLimit}
	sup, s := newTestSupervisor(t, runner)
	sup.lastSession = time.Now()
	require.NoError(t, s.store.Record(context.Background(),
		&captain.Event{Type: captain.EventShipIdle, Ship: "S", PlayerID: s.playerID}))

	ran, err := sup.Tick(context.Background(), time.Now())
	require.True(t, ran)
	require.ErrorIs(t, err, ErrUsageLimit)

	left, lerr := s.store.FindUnprocessed(context.Background(), s.playerID, 10)
	require.NoError(t, lerr)
	require.Len(t, left, 1, "failed session must leave events for retry")
}

func TestTickRespectsKillSwitch(t *testing.T) {
	runner := &stubRunner{}
	sup, s := newTestSupervisor(t, runner)
	sup.lastSession = time.Now().Add(-2 * time.Hour)
	require.NoError(t, os.WriteFile(filepath.Join(s.dir, "DISABLED"), nil, 0o644))

	ran, err := sup.Tick(context.Background(), time.Now())
	require.NoError(t, err)
	require.False(t, ran)
}

func TestTickRespectsHourlyCap(t *testing.T) {
	runner := &stubRunner{}
	sup, s := newTestSupervisor(t, runner)
	now := time.Now()
	for i := 0; i < 6; i++ {
		sup.sessionStarts = append(sup.sessionStarts, now.Add(-time.Duration(i)*time.Minute))
	}
	require.NoError(t, MarkMetaReviewDone(sup.ws, now))
	require.NoError(t, s.store.Record(context.Background(),
		&captain.Event{Type: captain.EventShipIdle, Ship: "S", PlayerID: s.playerID}))

	ran, err := sup.Tick(context.Background(), now)
	require.NoError(t, err)
	require.False(t, ran, "cap reached: events queue, no session")
}

func TestTickRunsMetaReviewWhenDueAndIdle(t *testing.T) {
	runner := &stubRunner{}
	sup, _ := newTestSupervisor(t, runner)
	sup.lastSession = time.Now() // no strategy trigger

	ran, err := sup.Tick(context.Background(), time.Now())
	require.NoError(t, err)
	require.True(t, ran)
	require.Len(t, runner.prompts, 1)
	require.Contains(t, runner.prompts[0], "Meta-review")

	// Immediately after, meta-review is no longer due.
	ran, err = sup.Tick(context.Background(), time.Now())
	require.NoError(t, err)
	require.False(t, ran)
	require.Len(t, runner.prompts, 1)
}

func TestTickPrefersStrategyOverMetaReview(t *testing.T) {
	runner := &stubRunner{}
	sup, s := newTestSupervisor(t, runner)
	sup.lastSession = time.Now()
	require.NoError(t, s.store.Record(context.Background(),
		&captain.Event{Type: captain.EventWorkflowFailed, Ship: "S", PlayerID: s.playerID}))

	ran, err := sup.Tick(context.Background(), time.Now())
	require.NoError(t, err)
	require.True(t, ran)
	require.Contains(t, runner.prompts[0], "Fleet situation report",
		"events outrank the meta-review")
}
