package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

func TestCreatePlayerWithEraPersistsBothRowsLinkedByPlayerID(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	reset := time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)
	player := &persistence.PlayerModel{AgentSymbol: "ORION", Token: "agent-jwt", CreatedAt: now}
	era := &persistence.EraModel{Name: "orion", AgentSymbol: "ORION", UniverseResetDate: &reset, RegisteredAt: &now}

	repo := persistence.NewEraRepository(db)
	require.NoError(t, repo.CreatePlayerWithEra(context.Background(), player, era))

	require.NotZero(t, player.ID)
	require.Equal(t, player.ID, era.PlayerID)

	var players []persistence.PlayerModel
	require.NoError(t, db.Find(&players).Error)
	require.Len(t, players, 1)

	var eras []persistence.EraModel
	require.NoError(t, db.Find(&eras).Error)
	require.Len(t, eras, 1)
	require.Equal(t, "orion", eras[0].Name)
	require.Equal(t, players[0].ID, eras[0].PlayerID)
}

func TestCreatePlayerWithEraRollsBackPlayerWhenEraInsertFails(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	closed := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, db.Create(&persistence.EraModel{
		Name: "orion", AgentSymbol: "ORION", PlayerID: 1, ClosedAt: &closed,
	}).Error)

	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	player := &persistence.PlayerModel{AgentSymbol: "ORION2", Token: "agent-jwt", CreatedAt: now}
	era := &persistence.EraModel{Name: "orion", AgentSymbol: "ORION", RegisteredAt: &now}

	repo := persistence.NewEraRepository(db)
	err = repo.CreatePlayerWithEra(context.Background(), player, era)

	require.Error(t, err)

	var players []persistence.PlayerModel
	require.NoError(t, db.Find(&players).Error)
	require.Empty(t, players)

	var eras []persistence.EraModel
	require.NoError(t, db.Find(&eras).Error)
	require.Len(t, eras, 1)
}
