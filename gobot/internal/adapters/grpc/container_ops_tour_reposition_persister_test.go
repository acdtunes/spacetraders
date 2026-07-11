package grpc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
)

// sp-zhii: committing a reposition must MERGE the in-flight destination into the run's
// persisted config — adding reposition_in_progress + target system/waypoint while
// preserving every launch knob the recovery rebuild also needs — and a full round-trip back
// through buildTourCoordinatorCommand must then reload it, so a restart mid-jump resumes
// toward the same ground (RULINGS #2).
func TestTourRepositionPersister_MergesInFlightStateAndRoundTripsThroughRebuild(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	const containerID = "tour-run-ZHII-PERSIST"
	launchConfig := `{"ship_symbol":"SHIP-A","container_id":"tour-run-ZHII-PERSIST","iterations":-1,"max_spend":300000}`
	insertRunningContainer(t, db, containerID, "tour_run", "TRADING", launchConfig, playerID, nil)

	persister := NewTourRepositionConfigPersister(s.containerRepo)
	require.NoError(t, persister.PersistRepositionState(context.Background(), containerID, playerID,
		tradingCmd.RepositionEpisode{InProgress: true, TargetSystem: "X1-S2", TargetWaypoint: "X1-S2-A"}))

	// The persisted config must now carry the in-flight destination AND still every launch knob.
	var model persistence.ContainerModel
	require.NoError(t, db.First(&model, "id = ?", containerID).Error)
	var merged map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(model.Config), &merged))
	require.Equal(t, true, merged["reposition_in_progress"], "the in-flight flag must be merged in")
	require.Equal(t, "X1-S2", merged["reposition_target_system"])
	require.Equal(t, "X1-S2-A", merged["reposition_target_waypoint"])
	require.Equal(t, "SHIP-A", merged["ship_symbol"], "the merge must preserve the launch knobs")
	require.EqualValues(t, -1, merged["iterations"])
	require.EqualValues(t, 300000, merged["max_spend"])

	// The whole point: a recovery rebuild from the merged config reloads the reposition
	// target, so the resumed run completes the jump instead of re-planning at an intermediate.
	rebuilt, err := s.buildCommandForType("tour_run", merged, playerID, containerID)
	require.NoError(t, err)
	tourCmd, ok := rebuilt.(*tradingCmd.RunTourCoordinatorCommand)
	require.True(t, ok, "tour_run must rebuild a RunTourCoordinatorCommand")
	require.True(t, tourCmd.RepositionInProgress, "the rebuilt command must reload the in-flight flag")
	require.Equal(t, "X1-S2", tourCmd.RepositionTargetSystem, "the rebuilt command must reload the target system")
	require.Equal(t, "X1-S2-A", tourCmd.RepositionTargetWaypoint, "the rebuilt command must reload the target waypoint")
	require.Equal(t, -1, tourCmd.Iterations, "the continuous mode must survive the reposition merge")
}

// Clearing the reposition (InProgress=false, after the jump landed) overwrites the prior
// in-flight state so a second restart does NOT re-resume a completed reposition.
func TestTourRepositionPersister_ClearsInFlightStateAfterLanding(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	const containerID = "tour-run-ZHII-CLEAR"
	insertRunningContainer(t, db, containerID, "tour_run", "TRADING",
		`{"ship_symbol":"SHIP-A","container_id":"tour-run-ZHII-CLEAR","iterations":-1,"reposition_in_progress":true,"reposition_target_system":"X1-S2","reposition_target_waypoint":"X1-S2-A"}`,
		playerID, nil)

	persister := NewTourRepositionConfigPersister(s.containerRepo)
	require.NoError(t, persister.PersistRepositionState(context.Background(), containerID, playerID,
		tradingCmd.RepositionEpisode{InProgress: false}))

	var model persistence.ContainerModel
	require.NoError(t, db.First(&model, "id = ?", containerID).Error)
	var merged map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(model.Config), &merged))
	require.Equal(t, false, merged["reposition_in_progress"], "the in-flight flag must be cleared after landing")

	// A rebuild from the cleared config must NOT resume a reposition.
	rebuilt, err := s.buildCommandForType("tour_run", merged, playerID, containerID)
	require.NoError(t, err)
	tourCmd := rebuilt.(*tradingCmd.RunTourCoordinatorCommand)
	require.False(t, tourCmd.RepositionInProgress, "a cleared config must rebuild a run that does NOT resume a reposition")
}

// sp-kl16 THE O34Q-CLASS PIN. The tour-reposition jump bound is a launch knob, so it must
// survive the REAL persist boundary a recovery restart crosses: written into the launch config
// (container_ops_tour.go), preserved by the mid-run PersistRepositionState read-modify-write
// (which merges only the reposition-state keys), and read back by buildTourCoordinatorCommand.
// The scout bug (sp-o34q) was a persist path that DROPPED the bound because its coordinator fakes
// captured the command STRUCT directly, never crossing the serialize → config → rebuild boundary.
// This pin models that real boundary — a persisted config row → the actual merge → the actual
// rebuild — so a dropped bound here fails the test, not a fake that hides it.
func TestTourRepositionJumpBound_RoundTripsThroughRebuildAcrossPersist(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	const containerID = "tour-run-KL16-BOUND"
	// The launch config carries reposition_jump_bound=9 exactly as StartTourRun writes it from
	// [trade_fleet].reposition_jump_bound.
	launchConfig := `{"ship_symbol":"SHIP-A","container_id":"tour-run-KL16-BOUND","iterations":-1,"reposition_jump_bound":9}`
	insertRunningContainer(t, db, containerID, "tour_run", "TRADING", launchConfig, playerID, nil)

	persister := NewTourRepositionConfigPersister(s.containerRepo)
	require.NoError(t, persister.PersistRepositionState(context.Background(), containerID, playerID,
		tradingCmd.RepositionEpisode{InProgress: true, TargetSystem: "X1-S2", TargetWaypoint: "X1-S2-A"}))

	// The merge must PRESERVE the bound alongside the in-flight reposition state.
	var model persistence.ContainerModel
	require.NoError(t, db.First(&model, "id = ?", containerID).Error)
	var merged map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(model.Config), &merged))
	require.EqualValues(t, 9, merged["reposition_jump_bound"], "the mid-run reposition-state merge must not drop the jump bound (the o34q class)")

	// The recovery rebuild from the merged config must reload the bound, so the resumed run's
	// reposition still routes over the stored adjacency at the captain's bound — never silently
	// degraded to the strict 5-jump resolver (the o34q failure the scout suffered live).
	rebuilt, err := s.buildCommandForType("tour_run", merged, playerID, containerID)
	require.NoError(t, err)
	tourCmd, ok := rebuilt.(*tradingCmd.RunTourCoordinatorCommand)
	require.True(t, ok, "tour_run must rebuild a RunTourCoordinatorCommand")
	require.Equal(t, 9, tourCmd.RepositionJumpBound, "the rebuilt command must reload the reposition jump bound across the persist boundary")
}

// An ABSENT reposition_jump_bound (a tour launched from a config that predates the knob, or the
// captain leaving it unset) rebuilds to 0, which the coordinator resolves to its own default (12,
// resolveRepositionJumpBound) — so the bound is never a magic value baked at the persist layer.
func TestTourRepositionJumpBound_AbsentRebuildsToZeroForCoordinatorDefault(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	const containerID = "tour-run-KL16-ABSENT"
	insertRunningContainer(t, db, containerID, "tour_run", "TRADING",
		`{"ship_symbol":"SHIP-A","container_id":"tour-run-KL16-ABSENT","iterations":-1}`, playerID, nil)

	var cfg map[string]interface{}
	var model persistence.ContainerModel
	require.NoError(t, db.First(&model, "id = ?", containerID).Error)
	require.NoError(t, json.Unmarshal([]byte(model.Config), &cfg))

	rebuilt, err := s.buildCommandForType("tour_run", cfg, playerID, containerID)
	require.NoError(t, err)
	tourCmd := rebuilt.(*tradingCmd.RunTourCoordinatorCommand)
	require.Equal(t, 0, tourCmd.RepositionJumpBound, "an absent knob must rebuild to 0 so the coordinator applies its own default (never a persist-layer magic value)")
}

// A persist against a container row that no longer exists (already terminalized/reaped)
// returns an error the caller logs and swallows — it must never panic or silently succeed.
func TestTourRepositionPersister_MissingContainer_ReturnsError(t *testing.T) {
	s, _, playerID := newRecoveryTestServer(t)

	persister := NewTourRepositionConfigPersister(s.containerRepo)
	err := persister.PersistRepositionState(context.Background(), "tour-run-DOES-NOT-EXIST", playerID,
		tradingCmd.RepositionEpisode{InProgress: true, TargetSystem: "X1-S2", TargetWaypoint: "X1-S2-A"})
	require.Error(t, err, "persisting to a missing container must surface an error (the caller logs and continues)")
}
