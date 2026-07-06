package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

func TestFindOpenEraReturnsTheEraWithNullClosedAt(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	closedAt := time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)
	openReset := time.Date(2026, 6, 22, 0, 0, 0, 0, time.UTC)
	require.NoError(t, db.Create(&persistence.EraModel{
		Name: "torwind", AgentSymbol: "TORWIND", PlayerID: 1, ClosedAt: &closedAt,
	}).Error)
	require.NoError(t, db.Create(&persistence.EraModel{
		Name: "orion", AgentSymbol: "ORION", PlayerID: 2, UniverseResetDate: &openReset,
	}).Error)

	repo := persistence.NewEraRepository(db)
	era, err := repo.FindOpenEra(context.Background())

	require.NoError(t, err)
	require.NotNil(t, era)
	require.Equal(t, "orion", era.Name)
	require.NotNil(t, era.UniverseResetDate)
	require.Equal(t, openReset, era.UniverseResetDate.UTC())
}

func TestFindOpenEraReturnsNilWhenEveryEraIsClosed(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	closedAt := time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)
	require.NoError(t, db.Create(&persistence.EraModel{
		Name: "torwind", AgentSymbol: "TORWIND", PlayerID: 1, ClosedAt: &closedAt,
	}).Error)

	repo := persistence.NewEraRepository(db)
	era, err := repo.FindOpenEra(context.Background())

	require.NoError(t, err)
	require.Nil(t, era)
}
