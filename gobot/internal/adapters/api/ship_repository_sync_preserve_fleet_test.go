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
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// syncPreserveFleetFakeAPIClient supplies both ListShips (for
// SyncAllFromAPI) and GetShip (for SyncShipFromAPI) - the sp-w870
// syncPreserveOwnerFakeAPIClient in the neighboring test file only
// implements ListShips, since TestSyncAllFromAPI_PreservesCaptainReservation
// never exercises the single-ship sync path.
type syncPreserveFleetFakeAPIClient struct {
	domainPorts.APIClient
	shipData *navigation.ShipData
}

func (f *syncPreserveFleetFakeAPIClient) ListShips(_ context.Context, _ string) ([]*navigation.ShipData, error) {
	return []*navigation.ShipData{f.shipData}, nil
}

func (f *syncPreserveFleetFakeAPIClient) GetShip(_ context.Context, _, _ string) (*navigation.ShipData, error) {
	return f.shipData, nil
}

// TestSyncAllFromAPI_PreservesDedicatedFleet is a regression test for sp-bi75:
// same bug class and same root cause as sp-w870
// (TestSyncAllFromAPI_PreservesCaptainReservation), one field over.
// shipDataToModel builds the fresh model from raw API data, which has no
// concept of the bot's DedicatedFleet tag (sp-l7h2), so that column is left
// at its Go zero value ("") on every ship synced from the API. The "preserve
// existing assignment data" block copies ContainerID/AssignmentStatus/
// AssignedAt/ReleasedAt/ReleaseReason/AssignmentOwner/AssignmentReason from
// the existing DB row onto the freshly-built model - but never copied
// DedicatedFleet. daemon_server.go's Start() runs syncAllShipsOnStartup() ->
// SyncAllFromAPI() unconditionally, for every player, on every restart -
// wiping every `fleet assign` pin in the fleet on the very next restart, and
// clearing the way for a manufacturing/contract coordinator to poach a hull
// the captain deliberately dedicated elsewhere.
func TestSyncAllFromAPI_PreservesDedicatedFleet(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	playerRow := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok-a", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&playerRow).Error)
	playerID := shared.MustNewPlayerID(playerRow.ID)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "TORWIND-19",
		PlayerID:         playerRow.ID,
		AssignmentStatus: "idle",
		DedicatedFleet:   "trade",
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
	require.Equal(t, "trade", model.DedicatedFleet, "fleet pin must survive the restart-time API sync")
}

// TestSyncShipFromAPI_PreservesDedicatedFleet is the single-ship-sync sibling
// of TestSyncAllFromAPI_PreservesDedicatedFleet - SyncShipFromAPI has its own
// independent "preserve existing assignment data" block (mirrored from
// SyncAllFromAPI, see the matching sp-w870 comment there) with the same gap.
func TestSyncShipFromAPI_PreservesDedicatedFleet(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	playerRow := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok-a", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&playerRow).Error)
	playerID := shared.MustNewPlayerID(playerRow.ID)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "TORWIND-3",
		PlayerID:         playerRow.ID,
		AssignmentStatus: "idle",
		DedicatedFleet:   "command",
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
	require.Equal(t, "command", model.DedicatedFleet, "fleet pin must survive a single-ship API sync")
}

// TestSyncAllFromAPI_PinnedHullNotPoachedAfterReload is the end-to-end
// acceptance scenario for sp-bi75: a pinned hull must not be poached after a
// restart. It chains the exact sequence that broke in production - a
// restart-time API sync (SyncAllFromAPI, standing in for
// syncAllShipsOnStartup) immediately followed by a foreign coordinator's
// claim attempt (ClaimShip, standing in for the manufacturing pool grabbing
// an idle-looking hull) - and asserts the claim is still rejected. ClaimShip's
// dedication guard reads DedicatedFleet fresh from the DB on every call, so
// this is really asserting the sync path did its job; it is included anyway
// because the bead's acceptance criteria calls out "not poached after
// restart" as its own observable behavior, not just "the column has the
// right value".
func TestSyncAllFromAPI_PinnedHullNotPoachedAfterReload(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	playerRow := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok-a", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&playerRow).Error)
	playerID := shared.MustNewPlayerID(playerRow.ID)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "TORWIND-19",
		PlayerID:         playerRow.ID,
		AssignmentStatus: "idle",
		DedicatedFleet:   "trade",
	}).Error)

	apiClient := &syncPreserveOwnerFakeAPIClient{shipData: &navigation.ShipData{
		Symbol:    "TORWIND-19",
		Location:  "X1-TEST-A1",
		NavStatus: "IN_ORBIT",
	}}
	playerRepo := &syncPreserveOwnerFakePlayerRepo{p: &player.Player{ID: playerID, Token: "tok-a"}}
	repo := NewShipRepository(apiClient, playerRepo, nil, syncPreserveOwnerFakeWaypointProvider{}, db, nil)

	// Simulate the daemon restart's startup sync.
	_, err = repo.SyncAllFromAPI(context.Background(), playerID)
	require.NoError(t, err)

	// Simulate the manufacturing pool immediately trying to poach the hull.
	claimErr := repo.ClaimShip(context.Background(), "TORWIND-19", "mfg-worker-1", playerID, "manufacturing")
	require.Error(t, claimErr, "a hull pinned to another fleet must not be claimable post-restart")

	var dedicated *shared.ShipDedicatedToOtherFleetError
	require.ErrorAs(t, claimErr, &dedicated)
	require.Equal(t, "trade", dedicated.Fleet)

	var model persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-19").First(&model).Error)
	require.Equal(t, "idle", model.AssignmentStatus, "the rejected poach must not mutate the assignment")
	require.Equal(t, "trade", model.DedicatedFleet, "the pin must remain intact after the rejected poach")
}

// TestSyncAllFromAPI_ClearedFleetStaysClearedAfterReload is the symmetric
// case to TestSyncAllFromAPI_PreservesDedicatedFleet: an explicit
// AssignFleet(..., "", ...) clear (the `fleet unassign` CLI path) must not
// resurrect on the next restart-time sync. Since the fix copies
// existingModel.DedicatedFleet unconditionally, an already-cleared "" copies
// forward as "" - this pins that behavior down explicitly rather than relying
// on it being an accidental side effect of the other tests.
func TestSyncAllFromAPI_ClearedFleetStaysClearedAfterReload(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	playerRow := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok-a", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&playerRow).Error)
	playerID := shared.MustNewPlayerID(playerRow.ID)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "TORWIND-7",
		PlayerID:         playerRow.ID,
		AssignmentStatus: "idle",
		DedicatedFleet:   "trade",
	}).Error)

	// `fleet unassign` - the single write path clears the tag.
	require.NoError(t, repoForClear(t, db).AssignFleet(context.Background(), "TORWIND-7", "", playerID))

	apiClient := &syncPreserveOwnerFakeAPIClient{shipData: &navigation.ShipData{
		Symbol:    "TORWIND-7",
		Location:  "X1-TEST-A1",
		NavStatus: "IN_ORBIT",
	}}
	playerRepo := &syncPreserveOwnerFakePlayerRepo{p: &player.Player{ID: playerID, Token: "tok-a"}}
	repo := NewShipRepository(apiClient, playerRepo, nil, syncPreserveOwnerFakeWaypointProvider{}, db, nil)

	_, err = repo.SyncAllFromAPI(context.Background(), playerID)
	require.NoError(t, err)

	var model persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-7").First(&model).Error)
	require.Equal(t, "", model.DedicatedFleet, "a cleared pin must stay cleared across the restart-time API sync")
}

// repoForClear builds a minimal repository against db purely to call
// AssignFleet, which only touches r.db - mirrors newDedicationTestRepo's
// nil-dependency reasoning in the neighboring claim-dedication test file.
func repoForClear(t *testing.T, db *gorm.DB) *ShipRepository {
	t.Helper()
	return NewShipRepository(nil, nil, nil, nil, db, nil)
}
