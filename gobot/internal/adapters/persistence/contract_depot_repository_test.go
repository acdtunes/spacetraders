package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/contract/depotstore"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// newDepotTestRepo builds a real GORM depot repository over an in-memory SQLite
// test DB, scoped to a freshly-created player (the FK the composite (id, player_id)
// primary key references). Mirrors newScoutPostTestRepo — depots carry no era, so
// no era row is needed (they follow the storage/gas operation player-scoped idiom).
func newDepotTestRepo(t *testing.T) (*persistence.GormContractDepotRepository, *gorm.DB, int) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	player := persistence.PlayerModel{AgentSymbol: "SP-DEPOT", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)
	return persistence.NewGormContractDepotRepository(db, player.ID), db, player.ID
}

func buildDepot(t *testing.T, id string, warehouses, stockers, deliveryHulls, sourceHubs []depot.Element) *depot.ContractDepot {
	t.Helper()
	c, err := depot.NewContractDepot(id, warehouses, stockers, deliveryHulls, sourceHubs)
	require.NoError(t, err)
	return c
}

// A depot with every element class populated survives a full Save->Get round-trip
// through the real DB: each of the four JSON-encoded slices (warehouse, stocker,
// delivery hull, source hub) reloads with its waypoints and crewed ship symbols
// intact. This is the durable substrate the restart-safe registry rebuild stands on.
func TestContractDepotRepo_SaveThenGet_RoundTripsAllRoles(t *testing.T) {
	repo, _, _ := newDepotTestRepo(t)
	ctx := context.Background()

	original := buildDepot(t, "alpha",
		[]depot.Element{{Waypoint: "X1-A-1", ShipSymbol: "WH-1"}},
		[]depot.Element{{Waypoint: "X1-SRC-1", ShipSymbol: "ST-9"}},
		[]depot.Element{{Waypoint: "X1-A-1", ShipSymbol: "DH-1"}},
		[]depot.Element{{Waypoint: "X1-HUB-1", ShipSymbol: ""}},
	)
	require.NoError(t, repo.Save(ctx, original))

	got, ok, err := repo.Get(ctx, "alpha")
	require.NoError(t, err)
	require.True(t, ok, "a saved depot must be found")
	require.Equal(t, "alpha", got.ID())
	require.Equal(t, []depot.Element{{Waypoint: "X1-A-1", ShipSymbol: "WH-1"}}, got.Warehouses())
	require.Equal(t, []depot.Element{{Waypoint: "X1-SRC-1", ShipSymbol: "ST-9"}}, got.Stockers())
	require.Equal(t, []depot.Element{{Waypoint: "X1-A-1", ShipSymbol: "DH-1"}}, got.DeliveryHulls())
	require.Equal(t, []depot.Element{{Waypoint: "X1-HUB-1", ShipSymbol: ""}}, got.SourceHubs())
}

// Save is an upsert keyed on (id, player_id): re-saving the same depot id with a
// changed topology replaces the row in place rather than duplicating it, so the
// store's granular mutations (load->update->Save) persist without accumulating rows.
func TestContractDepotRepo_Save_UpsertsInPlace(t *testing.T) {
	repo, _, _ := newDepotTestRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.Save(ctx, buildDepot(t, "alpha",
		[]depot.Element{{Waypoint: "X1-A-1", ShipSymbol: "WH-1"}}, nil, nil, nil)))
	require.NoError(t, repo.Save(ctx, buildDepot(t, "alpha",
		[]depot.Element{{Waypoint: "X1-A-1", ShipSymbol: "WH-1"}},
		[]depot.Element{{Waypoint: "X1-SRC-1", ShipSymbol: "ST-9"}}, nil, nil)))

	all, err := repo.List(ctx)
	require.NoError(t, err)
	require.Len(t, all, 1, "re-saving the same id must update in place, not duplicate")
	require.Len(t, all[0].Stockers(), 1, "the updated topology is what persists")
}

// Get of an unknown id returns (nil, false, nil) — the not-found contract the store's
// granular ops key on to report "depot does not exist" rather than fabricate one.
func TestContractDepotRepo_Get_MissingReturnsNotFound(t *testing.T) {
	repo, _, _ := newDepotTestRepo(t)
	got, ok, err := repo.Get(context.Background(), "ghost")
	require.NoError(t, err, "a missing depot is not an error")
	require.False(t, ok)
	require.Nil(t, got)
}

// List returns every persisted depot for the player; Delete removes one and is a
// no-op (not an error) on a missing id — the idempotent delete the store's declarative
// ApplyTopology relies on when pruning stale depots.
func TestContractDepotRepo_ListAndDelete(t *testing.T) {
	repo, _, _ := newDepotTestRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.Save(ctx, buildDepot(t, "alpha", []depot.Element{{Waypoint: "X1-A-1", ShipSymbol: "WH-A"}}, nil, nil, nil)))
	require.NoError(t, repo.Save(ctx, buildDepot(t, "beta", []depot.Element{{Waypoint: "X1-B-1", ShipSymbol: "WH-B"}}, nil, nil, nil)))

	all, err := repo.List(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"alpha", "beta"}, ids(all))

	require.NoError(t, repo.Delete(ctx, "alpha"))
	all, err = repo.List(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"beta"}, ids(all))

	require.NoError(t, repo.Delete(ctx, "nonexistent"), "deleting a missing depot is not an error")
}

// The repository is scoped to the player baked in at construction: a depot saved
// under player A is invisible to a repository over the same DB for player B. Two
// players may even share a depot id (composite PK), and neither sees the other's.
func TestContractDepotRepo_PlayerScoped(t *testing.T) {
	repoA, db, _ := newDepotTestRepo(t)
	ctx := context.Background()
	require.NoError(t, repoA.Save(ctx, buildDepot(t, "alpha", []depot.Element{{Waypoint: "X1-A-1", ShipSymbol: "WH-A"}}, nil, nil, nil)))

	playerB := persistence.PlayerModel{AgentSymbol: "SP-DEPOT-B", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&playerB).Error)
	repoB := persistence.NewGormContractDepotRepository(db, playerB.ID)

	_, ok, err := repoB.Get(ctx, "alpha")
	require.NoError(t, err)
	require.False(t, ok, "player B must not see player A's depot")
	listB, err := repoB.List(ctx)
	require.NoError(t, err)
	require.Empty(t, listB, "player B's view is empty")
}

// The money integration test: the application Store composed over the REAL adapter is
// restart-safe. After a declarative ApplyTopology, a fresh Store over a fresh repo on
// the SAME database rebuilds the identical registry, and the persisted warehouse
// waypoint still drives contract routing — proving the whole port-to-port path
// (Store -> Repository -> DB -> Store) survives a simulated daemon restart.
func TestContractDepotRepo_RestartSafeThroughStore(t *testing.T) {
	repo, db, playerID := newDepotTestRepo(t)
	ctx := context.Background()

	applied := buildDepot(t, "central",
		[]depot.Element{{Waypoint: "X1-CENTRAL-A1", ShipSymbol: "WH-CENTRAL"}},
		nil,
		[]depot.Element{{Waypoint: "X1-CENTRAL-A1", ShipSymbol: "DH-CENTRAL"}}, nil)
	require.NoError(t, depotstore.New(repo).ApplyTopology(ctx, []*depot.ContractDepot{applied}))

	// Simulate a daemon restart: a brand-new repo + store over the same durable DB.
	reloaded := persistence.NewGormContractDepotRepository(db, playerID)
	registry, err := depotstore.New(reloaded).LoadRegistry(ctx)
	require.NoError(t, err)

	depots := registry.Depots()
	require.Len(t, depots, 1)
	require.Equal(t, "central", depots[0].ID())

	routed := registry.RouteContract([]string{"X1-CENTRAL-A1"})
	require.NotNil(t, routed, "the persisted warehouse waypoint must still own its destination after restart")
	require.Equal(t, "central", routed.ID())
}

func ids(depots []*depot.ContractDepot) []string {
	out := make([]string, 0, len(depots))
	for _, c := range depots {
		out = append(out, c.ID())
	}
	return out
}
