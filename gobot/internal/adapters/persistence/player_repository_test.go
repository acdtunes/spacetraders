package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// The same agent symbol can be registered in multiple universe eras, so
// FindByAgentSymbol must resolve deterministically when more than one player
// row shares a symbol.

func TestFindByAgentSymbolPrefersThePlayerInTheOpenEra(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	older := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)

	// The OLDER player is the one with the currently open era, proving
	// resolution isn't just "pick the newest row".
	openEraPlayer := &persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "open-era-token", CreatedAt: older}
	require.NoError(t, db.Create(openEraPlayer).Error)
	require.NoError(t, db.Create(&persistence.EraModel{
		Name: "torwind", AgentSymbol: "TORWIND", PlayerID: openEraPlayer.ID,
	}).Error)

	noEraPlayer := &persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "no-era-token", CreatedAt: newer}
	require.NoError(t, db.Create(noEraPlayer).Error)

	repo := persistence.NewGormPlayerRepository(db)
	found, err := repo.FindByAgentSymbol(context.Background(), "TORWIND")

	require.NoError(t, err)
	require.Equal(t, "open-era-token", found.Token)
}

func TestFindByAgentSymbolFallsBackToNewestPlayerWhenNoEraIsOpen(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	older := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)
	closedAt := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	closedEraPlayer := &persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "old-token", CreatedAt: older}
	require.NoError(t, db.Create(closedEraPlayer).Error)
	require.NoError(t, db.Create(&persistence.EraModel{
		Name: "torwind", AgentSymbol: "TORWIND", PlayerID: closedEraPlayer.ID, ClosedAt: &closedAt,
	}).Error)

	newestPlayer := &persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "new-token", CreatedAt: newer}
	require.NoError(t, db.Create(newestPlayer).Error)

	repo := persistence.NewGormPlayerRepository(db)
	found, err := repo.FindByAgentSymbol(context.Background(), "TORWIND")

	require.NoError(t, err)
	require.Equal(t, "new-token", found.Token)
}

func TestFindByAgentSymbolReturnsErrorWhenNoPlayerHasSymbol(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	repo := persistence.NewGormPlayerRepository(db)
	_, err = repo.FindByAgentSymbol(context.Background(), "GHOST")

	require.Error(t, err)
}
