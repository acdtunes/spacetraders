package persistence_test

// Integration tests (real GORM/sqlite) for the charting write-back. The
// interesting behaviour is not that a row round-trips — it is that a waypoint
// which was UNCHARTED stops being uncharted, because a charting tour picks its
// next stop from exactly that set and would otherwise chart the same waypoint on
// every tick forever.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

func TestUpsertFromDetail_ChartingClearsTheUnchartedTrait(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	repo := persistence.NewGormWaypointRepository(db)
	ctx := context.Background()

	require.NoError(t, db.Create(&persistence.WaypointModel{
		WaypointSymbol: "X1-BB-A1",
		SystemSymbol:   "X1-BB",
		Type:           "PLANET",
		Traits:         `["UNCHARTED"]`,
	}).Error)

	before, err := repo.ListBySystemWithTrait(ctx, "X1-BB", "UNCHARTED")
	require.NoError(t, err)
	require.Len(t, before, 1)

	require.NoError(t, repo.UpsertFromDetail(ctx, &ports.WaypointDetail{
		Symbol: "X1-BB-A1", Type: "PLANET", X: 12, Y: -4,
		Traits: []string{"MARKETPLACE", "SHIPYARD"},
	}))

	after, err := repo.ListBySystemWithTrait(ctx, "X1-BB", "UNCHARTED")
	require.NoError(t, err)
	require.Empty(t, after, "a charted waypoint must leave the uncharted set, or the tour never ends")

	markets, err := repo.ListBySystemWithTrait(ctx, "X1-BB", "MARKETPLACE")
	require.NoError(t, err)
	require.Len(t, markets, 1)
	require.Equal(t, float64(12), markets[0].X)
	require.Equal(t, float64(-4), markets[0].Y)
	require.Equal(t, "X1-BB", markets[0].SystemSymbol)
}

func TestUpsertFromDetail_DerivesOnSiteFuelFromTheTraits(t *testing.T) {
	// Fuel availability is a CONSEQUENCE of the traits and the domain owns that
	// rule; restating it in the cache would give the router a second, drifting
	// definition of where it may refuel.
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	repo := persistence.NewGormWaypointRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.UpsertFromDetail(ctx, &ports.WaypointDetail{
		Symbol: "X1-BB-A1", Type: "PLANET", Traits: []string{"MARKETPLACE"},
	}))
	require.NoError(t, repo.UpsertFromDetail(ctx, &ports.WaypointDetail{
		Symbol: "X1-BB-B2", Type: "ASTEROID", Traits: []string{"BARREN"},
	}))

	fuelled, err := repo.ListBySystemWithTrait(ctx, "X1-BB", "MARKETPLACE")
	require.NoError(t, err)
	require.Len(t, fuelled, 1)
	require.True(t, fuelled[0].HasFuel)

	dry, err := repo.ListBySystemWithTrait(ctx, "X1-BB", "BARREN")
	require.NoError(t, err)
	require.Len(t, dry, 1)
	require.False(t, dry[0].HasFuel)
}

func TestUpsertFromDetail_RejectsANilDetailAndAnUnnamedWaypoint(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	repo := persistence.NewGormWaypointRepository(db)
	ctx := context.Background()

	require.Error(t, repo.UpsertFromDetail(ctx, nil))
	require.Error(t, repo.UpsertFromDetail(ctx, &ports.WaypointDetail{Symbol: ""}))
}
