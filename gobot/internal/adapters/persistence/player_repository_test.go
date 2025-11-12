package persistence_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/test/helpers"
)

func TestPlayerRepository_SaveAndFind(t *testing.T) {
	// Arrange
	db := helpers.NewTestDB(t)
	repo := persistence.NewGormPlayerRepository(db)

	player := &common.Player{
		ID:          1,
		AgentSymbol: "TEST-AGENT",
		Token:       "test-token-123",
		Credits:     100000,
		Metadata: map[string]interface{}{
			"faction": "COSMIC",
		},
	}

	// Act - Save
	err := repo.Save(context.Background(), player)

	// Assert
	require.NoError(t, err)

	// Act - FindByID
	found, err := repo.FindByID(context.Background(), 1)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, player.ID, found.ID)
	assert.Equal(t, player.AgentSymbol, found.AgentSymbol)
	assert.Equal(t, player.Token, found.Token)
	assert.Equal(t, player.Credits, found.Credits)
	assert.NotNil(t, found.Metadata)
}

func TestPlayerRepository_FindByAgentSymbol(t *testing.T) {
	// Arrange
	db := helpers.NewTestDB(t)
	repo := persistence.NewGormPlayerRepository(db)

	player := &common.Player{
		ID:          2,
		AgentSymbol: "AGENT-2",
		Token:       "token-456",
		Credits:     50000,
	}

	err := repo.Save(context.Background(), player)
	require.NoError(t, err)

	// Act
	found, err := repo.FindByAgentSymbol(context.Background(), "AGENT-2")

	// Assert
	require.NoError(t, err)
	assert.Equal(t, player.ID, found.ID)
	assert.Equal(t, player.AgentSymbol, found.AgentSymbol)
}

func TestPlayerRepository_NotFound(t *testing.T) {
	// Arrange
	db := helpers.NewTestDB(t)
	repo := persistence.NewGormPlayerRepository(db)

	// Act
	_, err := repo.FindByID(context.Background(), 999)

	// Assert
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "player not found")
}
