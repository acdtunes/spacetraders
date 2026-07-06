package captainsup

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWorkspaceStatePathUnderStateSubdir(t *testing.T) {
	ws := NewWorkspace("/tmp/example-workspace")
	require.Equal(t, filepath.Join("/tmp/example-workspace", "state", "supervisor-state.json"), ws.StatePath())
}

func TestLoadSupervisorStateMissingFileReturnsZeroValue(t *testing.T) {
	dir := t.TempDir()
	st, err := loadSupervisorState(filepath.Join(dir, "state", "supervisor-state.json"))
	require.NoError(t, err)
	require.True(t, st.LastSession.IsZero())
	require.True(t, st.LastSurveyorNudge.IsZero())
	require.Empty(t, st.Renudges)
	require.Empty(t, st.Escalated)
}

func TestLoadSupervisorStateCorruptFileReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "supervisor-state.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o644))

	_, err := loadSupervisorState(path)
	require.Error(t, err)
}

func TestSaveAndLoadSupervisorStateRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "supervisor-state.json")

	want := supervisorState{
		LastSession:       time.Now().Truncate(time.Second),
		LastSurveyorNudge: time.Now().Add(-3 * 24 * time.Hour).Truncate(time.Second),
		Renudges:          map[int64]int{101: 2, 202: 0},
		Escalated:         map[int64]bool{101: true},
	}
	require.NoError(t, saveSupervisorState(path, want))

	got, err := loadSupervisorState(path)
	require.NoError(t, err)
	require.True(t, want.LastSession.Equal(got.LastSession))
	require.True(t, want.LastSurveyorNudge.Equal(got.LastSurveyorNudge))
	require.Equal(t, want.Renudges, got.Renudges)
	require.Equal(t, want.Escalated, got.Escalated)
}

func TestSaveSupervisorStateCreatesStateDirIfMissing(t *testing.T) {
	dir := t.TempDir() // no "state" subdir pre-created
	path := filepath.Join(dir, "state", "supervisor-state.json")

	require.NoError(t, saveSupervisorState(path, supervisorState{LastSession: time.Now()}))
	require.FileExists(t, path)
}
