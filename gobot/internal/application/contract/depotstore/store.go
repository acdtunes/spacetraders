// Package depotstore is the application-layer manager for contract depots
// (bead sp-u9xa). It turns the immutable domain depot model into a DURABLE,
// restart-safe topology: a declarative bulk apply plus granular live add / remove /
// place, every mutation persisted through the Repository port so a daemon restart
// re-derives the exact same registry the contract engine routes against.
//
// Hexagonal split: Store is the driving port the CLI (and daemon boot) calls;
// Repository is the driven port a DB/config-backed adapter implements. The store owns
// NO in-memory authority — all state lives in the repo, which is what makes every
// operation restart-safe by construction.
package depotstore

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
)

// Repository is the durable store of contract depots (the driven port). A
// DB/config-backed adapter implements it; the in-memory fake in tests stands in at
// the boundary. List drives the restart-safe registry rebuild; Get/Save/Delete drive
// the granular and declarative mutations.
type Repository interface {
	List(ctx context.Context) ([]*depot.ContractDepot, error)
	Get(ctx context.Context, id string) (*depot.ContractDepot, bool, error)
	Save(ctx context.Context, c *depot.ContractDepot) error
	Delete(ctx context.Context, id string) error
}

// Store is the depot-management driving port. It is stateless beyond its Repository
// handle, so any number of stores over the same repo observe the same durable state.
type Store struct {
	repo Repository
}

// New builds a Store over the given durable repository.
func New(repo Repository) *Store { return &Store{repo: repo} }

// ApplyTopology declaratively makes the persisted set EXACTLY the given depots: each
// is upserted, and any previously-persisted depot absent from the desired set is
// deleted. It is the bulk-apply the CLI uses to push a whole analyst-produced topology
// at once; after it returns, LoadRegistry (even from a fresh process) reflects precisely
// what was applied.
func (s *Store) ApplyTopology(ctx context.Context, depots []*depot.ContractDepot) error {
	desired := make(map[string]bool, len(depots))
	for _, c := range depots {
		if c == nil {
			continue
		}
		desired[c.ID()] = true
		if err := s.repo.Save(ctx, c); err != nil {
			return fmt.Errorf("apply topology: save depot %q: %w", c.ID(), err)
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
			return fmt.Errorf("apply topology: delete stale depot %q: %w", c.ID(), err)
		}
	}
	return nil
}

// AddElement adds one element to a depot's named role and persists the result — a
// granular live op. It errors when the depot does not exist so the CLI reports it
// rather than fabricate a depot.
func (s *Store) AddElement(ctx context.Context, depotID string, role depot.Role, e depot.Element) error {
	return s.mutate(ctx, depotID, "add", func(c *depot.ContractDepot) (*depot.ContractDepot, error) {
		return c.WithElementAdded(role, e)
	})
}

// RemoveElement drops the element crewed by shipSymbol from a depot's named role and
// persists the result — a granular live op. Errors bubble up from the domain (unknown
// ship, or removing the last warehouse).
func (s *Store) RemoveElement(ctx context.Context, depotID string, role depot.Role, shipSymbol string) error {
	return s.mutate(ctx, depotID, "remove", func(c *depot.ContractDepot) (*depot.ContractDepot, error) {
		return c.WithElementRemoved(role, shipSymbol)
	})
}

// PlaceElement repositions the element crewed by shipSymbol in a depot's named role to
// waypoint and persists the result — the granular positioning op (placement stays pure
// config: the caller supplies the waypoint).
func (s *Store) PlaceElement(ctx context.Context, depotID string, role depot.Role, shipSymbol, waypoint string) error {
	return s.mutate(ctx, depotID, "place", func(c *depot.ContractDepot) (*depot.ContractDepot, error) {
		return c.WithElementPlaced(role, shipSymbol, waypoint)
	})
}

// AddDepot persists a single new depot WITHOUT touching the rest of the set — the
// granular depot-level create the CLI's `depot add` uses, as opposed to
// ApplyTopology's whole-set replace. An already-present id is upserted (the repo's Save
// is an upsert), so re-adding a depot replaces just that one.
func (s *Store) AddDepot(ctx context.Context, c *depot.ContractDepot) error {
	if c == nil {
		return fmt.Errorf("add depot: nil depot")
	}
	if err := s.repo.Save(ctx, c); err != nil {
		return fmt.Errorf("add depot %q: %w", c.ID(), err)
	}
	return nil
}

// RemoveDepot deletes a single depot by id — the granular depot-level delete the
// CLI's `depot remove` uses. Idempotent: removing an absent depot is not an error,
// so the CLI can re-issue a removal safely.
func (s *Store) RemoveDepot(ctx context.Context, id string) error {
	if err := s.repo.Delete(ctx, id); err != nil {
		return fmt.Errorf("remove depot %q: %w", id, err)
	}
	return nil
}

// LoadRegistry rebuilds the immutable routing registry from the durable set — the
// restart-safe entry point the daemon calls at boot and the CLI calls for status. An
// empty repo yields an empty registry (destination warehousing OFF), never an error.
func (s *Store) LoadRegistry(ctx context.Context) (*depot.Registry, error) {
	depots, err := s.repo.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("load registry: %w", err)
	}
	return depot.NewRegistry(depots), nil
}

// mutate loads a depot by id, applies an immutable domain update, and persists the new
// depot. The shared spine of the granular ops: a missing depot is a loud error, and
// the domain update's own invariants (unknown ship, last-warehouse) surface verbatim.
func (s *Store) mutate(
	ctx context.Context,
	depotID, op string,
	apply func(*depot.ContractDepot) (*depot.ContractDepot, error),
) error {
	c, ok, err := s.repo.Get(ctx, depotID)
	if err != nil {
		return fmt.Errorf("%s element: load depot %q: %w", op, depotID, err)
	}
	if !ok {
		return fmt.Errorf("%s element: depot %q does not exist", op, depotID)
	}
	updated, err := apply(c)
	if err != nil {
		return fmt.Errorf("%s element on depot %q: %w", op, depotID, err)
	}
	if err := s.repo.Save(ctx, updated); err != nil {
		return fmt.Errorf("%s element: persist depot %q: %w", op, depotID, err)
	}
	return nil
}
