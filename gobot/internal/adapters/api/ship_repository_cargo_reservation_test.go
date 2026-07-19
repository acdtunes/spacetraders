package api

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// --- Cargo reservation persistence ----------------------------------
//
// RULINGS #2: every piece of operational state must survive a restart. A cargo
// do-not-sell reservation is persisted as a per-hull JSONB column and reloaded on
// boot, and — like DedicatedFleet — must be preserved across the restart-time API
// sync (which has no concept of it) or it is silently wiped and a staged module is
// re-exposed to coordinator liquidation.

// validReservationShipModel is a complete-enough ship row for FindBySymbol to
// reconstruct a domain ship (ReconstructShip validates engine speed, fuel, cargo,
// nav status).
func validReservationShipModel(playerID int) persistence.ShipModel {
	return persistence.ShipModel{
		ShipSymbol:       "TORWIND-1E",
		PlayerID:         playerID,
		AssignmentStatus: "idle",
		NavStatus:        "DOCKED",
		FlightMode:       "CRUISE",
		LocationSymbol:   "X1-ZC66-BA9D",
		SystemSymbol:     "X1-ZC66",
		FuelCurrent:      100,
		FuelCapacity:     100,
		CargoCapacity:    40,
		CargoUnits:       0,
		EngineSpeed:      30,
		FrameSymbol:      "FRAME_LIGHT_FREIGHTER",
		Role:             "HAULER",
	}
}

// The atomic setter persists overrides and they reload into the domain ship's
// IsCargoReserved verdict — proving a reservation survives a restart (a fresh
// FindBySymbol reads the persisted column).
func TestSetCargoReservation_PersistsAndSurvivesReload(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	playerRow := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok-a", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&playerRow).Error)
	playerID := shared.MustNewPlayerID(playerRow.ID)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "TORWIND-1E", PlayerID: playerRow.ID, AssignmentStatus: "idle",
	}).Error)

	repo := NewShipRepository(nil, nil, nil, syncPreserveOwnerFakeWaypointProvider{}, db, nil)
	ctx := context.Background()

	// Protect an extra good, and release a default-reserved module (deliberate resale).
	require.NoError(t, repo.SetCargoReservation(ctx, "TORWIND-1E", "ANTIMATTER", true, playerID))
	require.NoError(t, repo.SetCargoReservation(ctx, "TORWIND-1E", "MODULE_CARGO_HOLD_III", false, playerID))

	// Reload a full domain ship from the DB (simulating a restart) and check verdicts.
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-1E").
		Updates(validReservationShipModel(playerRow.ID)).Error) // fill fields FindBySymbol reconstructs

	ship, err := repo.FindBySymbol(ctx, "TORWIND-1E", playerID)
	require.NoError(t, err)
	require.NotNil(t, ship)

	require.True(t, ship.IsCargoReserved("ANTIMATTER"), "an explicitly reserved good must reload as reserved")
	require.False(t, ship.IsCargoReserved("MODULE_CARGO_HOLD_III"), "an explicitly released module must reload as sellable")
	require.True(t, ship.IsCargoReserved("MODULE_JUMP_DRIVE_I"), "an un-overridden module stays reserved by default")
	require.False(t, ship.IsCargoReserved("IRON_ORE"), "an un-overridden trade good stays sellable by default")
}

// Idempotent: writing the already-persisted decision is a no-op that still leaves
// the correct value.
func TestSetCargoReservation_IdempotentRepeat(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	playerRow := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok-a", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&playerRow).Error)
	playerID := shared.MustNewPlayerID(playerRow.ID)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "TORWIND-1E", PlayerID: playerRow.ID, AssignmentStatus: "idle",
	}).Error)

	repo := NewShipRepository(nil, nil, nil, syncPreserveOwnerFakeWaypointProvider{}, db, nil)
	ctx := context.Background()

	require.NoError(t, repo.SetCargoReservation(ctx, "TORWIND-1E", "ANTIMATTER", true, playerID))
	require.NoError(t, repo.SetCargoReservation(ctx, "TORWIND-1E", "ANTIMATTER", true, playerID))

	var model persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-1E").First(&model).Error)
	overrides, corrupt := parseReservationOverrides(model.ReservationOverrides)
	require.False(t, corrupt)
	require.Equal(t, map[string]bool{"ANTIMATTER": true}, overrides)
}

// A ship not in the DB is a clean error, not a silent success.
func TestSetCargoReservation_MissingShipErrors(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	repo := NewShipRepository(nil, nil, nil, syncPreserveOwnerFakeWaypointProvider{}, db, nil)

	err = repo.SetCargoReservation(context.Background(), "GHOST-1", "ANTIMATTER", true, shared.MustNewPlayerID(1))
	require.Error(t, err)
}

// TestSyncAllFromAPI_PreservesReservationOverrides: the restart-time bulk sync
// (syncAllShipsOnStartup) must not wipe a hull's reservation — same clobber class
// as DedicatedFleet, one column over.
func TestSyncAllFromAPI_PreservesReservationOverrides(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	playerRow := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok-a", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&playerRow).Error)
	playerID := shared.MustNewPlayerID(playerRow.ID)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:           "TORWIND-19",
		PlayerID:             playerRow.ID,
		AssignmentStatus:     "idle",
		ReservationOverrides: `{"ANTIMATTER":true,"MODULE_CARGO_HOLD_III":false}`,
	}).Error)

	apiClient := &syncPreserveOwnerFakeAPIClient{shipData: &navigation.ShipData{
		Symbol:    "TORWIND-19",
		Location:  "X1-TEST-A1",
		NavStatus: "IN_ORBIT",
	}}
	playerRepo := &syncPreserveOwnerFakePlayerRepo{p: &player.Player{ID: playerID, Token: "tok-a"}}
	repo := NewShipRepository(apiClient, playerRepo, nil, syncPreserveOwnerFakeWaypointProvider{}, db, nil)

	_, err = repo.SyncAllFromAPI(context.Background(), playerID)
	require.NoError(t, err)

	var model persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-19").First(&model).Error)
	require.JSONEq(t, `{"ANTIMATTER":true,"MODULE_CARGO_HOLD_III":false}`, model.ReservationOverrides,
		"cargo reservations must survive the restart-time bulk API sync")
}

// TestSyncShipFromAPI_PreservesReservationOverrides: the single-ship sync path has
// its own independent preserve block with the same requirement.
func TestSyncShipFromAPI_PreservesReservationOverrides(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	playerRow := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok-a", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&playerRow).Error)
	playerID := shared.MustNewPlayerID(playerRow.ID)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:           "TORWIND-3",
		PlayerID:             playerRow.ID,
		AssignmentStatus:     "idle",
		ReservationOverrides: `{"MODULE_CARGO_HOLD_III":true}`,
	}).Error)

	apiClient := &syncPreserveFleetFakeAPIClient{shipData: &navigation.ShipData{
		Symbol:      "TORWIND-3",
		Location:    "X1-TEST-A1",
		NavStatus:   "IN_ORBIT",
		EngineSpeed: 10,
		FrameSymbol: "FRAME_FRIGATE",
		Role:        "COMMAND",
	}}
	playerRepo := &syncPreserveOwnerFakePlayerRepo{p: &player.Player{ID: playerID, Token: "tok-a"}}
	repo := NewShipRepository(apiClient, playerRepo, nil, syncPreserveOwnerFakeWaypointProvider{}, db, nil)

	_, err = repo.SyncShipFromAPI(context.Background(), "TORWIND-3", playerID)
	require.NoError(t, err)

	var model persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-3").First(&model).Error)
	require.JSONEq(t, `{"MODULE_CARGO_HOLD_III":true}`, model.ReservationOverrides,
		"a cargo reservation must survive a single-ship API sync")
}
