package watchkeeper

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
		LastCredits:       123456,
	}
	require.NoError(t, saveSupervisorState(path, want))

	got, err := loadSupervisorState(path)
	require.NoError(t, err)
	require.True(t, want.LastSession.Equal(got.LastSession))
	require.True(t, want.LastSurveyorNudge.Equal(got.LastSurveyorNudge))
	require.Equal(t, want.Renudges, got.Renudges)
	require.Equal(t, want.Escalated, got.Escalated)
	require.Equal(t, want.LastCredits, got.LastCredits)
}

func TestSaveSupervisorStateCreatesStateDirIfMissing(t *testing.T) {
	dir := t.TempDir() // no "state" subdir pre-created
	path := filepath.Join(dir, "state", "supervisor-state.json")

	require.NoError(t, saveSupervisorState(path, supervisorState{LastSession: time.Now()}))
	require.FileExists(t, path)
}

func TestWriteStateAtomicLeavesNoTempFilesBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state", "supervisor-state.json")
	require.NoError(t, saveSupervisorState(path, supervisorState{LastSession: time.Now()}))

	entries, err := os.ReadDir(filepath.Join(dir, "state"))
	require.NoError(t, err)
	require.Len(t, entries, 1, "exactly one file (the final state file), no leftover temp files from the atomic write")
	require.Equal(t, supervisorStateFile, entries[0].Name())
}

// --- Captain-declared wake policy: dual-writer-safe persistence ---
//
// The supervisor (cadence fields: LastSession, LastSurveyorNudge, Renudges,
// Escalated, LastCredits) and the `captain wake set` CLI (policy fields:
// NextWakeAt, CreditsAbove, CreditsBelow, InterruptTypes, DeclaredAt) both
// write supervisor-state.json. Each writer must touch only the fields it
// owns: a read-merge-write round trip, not a wholesale overwrite.

func TestLoadWakePolicyMissingFileReturnsZeroValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "supervisor-state.json")

	policy, err := LoadWakePolicy(path)
	require.NoError(t, err)
	require.Nil(t, policy.NextWakeAt)
	require.Nil(t, policy.CreditsAbove)
	require.Nil(t, policy.CreditsBelow)
	require.Empty(t, policy.InterruptTypes)
}

func TestWakePolicyRoundTripsThroughSaveAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "supervisor-state.json")
	nextWake := time.Now().Add(3 * time.Hour).Truncate(time.Second)
	above := 500000
	below := 1000
	declaredAt := time.Now().Truncate(time.Second)
	want := WakePolicy{
		NextWakeAt:     &nextWake,
		CreditsAbove:   &above,
		CreditsBelow:   &below,
		InterruptTypes: []string{"ship.idle", "workflow.finished"},
		DeclaredAt:     declaredAt,
	}
	require.NoError(t, SaveWakePolicy(path, want))

	got, err := LoadWakePolicy(path)
	require.NoError(t, err)
	require.NotNil(t, got.NextWakeAt)
	require.True(t, want.NextWakeAt.Equal(*got.NextWakeAt))
	require.NotNil(t, got.CreditsAbove)
	require.Equal(t, *want.CreditsAbove, *got.CreditsAbove)
	require.NotNil(t, got.CreditsBelow)
	require.Equal(t, *want.CreditsBelow, *got.CreditsBelow)
	require.Equal(t, want.InterruptTypes, got.InterruptTypes)
	require.True(t, want.DeclaredAt.Equal(got.DeclaredAt))
}

func TestSaveWakePolicyPreservesCadenceFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "supervisor-state.json")
	cadence := supervisorState{
		LastSession:       time.Now().Truncate(time.Second),
		LastSurveyorNudge: time.Now().Add(-time.Hour).Truncate(time.Second),
		Renudges:          map[int64]int{7: 1},
		Escalated:         map[int64]bool{7: false},
		LastCredits:       250000,
	}
	require.NoError(t, saveCadenceState(path, cadence))

	above := 900000
	require.NoError(t, SaveWakePolicy(path, WakePolicy{CreditsAbove: &above}))

	got, err := loadSupervisorState(path)
	require.NoError(t, err)
	require.True(t, cadence.LastSession.Equal(got.LastSession), "cadence LastSession must survive a policy-only write")
	require.True(t, cadence.LastSurveyorNudge.Equal(got.LastSurveyorNudge))
	require.Equal(t, cadence.Renudges, got.Renudges)
	require.Equal(t, cadence.Escalated, got.Escalated)
	require.Equal(t, cadence.LastCredits, got.LastCredits)
	require.NotNil(t, got.CreditsAbove)
	require.Equal(t, above, *got.CreditsAbove)
}

func TestSaveCadenceStatePreservesWakePolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "supervisor-state.json")
	below := 1000
	require.NoError(t, SaveWakePolicy(path, WakePolicy{CreditsBelow: &below, InterruptTypes: []string{"ship.idle"}}))

	cadence := supervisorState{
		LastSession: time.Now().Truncate(time.Second),
		LastCredits: 42,
	}
	require.NoError(t, saveCadenceState(path, cadence))

	got, err := loadSupervisorState(path)
	require.NoError(t, err)
	require.True(t, cadence.LastSession.Equal(got.LastSession), "a cadence-only write must not clobber the previously-declared policy")
	require.Equal(t, cadence.LastCredits, got.LastCredits)
	require.NotNil(t, got.CreditsBelow)
	require.Equal(t, below, *got.CreditsBelow)
	require.Equal(t, []string{"ship.idle"}, got.InterruptTypes)
}
