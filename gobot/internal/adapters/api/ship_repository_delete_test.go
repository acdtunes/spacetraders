package api

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

// DeleteShip is the reconcile after a scrap, and it is a DELETE with no live
// fleet read behind it. It must remove exactly the one named hull.
func TestDeleteShip_RemovesOnlyTheNamedHull(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	owner := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&owner).Error)
	other := persistence.PlayerModel{AgentSymbol: "OTHERAGENT", Token: "tok2", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&other).Error)

	seedHull(t, db, "TORWIND-446", owner.ID)
	seedHull(t, db, "TORWIND-457", owner.ID)
	seedHull(t, db, "TORWIND-446", other.ID)

	repo := NewShipRepository(nil, nil, nil, nil, db, nil)

	require.NoError(t, repo.DeleteShip(context.Background(), "TORWIND-446", shared.MustNewPlayerID(owner.ID)))

	require.Zero(t, hullCount(t, db, "TORWIND-446", owner.ID), "the scrapped hull must be gone")
	require.Equal(t, int64(1), hullCount(t, db, "TORWIND-457", owner.ID), "a sibling hull must survive")
	require.Equal(t, int64(1), hullCount(t, db, "TORWIND-446", other.ID), "another player's hull must survive")
}

// Deleting a hull that is already absent is not an error: the scrap succeeded and
// the reconcile must converge rather than fail on a row a sync already pruned.
func TestDeleteShip_IsIdempotent(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	owner := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&owner).Error)

	repo := NewShipRepository(nil, nil, nil, nil, db, nil)

	require.NoError(t, repo.DeleteShip(context.Background(), "TORWIND-446", shared.MustNewPlayerID(owner.ID)))
}

func seedHull(t *testing.T, db *gorm.DB, symbol string, playerID int) {
	t.Helper()
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: symbol, PlayerID: playerID,
		NavStatus: "DOCKED", LocationSymbol: "X1-JP61-A1", SystemSymbol: "X1-JP61",
		CargoCapacity: 240, Modules: "[]", CargoInventory: "[]", AssignmentStatus: "idle",
	}).Error)
}

func hullCount(t *testing.T, db *gorm.DB, symbol string, playerID int) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Model(&persistence.ShipModel{}).
		Where("ship_symbol = ? AND player_id = ?", symbol, playerID).Count(&n).Error)
	return n
}
