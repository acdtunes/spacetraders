package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/contract/clusterstore"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/cluster"
)

// freshRegistry rebuilds the routing registry from the DB via a BRAND-NEW store — the
// "simulate a daemon restart" read the handler tests assert against. It proves each op
// persisted all the way through the Store -> Repository -> DB, not merely an in-memory
// mutation the next restart would forget.
func freshRegistry(t *testing.T, db *gorm.DB, playerID int) *cluster.Registry {
	t.Helper()
	reg, err := clusterstore.New(persistence.NewGormContractClusterRepository(db, playerID)).LoadRegistry(context.Background())
	require.NoError(t, err)
	return reg
}

func clusterByID(reg *cluster.Registry, id string) *cluster.ContractCluster {
	for _, c := range reg.Clusters() {
		if c.ID() == id {
			return c
		}
	}
	return nil
}

// (A) DECLARATIVE/BULK: applying a whole topology spec persists every cluster durably,
// and the persisted warehouse waypoints drive contract routing after a restart — the
// end-to-end proof that the bulk handler goes through the Store into the Repository.
func TestApplyClusterTopology_PersistsSpecRestartSafe(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	ctx := context.Background()

	spec := ClusterTopologySpec{Clusters: []ClusterSpec{
		{
			ID:            "central",
			Warehouses:    []ElementSpec{{Waypoint: "X1-CENTRAL-A1", ShipSymbol: "WH-CENTRAL"}},
			DeliveryHulls: []ElementSpec{{Waypoint: "X1-CENTRAL-A1", ShipSymbol: "DH-CENTRAL"}},
		},
		{
			ID:         "outpost",
			Warehouses: []ElementSpec{{Waypoint: "X1-OUT-B2", ShipSymbol: "WH-OUT"}},
			Stockers:   []ElementSpec{{Waypoint: "X1-SRC-1", ShipSymbol: "ST-1"}},
		},
	}}
	require.NoError(t, s.ApplyClusterTopology(ctx, playerID, spec))

	reg := freshRegistry(t, db, playerID)
	require.Len(t, reg.Clusters(), 2)
	routed := reg.RouteContract([]string{"X1-CENTRAL-A1"})
	require.NotNil(t, routed, "the applied warehouse waypoint must own its destination after restart")
	require.Equal(t, "central", routed.ID())
}

// (B) GRANULAR cluster-level: AddCluster grows the set one cluster at a time and
// RemoveCluster drops one without disturbing the rest — no bulk replace, and durable.
func TestAddAndRemoveCluster_Granular(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	ctx := context.Background()

	require.NoError(t, s.AddCluster(ctx, playerID, ClusterSpec{ID: "alpha", Warehouses: []ElementSpec{{Waypoint: "X1-A-1", ShipSymbol: "WH-A"}}}))
	require.NoError(t, s.AddCluster(ctx, playerID, ClusterSpec{ID: "beta", Warehouses: []ElementSpec{{Waypoint: "X1-B-1", ShipSymbol: "WH-B"}}}))
	require.Len(t, freshRegistry(t, db, playerID).Clusters(), 2)

	require.NoError(t, s.RemoveCluster(ctx, playerID, "alpha"))
	reg := freshRegistry(t, db, playerID)
	require.Len(t, reg.Clusters(), 1)
	require.NotNil(t, clusterByID(reg, "beta"), "removing alpha must leave beta")
	require.Nil(t, clusterByID(reg, "alpha"))
}

// (B) GRANULAR element-level: adding an element to any of the four roles persists — one
// parametrized test proves the role-string parsing plus durable add for every role
// (Mandate 5: input variations of one behavior are one parametrized test).
func TestAddClusterElement_PersistsAcrossRoles(t *testing.T) {
	roles := []string{"warehouse", "stocker", "delivery-hull", "source-hub"}
	for _, role := range roles {
		role := role
		t.Run(role, func(t *testing.T) {
			s, db, playerID := newRecoveryTestServer(t)
			ctx := context.Background()
			require.NoError(t, s.AddCluster(ctx, playerID, ClusterSpec{ID: "alpha", Warehouses: []ElementSpec{{Waypoint: "X1-A-1", ShipSymbol: "WH-A"}}}))

			require.NoError(t, s.AddClusterElement(ctx, playerID, "alpha", role, "X1-NEW-1", "SHIP-NEW"))

			got := clusterByID(freshRegistry(t, db, playerID), "alpha")
			require.NotNil(t, got)
			require.True(t, hasElement(elementsForRole(t, got, role), "SHIP-NEW", "X1-NEW-1"),
				"role %q must carry the durably-added element", role)
		})
	}
}

// (B) GRANULAR: removing an element by its crewing ship persists.
func TestRemoveClusterElement_Persists(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	ctx := context.Background()
	require.NoError(t, s.AddCluster(ctx, playerID, ClusterSpec{
		ID:         "alpha",
		Warehouses: []ElementSpec{{Waypoint: "X1-A-1", ShipSymbol: "WH-A"}},
		Stockers:   []ElementSpec{{Waypoint: "X1-SRC-1", ShipSymbol: "ST-9"}},
	}))

	require.NoError(t, s.RemoveClusterElement(ctx, playerID, "alpha", "stocker", "ST-9"))

	got := clusterByID(freshRegistry(t, db, playerID), "alpha")
	require.NotNil(t, got)
	require.Empty(t, got.Stockers(), "the removed stocker must be gone after restart")
}

// (B) GRANULAR: PLACE repositions an existing element to a new waypoint, durably — the
// co-location op (e.g. parking a delivery hull at its warehouse).
func TestPlaceClusterElement_Repositions(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	ctx := context.Background()
	require.NoError(t, s.AddCluster(ctx, playerID, ClusterSpec{
		ID:            "alpha",
		Warehouses:    []ElementSpec{{Waypoint: "X1-A-1", ShipSymbol: "WH-A"}},
		DeliveryHulls: []ElementSpec{{Waypoint: "X1-OFF-1", ShipSymbol: "DH-1"}},
	}))

	require.NoError(t, s.PlaceClusterElement(ctx, playerID, "alpha", "delivery-hull", "DH-1", "X1-A-1"))

	got := clusterByID(freshRegistry(t, db, playerID), "alpha")
	require.NotNil(t, got)
	hulls := got.DeliveryHulls()
	require.Len(t, hulls, 1)
	require.Equal(t, "X1-A-1", hulls[0].Waypoint, "the hull must be repositioned to its warehouse, durably")
}

// A mistyped role is rejected loudly rather than silently touching the wrong class.
func TestClusterElement_InvalidRoleRejected(t *testing.T) {
	s, _, playerID := newRecoveryTestServer(t)
	ctx := context.Background()
	require.NoError(t, s.AddCluster(ctx, playerID, ClusterSpec{ID: "alpha", Warehouses: []ElementSpec{{Waypoint: "X1-A-1", ShipSymbol: "WH-A"}}}))

	err := s.AddClusterElement(ctx, playerID, "alpha", "not-a-role", "X1-1", "S")
	require.Error(t, err)
	require.Contains(t, err.Error(), "role")
}

// A granular op on a cluster that does not exist errors (the store's not-found surfaces),
// so the CLI reports it rather than fabricating a malformed cluster.
func TestClusterElement_UnknownClusterRejected(t *testing.T) {
	s, _, playerID := newRecoveryTestServer(t)
	err := s.AddClusterElement(context.Background(), playerID, "ghost", "stocker", "X1-1", "S")
	require.Error(t, err)
}

// ListClusters returns exactly the player's persisted clusters — the read the CLI's
// `cluster list` renders.
func TestListClusters_ReturnsPersisted(t *testing.T) {
	s, _, playerID := newRecoveryTestServer(t)
	ctx := context.Background()
	require.NoError(t, s.AddCluster(ctx, playerID, ClusterSpec{ID: "alpha", Warehouses: []ElementSpec{{Waypoint: "X1-A-1", ShipSymbol: "WH-A"}}}))
	require.NoError(t, s.AddCluster(ctx, playerID, ClusterSpec{ID: "beta", Warehouses: []ElementSpec{{Waypoint: "X1-B-1", ShipSymbol: "WH-B"}}}))

	clusters, err := s.ListClusters(ctx, playerID)
	require.NoError(t, err)
	require.Len(t, clusters, 2)
}

// Item 4 (boot wiring, RULINGS #2): a FRESH daemon server sharing only the durable DB
// rebuilds the IDENTICAL cluster registry from the repository — the restart-safe reload
// the contract engine routes through. Nothing survives in memory across the "restart";
// the registry is re-derived entirely from persisted rows, and the persisted warehouse
// still owns its destination.
func TestLoadClusterRegistry_RestartSafeRebuild(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	ctx := context.Background()
	require.NoError(t, s.ApplyClusterTopology(ctx, playerID, ClusterTopologySpec{Clusters: []ClusterSpec{
		{ID: "central", Warehouses: []ElementSpec{{Waypoint: "X1-CENTRAL-A1", ShipSymbol: "WH-C"}}},
		{ID: "outpost", Warehouses: []ElementSpec{{Waypoint: "X1-OUT-B2", ShipSymbol: "WH-O"}}},
	}}))

	// Simulate a daemon restart: a brand-new server holding only the durable DB handle.
	restarted := &DaemonServer{db: db}
	reg, err := restarted.LoadClusterRegistry(ctx, playerID)
	require.NoError(t, err)
	require.Len(t, reg.Clusters(), 2, "restart rebuilds every persisted cluster from the repo")

	routed := reg.RouteContract([]string{"X1-CENTRAL-A1"})
	require.NotNil(t, routed, "the persisted warehouse must still own its destination after restart")
	require.Equal(t, "central", routed.ID())
}

// An empty repo yields the regression-safe default at boot: an empty registry that owns
// nothing (destination warehousing OFF), never an error — so a daemon with no configured
// clusters routes exactly as it did before the feature existed.
func TestLoadClusterRegistry_EmptyRepoOwnsNothing(t *testing.T) {
	s, _, playerID := newRecoveryTestServer(t)
	reg, err := s.LoadClusterRegistry(context.Background(), playerID)
	require.NoError(t, err)
	require.Empty(t, reg.Clusters())
	require.Nil(t, reg.RouteContract([]string{"X1-ANYWHERE"}), "no clusters -> no routing, the legacy long-haul fallback")
}

func elementsForRole(t *testing.T, c *cluster.ContractCluster, role string) []cluster.Element {
	t.Helper()
	switch role {
	case "warehouse":
		return c.Warehouses()
	case "stocker":
		return c.Stockers()
	case "delivery-hull":
		return c.DeliveryHulls()
	case "source-hub":
		return c.SourceHubs()
	default:
		t.Fatalf("unknown role %q in test", role)
		return nil
	}
}

func hasElement(elems []cluster.Element, ship, waypoint string) bool {
	for _, e := range elems {
		if e.ShipSymbol == ship && e.Waypoint == waypoint {
			return true
		}
	}
	return false
}
