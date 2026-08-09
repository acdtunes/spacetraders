package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

func newScoutWorkersTestDB(t *testing.T) (*gorm.DB, int) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	player := persistence.PlayerModel{AgentSymbol: "SWEEP-AGENT", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)
	return db, player.ID
}

func insertScoutWorkerContainer(t *testing.T, db *gorm.DB, id, commandType, status, config string, playerID int) {
	t.Helper()
	now := time.Now()
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID:            id,
		PlayerID:      playerID,
		ContainerType: "SCOUT",
		CommandType:   commandType,
		Status:        status,
		Config:        config,
		StartedAt:     &now,
		HeartbeatAt:   &now,
	}).Error)
}

// ListRunningScoutWorkers is the zombie sweep's container-side read: RUNNING
// scout_tour + scout_reposition rows for THIS player only, each with the
// coordinator_id parsed out of the persisted config — "" for a manual tour
// (no coordinator_id key) and for an unparseable config, since a worker the
// sweep cannot identify must never be stopped.
func TestListRunningScoutWorkers_FiltersAndParsesCoordinatorID(t *testing.T) {
	db, playerID := newScoutWorkersTestDB(t)
	repo := persistence.NewContainerRepository(db)

	insertScoutWorkerContainer(t, db, "tour-managed", "scout_tour", "RUNNING",
		`{"ship_symbol":"SAT-1","markets":["X1-GZ7-A1"],"iterations":-1,"coordinator_id":"scoutpost-1"}`, playerID)
	insertScoutWorkerContainer(t, db, "relay-managed", "scout_reposition", "RUNNING",
		`{"ship_symbol":"SAT-2","destination":"X1-QW1-A1","coordinator_id":"scoutpost-1"}`, playerID)
	insertScoutWorkerContainer(t, db, "tour-manual", "scout_tour", "RUNNING",
		`{"ship_symbol":"SAT-3","markets":["X1-GZ7-A1"],"iterations":3}`, playerID)
	insertScoutWorkerContainer(t, db, "tour-garbled", "scout_tour", "RUNNING", `not-json`, playerID)
	// Excluded rows: wrong status, wrong command type, wrong player.
	insertScoutWorkerContainer(t, db, "tour-done", "scout_tour", "COMPLETED",
		`{"coordinator_id":"scoutpost-1"}`, playerID)
	insertScoutWorkerContainer(t, db, "ferry-1", "worker_ferry", "RUNNING",
		`{"coordinator_id":"rebalancer-1"}`, playerID)
	foreign := persistence.PlayerModel{AgentSymbol: "OTHER-AGENT", Token: "tok2", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&foreign).Error)
	insertScoutWorkerContainer(t, db, "tour-foreign", "scout_tour", "RUNNING",
		`{"coordinator_id":"scoutpost-9"}`, foreign.ID)

	workers, err := repo.ListRunningScoutWorkers(context.Background(), shared.MustNewPlayerID(playerID))
	require.NoError(t, err)

	byID := map[string]string{}
	for _, w := range workers {
		byID[w.ID] = w.CoordinatorID
	}
	require.Len(t, workers, 4, "RUNNING scout_tour/scout_reposition rows for this player only")
	require.Equal(t, "scoutpost-1", byID["tour-managed"])
	require.Equal(t, "scoutpost-1", byID["relay-managed"])
	require.Equal(t, "", byID["tour-manual"], "a config without coordinator_id is a manual tour")
	require.Equal(t, "", byID["tour-garbled"], "an unparseable config must read as manual — never stoppable")
	require.NotContains(t, byID, "tour-done")
	require.NotContains(t, byID, "ferry-1")
	require.NotContains(t, byID, "tour-foreign")
}

// ListScoutTourShips is the bootstrap tour's HISTORY read: which hulls have ever been given a
// circuit. Status is deliberately unscoped — a COMPLETED or FAILED tour still answers "this hull
// has been out" — because the caller uses the absence of a row to decide a hull is one the fleet
// GAINED rather than one whose tour ended, and re-cuts the post for it.
func TestListScoutTourShips_ReadsEveryStatusForThisPlayerOnly(t *testing.T) {
	db, playerID := newScoutWorkersTestDB(t)
	repo := persistence.NewContainerRepository(db)

	insertScoutWorkerContainer(t, db, "tour-running", "scout_tour", "RUNNING",
		`{"ship_symbol":"SAT-1","markets":["X1-GZ7-A1"],"iterations":-1}`, playerID)
	insertScoutWorkerContainer(t, db, "tour-completed", "scout_tour", "COMPLETED",
		`{"ship_symbol":"SAT-2","markets":["X1-GZ7-B2"],"iterations":-1}`, playerID)
	insertScoutWorkerContainer(t, db, "tour-failed", "scout_tour", "FAILED",
		`{"ship_symbol":"SAT-3","markets":["X1-GZ7-C3"],"iterations":-1}`, playerID)
	// Excluded: another command type, an unattributable config, and another player's tour.
	insertScoutWorkerContainer(t, db, "relay-1", "scout_reposition", "RUNNING",
		`{"ship_symbol":"SAT-4","destination":"X1-QW1-A1"}`, playerID)
	insertScoutWorkerContainer(t, db, "tour-garbled", "scout_tour", "RUNNING", `not-json`, playerID)
	foreign := persistence.PlayerModel{AgentSymbol: "OTHER-TOUR-AGENT", Token: "tok3", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&foreign).Error)
	insertScoutWorkerContainer(t, db, "tour-foreign", "scout_tour", "RUNNING",
		`{"ship_symbol":"SAT-9","markets":["X1-ZZ9-A1"],"iterations":-1}`, foreign.ID)

	ships, err := repo.ListScoutTourShips(context.Background(), playerID)
	require.NoError(t, err)

	require.Equal(t, map[string]bool{"SAT-1": true, "SAT-2": true, "SAT-3": true}, ships,
		"every status counts as history; a relay, an unreadable config and a foreign player's tour do not")
}
