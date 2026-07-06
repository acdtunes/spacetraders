package captainsup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

type scriptedStatus struct {
	status *api.ServerStatus
	err    error
	calls  int
}

func (s *scriptedStatus) GetServerStatus(_ context.Context) (*api.ServerStatus, error) {
	s.calls++
	return s.status, s.err
}

func newUniverseSupervisor(t *testing.T) (*Supervisor, *gorm.DB, int, *fakeGateway, string) {
	t.Helper()
	db, playerID, store := setupDB(t)
	dir := t.TempDir()
	cfg := config.CaptainConfig{
		Enabled: true, PlayerID: playerID, WorkspaceDir: dir,
		PollIntervalSeconds: 30, HeartbeatMinutes: 45, MaxSessionsPerHour: 6,
		EngineMode: "bridge", CaptainAgent: "captain", AdmiralAlias: "human",
		AckTimeoutMinutes: 10, EscalateAfterRenudges: 3, UniverseCheckHours: 24,
	}
	gw := &fakeGateway{}
	sup, err := NewSupervisor(db, store, NewWorkspace(dir), cfg)
	require.NoError(t, err)
	sup.gw = gw
	return sup, db, playerID, gw, dir
}

func seedEra(t *testing.T, db *gorm.DB, playerID int, name, resetDate string) {
	t.Helper()
	d, err := time.Parse("2006-01-02", resetDate)
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{
		Name: name, AgentSymbol: name, PlayerID: playerID, UniverseResetDate: &d,
	}).Error)
}

func TestUniverseResetTouchesDisabledAndMailsOnMismatch(t *testing.T) {
	sup, db, playerID, gw, dir := newUniverseSupervisor(t)
	seedEra(t, db, playerID, "torwind", "2026-07-05")
	sup.SetUniverseWatch(&scriptedStatus{status: &api.ServerStatus{ResetDate: "2026-07-06"}},
		persistence.NewEraRepository(db))

	ran, err := sup.Tick(context.Background(), time.Now())
	require.NoError(t, err)
	require.False(t, ran, "reset detection halts the tick")

	data, err := os.ReadFile(filepath.Join(dir, "DISABLED"))
	require.NoError(t, err)
	require.Contains(t, string(data), "torwind")
	require.Contains(t, string(data), "2026-07-06")

	require.Equal(t, 1, mailsTo(gw, "human"))
}

func TestUniverseResetDoesNotReMailOrRewriteWhileDisabled(t *testing.T) {
	sup, db, playerID, gw, dir := newUniverseSupervisor(t)
	seedEra(t, db, playerID, "torwind", "2026-07-05")
	st := &scriptedStatus{status: &api.ServerStatus{ResetDate: "2026-07-06"}}
	sup.SetUniverseWatch(st, persistence.NewEraRepository(db))

	now := time.Now()
	sup.checkUniverseReset(context.Background(), now)
	first, err := os.ReadFile(filepath.Join(dir, "DISABLED"))
	require.NoError(t, err)

	st.status = &api.ServerStatus{ResetDate: "2026-07-07"}
	sup.lastUniverseCheck = time.Time{}
	sup.checkUniverseReset(context.Background(), now.Add(time.Hour))

	after, err := os.ReadFile(filepath.Join(dir, "DISABLED"))
	require.NoError(t, err)
	require.Equal(t, string(first), string(after), "existing DISABLED is never rewritten")
	require.Equal(t, 1, mailsTo(gw, "human"), "no second mail while DISABLED exists")
}

func TestUniverseResetNoopWhenResetDateMatches(t *testing.T) {
	sup, db, playerID, gw, dir := newUniverseSupervisor(t)
	seedEra(t, db, playerID, "torwind", "2026-07-06")
	sup.SetUniverseWatch(&scriptedStatus{status: &api.ServerStatus{ResetDate: "2026-07-06"}},
		persistence.NewEraRepository(db))

	sup.checkUniverseReset(context.Background(), time.Now())

	require.NoFileExists(t, filepath.Join(dir, "DISABLED"))
	require.Equal(t, 0, mailsTo(gw, "human"))
}

func TestUniverseResetNoopWhenNoEraRow(t *testing.T) {
	sup, db, _, gw, dir := newUniverseSupervisor(t)
	sup.SetUniverseWatch(&scriptedStatus{status: &api.ServerStatus{ResetDate: "2026-07-06"}},
		persistence.NewEraRepository(db))

	sup.checkUniverseReset(context.Background(), time.Now())

	require.NoFileExists(t, filepath.Join(dir, "DISABLED"))
	require.Equal(t, 0, mailsTo(gw, "human"))
}

func TestUniverseResetFailsQuietOnAPIErrorAndRetries(t *testing.T) {
	sup, db, playerID, gw, dir := newUniverseSupervisor(t)
	seedEra(t, db, playerID, "torwind", "2026-07-05")
	st := &scriptedStatus{err: errors.New("boom")}
	sup.SetUniverseWatch(st, persistence.NewEraRepository(db))

	sup.checkUniverseReset(context.Background(), time.Now())
	require.NoFileExists(t, filepath.Join(dir, "DISABLED"))
	require.Equal(t, 0, mailsTo(gw, "human"))

	sup.checkUniverseReset(context.Background(), time.Now().Add(time.Minute))
	require.Equal(t, 2, st.calls, "API error does not consume the check cadence; next tick retries")
}
