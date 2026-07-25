package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

func seedEdgeEra(t *testing.T) (*persistence.GormGateEdgeRepository, context.Context) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)
	repo := persistence.NewGormGateEdgeRepository(db)
	ctx := context.Background()
	require.NoError(t, repo.Replace(ctx, "X1-KA42", []system.GateEdge{
		{ConnectedSystem: "X1-PA3", GateWaypoint: "X1-PA3-I51"},
		{ConnectedSystem: "X1-GQ92", GateWaypoint: "X1-GQ92-I77"},
	}))
	return repo, ctx
}

// The stored edge already carries the destination's gate WAYPOINT — the exact value
// the live jump-gate read is called for.
func TestStoredGateWaypoint_ReturnsTheStoredDestinationGate(t *testing.T) {
	repo, ctx := seedEdgeEra(t)

	waypoint, ok, err := repo.StoredGateWaypoint(ctx, "X1-KA42", "X1-GQ92")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "X1-GQ92-I77", waypoint)
}

// A system pair with no stored edge is a miss — the caller reads live.
func TestStoredGateWaypoint_UnknownPairIsAMiss(t *testing.T) {
	repo, ctx := seedEdgeEra(t)

	_, ok, err := repo.StoredGateWaypoint(ctx, "X1-KA42", "X1-NOPE")
	require.NoError(t, err)
	require.False(t, ok)
}

// GUARD: the lookup is DIRECTED. An edge KA42->PA3 must not answer a query for
// PA3->KA42; answering it would hand the jump the wrong gate waypoint.
func TestStoredGateWaypoint_IsDirected(t *testing.T) {
	repo, ctx := seedEdgeEra(t)

	_, ok, err := repo.StoredGateWaypoint(ctx, "X1-PA3", "X1-KA42")
	require.NoError(t, err)
	require.False(t, ok, "a reverse-direction query must not be answered by a forward edge")
}

// GUARD: a stale row is not trusted — same freshness bound the routing cache uses.
func TestStoredGateWaypoint_StaleRowIsAMiss(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)
	require.NoError(t, db.Create(&persistence.GateEdgeModel{
		SystemSymbol: "X1-KA42", ConnectedSystem: "X1-PA3", GateWaypoint: "X1-PA3-I51",
		EraID: intPtr(1), SyncedAt: staleTS(),
	}).Error)

	repo := persistence.NewGormGateEdgeRepository(db)
	_, ok, err := repo.StoredGateWaypoint(context.Background(), "X1-KA42", "X1-PA3")
	require.NoError(t, err)
	require.False(t, ok)
}

// GUARD: an era boundary invalidates the record — a dead era's topology is not this
// universe's topology.
func TestStoredGateWaypoint_DeadEraRowIsAMiss(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	closed := time.Now().Add(-time.Hour)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "old", AgentSymbol: "OLD", PlayerID: 1, ClosedAt: &closed}).Error)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "new", AgentSymbol: "NEW", PlayerID: 2}).Error)
	require.NoError(t, db.Create(&persistence.GateEdgeModel{
		SystemSymbol: "X1-KA42", ConnectedSystem: "X1-PA3", GateWaypoint: "X1-PA3-I51",
		EraID: intPtr(1), SyncedAt: freshTS(),
	}).Error)

	repo := persistence.NewGormGateEdgeRepository(db)
	_, ok, err := repo.StoredGateWaypoint(context.Background(), "X1-KA42", "X1-PA3")
	require.NoError(t, err)
	require.False(t, ok)
}

// GUARD: a negative-result BACKOFF MARKER stores an empty connected_system and holds the
// UNREADABLE ORIGIN gate in gate_waypoint. Serving it would send a hull to jump at the gate
// it is already standing on. An empty destination must therefore always be a miss — that
// rejection is exactly what makes a marker row unmatchable by this lookup.
func TestStoredGateWaypoint_EmptyDestinationCannotMatchABackoffMarker(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)
	require.NoError(t, db.Create(&persistence.GateEdgeModel{
		SystemSymbol: "X1-KA42", ConnectedSystem: "", GateWaypoint: "X1-KA42-I50",
		EraID: intPtr(1), SyncedAt: freshTS(), UnreadableSince: freshTS(), AttemptCount: 2,
	}).Error)

	repo := persistence.NewGormGateEdgeRepository(db)
	waypoint, ok, err := repo.StoredGateWaypoint(context.Background(), "X1-KA42", "")
	require.NoError(t, err)
	require.False(t, ok, "an empty destination must never resolve to a stored gate")
	require.Empty(t, waypoint, "the marker's origin gate must never be handed back as a destination")
}

// GUARD: an empty ORIGIN is likewise a miss — it must not scan across systems.
func TestStoredGateWaypoint_EmptyOriginIsAMiss(t *testing.T) {
	repo, ctx := seedEdgeEra(t)

	_, ok, err := repo.StoredGateWaypoint(ctx, "", "X1-PA3")
	require.NoError(t, err)
	require.False(t, ok)
}
