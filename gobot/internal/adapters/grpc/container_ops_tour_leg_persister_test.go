package grpc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
)

// Recording the leg a hull is flying must MERGE the sink and its goods into the run's
// persisted config — preserving every launch knob the recovery rebuild also needs — and a
// full round trip back through buildTourCoordinatorCommand must reload it, so a bounce
// mid-leg finishes the discharge at the sink the cargo was already going to (RULINGS #2).
func TestTourLegPersister_MergesInFlightLegAndRoundTripsThroughRebuild(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	const containerID = "tour-run-LEG-PERSIST"
	launchConfig := `{"ship_symbol":"SHIP-A","container_id":"tour-run-LEG-PERSIST","iterations":-1,"max_spend":300000}`
	insertRunningContainer(t, db, containerID, "tour_run", "TRADING", launchConfig, playerID, nil)

	persister := NewTourRepositionConfigPersister(s.containerRepo)
	require.NoError(t, persister.PersistTourLegState(context.Background(), containerID, playerID,
		tradingCmd.TourLegState{Waypoint: "X1-S2-B", Goods: "FABRICS,MEDICINE"}))

	var model persistence.ContainerModel
	require.NoError(t, db.First(&model, "id = ?", containerID).Error)
	var merged map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(model.Config), &merged))
	require.Equal(t, "X1-S2-B", merged["tour_leg_waypoint"])
	require.Equal(t, "FABRICS,MEDICINE", merged["tour_leg_goods"])
	require.Equal(t, "SHIP-A", merged["ship_symbol"], "the merge must preserve the launch knobs")
	require.EqualValues(t, -1, merged["iterations"])
	require.EqualValues(t, 300000, merged["max_spend"])

	rebuilt, err := s.buildCommandForType("tour_run", merged, playerID, containerID)
	require.NoError(t, err)
	tourCmd, ok := rebuilt.(*tradingCmd.RunTourCoordinatorCommand)
	require.True(t, ok, "tour_run must rebuild a RunTourCoordinatorCommand")
	require.Equal(t, "X1-S2-B", tourCmd.TourLegWaypoint, "the rebuilt command must reload the sink")
	require.Equal(t, "FABRICS,MEDICINE", tourCmd.TourLegGoods, "the rebuilt command must reload the goods in flight")
	require.Equal(t, -1, tourCmd.Iterations, "the continuous mode must survive the leg merge")
}

// Clearing the leg (the zero state, written by a leg that discharges nothing) overwrites the
// prior record so a second restart cannot re-fly a leg that has already been flown.
func TestTourLegPersister_ClearsTheLegOnceItIsFlown(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	const containerID = "tour-run-LEG-CLEAR"
	insertRunningContainer(t, db, containerID, "tour_run", "TRADING",
		`{"ship_symbol":"SHIP-A","container_id":"tour-run-LEG-CLEAR","iterations":-1,"tour_leg_waypoint":"X1-S2-B","tour_leg_goods":"FABRICS"}`,
		playerID, nil)

	persister := NewTourRepositionConfigPersister(s.containerRepo)
	require.NoError(t, persister.PersistTourLegState(context.Background(), containerID, playerID, tradingCmd.TourLegState{}))

	var model persistence.ContainerModel
	require.NoError(t, db.First(&model, "id = ?", containerID).Error)
	var merged map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(model.Config), &merged))
	require.Equal(t, "", merged["tour_leg_waypoint"], "the sink must be cleared once the leg is flown")
	require.Equal(t, "", merged["tour_leg_goods"])

	rebuilt, err := s.buildCommandForType("tour_run", merged, playerID, containerID)
	require.NoError(t, err)
	tourCmd := rebuilt.(*tradingCmd.RunTourCoordinatorCommand)
	require.Empty(t, tourCmd.TourLegWaypoint, "a cleared config must rebuild a run that resumes no leg")
}

// A container launched before this key existed rebuilds with no leg in flight, so the resume
// declines and the run plans exactly as it does today.
func TestTourLegPersister_AbsentKeysRebuildWithNoLegInFlight(t *testing.T) {
	s, _, playerID := newRecoveryTestServer(t)

	rebuilt, err := s.buildCommandForType("tour_run", map[string]interface{}{
		"ship_symbol": "SHIP-A", "iterations": -1,
	}, playerID, "tour-run-LEG-LEGACY")
	require.NoError(t, err)
	tourCmd := rebuilt.(*tradingCmd.RunTourCoordinatorCommand)
	require.Empty(t, tourCmd.TourLegWaypoint)
	require.Empty(t, tourCmd.TourLegGoods)
}
