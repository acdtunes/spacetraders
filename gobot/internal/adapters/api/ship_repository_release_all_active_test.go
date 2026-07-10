package api

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// TestReleaseAllActiveScopesToPlayer proves that ReleaseAllActive only releases
// active ship assignments belonging to the given player. Regression test for
// sp-s7b7: at daemon startup, ReleaseAllActive previously ran an UPDATE with no
// player_id predicate, releasing every player's active ship assignments. After
// a universe reset there can be multiple player rows (a dead closed-era player
// and the live open-era player); an unscoped release corrupts the other
// player's assignment state.
func TestReleaseAllActiveScopesToPlayer(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	playerA := persistence.PlayerModel{AgentSymbol: "PLAYER-A", Token: "tok-a", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&playerA).Error)
	playerB := persistence.PlayerModel{AgentSymbol: "PLAYER-B", Token: "tok-b", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&playerB).Error)

	containerID := "CTR-1"
	seedContainerParent(t, db, containerID, playerA.ID)
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "SHIP-A",
		PlayerID:         playerA.ID,
		ContainerID:      &containerID,
		AssignmentStatus: "active",
	}).Error)
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "SHIP-B",
		PlayerID:         playerB.ID,
		AssignmentStatus: "active",
	}).Error)

	// ReleaseAllActive only touches r.db, so nil apiClient/waypoint/player deps are safe here.
	repo := NewShipRepository(nil, nil, nil, nil, db, nil)

	count, err := repo.ReleaseAllActive(context.Background(), shared.MustNewPlayerID(playerA.ID), "daemon_restart")
	require.NoError(t, err)
	require.Equal(t, 1, count, "should only release player A's active assignment")

	var shipA persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ? AND player_id = ?", "SHIP-A", playerA.ID).First(&shipA).Error)
	require.Equal(t, "idle", shipA.AssignmentStatus, "player A's ship should be released to idle")
	require.Nil(t, shipA.ContainerID, "player A's ship container should be cleared")

	var shipB persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ? AND player_id = ?", "SHIP-B", playerB.ID).First(&shipB).Error)
	require.Equal(t, "active", shipB.AssignmentStatus, "player B's assignment must NOT be touched by player A's release")
}

// TestReleaseAllActiveExcludesCaptainReservations proves that ReleaseAllActive
// — invoked unconditionally on every daemon restart (daemon_server.go Start())
// to clean up zombie coordinator claims from a previous run — never touches a
// captain reservation. Regression test for sp-i1ku: a captain reservation is
// persisted as an assignment row with assignment_status="active" (the same
// status a live coordinator claim uses), so an owner-blind bulk release would
// silently flip a captain-reserved hull back to idle on the very next daemon
// restart, breaking the feature's central promise that reservations survive
// restarts and coordinators can never reclaim a reserved ship.
func TestReleaseAllActiveExcludesCaptainReservations(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	player := persistence.PlayerModel{AgentSymbol: "PLAYER-A", Token: "tok-a", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)

	containerID := "CTR-1"
	seedContainerParent(t, db, containerID, player.ID)
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "SHIP-CONTAINER",
		PlayerID:         player.ID,
		ContainerID:      &containerID,
		AssignmentStatus: "active",
		AssignmentOwner:  string(navigation.AssignmentOwnerContainer),
	}).Error)
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "SHIP-RESERVED",
		PlayerID:         player.ID,
		AssignmentStatus: "active",
		AssignmentOwner:  string(navigation.AssignmentOwnerCaptain),
		AssignmentReason: "manual gate-supply errand",
	}).Error)

	// ReleaseAllActive only touches r.db, so nil apiClient/waypoint/player deps are safe here.
	repo := NewShipRepository(nil, nil, nil, nil, db, nil)

	count, err := repo.ReleaseAllActive(context.Background(), shared.MustNewPlayerID(player.ID), "daemon_restart")
	require.NoError(t, err)
	require.Equal(t, 1, count, "should only release the container-claimed ship, not the captain reservation")

	var containerShip persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "SHIP-CONTAINER").First(&containerShip).Error)
	require.Equal(t, "idle", containerShip.AssignmentStatus, "zombie container claim should be released to idle")
	require.Nil(t, containerShip.ContainerID, "zombie container claim's container should be cleared")

	var reservedShip persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "SHIP-RESERVED").First(&reservedShip).Error)
	require.Equal(t, "active", reservedShip.AssignmentStatus, "captain reservation must survive a daemon restart")
	require.Equal(t, string(navigation.AssignmentOwnerCaptain), reservedShip.AssignmentOwner, "captain ownership must be untouched")
	require.Equal(t, "manual gate-supply errand", reservedShip.AssignmentReason, "reservation reason must be untouched")
}
