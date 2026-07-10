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

// A persist against a container row that no longer exists (already terminalized/reaped)
// returns an error the caller logs and swallows — it must never panic or silently succeed.
func TestTourRepositionPersister_MissingContainer_ReturnsError(t *testing.T) {
	s, _, playerID := newRecoveryTestServer(t)

	persister := NewTourRepositionConfigPersister(s.containerRepo)
	err := persister.PersistRepositionState(context.Background(), "tour-run-DOES-NOT-EXIST", playerID,
		tradingCmd.RepositionEpisode{InProgress: true, TargetSystem: "X1-S2", TargetWaypoint: "X1-S2-A"})
	require.Error(t, err, "persisting to a missing container must surface an error (the caller logs and continues)")
}
