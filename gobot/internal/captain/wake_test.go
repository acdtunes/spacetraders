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

type fakeGateway struct {
	mails, nudges [][]string
	alive         map[string]bool
	spawned       [][]string
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

func (f *fakeGateway) SpawnSession(_ context.Context, agent, alias string) error {
	f.spawned = append(f.spawned, []string{agent, alias})
	return nil
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
