package api

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// --- Retirement mark persistence ------------------------------------
//
// RULINGS #2: a retirement must survive a restart, or a hull the operator withdrew from
// service quietly rejoins it on the next daemon bounce. Like DedicatedFleet, the mark must
// also survive the restart-time API sync, which has no concept of it.

func retirementPlayer(t *testing.T, db *gorm.DB) (shared.PlayerID, int) {
	t.Helper()
	playerRow := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok-a", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&playerRow).Error)
	return shared.MustNewPlayerID(playerRow.ID), playerRow.ID
}

// The atomic setter persists the mark and it reloads into the domain ship — proving a
// retirement survives a restart (a fresh FindBySymbol reads the persisted column).
func TestSetShipRetiring_PersistsAndSurvivesReload(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	playerID, rawID := retirementPlayer(t, db)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "TORWIND-1E", PlayerID: rawID, AssignmentStatus: "idle",
	}).Error)

	repo := NewShipRepository(nil, nil, nil, syncPreserveOwnerFakeWaypointProvider{}, db, nil)
	ctx := context.Background()

	require.NoError(t, repo.SetShipRetiring(ctx, "TORWIND-1E", true, playerID))

	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-1E").
		Updates(validReservationShipModel(rawID)).Error)

	ship, err := repo.FindBySymbol(ctx, "TORWIND-1E", playerID)
	require.NoError(t, err)
	require.NotNil(t, ship)
	require.True(t, ship.IsRetiring(), "the retirement mark must reload after a restart")
	require.True(t, ship.RetirementDrained(), "an empty retiring hull reloads as drained")
}

// Cancelling clears the mark, returning the hull to normal service.
func TestSetShipRetiring_CancelClearsTheMark(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	playerID, rawID := retirementPlayer(t, db)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "TORWIND-1E", PlayerID: rawID, AssignmentStatus: "idle",
	}).Error)

	repo := NewShipRepository(nil, nil, nil, syncPreserveOwnerFakeWaypointProvider{}, db, nil)
	ctx := context.Background()

	require.NoError(t, repo.SetShipRetiring(ctx, "TORWIND-1E", true, playerID))
	require.NoError(t, repo.SetShipRetiring(ctx, "TORWIND-1E", false, playerID))

	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-1E").
		Updates(validReservationShipModel(rawID)).Error)

	ship, err := repo.FindBySymbol(ctx, "TORWIND-1E", playerID)
	require.NoError(t, err)
	require.False(t, ship.IsRetiring(), "a cancelled retirement returns the hull to service")
}

// A ship not in the DB is a clean error, not a silent success.
func TestSetShipRetiring_MissingShipErrors(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	repo := NewShipRepository(nil, nil, nil, syncPreserveOwnerFakeWaypointProvider{}, db, nil)

	err = repo.SetShipRetiring(context.Background(), "GHOST-1", true, shared.MustNewPlayerID(1))
	require.Error(t, err)
}

// The restart-time bulk sync must not wipe a retirement — same clobber class as
// DedicatedFleet, one column over.
func TestSyncAllFromAPI_PreservesRetirementMark(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	playerID, rawID := retirementPlayer(t, db)

	marked := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "TORWIND-19", PlayerID: rawID, AssignmentStatus: "idle", RetiringAt: &marked,
	}).Error)

	apiClient := &syncPreserveOwnerFakeAPIClient{shipData: &navigation.ShipData{
		Symbol: "TORWIND-19", Location: "X1-TEST-A1", NavStatus: "IN_ORBIT",
	}}
	playerRepo := &syncPreserveOwnerFakePlayerRepo{p: &player.Player{ID: playerID, Token: "tok-a"}}
	repo := NewShipRepository(apiClient, playerRepo, nil, syncPreserveOwnerFakeWaypointProvider{}, db, nil)

	_, err = repo.SyncAllFromAPI(context.Background(), playerID)
	require.NoError(t, err)

	var model persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-19").First(&model).Error)
	require.NotNil(t, model.RetiringAt, "a retirement must survive the restart-time bulk API sync")
}

// The single-ship sync path has its own independent preserve block with the same
// requirement.
func TestSyncShipFromAPI_PreservesRetirementMark(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	playerID, rawID := retirementPlayer(t, db)

	marked := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "TORWIND-3", PlayerID: rawID, AssignmentStatus: "idle", RetiringAt: &marked,
	}).Error)

	apiClient := &syncPreserveFleetFakeAPIClient{shipData: &navigation.ShipData{
		Symbol: "TORWIND-3", Location: "X1-TEST-A1", NavStatus: "IN_ORBIT",
		EngineSpeed: 10, FrameSymbol: "FRAME_FRIGATE", Role: "COMMAND",
	}}
	playerRepo := &syncPreserveOwnerFakePlayerRepo{p: &player.Player{ID: playerID, Token: "tok-a"}}
	repo := NewShipRepository(apiClient, playerRepo, nil, syncPreserveOwnerFakeWaypointProvider{}, db, nil)

	_, err = repo.SyncShipFromAPI(context.Background(), "TORWIND-3", playerID)
	require.NoError(t, err)

	var model persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-3").First(&model).Error)
	require.NotNil(t, model.RetiringAt, "a retirement must survive a single-ship API sync")
}
