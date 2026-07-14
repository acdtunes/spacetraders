package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/contract/clusterstore"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/cluster"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// newClusterTestRepo builds a real GORM cluster repository over an in-memory SQLite
// test DB, scoped to a freshly-created player (the FK the composite (id, player_id)
// primary key references). Mirrors newScoutPostTestRepo — clusters carry no era, so
// no era row is needed (they follow the storage/gas operation player-scoped idiom).
func newClusterTestRepo(t *testing.T) (*persistence.GormContractClusterRepository, *gorm.DB, int) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	player := persistence.PlayerModel{AgentSymbol: "SP-CLUSTER", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)
	return persistence.NewGormContractClusterRepository(db, player.ID), db, player.ID
}

func buildCluster(t *testing.T, id string, warehouses, stockers, deliveryHulls, sourceHubs []cluster.Element) *cluster.ContractCluster {
	t.Helper()
	c, err := cluster.NewContractCluster(id, warehouses, stockers, deliveryHulls, sourceHubs)
	require.NoError(t, err)
	return c
}

// A cluster with every element class populated survives a full Save->Get round-trip
// through the real DB: each of the four JSON-encoded slices (warehouse, stocker,
// delivery hull, source hub) reloads with its waypoints and crewed ship symbols
// intact. This is the durable substrate the restart-safe registry rebuild stands on.
func TestContractClusterRepo_SaveThenGet_RoundTripsAllRoles(t *testing.T) {
	repo, _, _ := newClusterTestRepo(t)
	ctx := context.Background()

	original := buildCluster(t, "alpha",
		[]cluster.Element{{Waypoint: "X1-A-1", ShipSymbol: "WH-1"}},
		[]cluster.Element{{Waypoint: "X1-SRC-1", ShipSymbol: "ST-9"}},
		[]cluster.Element{{Waypoint: "X1-A-1", ShipSymbol: "DH-1"}},
		[]cluster.Element{{Waypoint: "X1-HUB-1", ShipSymbol: ""}},
	)
	require.NoError(t, repo.Save(ctx, original))

	got, ok, err := repo.Get(ctx, "alpha")
	require.NoError(t, err)
	require.True(t, ok, "a saved cluster must be found")
	require.Equal(t, "alpha", got.ID())
	require.Equal(t, []cluster.Element{{Waypoint: "X1-A-1", ShipSymbol: "WH-1"}}, got.Warehouses())
	require.Equal(t, []cluster.Element{{Waypoint: "X1-SRC-1", ShipSymbol: "ST-9"}}, got.Stockers())
	require.Equal(t, []cluster.Element{{Waypoint: "X1-A-1", ShipSymbol: "DH-1"}}, got.DeliveryHulls())
	require.Equal(t, []cluster.Element{{Waypoint: "X1-HUB-1", ShipSymbol: ""}}, got.SourceHubs())
}

// Save is an upsert keyed on (id, player_id): re-saving the same cluster id with a
// changed topology replaces the row in place rather than duplicating it, so the
// store's granular mutations (load->update->Save) persist without accumulating rows.
func TestContractClusterRepo_Save_UpsertsInPlace(t *testing.T) {
	repo, _, _ := newClusterTestRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.Save(ctx, buildCluster(t, "alpha",
		[]cluster.Element{{Waypoint: "X1-A-1", ShipSymbol: "WH-1"}}, nil, nil, nil)))
	require.NoError(t, repo.Save(ctx, buildCluster(t, "alpha",
		[]cluster.Element{{Waypoint: "X1-A-1", ShipSymbol: "WH-1"}},
		[]cluster.Element{{Waypoint: "X1-SRC-1", ShipSymbol: "ST-9"}}, nil, nil)))

	all, err := repo.List(ctx)
	require.NoError(t, err)
	require.Len(t, all, 1, "re-saving the same id must update in place, not duplicate")
	require.Len(t, all[0].Stockers(), 1, "the updated topology is what persists")
}

// Get of an unknown id returns (nil, false, nil) — the not-found contract the store's
// granular ops key on to report "cluster does not exist" rather than fabricate one.
func TestContractClusterRepo_Get_MissingReturnsNotFound(t *testing.T) {
	repo, _, _ := newClusterTestRepo(t)
	got, ok, err := repo.Get(context.Background(), "ghost")
	require.NoError(t, err, "a missing cluster is not an error")
	require.False(t, ok)
	require.Nil(t, got)
}

// List returns every persisted cluster for the player; Delete removes one and is a
// no-op (not an error) on a missing id — the idempotent delete the store's declarative
// ApplyTopology relies on when pruning stale clusters.
func TestContractClusterRepo_ListAndDelete(t *testing.T) {
	repo, _, _ := newClusterTestRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.Save(ctx, buildCluster(t, "alpha", []cluster.Element{{Waypoint: "X1-A-1", ShipSymbol: "WH-A"}}, nil, nil, nil)))
	require.NoError(t, repo.Save(ctx, buildCluster(t, "beta", []cluster.Element{{Waypoint: "X1-B-1", ShipSymbol: "WH-B"}}, nil, nil, nil)))

	all, err := repo.List(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"alpha", "beta"}, ids(all))

	require.NoError(t, repo.Delete(ctx, "alpha"))
	all, err = repo.List(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"beta"}, ids(all))

	require.NoError(t, repo.Delete(ctx, "nonexistent"), "deleting a missing cluster is not an error")
}

// The repository is scoped to the player baked in at construction: a cluster saved
// under player A is invisible to a repository over the same DB for player B. Two
// players may even share a cluster id (composite PK), and neither sees the other's.
func TestContractClusterRepo_PlayerScoped(t *testing.T) {
	repoA, db, _ := newClusterTestRepo(t)
	ctx := context.Background()
	require.NoError(t, repoA.Save(ctx, buildCluster(t, "alpha", []cluster.Element{{Waypoint: "X1-A-1", ShipSymbol: "WH-A"}}, nil, nil, nil)))

	playerB := persistence.PlayerModel{AgentSymbol: "SP-CLUSTER-B", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&playerB).Error)
	repoB := persistence.NewGormContractClusterRepository(db, playerB.ID)

	_, ok, err := repoB.Get(ctx, "alpha")
	require.NoError(t, err)
	require.False(t, ok, "player B must not see player A's cluster")
	listB, err := repoB.List(ctx)
	require.NoError(t, err)
	require.Empty(t, listB, "player B's view is empty")
}

// The money integration test: the application Store composed over the REAL adapter is
// restart-safe. After a declarative ApplyTopology, a fresh Store over a fresh repo on
// the SAME database rebuilds the identical registry, and the persisted warehouse
// waypoint still drives contract routing — proving the whole port-to-port path
// (Store -> Repository -> DB -> Store) survives a simulated daemon restart.
func TestContractClusterRepo_RestartSafeThroughStore(t *testing.T) {
	repo, db, playerID := newClusterTestRepo(t)
	ctx := context.Background()

	applied := buildCluster(t, "central",
		[]cluster.Element{{Waypoint: "X1-CENTRAL-A1", ShipSymbol: "WH-CENTRAL"}},
		nil,
		[]cluster.Element{{Waypoint: "X1-CENTRAL-A1", ShipSymbol: "DH-CENTRAL"}}, nil)
	require.NoError(t, clusterstore.New(repo).ApplyTopology(ctx, []*cluster.ContractCluster{applied}))

	// Simulate a daemon restart: a brand-new repo + store over the same durable DB.
	reloaded := persistence.NewGormContractClusterRepository(db, playerID)
	registry, err := clusterstore.New(reloaded).LoadRegistry(ctx)
	require.NoError(t, err)

	clusters := registry.Clusters()
	require.Len(t, clusters, 1)
	require.Equal(t, "central", clusters[0].ID())

	routed := registry.RouteContract([]string{"X1-CENTRAL-A1"})
	require.NotNil(t, routed, "the persisted warehouse waypoint must still own its destination after restart")
	require.Equal(t, "central", routed.ID())
}

func ids(clusters []*cluster.ContractCluster) []string {
	out := make([]string, 0, len(clusters))
	for _, c := range clusters {
		out = append(out, c.ID())
	}
	return out
}
