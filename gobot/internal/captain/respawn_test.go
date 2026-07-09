package watchkeeper

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

type respawnGateway struct {
	alive    map[string]bool
	mails    [][]string
	mailErr  error
	aliveErr error
}

func (g *respawnGateway) SendMail(_ context.Context, to, subject, body string) error {
	g.mails = append(g.mails, []string{to, subject, body})
	return g.mailErr
}

func (g *respawnGateway) Nudge(_ context.Context, alias, text string) error { return nil }

func (g *respawnGateway) SessionAlive(_ context.Context, alias string) (bool, error) {
	if g.aliveErr != nil {
		return false, g.aliveErr
	}
	return g.alive[alias], nil
}

type fakeBeads struct {
	inProgress []PipelineBead
	reopened   [][]string
}

func (f *fakeBeads) ListInProgressPipeline(_ context.Context) ([]PipelineBead, error) {
	return f.inProgress, nil
}

func (f *fakeBeads) Reopen(_ context.Context, id, reason string) error {
	f.reopened = append(f.reopened, []string{id, reason})
	return nil
}

// TestEnsureCaptainAliveAlertsAdmiralAndNeverSpawnsWhenDead is the core of the
// sp-qv71 ruling: on a dead captain the watchkeeper must ALERT (Admiral mail +
// a grep-able local log line) and must NEVER create a session. "No session
// created" is now structural — SpawnSession no longer exists on the gateway —
// so this asserts the alert channels fired.
func TestEnsureCaptainAliveAlertsAdmiralAndNeverSpawnsWhenDead(t *testing.T) {
	gw := &respawnGateway{alive: map[string]bool{"captain": false}}
	sup := &Supervisor{cfg: config.CaptainConfig{CaptainAgent: "captain", AdmiralAlias: "human"}, gw: gw}

	out := captureOutput(t, func() {
		sup.ensureCaptainAlive(context.Background(), time.Now())
	})

	require.Len(t, gw.mails, 1, "a dead captain alerts the Admiral")
	require.Equal(t, "human", gw.mails[0][0])
	require.Contains(t, out, "STANDING SESSION DOWN",
		"a dead captain must emit a grep-able local log line that survives a mail/gc/bd outage")
}

// TestEnsureCaptainAliveLogsDownEvenWhenAdmiralMailFails covers the swallow bug
// at the old respawn.go:50: the down signal must survive a broken mail channel.
// The local log line fires regardless, and the mail error is itself logged
// rather than `_ =`-swallowed.
func TestEnsureCaptainAliveLogsDownEvenWhenAdmiralMailFails(t *testing.T) {
	gw := &respawnGateway{alive: map[string]bool{"captain": false}, mailErr: errors.New("mail channel down")}
	sup := &Supervisor{cfg: config.CaptainConfig{CaptainAgent: "captain", AdmiralAlias: "human"}, gw: gw}

	out := captureOutput(t, func() {
		sup.ensureCaptainAlive(context.Background(), time.Now())
	})

	require.Len(t, gw.mails, 1, "mail delivery was attempted")
	require.Contains(t, out, "STANDING SESSION DOWN",
		"the down log line must fire even when the Admiral mail fails")
	require.Contains(t, out, "mail FAILED",
		"a failed Admiral alert mail must itself be logged, not swallowed")
}

// TestEnsureCaptainAliveThrottlesRepeatedDownAlerts proves a captain that stays
// dead across 30s polls is not mailed to the Admiral every tick.
func TestEnsureCaptainAliveThrottlesRepeatedDownAlerts(t *testing.T) {
	gw := &respawnGateway{alive: map[string]bool{"captain": false}}
	sup := &Supervisor{cfg: config.CaptainConfig{CaptainAgent: "captain", AdmiralAlias: "human"}, gw: gw}

	t0 := time.Now()
	_ = captureOutput(t, func() {
		sup.ensureCaptainAlive(context.Background(), t0)
		sup.ensureCaptainAlive(context.Background(), t0.Add(30*time.Second)) // next poll
		sup.ensureCaptainAlive(context.Background(), t0.Add(5*time.Minute))  // still within window
	})

	require.Len(t, gw.mails, 1,
		"a still-dead captain alerts the Admiral once per throttle window, not every poll")
}

// TestEnsureCaptainAliveReAlertsAfterThrottleWindow proves the outage is not
// forgotten: once the window elapses the still-dead captain is alerted again.
func TestEnsureCaptainAliveReAlertsAfterThrottleWindow(t *testing.T) {
	gw := &respawnGateway{alive: map[string]bool{"captain": false}}
	sup := &Supervisor{cfg: config.CaptainConfig{CaptainAgent: "captain", AdmiralAlias: "human"}, gw: gw}

	t0 := time.Now()
	_ = captureOutput(t, func() {
		sup.ensureCaptainAlive(context.Background(), t0)
		sup.ensureCaptainAlive(context.Background(), t0.Add(31*time.Minute)) // past the window
	})

	require.Len(t, gw.mails, 2,
		"a still-dead captain is re-alerted once the throttle window elapses")
}

// TestEnsureCaptainAliveLogsAndSkipsOnProbeError covers sp-sk68 D5: during the
// gc/bd outage every SessionAlive probe errored, and the old
// `if err != nil || alive { return }` treated that exactly like "alive". A
// probe error is conservatively NOT treated as a death (a flaky probe must not
// trigger a false down-alert), but it must be visible in the log — never
// silent — and it must not mail the Admiral.
func TestEnsureCaptainAliveLogsAndSkipsOnProbeError(t *testing.T) {
	gw := &respawnGateway{aliveErr: errors.New("gc failed: bd-router: cannot find the real bd binary")}
	sup := &Supervisor{cfg: config.CaptainConfig{CaptainAgent: "captain", AdmiralAlias: "human"}, gw: gw}

	out := captureOutput(t, func() {
		sup.ensureCaptainAlive(context.Background(), time.Now())
	})

	require.Empty(t, gw.mails, "a probe error is not a death: no Admiral alert")
	require.Contains(t, out, "session-alive probe failed",
		"a swallowed probe error must be logged, not invisible")
}

func TestEnsureCaptainAliveNoopWhenAlive(t *testing.T) {
	gw := &respawnGateway{alive: map[string]bool{"captain": true}}
	sup := &Supervisor{cfg: config.CaptainConfig{CaptainAgent: "captain", AdmiralAlias: "human"}, gw: gw}

	out := captureOutput(t, func() {
		sup.ensureCaptainAlive(context.Background(), time.Now())
	})

	require.Empty(t, gw.mails, "a live captain needs no alert")
	require.NotContains(t, out, "STANDING SESSION DOWN")
}

func TestRequeueReopensBeadsWithDeadAssignee(t *testing.T) {
	gw := &respawnGateway{alive: map[string]bool{"ship-live": true, "ship-dead": false}}
	beads := &fakeBeads{inProgress: []PipelineBead{
		{ID: "sp-1", Type: "bug", Assignee: "ship-dead"},
		{ID: "sp-2", Type: "feature", Assignee: "ship-live"},
	}}
	sup := &Supervisor{cfg: config.CaptainConfig{}, gw: gw, bc: beads}

	out := captureOutput(t, func() {
		sup.requeueOrphanedPipelineBeads(context.Background())
	})

	require.Equal(t, [][]string{{"sp-1", "session died"}}, beads.reopened,
		"only the bead with a dead assignee is re-queued")
	require.Contains(t, out, "requeueOrphanedPipelineBeads: checked 2 in-progress pipeline bead(s), found 1 orphaned, reset 1",
		"the pass must log a grep-able count of what it found and reset (sp-vvnw)")
}

// TestRequeueLogsZeroOrphansSoGoLiveIsAGrepNotAGuess is the sp-vvnw fix: on a
// fresh era there are typically zero stranded in-progress beads, and absence
// of effect must not read as absence of the pass having run. The line fires
// even when nothing was found or reset.
func TestRequeueLogsZeroOrphansSoGoLiveIsAGrepNotAGuess(t *testing.T) {
	gw := &respawnGateway{}
	beads := &fakeBeads{}
	sup := &Supervisor{cfg: config.CaptainConfig{}, gw: gw, bc: beads}

	out := captureOutput(t, func() {
		sup.requeueOrphanedPipelineBeads(context.Background())
	})

	require.Empty(t, beads.reopened, "nothing in progress means nothing to reopen")
	require.Contains(t, out, "requeueOrphanedPipelineBeads: checked 0 in-progress pipeline bead(s), found 0 orphaned, reset 0",
		"the zero case must still emit the count line — its absence is what makes go-live unverifiable")
}

func TestWatchkeeperRespectsKillSwitch(t *testing.T) {
	sup, s, _ := newBridgeSupervisor(t)
	gw := &respawnGateway{alive: map[string]bool{"captain": false}}
	beads := &fakeBeads{inProgress: []PipelineBead{{ID: "sp-1", Type: "bug", Assignee: "dead"}}}
	sup.gw = gw
	sup.bc = beads
	require.NoError(t, os.WriteFile(filepath.Join(s.dir, "DISABLED"), nil, 0o644))

	ran, err := sup.Tick(context.Background(), time.Now())

	require.NoError(t, err)
	require.False(t, ran)
	require.Empty(t, gw.mails, "no Admiral down-alert while DISABLED (watchkeeper gated by kill switch)")
	require.Empty(t, beads.reopened, "no requeue while DISABLED")
}
