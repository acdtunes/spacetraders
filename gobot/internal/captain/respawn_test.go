package captainsup

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
	spawned  [][]string
	mails    [][]string
	spawnErr error
}

func (g *respawnGateway) SendMail(_ context.Context, to, subject, body string) error {
	g.mails = append(g.mails, []string{to, subject, body})
	return nil
}

func (g *respawnGateway) Nudge(_ context.Context, alias, text string) error { return nil }

func (g *respawnGateway) SessionAlive(_ context.Context, alias string) (bool, error) {
	return g.alive[alias], nil
}

func (g *respawnGateway) SpawnSession(_ context.Context, agent, alias string) error {
	g.spawned = append(g.spawned, []string{agent, alias})
	return g.spawnErr
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

func TestEnsureCaptainAliveRespawnsDeadSession(t *testing.T) {
	gw := &respawnGateway{alive: map[string]bool{"captain": false}}
	sup := &Supervisor{cfg: config.CaptainConfig{CaptainAgent: "captain", AdmiralAlias: "human"}, gw: gw}

	sup.ensureCaptainAlive(context.Background())

	require.Equal(t, [][]string{{"captain", "captain"}}, gw.spawned)
	require.Empty(t, gw.mails, "no Admiral mail when respawn succeeds")
}

func TestEnsureCaptainAliveNoopWhenAlive(t *testing.T) {
	gw := &respawnGateway{alive: map[string]bool{"captain": true}}
	sup := &Supervisor{cfg: config.CaptainConfig{CaptainAgent: "captain"}, gw: gw}

	sup.ensureCaptainAlive(context.Background())

	require.Empty(t, gw.spawned)
}

func TestEnsureCaptainAliveMailsAdmiralWhenRespawnFails(t *testing.T) {
	gw := &respawnGateway{alive: map[string]bool{"captain": false}, spawnErr: errors.New("no tmux")}
	sup := &Supervisor{cfg: config.CaptainConfig{CaptainAgent: "captain", AdmiralAlias: "human"}, gw: gw}

	sup.ensureCaptainAlive(context.Background())

	require.Len(t, gw.spawned, 1, "respawn attempted once")
	require.Len(t, gw.mails, 1, "Admiral alerted only after respawn fails")
	require.Equal(t, "human", gw.mails[0][0])
}

func TestRequeueReopensBeadsWithDeadAssignee(t *testing.T) {
	gw := &respawnGateway{alive: map[string]bool{"ship-live": true, "ship-dead": false}}
	beads := &fakeBeads{inProgress: []PipelineBead{
		{ID: "sp-1", Type: "bug", Assignee: "ship-dead"},
		{ID: "sp-2", Type: "feature", Assignee: "ship-live"},
	}}
	sup := &Supervisor{cfg: config.CaptainConfig{}, gw: gw, bc: beads}

	sup.requeueOrphanedPipelineBeads(context.Background())

	require.Equal(t, [][]string{{"sp-1", "session died"}}, beads.reopened,
		"only the bead with a dead assignee is re-queued")
}

func TestRespawnRespectsKillSwitch(t *testing.T) {
	sup, s, _ := newBridgeSupervisor(t)
	gw := &respawnGateway{alive: map[string]bool{"captain": false}}
	beads := &fakeBeads{inProgress: []PipelineBead{{ID: "sp-1", Type: "bug", Assignee: "dead"}}}
	sup.gw = gw
	sup.bc = beads
	require.NoError(t, os.WriteFile(filepath.Join(s.dir, "DISABLED"), nil, 0o644))

	ran, err := sup.Tick(context.Background(), time.Now())

	require.NoError(t, err)
	require.False(t, ran)
	require.Empty(t, gw.spawned, "no respawn while DISABLED")
	require.Empty(t, beads.reopened, "no requeue while DISABLED")
}
