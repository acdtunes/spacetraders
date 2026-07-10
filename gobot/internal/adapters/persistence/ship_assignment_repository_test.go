package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
	"gorm.io/gorm"
)

func setupShipAssignmentRepo(t *testing.T) (*persistence.ShipAssignmentRepositoryGORM, int, *gorm.DB) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	player := persistence.PlayerModel{AgentSymbol: "TEST-AGENT", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)
	return persistence.NewShipAssignmentRepository(db), player.ID, db
}

// seedContainerParent inserts the parent containers row a real coordinator creates
// before a hull is claimed into it. A ship carrying a container_id references it
// through the composite FK (container_id, player_id) -> containers(id, player_id);
// with the sp-55aa harness enforcing foreign keys that parent must exist.
func seedContainerParent(t *testing.T, db *gorm.DB, id string, playerID int) {
	t.Helper()
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: id, PlayerID: playerID, Status: "RUNNING",
	}).Error)
}

func TestListActiveReturnsRoleAssignmentAndSyncedAtForEveryShip(t *testing.T) {
	repo, playerID, db := setupShipAssignmentRepo(t)
	ctx := context.Background()

	assignedAt := time.Now().Add(-90 * time.Second)
	idleSyncedAt := time.Now().Add(-4100 * time.Second)
	containerID := "CTR-1"
	seedContainerParent(t, db, containerID, playerID)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "SHIP-1", PlayerID: playerID, Role: "HAULER",
		ContainerID: &containerID, AssignmentStatus: "active", SyncedAt: assignedAt,
	}).Error)
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "SHIP-2", PlayerID: playerID, Role: "EXCAVATOR",
		AssignmentStatus: "idle", SyncedAt: idleSyncedAt,
	}).Error)

	infos, err := repo.ListActive(ctx, playerID)
	require.NoError(t, err)
	require.Len(t, infos, 2)

	byShip := make(map[string]persistence.ShipAssignmentInfo, len(infos))
	for _, info := range infos {
		byShip[info.ShipSymbol] = info
	}

	require.Equal(t, "HAULER", byShip["SHIP-1"].Role)
	require.Equal(t, "CTR-1", byShip["SHIP-1"].ContainerID)
	require.WithinDuration(t, assignedAt, byShip["SHIP-1"].SyncedAt, time.Second)

	require.Equal(t, "EXCAVATOR", byShip["SHIP-2"].Role)
	require.Empty(t, byShip["SHIP-2"].ContainerID)
	require.WithinDuration(t, idleSyncedAt, byShip["SHIP-2"].SyncedAt, time.Second)
}

// TestListActiveReturnsDedicatedFleet is a regression test for sp-ioqt: the
// `ship list` FLEET column (the sp-lybx-prevention payload) reads
// DedicatedFleet off ShipAssignmentInfo, so ListActive must actually
// populate it from the ships row rather than leaving it zero-valued.
func TestListActiveReturnsDedicatedFleet(t *testing.T) {
	repo, playerID, db := setupShipAssignmentRepo(t)
	ctx := context.Background()

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "SHIP-PINNED", PlayerID: playerID, Role: "PROBE",
		AssignmentStatus: "idle", SyncedAt: time.Now(), DedicatedFleet: "contract",
	}).Error)
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "SHIP-UNPINNED", PlayerID: playerID, Role: "PROBE",
		AssignmentStatus: "idle", SyncedAt: time.Now(),
	}).Error)

	infos, err := repo.ListActive(ctx, playerID)
	require.NoError(t, err)
	require.Len(t, infos, 2)

	byShip := make(map[string]persistence.ShipAssignmentInfo, len(infos))
	for _, info := range infos {
		byShip[info.ShipSymbol] = info
	}

	require.Equal(t, "contract", byShip["SHIP-PINNED"].DedicatedFleet)
	require.Empty(t, byShip["SHIP-UNPINNED"].DedicatedFleet)
}

func TestListActiveScopesToPlayer(t *testing.T) {
	repo, playerID, db := setupShipAssignmentRepo(t)
	ctx := context.Background()

	otherPlayer := persistence.PlayerModel{AgentSymbol: "OTHER-AGENT", Token: "tok2", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&otherPlayer).Error)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "SHIP-1", PlayerID: playerID, Role: "HAULER", SyncedAt: time.Now(),
	}).Error)
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "SHIP-9", PlayerID: otherPlayer.ID, Role: "HAULER", SyncedAt: time.Now(),
	}).Error)

	infos, err := repo.ListActive(ctx, playerID)
	require.NoError(t, err)
	require.Len(t, infos, 1)
	require.Equal(t, "SHIP-1", infos[0].ShipSymbol)
}

// TestReleaseAllActiveScopesToPlayer is a regression test for sp-s7b7: ReleaseAllActive
// previously ran an unscoped UPDATE (no player_id predicate) that released every
// player's active ship assignments. This proves it only releases the given player's
// active assignments, leaving other players' assignments untouched.
func TestReleaseAllActiveScopesToPlayer(t *testing.T) {
	repo, playerID, db := setupShipAssignmentRepo(t)
	ctx := context.Background()

	otherPlayer := persistence.PlayerModel{AgentSymbol: "OTHER-AGENT", Token: "tok2", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&otherPlayer).Error)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "SHIP-1", PlayerID: playerID, Role: "HAULER",
		AssignmentStatus: "active", SyncedAt: time.Now(),
	}).Error)
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "SHIP-9", PlayerID: otherPlayer.ID, Role: "HAULER",
		AssignmentStatus: "active", SyncedAt: time.Now(),
	}).Error)

	count, err := repo.ReleaseAllActive(ctx, playerID, "daemon_restart")
	require.NoError(t, err)
	require.Equal(t, 1, count, "should only release the scoped player's active assignment")

	var mine persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ? AND player_id = ?", "SHIP-1", playerID).First(&mine).Error)
	require.Equal(t, "idle", mine.AssignmentStatus, "scoped player's ship should be released to idle")

	var other persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ? AND player_id = ?", "SHIP-9", otherPlayer.ID).First(&other).Error)
	require.Equal(t, "active", other.AssignmentStatus, "other player's assignment must NOT be touched")
}

// TestReleaseAllActiveExcludesCaptainReservations is a regression test for
// sp-i1ku: a captain reservation is persisted as an assignment row with
// assignment_status="active" (the same status a live coordinator claim uses),
// so an owner-blind bulk release would silently flip a captain-reserved hull
// back to idle. This proves ReleaseAllActive releases a zombie container claim
// but leaves a captain reservation untouched.
func TestReleaseAllActiveExcludesCaptainReservations(t *testing.T) {
	repo, playerID, db := setupShipAssignmentRepo(t)
	ctx := context.Background()

	containerID := "CTR-1"
	seedContainerParent(t, db, containerID, playerID)
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "SHIP-CONTAINER", PlayerID: playerID, Role: "HAULER",
		ContainerID: &containerID, AssignmentStatus: "active", SyncedAt: time.Now(),
		AssignmentOwner: string(navigation.AssignmentOwnerContainer),
	}).Error)
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "SHIP-RESERVED", PlayerID: playerID, Role: "HAULER",
		AssignmentStatus: "active", SyncedAt: time.Now(),
		AssignmentOwner:  string(navigation.AssignmentOwnerCaptain),
		AssignmentReason: "manual gate-supply errand",
	}).Error)

	count, err := repo.ReleaseAllActive(ctx, playerID, "daemon_restart")
	require.NoError(t, err)
	require.Equal(t, 1, count, "should only release the container-claimed ship, not the captain reservation")

	var containerShip persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "SHIP-CONTAINER").First(&containerShip).Error)
	require.Equal(t, "idle", containerShip.AssignmentStatus, "zombie container claim should be released to idle")

	var reservedShip persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "SHIP-RESERVED").First(&reservedShip).Error)
	require.Equal(t, "active", reservedShip.AssignmentStatus, "captain reservation must survive a daemon restart")
	require.Equal(t, string(navigation.AssignmentOwnerCaptain), reservedShip.AssignmentOwner, "captain ownership must be untouched")
	require.Equal(t, "manual gate-supply errand", reservedShip.AssignmentReason, "reservation reason must be untouched")
}
