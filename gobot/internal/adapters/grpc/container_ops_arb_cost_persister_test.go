package grpc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
)

// sp-dkj7: the config-backed persister must MERGE the buy cost into the run's persisted
// config — adding prior_attempt_cost while preserving every launch knob the recovery
// rebuild also needs — and a full round-trip back through buildArbCoordinatorCommand must
// then reload it, so a restart-resumed run reports honest P&L (RULINGS #2).
func TestArbCostConfigPersister_MergesCostAndRoundTripsThroughRebuild(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	const containerID = "arb-run-DKJ7-PERSIST"
	launchConfig := `{"ship_symbol":"SHIP-A","container_id":"arb-run-DKJ7-PERSIST","good":"WIDGET","buy_at":"X1-TR-EXPORT","sell_at":"X1-TR-IMPORT","max_spend":100000,"working_capital_reserve":50000}`
	insertRunningContainer(t, db, containerID, "arb_run", "TRADING", launchConfig, playerID, nil)

	persister := NewArbCostConfigPersister(s.containerRepo)
	require.NoError(t, persister.PersistBuyCost(context.Background(), containerID, playerID, 80000))

	// The persisted config must now carry the cost AND still every launch knob.
	var model persistence.ContainerModel
	require.NoError(t, db.First(&model, "id = ?", containerID).Error)
	var merged map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(model.Config), &merged))
	require.EqualValues(t, 80000, merged["prior_attempt_cost"], "the buy cost must be merged into the config")
	require.Equal(t, "SHIP-A", merged["ship_symbol"], "the merge must preserve the launch knobs")
	require.Equal(t, "WIDGET", merged["good"])
	require.Equal(t, "X1-TR-EXPORT", merged["buy_at"])
	require.Equal(t, "X1-TR-IMPORT", merged["sell_at"])
	require.EqualValues(t, 100000, merged["max_spend"])
	require.EqualValues(t, 50000, merged["working_capital_reserve"])

	// The whole point: a recovery rebuild from the merged config reloads the cost, so the
	// resumed run's accounting restores the prior basis instead of starting at zero.
	rebuilt, err := s.buildCommandForType("arb_run", merged, playerID, containerID)
	require.NoError(t, err)
	arbCmd, ok := rebuilt.(*tradingCmd.RunArbCoordinatorCommand)
	require.True(t, ok, "arb_run must rebuild a RunArbCoordinatorCommand")
	require.Equal(t, 80000, arbCmd.PriorAttemptCost, "the rebuilt command must reload the persisted buy cost")
	require.Equal(t, 100000, arbCmd.MaxSpend, "launch caps must survive the cost merge")
}

// A second buy-cost persist overwrites the first (last write wins) rather than accreting —
// a run only ever has one tranche cost to carry.
func TestArbCostConfigPersister_OverwritesPriorValue(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	const containerID = "arb-run-DKJ7-OVERWRITE"
	insertRunningContainer(t, db, containerID, "arb_run", "TRADING",
		`{"ship_symbol":"SHIP-A","container_id":"arb-run-DKJ7-OVERWRITE","good":"WIDGET","buy_at":"X1-TR-EXPORT","sell_at":"X1-TR-IMPORT","prior_attempt_cost":11111}`,
		playerID, nil)

	persister := NewArbCostConfigPersister(s.containerRepo)
	require.NoError(t, persister.PersistBuyCost(context.Background(), containerID, playerID, 42000))

	var model persistence.ContainerModel
	require.NoError(t, db.First(&model, "id = ?", containerID).Error)
	var merged map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(model.Config), &merged))
	require.EqualValues(t, 42000, merged["prior_attempt_cost"], "the latest buy cost must overwrite the prior one")
}

// A persist against a container row that no longer exists (already terminalized/reaped)
// returns an error the caller logs and swallows — it must never panic or silently succeed.
func TestArbCostConfigPersister_MissingContainer_ReturnsError(t *testing.T) {
	s, _, playerID := newRecoveryTestServer(t)

	persister := NewArbCostConfigPersister(s.containerRepo)
	err := persister.PersistBuyCost(context.Background(), "arb-run-DOES-NOT-EXIST", playerID, 5000)
	require.Error(t, err, "persisting to a missing container must surface an error (the caller logs and continues)")
}
