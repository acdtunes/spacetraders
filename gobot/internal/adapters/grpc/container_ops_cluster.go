package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/contract/clusterstore"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/cluster"
)

// This file is the daemon-side cluster-management surface (bead sp-u9xa): the single
// writer of contract-cluster topology (RULINGS #3), exposing BOTH modes over the
// application Store, which persists through the Item-1 Repository. The CLI reaches these
// through the RPC; every mutation is durable the instant it returns, so a granular edit
// needs no daemon restart to take effect — a fresh LoadRegistry (the contract engine's
// per-boot rebuild, Item 4) re-derives exactly what was written.
//
// Nothing here hardcodes a waypoint, ship, or count: the whole topology is operator data
// carried in the spec / granular args (the sp-u9xa parametrization principle).

// ElementSpec is the boundary DTO for one placed cluster member — a {waypoint, ship}
// pair. ShipSymbol may be empty for a declared-but-uncrewed slot.
type ElementSpec struct {
	Waypoint   string `json:"waypoint"`
	ShipSymbol string `json:"ship_symbol"`
}

// ClusterSpec is the boundary DTO for one whole cluster: its id and its four element
// classes. It is the serializable shape the CLI parses from an operator spec and the
// granular `cluster add` sends. It maps 1:1 onto the domain cluster via toDomain.
type ClusterSpec struct {
	ID            string        `json:"id"`
	Warehouses    []ElementSpec `json:"warehouses"`
	Stockers      []ElementSpec `json:"stockers"`
	DeliveryHulls []ElementSpec `json:"delivery_hulls"`
	SourceHubs    []ElementSpec `json:"source_hubs"`
}

// ClusterTopologySpec is the whole-topology spec the declarative bulk apply consumes.
type ClusterTopologySpec struct {
	Clusters []ClusterSpec `json:"clusters"`
}

// clusterStore builds the application Store over the durable, player-scoped repository —
// the single construction point every handler below routes through, so all of them share
// the exact persistence the boot-time registry rebuild reads.
func (s *DaemonServer) clusterStore(playerID int) *clusterstore.Store {
	return clusterstore.New(persistence.NewGormContractClusterRepository(s.db, playerID))
}

// ApplyClusterTopology is mode (A), DECLARATIVE/BULK: it makes the player's persisted set
// EXACTLY the given spec (upserting each cluster, deleting any not named), in one call
// through Store.ApplyTopology. After it returns, a restart's LoadRegistry reflects
// precisely what was applied.
func (s *DaemonServer) ApplyClusterTopology(ctx context.Context, playerID int, spec ClusterTopologySpec) error {
	clusters := make([]*cluster.ContractCluster, 0, len(spec.Clusters))
	for _, cs := range spec.Clusters {
		c, err := cs.toDomain()
		if err != nil {
			return err
		}
		clusters = append(clusters, c)
	}
	return s.clusterStore(playerID).ApplyTopology(ctx, clusters)
}

// AddCluster is mode (B) at cluster granularity: it persists ONE new cluster without
// disturbing the rest of the set, via Store.AddCluster.
func (s *DaemonServer) AddCluster(ctx context.Context, playerID int, spec ClusterSpec) error {
	c, err := spec.toDomain()
	if err != nil {
		return err
	}
	return s.clusterStore(playerID).AddCluster(ctx, c)
}

// RemoveCluster is mode (B) at cluster granularity: it deletes ONE cluster by id, leaving
// the rest, via Store.RemoveCluster (idempotent).
func (s *DaemonServer) RemoveCluster(ctx context.Context, playerID int, clusterID string) error {
	return s.clusterStore(playerID).RemoveCluster(ctx, clusterID)
}

// AddClusterElement is mode (B) at element granularity: it adds one element to a
// cluster's named role and persists it, via Store.AddElement. role is the CLI role name
// (warehouse | stocker | delivery-hull | source-hub).
func (s *DaemonServer) AddClusterElement(ctx context.Context, playerID int, clusterID, role, waypoint, shipSymbol string) error {
	parsedRole, err := cluster.ParseRole(role)
	if err != nil {
		return err
	}
	return s.clusterStore(playerID).AddElement(ctx, clusterID, parsedRole, cluster.Element{Waypoint: waypoint, ShipSymbol: shipSymbol})
}

// RemoveClusterElement is mode (B) at element granularity: it drops the element crewed by
// shipSymbol from a cluster's named role and persists the result, via Store.RemoveElement.
func (s *DaemonServer) RemoveClusterElement(ctx context.Context, playerID int, clusterID, role, shipSymbol string) error {
	parsedRole, err := cluster.ParseRole(role)
	if err != nil {
		return err
	}
	return s.clusterStore(playerID).RemoveElement(ctx, clusterID, parsedRole, shipSymbol)
}

// PlaceClusterElement is mode (B) at element granularity: it repositions the element
// crewed by shipSymbol in a cluster's named role to waypoint and persists it, via
// Store.PlaceElement — the parametrized co-location op (the caller supplies the waypoint).
func (s *DaemonServer) PlaceClusterElement(ctx context.Context, playerID int, clusterID, role, shipSymbol, waypoint string) error {
	parsedRole, err := cluster.ParseRole(role)
	if err != nil {
		return err
	}
	return s.clusterStore(playerID).PlaceElement(ctx, clusterID, parsedRole, shipSymbol, waypoint)
}

// ListClusters returns the player's persisted clusters — the read the CLI's
// `cluster list` renders. It is the registry's read model, rebuilt from the repository.
func (s *DaemonServer) ListClusters(ctx context.Context, playerID int) ([]*cluster.ContractCluster, error) {
	reg, err := s.clusterStore(playerID).LoadRegistry(ctx)
	if err != nil {
		return nil, err
	}
	return reg.Clusters(), nil
}

// LoadClusterRegistry rebuilds the immutable routing registry from the durable repository
// for a player — the restart-safe seam (RULINGS #2) the contract engine routes through.
// The Store owns NO in-memory authority, so every call re-derives the registry from the
// persisted rows: a daemon restart therefore reconstructs the identical registry with no
// snapshot to lose, and an empty repo yields an empty registry (destination warehousing
// OFF, the regression-safe legacy long-haul default), never an error.
func (s *DaemonServer) LoadClusterRegistry(ctx context.Context, playerID int) (*cluster.Registry, error) {
	return s.clusterStore(playerID).LoadRegistry(ctx)
}

// reloadClusterRegistryAtBoot re-derives the player's cluster registry from the durable
// repository at daemon startup and logs a one-line summary — the boot-time reload that
// makes RULINGS #2 visible (persisted clusters reload on restart). It is a pure read,
// safely re-runnable every boot, and fail-open: a load error is logged and swallowed so a
// transient DB hiccup never blocks startup. It routes through the same LoadClusterRegistry
// seam the contract engine consumes, so the boot log reflects exactly what routing will see.
func (s *DaemonServer) reloadClusterRegistryAtBoot(ctx context.Context, playerID int) {
	reg, err := s.LoadClusterRegistry(ctx, playerID)
	if err != nil {
		fmt.Printf("Warning: failed to reload contract cluster registry for player %d: %v\n", playerID, err)
		return
	}
	clusters := reg.Clusters()
	if len(clusters) == 0 {
		return
	}
	fmt.Printf("Reloaded %d contract cluster(s) for player %d from durable store (restart-safe registry)\n", len(clusters), playerID)
}

// toDomain converts a boundary ClusterSpec into a validated domain cluster (the
// NewContractCluster invariants — non-empty id, at least one warehouse — surface here).
func (cs ClusterSpec) toDomain() (*cluster.ContractCluster, error) {
	c, err := cluster.NewContractCluster(
		cs.ID,
		toElements(cs.Warehouses),
		toElements(cs.Stockers),
		toElements(cs.DeliveryHulls),
		toElements(cs.SourceHubs),
	)
	if err != nil {
		return nil, fmt.Errorf("invalid cluster spec %q: %w", cs.ID, err)
	}
	return c, nil
}

// toElements maps a slice of boundary ElementSpecs to domain Elements.
func toElements(specs []ElementSpec) []cluster.Element {
	if len(specs) == 0 {
		return nil
	}
	out := make([]cluster.Element, len(specs))
	for i, e := range specs {
		out[i] = cluster.Element{Waypoint: e.Waypoint, ShipSymbol: e.ShipSymbol}
	}
	return out
}
