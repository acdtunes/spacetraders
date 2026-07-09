package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// syncPreserveOwnerFakeAPIClient returns one hardcoded ShipData from ListShips
// so SyncAllFromAPI has something to upsert without a live SpaceTraders API.
// Every other APIClient method is unused by SyncAllFromAPI and stays nil via
// the embedded interface.
type syncPreserveOwnerFakeAPIClient struct {
	domainPorts.APIClient
	shipData *navigation.ShipData
}

func (f *syncPreserveOwnerFakeAPIClient) ListShips(_ context.Context, _ string) ([]*navigation.ShipData, error) {
	return []*navigation.ShipData{f.shipData}, nil
}

// syncPreserveOwnerFakePlayerRepo supplies just enough of player.PlayerRepository
// for SyncAllFromAPI to resolve an API token; every other method is unused and
// stays nil via the embedded interface.
type syncPreserveOwnerFakePlayerRepo struct {
	player.PlayerRepository
	p *player.Player
}

func (f *syncPreserveOwnerFakePlayerRepo) FindByID(_ context.Context, _ shared.PlayerID) (*player.Player, error) {
	return f.p, nil
}

// syncPreserveOwnerFakeWaypointProvider always errors, so shipDataToModel's
// best-effort location lookup is skipped via its `err == nil` guard - it has
// no bearing on the assignment columns this test cares about.
type syncPreserveOwnerFakeWaypointProvider struct{}

func (syncPreserveOwnerFakeWaypointProvider) GetWaypoint(_ context.Context, _, _ string, _ int) (*shared.Waypoint, error) {
	return nil, errors.New("stub: waypoint lookup not needed by this test")
}

// TestSyncAllFromAPI_PreservesCaptainReservation is a regression test for
// sp-w870: ReleaseAllActive (sp-i1ku) correctly excludes captain reservations
// from the daemon-restart zombie release (see
// TestReleaseAllActiveExcludesCaptainReservations), but daemon_server.go's
// Start() runs syncAllShipsOnStartup() -> SyncAllFromAPI() immediately
// afterward, for every player, unconditionally, on every restart.
// SyncAllFromAPI's "preserve existing assignment data" block copies
// ContainerID/AssignmentStatus/AssignedAt/ReleasedAt/ReleaseReason from the
// existing DB row onto the freshly-built API model - but never copies
// AssignmentOwner or AssignmentReason. shipDataToModel builds the fresh model
// from raw API data, which has no concept of captain reservations, so those
// two columns are left at their Go zero value and get upserted straight back
// over the existing row - wiping a captain reservation's ownership on the
// very next restart, even though ReleaseAllActive never touched it. This is
// the "ghost-assignment" the captain observed post-PID-60233: the ship stays
// assignment_status="active" (so no coordinator can steal it) but is no
// longer identifiable as a captain reservation, so ReleaseCaptainReservation
// starts rejecting it as "not reserved".
func TestSyncAllFromAPI_PreservesCaptainReservation(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	playerRow := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok-a", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&playerRow).Error)
	playerID := shared.MustNewPlayerID(playerRow.ID)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "ENDURANCE-1",
		PlayerID:         playerRow.ID,
		AssignmentStatus: "active",
		AssignmentOwner:  string(navigation.AssignmentOwnerCaptain),
		AssignmentReason: "manual gate-supply errand",
	}).Error)

	apiClient := &syncPreserveOwnerFakeAPIClient{shipData: &navigation.ShipData{
		Symbol:    "ENDURANCE-1",
		Location:  "X1-TEST-A1",
		NavStatus: "IN_ORBIT",
	}}
	playerRepo := &syncPreserveOwnerFakePlayerRepo{p: &player.Player{ID: playerID, Token: "tok-a"}}

	repo := NewShipRepository(apiClient, playerRepo, nil, syncPreserveOwnerFakeWaypointProvider{}, db, nil)

	_, err = repo.SyncAllFromAPI(context.Background(), playerID)
	require.NoError(t, err)

	var model persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "ENDURANCE-1").First(&model).Error)
	require.Equal(t, "active", model.AssignmentStatus, "reservation must still be active after the restart-time API sync")
	require.Equal(t, string(navigation.AssignmentOwnerCaptain), model.AssignmentOwner, "captain ownership must survive the restart-time API sync")
	require.Equal(t, "manual gate-supply errand", model.AssignmentReason, "reservation reason must survive the restart-time API sync")
}
