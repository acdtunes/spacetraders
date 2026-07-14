// Package clusterstore is the application-layer manager for contract clusters
// (bead sp-u9xa). It turns the immutable domain cluster model into a DURABLE,
// restart-safe topology: a declarative bulk apply plus granular live add / remove /
// place, every mutation persisted through the Repository port so a daemon restart
// re-derives the exact same registry the contract engine routes against.
//
// Hexagonal split: Store is the driving port the CLI (and daemon boot) calls;
// Repository is the driven port a DB/config-backed adapter implements. The store owns
// NO in-memory authority — all state lives in the repo, which is what makes every
// operation restart-safe by construction.
package clusterstore

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/cluster"
)

// Repository is the durable store of contract clusters (the driven port). A
// DB/config-backed adapter implements it; the in-memory fake in tests stands in at
// the boundary. List drives the restart-safe registry rebuild; Get/Save/Delete drive
// the granular and declarative mutations.
type Repository interface {
	List(ctx context.Context) ([]*cluster.ContractCluster, error)
	Get(ctx context.Context, id string) (*cluster.ContractCluster, bool, error)
	Save(ctx context.Context, c *cluster.ContractCluster) error
	Delete(ctx context.Context, id string) error
}

// Store is the cluster-management driving port. It is stateless beyond its Repository
// handle, so any number of stores over the same repo observe the same durable state.
type Store struct {
	repo Repository
}

// New builds a Store over the given durable repository.
func New(repo Repository) *Store { return &Store{repo: repo} }

// ApplyTopology declaratively makes the persisted set EXACTLY the given clusters: each
// is upserted, and any previously-persisted cluster absent from the desired set is
// deleted. It is the bulk-apply the CLI uses to push a whole analyst-produced topology
// at once; after it returns, LoadRegistry (even from a fresh process) reflects precisely
// what was applied.
func (s *Store) ApplyTopology(ctx context.Context, clusters []*cluster.ContractCluster) error {
	desired := make(map[string]bool, len(clusters))
	for _, c := range clusters {
		if c == nil {
			continue
		}
		desired[c.ID()] = true
		if err := s.repo.Save(ctx, c); err != nil {
			return fmt.Errorf("apply topology: save cluster %q: %w", c.ID(), err)
		}
	}
	existing, err := s.repo.List(ctx)
	if err != nil {
		return fmt.Errorf("apply topology: list existing: %w", err)
	}
	for _, c := range existing {
		if desired[c.ID()] {
			continue
		}
		if err := s.repo.Delete(ctx, c.ID()); err != nil {
			return fmt.Errorf("apply topology: delete stale cluster %q: %w", c.ID(), err)
		}
	}
	return nil
}

// AddElement adds one element to a cluster's named role and persists the result — a
// granular live op. It errors when the cluster does not exist so the CLI reports it
// rather than fabricate a cluster.
func (s *Store) AddElement(ctx context.Context, clusterID string, role cluster.Role, e cluster.Element) error {
	return s.mutate(ctx, clusterID, "add", func(c *cluster.ContractCluster) (*cluster.ContractCluster, error) {
		return c.WithElementAdded(role, e)
	})
}

// RemoveElement drops the element crewed by shipSymbol from a cluster's named role and
// persists the result — a granular live op. Errors bubble up from the domain (unknown
// ship, or removing the last warehouse).
func (s *Store) RemoveElement(ctx context.Context, clusterID string, role cluster.Role, shipSymbol string) error {
	return s.mutate(ctx, clusterID, "remove", func(c *cluster.ContractCluster) (*cluster.ContractCluster, error) {
		return c.WithElementRemoved(role, shipSymbol)
	})
}

// PlaceElement repositions the element crewed by shipSymbol in a cluster's named role to
// waypoint and persists the result — the granular positioning op (placement stays pure
// config: the caller supplies the waypoint).
func (s *Store) PlaceElement(ctx context.Context, clusterID string, role cluster.Role, shipSymbol, waypoint string) error {
	return s.mutate(ctx, clusterID, "place", func(c *cluster.ContractCluster) (*cluster.ContractCluster, error) {
		return c.WithElementPlaced(role, shipSymbol, waypoint)
	})
}

// AddCluster persists a single new cluster WITHOUT touching the rest of the set — the
// granular cluster-level create the CLI's `cluster add` uses, as opposed to
// ApplyTopology's whole-set replace. An already-present id is upserted (the repo's Save
// is an upsert), so re-adding a cluster replaces just that one.
func (s *Store) AddCluster(ctx context.Context, c *cluster.ContractCluster) error {
	if c == nil {
		return fmt.Errorf("add cluster: nil cluster")
	}
	if err := s.repo.Save(ctx, c); err != nil {
		return fmt.Errorf("add cluster %q: %w", c.ID(), err)
	}
	return nil
}

// RemoveCluster deletes a single cluster by id — the granular cluster-level delete the
// CLI's `cluster remove` uses. Idempotent: removing an absent cluster is not an error,
// so the CLI can re-issue a removal safely.
func (s *Store) RemoveCluster(ctx context.Context, id string) error {
	if err := s.repo.Delete(ctx, id); err != nil {
		return fmt.Errorf("remove cluster %q: %w", id, err)
	}
	return nil
}

// LoadRegistry rebuilds the immutable routing registry from the durable set — the
// restart-safe entry point the daemon calls at boot and the CLI calls for status. An
// empty repo yields an empty registry (destination warehousing OFF), never an error.
func (s *Store) LoadRegistry(ctx context.Context) (*cluster.Registry, error) {
	clusters, err := s.repo.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("load registry: %w", err)
	}
	return cluster.NewRegistry(clusters), nil
}

// mutate loads a cluster by id, applies an immutable domain update, and persists the new
// cluster. The shared spine of the granular ops: a missing cluster is a loud error, and
// the domain update's own invariants (unknown ship, last-warehouse) surface verbatim.
func (s *Store) mutate(
	ctx context.Context,
	clusterID, op string,
	apply func(*cluster.ContractCluster) (*cluster.ContractCluster, error),
) error {
	c, ok, err := s.repo.Get(ctx, clusterID)
	if err != nil {
		return fmt.Errorf("%s element: load cluster %q: %w", op, clusterID, err)
	}
	if !ok {
		return fmt.Errorf("%s element: cluster %q does not exist", op, clusterID)
	}
	updated, err := apply(c)
	if err != nil {
		return fmt.Errorf("%s element on cluster %q: %w", op, clusterID, err)
	}
	if err := s.repo.Save(ctx, updated); err != nil {
		return fmt.Errorf("%s element: persist cluster %q: %w", op, clusterID, err)
	}
	return nil
}
