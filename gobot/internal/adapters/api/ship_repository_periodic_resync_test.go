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

// TestPeriodicResync_PreservesDedicatedFleetAcrossRepeatedSyncs is the sp-p1ci
// regression guard for the sp-90a3 hazard. The new hourly resync calls the SAME
// SyncAllFromAPI write path as the startup sync, REPEATEDLY. SyncAllFromAPI is
// already dedicated_fleet-safe: its "preserve existing assignment data" block
// copies existingModel.DedicatedFleet forward (sp-bi75), the same preservation
// sp-90a3's preserveDedicatedFleetTag mirrors for the general Save path. This
// test locks that in for the periodic case by driving the sync TWICE — so the
// tag must survive not just the first API overwrite but the second, which reads
// back the row the first one wrote. A naive hourly resync that routed through a
// path bypassing this preservation would re-break gate-hull dedication
// fleet-wide every hour; this fails loudly if that ever happens.
func TestPeriodicResync_PreservesDedicatedFleetAcrossRepeatedSyncs(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	playerRow := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok-a", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&playerRow).Error)
	playerID := shared.MustNewPlayerID(playerRow.ID)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "TORWIND-42",
		PlayerID:         playerRow.ID,
		AssignmentStatus: "idle",
		DedicatedFleet:   "manufacturing",
	}).Error)

	// The API (source of truth for hull state) has no concept of the bot's
	// dedication tag — it returns the ship with an empty DedicatedFleet, exactly
	// the stale-snapshot shape that would clobber the pin if unprotected.
	apiClient := &syncPreserveOwnerFakeAPIClient{shipData: &navigation.ShipData{
		Symbol:    "TORWIND-42",
		Location:  "X1-TEST-A1",
		NavStatus: "IN_ORBIT",
	}}
	playerRepo := &syncPreserveOwnerFakePlayerRepo{p: &player.Player{ID: playerID, Token: "tok-a"}}
	repo := NewShipRepository(apiClient, playerRepo, nil, syncPreserveOwnerFakeWaypointProvider{}, db, nil)

	// Startup sync, then a later periodic resync tick — the exact repeated call
	// the ShipResyncScheduler makes.
	_, err = repo.SyncAllFromAPI(context.Background(), playerID)
	require.NoError(t, err)
	_, err = repo.SyncAllFromAPI(context.Background(), playerID)
	require.NoError(t, err)

	var model persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-42").First(&model).Error)
	require.Equal(t, "manufacturing", model.DedicatedFleet,
		"the hourly resync must not drop a hull's dedicated_fleet pin (sp-90a3)")
}
