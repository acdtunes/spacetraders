package persistence_test

// A universe reset renames every waypoint. The graph cache was keyed by system symbol alone, so
// a graph built in a closed era was still served afterwards and routing steered hulls at gate
// waypoints the server no longer knows — 4201, retried forever, hull pinned to the probe cap.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

func graphOf(gate string) *system.NavigationGraph {
	return &system.NavigationGraph{
		SystemSymbol: "X1-KF69",
		Waypoints:    map[string]*shared.Waypoint{gate: {Symbol: gate, Type: "JUMP_GATE"}},
	}
}

func TestSystemGraph_ClosedEraGraphReadsAsMiss(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "old", AgentSymbol: "OLD", PlayerID: 1}).Error)
	repo := persistence.NewGormSystemGraphRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.Add(ctx, "X1-KF69", graphOf("X1-KF69-A21F")))

	// The universe resets: the old era closes and a new one opens.
	closed := time.Now()
	require.NoError(t, db.Model(&persistence.EraModel{}).Where("name = ?", "old").Update("closed_at", &closed).Error)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "new", AgentSymbol: "NEW", PlayerID: 1}).Error)

	got, err := repo.Get(ctx, "X1-KF69")
	require.NoError(t, err)
	if got != nil {
		t.Fatalf("a graph cached in a CLOSED era must read as a cache miss, got %+v", got.Waypoints)
	}
}

// The open era's own graph still serves, or every read would rebuild from the API forever.
func TestSystemGraph_OpenEraGraphStillServes(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "now", AgentSymbol: "NOW", PlayerID: 1}).Error)
	repo := persistence.NewGormSystemGraphRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.Add(ctx, "X1-KF69", graphOf("X1-KF69-X30X")))

	got, err := repo.Get(ctx, "X1-KF69")
	require.NoError(t, err)
	require.NotNil(t, got, "the open era's graph must still be served from cache")
	if _, ok := got.Waypoints["X1-KF69-X30X"]; !ok {
		t.Fatalf("expected the current era's gate, got %+v", got.Waypoints)
	}
}

// A row the previous era left behind is RE-STAMPED by this era's first write, so a system does
// not stay a permanent miss once its graph has been rebuilt.
func TestSystemGraph_RewriteRestampsTheEra(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "old", AgentSymbol: "OLD", PlayerID: 1}).Error)
	repo := persistence.NewGormSystemGraphRepository(db)
	ctx := context.Background()
	require.NoError(t, repo.Add(ctx, "X1-KF69", graphOf("X1-KF69-A21F")))

	closed := time.Now()
	require.NoError(t, db.Model(&persistence.EraModel{}).Where("name = ?", "old").Update("closed_at", &closed).Error)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "new", AgentSymbol: "NEW", PlayerID: 1}).Error)

	require.NoError(t, repo.Add(ctx, "X1-KF69", graphOf("X1-KF69-X30X")))

	got, err := repo.Get(ctx, "X1-KF69")
	require.NoError(t, err)
	require.NotNil(t, got, "a re-stamped graph must be readable in the new era")
	if _, ok := got.Waypoints["X1-KF69-X30X"]; !ok {
		t.Fatalf("expected the rebuilt gate, got %+v", got.Waypoints)
	}
}
