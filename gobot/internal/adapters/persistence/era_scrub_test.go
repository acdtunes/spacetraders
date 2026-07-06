package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

func TestScrubEraRefusesOnOpenEra(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.PlayerModel{ID: 1, AgentSymbol: "TORWIND", Token: "t", CreatedAt: time.Now()}).Error)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "torwind", AgentSymbol: "TORWIND", PlayerID: 1}).Error)
	require.NoError(t, db.Create(&persistence.ShipModel{ShipSymbol: "S1", PlayerID: 1}).Error)

	repo := persistence.NewEraRepository(db)
	_, err = repo.ScrubEra(context.Background(), "torwind")
	require.Error(t, err)

	var shipCount int64
	require.NoError(t, db.Model(&persistence.ShipModel{}).Count(&shipCount).Error)
	require.Equal(t, int64(1), shipCount)
}

func TestScrubEraDeletesWipeClassRowsOfDeadPlayerOnly(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	closedAt := time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)
	require.NoError(t, db.Create(&persistence.PlayerModel{ID: 1, AgentSymbol: "TORWIND", Token: "", CreatedAt: time.Now()}).Error)
	require.NoError(t, db.Create(&persistence.PlayerModel{ID: 2, AgentSymbol: "ORION", Token: "live", CreatedAt: time.Now()}).Error)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "torwind", AgentSymbol: "TORWIND", PlayerID: 1, ClosedAt: &closedAt}).Error)

	require.NoError(t, db.Create(&persistence.ContainerModel{ID: "ct1", PlayerID: 1}).Error)
	require.NoError(t, db.Create(&persistence.ContainerLogModel{ContainerID: "ct1", PlayerID: 1, Timestamp: time.Now(), Level: "INFO", Message: "m"}).Error)
	require.NoError(t, db.Create(&persistence.ShipModel{ShipSymbol: "S1", PlayerID: 1}).Error)
	require.NoError(t, db.Create(&persistence.ManufacturingFactoryStateModel{FactorySymbol: "F1", OutputGood: "G", PlayerID: 1, PipelineID: "p", RequiredInputs: "{}", CreatedAt: time.Now()}).Error)
	require.NoError(t, db.Create(&persistence.GasOperationModel{ID: "g1", PlayerID: 1, GasGiant: "GG", CreatedAt: time.Now(), UpdatedAt: time.Now()}).Error)
	require.NoError(t, db.Create(&persistence.StorageOperationModel{ID: "so1", PlayerID: 1, WaypointSymbol: "W", OperationType: "GAS_SIPHON", CreatedAt: time.Now(), UpdatedAt: time.Now()}).Error)

	require.NoError(t, db.Create(&persistence.ShipModel{ShipSymbol: "S2", PlayerID: 2}).Error)
	require.NoError(t, db.Create(&persistence.TransactionModel{ID: "t1", PlayerID: 1, Timestamp: time.Now(), TransactionType: "SALE", Category: "TRADING", Amount: 1, BalanceAfter: 1, CreatedAt: time.Now()}).Error)
	require.NoError(t, db.Create(&persistence.ContractModel{ID: "c1", PlayerID: 1, FactionSymbol: "COSMIC", Type: "PROCUREMENT", DeadlineToAccept: "x", Deadline: "x", DeliveriesJSON: "[]", LastUpdated: "x"}).Error)

	repo := persistence.NewEraRepository(db)
	_, err = repo.ScrubEra(context.Background(), "torwind")
	require.NoError(t, err)

	assertCount := func(model interface{}, where string, args []interface{}, expected int64) {
		var c int64
		q := db.Model(model)
		if where != "" {
			q = q.Where(where, args...)
		}
		require.NoError(t, q.Count(&c).Error)
		require.Equal(t, expected, c)
	}

	assertCount(&persistence.ContainerModel{}, "player_id = ?", []interface{}{1}, 0)
	assertCount(&persistence.ContainerLogModel{}, "player_id = ?", []interface{}{1}, 0)
	assertCount(&persistence.ShipModel{}, "player_id = ?", []interface{}{1}, 0)
	assertCount(&persistence.ManufacturingFactoryStateModel{}, "player_id = ?", []interface{}{1}, 0)
	assertCount(&persistence.GasOperationModel{}, "player_id = ?", []interface{}{1}, 0)
	assertCount(&persistence.StorageOperationModel{}, "player_id = ?", []interface{}{1}, 0)

	assertCount(&persistence.ShipModel{}, "player_id = ?", []interface{}{2}, 1)
	assertCount(&persistence.TransactionModel{}, "", nil, 1)
	assertCount(&persistence.ContractModel{}, "", nil, 1)
}
