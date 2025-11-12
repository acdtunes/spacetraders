package persistence_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/test/helpers"
)

func TestWaypointRepository_SaveAndFind(t *testing.T) {
	// Arrange
	db := helpers.NewTestDB(t)
	repo := persistence.NewGormWaypointRepository(db)

	waypoint, err := shared.NewWaypoint("X1-GZ7-A1", 10.5, 20.3)
	require.NoError(t, err)

	waypoint.SystemSymbol = "X1-GZ7"
	waypoint.Type = "PLANET"
	waypoint.HasFuel = true
	waypoint.Traits = []string{"MARKETPLACE", "SHIPYARD"}
	waypoint.Orbitals = []string{"X1-GZ7-A1a", "X1-GZ7-A1b"}

	// Act - Save
	err = repo.Save(context.Background(), waypoint)

	// Assert
	require.NoError(t, err)

	// Act - FindBySymbol
	found, err := repo.FindBySymbol(context.Background(), "X1-GZ7-A1", "X1-GZ7")

	// Assert
	require.NoError(t, err)
	assert.Equal(t, waypoint.Symbol, found.Symbol)
	assert.Equal(t, waypoint.SystemSymbol, found.SystemSymbol)
	assert.Equal(t, waypoint.Type, found.Type)
	assert.Equal(t, waypoint.X, found.X)
	assert.Equal(t, waypoint.Y, found.Y)
	assert.Equal(t, waypoint.HasFuel, found.HasFuel)
	assert.Equal(t, waypoint.Traits, found.Traits)
	assert.Equal(t, waypoint.Orbitals, found.Orbitals)
}

func TestWaypointRepository_ListBySystem(t *testing.T) {
	// Arrange
	db := helpers.NewTestDB(t)
	repo := persistence.NewGormWaypointRepository(db)

	// Create multiple waypoints in same system
	wp1, _ := shared.NewWaypoint("X1-GZ7-A1", 10.0, 20.0)
	wp1.SystemSymbol = "X1-GZ7"
	wp1.Type = "PLANET"

	wp2, _ := shared.NewWaypoint("X1-GZ7-B2", 30.0, 40.0)
	wp2.SystemSymbol = "X1-GZ7"
	wp2.Type = "MOON"

	wp3, _ := shared.NewWaypoint("X1-ABC-C3", 50.0, 60.0)
	wp3.SystemSymbol = "X1-ABC"
	wp3.Type = "ASTEROID"

	require.NoError(t, repo.Save(context.Background(), wp1))
	require.NoError(t, repo.Save(context.Background(), wp2))
	require.NoError(t, repo.Save(context.Background(), wp3))

	// Act
	waypoints, err := repo.ListBySystem(context.Background(), "X1-GZ7")

	// Assert
	require.NoError(t, err)
	assert.Len(t, waypoints, 2)

	symbols := make([]string, len(waypoints))
	for i, wp := range waypoints {
		symbols[i] = wp.Symbol
	}
	assert.Contains(t, symbols, "X1-GZ7-A1")
	assert.Contains(t, symbols, "X1-GZ7-B2")
}
