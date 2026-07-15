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

// --- sp-wfut: a consumed one-shot credits bound is persisted as absent ---
//
// sp-wfut retires the sp-l6pz credits_above_fired/credits_below_fired edge-state
// entirely: a captain-set bound is ONE-SHOT and consumed by nilling it in the
// embedded WakePolicy on the delivered wake. The consumption therefore rides the
// ordinary WakePolicy persistence path (SaveWakePolicy), whose cross-writer
// preservation is already covered by the WakePolicy tests above; the Tick-level
// consume-and-persist behaviour (RULINGS #2) is covered by the sp-wfut tests in
// wake_test.go. Here we only pin that a cadence write does not resurrect a bound
// the captain never set — the same dual-writer property, now with no fired-flag.
func TestCadenceSaveDoesNotIntroduceCreditsBound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "supervisor-state.json")
	require.NoError(t, saveCadenceState(path, supervisorState{
		LastSession: time.Now().Truncate(time.Second),
		LastCredits: 42,
	}))

	got, err := loadSupervisorState(path)
	require.NoError(t, err)
	require.Nil(t, got.CreditsAbove, "a cadence-only write must not introduce a credits bound")
	require.Nil(t, got.CreditsBelow, "a cadence-only write must not introduce a credits bound")
	require.Equal(t, 42, got.LastCredits)
}

// --- sp-zlfv: RegimePolicy persistence, mirroring the WakePolicy tests
// above. supervisor-state.json now has three independent owners (cadence,
// WakePolicy, RegimePolicy), so the cross-policy preservation tests below
// guard the same dual-writer-safety property the WakePolicy tests already
// establish, extended to the new third writer.

func TestLoadRegimePolicyMissingFileReturnsZeroValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "supervisor-state.json")

	policy, err := LoadRegimePolicy(path)
	require.NoError(t, err)
	require.Empty(t, policy.Tripwires)
}

func TestRegimePolicyRoundTripsThroughSaveAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "supervisor-state.json")
	threshold := 200
	multiplier := 3.0
	createdAt := time.Now().Truncate(time.Second)
	want := RegimePolicy{
		Tripwires: []RegimeTripwire{
			{Good: "ORE", Direction: "bid-above", Threshold: &threshold, Window: 30 * time.Minute, CreatedAt: createdAt},
			{Good: "GAS", Direction: "bid-above", Multiplier: &multiplier, Window: 4 * time.Hour, CreatedAt: createdAt},
		},
	}
	require.NoError(t, SaveRegimePolicy(path, want))

	got, err := LoadRegimePolicy(path)
	require.NoError(t, err)
	require.Len(t, got.Tripwires, 2)

	require.Equal(t, "ORE", got.Tripwires[0].Good)
	require.Equal(t, "bid-above", got.Tripwires[0].Direction)
	require.NotNil(t, got.Tripwires[0].Threshold)
	require.Equal(t, threshold, *got.Tripwires[0].Threshold)
	require.Nil(t, got.Tripwires[0].Multiplier)
	require.Equal(t, 30*time.Minute, got.Tripwires[0].Window)
	require.True(t, createdAt.Equal(got.Tripwires[0].CreatedAt))

	require.Equal(t, "GAS", got.Tripwires[1].Good)
	require.NotNil(t, got.Tripwires[1].Multiplier)
	require.Equal(t, multiplier, *got.Tripwires[1].Multiplier)
	require.Nil(t, got.Tripwires[1].Threshold)
	require.Equal(t, 4*time.Hour, got.Tripwires[1].Window)
}

func TestSaveRegimePolicyPreservesCadenceFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "supervisor-state.json")
	cadence := supervisorState{
		LastSession:       time.Now().Truncate(time.Second),
		LastSurveyorNudge: time.Now().Add(-time.Hour).Truncate(time.Second),
		Renudges:          map[int64]int{7: 1},
		Escalated:         map[int64]bool{7: false},
		LastCredits:       250000,
	}
	require.NoError(t, saveCadenceState(path, cadence))

	threshold := 150
	require.NoError(t, SaveRegimePolicy(path, RegimePolicy{
		Tripwires: []RegimeTripwire{{Good: "GAS", Direction: "bid-above", Threshold: &threshold, Window: time.Hour}},
	}))

	got, err := loadSupervisorState(path)
	require.NoError(t, err)
	require.True(t, cadence.LastSession.Equal(got.LastSession), "cadence LastSession must survive a regime-policy-only write")
	require.True(t, cadence.LastSurveyorNudge.Equal(got.LastSurveyorNudge))
	require.Equal(t, cadence.Renudges, got.Renudges)
	require.Equal(t, cadence.Escalated, got.Escalated)
	require.Equal(t, cadence.LastCredits, got.LastCredits)
	require.Len(t, got.Tripwires, 1)
	require.Equal(t, "GAS", got.Tripwires[0].Good)
}

func TestSaveCadenceStatePreservesRegimePolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "supervisor-state.json")
	threshold := 200
	require.NoError(t, SaveRegimePolicy(path, RegimePolicy{
		Tripwires: []RegimeTripwire{{Good: "ORE", Direction: "bid-above", Threshold: &threshold, Window: 30 * time.Minute}},
	}))

	cadence := supervisorState{LastSession: time.Now().Truncate(time.Second), LastCredits: 42}
	require.NoError(t, saveCadenceState(path, cadence))

	got, err := loadSupervisorState(path)
	require.NoError(t, err)
	require.True(t, cadence.LastSession.Equal(got.LastSession), "a cadence-only write must not clobber the previously-declared regime policy")
	require.Equal(t, cadence.LastCredits, got.LastCredits)
	require.Len(t, got.Tripwires, 1)
	require.Equal(t, "ORE", got.Tripwires[0].Good)
}

func TestSaveRegimePolicyPreservesWakePolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "supervisor-state.json")
	below := 1000
	require.NoError(t, SaveWakePolicy(path, WakePolicy{CreditsBelow: &below, InterruptTypes: []string{"ship.idle"}}))

	threshold := 200
	require.NoError(t, SaveRegimePolicy(path, RegimePolicy{
		Tripwires: []RegimeTripwire{{Good: "ORE", Direction: "bid-above", Threshold: &threshold, Window: 30 * time.Minute}},
	}))

	gotWake, err := LoadWakePolicy(path)
	require.NoError(t, err)
	require.NotNil(t, gotWake.CreditsBelow, "a regime-policy write must not clobber the previously-declared wake policy")
	require.Equal(t, below, *gotWake.CreditsBelow)
	require.Equal(t, []string{"ship.idle"}, gotWake.InterruptTypes)

	gotRegime, err := LoadRegimePolicy(path)
	require.NoError(t, err)
	require.Len(t, gotRegime.Tripwires, 1)
}

func TestSaveWakePolicyPreservesRegimePolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "supervisor-state.json")
	threshold := 200
	require.NoError(t, SaveRegimePolicy(path, RegimePolicy{
		Tripwires: []RegimeTripwire{{Good: "ORE", Direction: "bid-above", Threshold: &threshold, Window: 30 * time.Minute}},
	}))

	above := 500000
	require.NoError(t, SaveWakePolicy(path, WakePolicy{CreditsAbove: &above}))

	gotRegime, err := LoadRegimePolicy(path)
	require.NoError(t, err)
	require.Len(t, gotRegime.Tripwires, 1, "a wake-policy write must not clobber the previously-declared regime policy")
	require.Equal(t, "ORE", gotRegime.Tripwires[0].Good)

	gotWake, err := LoadWakePolicy(path)
	require.NoError(t, err)
	require.NotNil(t, gotWake.CreditsAbove)
	require.Equal(t, above, *gotWake.CreditsAbove)
}

// --- sp-q9s7: dynamic runtime GAG (soft stand-down) persistence ---

func TestLoadGagPolicyMissingFileReturnsUngagged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "supervisor-state.json")
	got, err := LoadGagPolicy(path)
	require.NoError(t, err, "a missing state file is not an error — it means ungagged")
	require.False(t, got.Gagged, "no config means the supervisor runs normally")
}

func TestGagPolicyRoundTripsThroughSaveAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "supervisor-state.json")
	declaredAt := time.Date(2026, 7, 15, 9, 0, 0, 0, time.UTC)

	require.NoError(t, SaveGagPolicy(path, GagPolicy{
		Gagged: true, GagReason: "admiral halt", GagDeclaredAt: declaredAt,
	}))

	got, err := LoadGagPolicy(path)
	require.NoError(t, err)
	require.True(t, got.Gagged)
	require.Equal(t, "admiral halt", got.GagReason)
	require.True(t, got.GagDeclaredAt.Equal(declaredAt))
}

// The gag is a SEPARATE embedded writer sharing the one state file with the
// wake/regime/cadence policies. A gag toggle must never clobber a co-resident
// policy — and, specifically, GagPolicy's own timestamp must not collide with
// WakePolicy.declared_at at the same embedding depth (which would silently drop
// BOTH from the JSON). This asserts all three writers coexist intact.
func TestSaveGagPolicyPreservesWakeAndRegimePolicies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "supervisor-state.json")
	below := 1000
	wakeDeclaredAt := time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	require.NoError(t, SaveWakePolicy(path, WakePolicy{CreditsBelow: &below, DeclaredAt: wakeDeclaredAt}))
	threshold := 200
	require.NoError(t, SaveRegimePolicy(path, RegimePolicy{
		Tripwires: []RegimeTripwire{{Good: "ORE", Direction: "bid-above", Threshold: &threshold, Window: 30 * time.Minute}},
	}))

	require.NoError(t, SaveGagPolicy(path, GagPolicy{Gagged: true, GagReason: "deploy freeze"}))

	gotGag, err := LoadGagPolicy(path)
	require.NoError(t, err)
	require.True(t, gotGag.Gagged)

	gotWake, err := LoadWakePolicy(path)
	require.NoError(t, err)
	require.NotNil(t, gotWake.CreditsBelow, "a gag write must not clobber the declared wake policy")
	require.Equal(t, below, *gotWake.CreditsBelow)
	require.True(t, gotWake.DeclaredAt.Equal(wakeDeclaredAt),
		"wake declared_at must survive a gag write (no JSON-tag collision)")

	gotRegime, err := LoadRegimePolicy(path)
	require.NoError(t, err)
	require.Len(t, gotRegime.Tripwires, 1, "a gag write must not clobber the declared regime policy")
}

// Clearing the gag (gag off) must land Gagged=false through Save/Load, not leave
// a stale true behind — the falling edge the supervisor resumes on.
func TestSaveGagPolicyOffClearsPriorGag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "supervisor-state.json")
	require.NoError(t, SaveGagPolicy(path, GagPolicy{Gagged: true, GagReason: "x"}))
	require.NoError(t, SaveGagPolicy(path, GagPolicy{Gagged: false}))

	got, err := LoadGagPolicy(path)
	require.NoError(t, err)
	require.False(t, got.Gagged, "gag off must persist as ungagged")
}
