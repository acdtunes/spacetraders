package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/contract/depotstore"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
)

// This file is the daemon-side depot-management surface (bead sp-u9xa): the single
// writer of contract-depot topology (RULINGS #3), exposing BOTH modes over the
// application Store, which persists through the Item-1 Repository. The CLI reaches these
// through the RPC; every mutation is durable the instant it returns, so a granular edit
// needs no daemon restart to take effect — a fresh LoadRegistry (the contract engine's
// per-boot rebuild, Item 4) re-derives exactly what was written.
//
// Nothing here hardcodes a waypoint, ship, or count: the whole topology is operator data
// carried in the spec / granular args (the sp-u9xa parametrization principle).

// ElementSpec is the boundary DTO for one placed depot member — a {waypoint, ship}
// pair. ShipSymbol may be empty for a declared-but-uncrewed slot.
type ElementSpec struct {
	Waypoint   string `json:"waypoint"`
	ShipSymbol string `json:"ship_symbol"`
}

// DepotSpec is the boundary DTO for one whole depot: its id and its four element
// classes. It is the serializable shape the CLI parses from an operator spec and the
// granular `depot add` sends. It maps 1:1 onto the domain depot via toDomain.
type DepotSpec struct {
	ID            string        `json:"id"`
	Warehouses    []ElementSpec `json:"warehouses"`
	Stockers      []ElementSpec `json:"stockers"`
	DeliveryHulls []ElementSpec `json:"delivery_hulls"`
	SourceHubs    []ElementSpec `json:"source_hubs"`
}

// DepotTopologySpec is the whole-topology spec the declarative bulk apply consumes.
type DepotTopologySpec struct {
	Depots []DepotSpec `json:"depots"`
}

// depotStore builds the application Store over the durable, player-scoped repository —
// the single construction point every handler below routes through, so all of them share
// the exact persistence the boot-time registry rebuild reads.
func (s *DaemonServer) depotStore(playerID int) *depotstore.Store {
	return depotstore.New(persistence.NewGormContractDepotRepository(s.db, playerID))
}

// ApplyDepotTopology is mode (A), DECLARATIVE/BULK: it makes the player's persisted set
// EXACTLY the given spec (upserting each depot, deleting any not named), in one call
// through Store.ApplyTopology. After it returns, a restart's LoadRegistry reflects
// precisely what was applied.
func (s *DaemonServer) ApplyDepotTopology(ctx context.Context, playerID int, spec DepotTopologySpec) error {
	depots := make([]*depot.ContractDepot, 0, len(spec.Depots))
	for _, cs := range spec.Depots {
		c, err := cs.toDomain()
		if err != nil {
			return err
		}
		depots = append(depots, c)
	}
	return s.depotStore(playerID).ApplyTopology(ctx, depots)
}

// AddDepot is mode (B) at depot granularity: it persists ONE new depot without
// disturbing the rest of the set, via Store.AddDepot.
func (s *DaemonServer) AddDepot(ctx context.Context, playerID int, spec DepotSpec) error {
	c, err := spec.toDomain()
	if err != nil {
		return err
	}
	return s.depotStore(playerID).AddDepot(ctx, c)
}

// RemoveDepot is mode (B) at depot granularity: it deletes ONE depot by id, leaving
// the rest, via Store.RemoveDepot (idempotent).
func (s *DaemonServer) RemoveDepot(ctx context.Context, playerID int, depotID string) error {
	return s.depotStore(playerID).RemoveDepot(ctx, depotID)
}

// AddDepotElement is mode (B) at element granularity: it adds one element to a
// depot's named role and persists it, via Store.AddElement. role is the CLI role name
// (warehouse | stocker | delivery-hull | source-hub).
func (s *DaemonServer) AddDepotElement(ctx context.Context, playerID int, depotID, role, waypoint, shipSymbol string) error {
	parsedRole, err := depot.ParseRole(role)
	if err != nil {
		return err
	}
	return s.depotStore(playerID).AddElement(ctx, depotID, parsedRole, depot.Element{Waypoint: waypoint, ShipSymbol: shipSymbol})
}

// RemoveDepotElement is mode (B) at element granularity: it drops the element crewed by
// shipSymbol from a depot's named role and persists the result, via Store.RemoveElement.
func (s *DaemonServer) RemoveDepotElement(ctx context.Context, playerID int, depotID, role, shipSymbol string) error {
	parsedRole, err := depot.ParseRole(role)
	if err != nil {
		return err
	}
	return s.depotStore(playerID).RemoveElement(ctx, depotID, parsedRole, shipSymbol)
}

// PlaceDepotElement is mode (B) at element granularity: it repositions the element
// crewed by shipSymbol in a depot's named role to waypoint and persists it, via
// Store.PlaceElement — the parametrized co-location op (the caller supplies the waypoint).
func (s *DaemonServer) PlaceDepotElement(ctx context.Context, playerID int, depotID, role, shipSymbol, waypoint string) error {
	parsedRole, err := depot.ParseRole(role)
	if err != nil {
		return err
	}
	return s.depotStore(playerID).PlaceElement(ctx, depotID, parsedRole, shipSymbol, waypoint)
}

// ListDepots returns the player's persisted depots — the read the CLI's
// `depot list` renders. It is the registry's read model, rebuilt from the repository.
func (s *DaemonServer) ListDepots(ctx context.Context, playerID int) ([]*depot.ContractDepot, error) {
	reg, err := s.depotStore(playerID).LoadRegistry(ctx)
	if err != nil {
		return nil, err
	}
	return reg.Depots(), nil
}

// LoadDepotRegistry rebuilds the immutable routing registry from the durable repository
// for a player — the restart-safe seam (RULINGS #2) the contract engine routes through.
// The Store owns NO in-memory authority, so every call re-derives the registry from the
// persisted rows: a daemon restart therefore reconstructs the identical registry with no
// snapshot to lose, and an empty repo yields an empty registry (destination warehousing
// OFF, the regression-safe legacy long-haul default), never an error.
func (s *DaemonServer) LoadDepotRegistry(ctx context.Context, playerID int) (*depot.Registry, error) {
	return s.depotStore(playerID).LoadRegistry(ctx)
}

// reloadDepotRegistryAtBoot re-derives the player's depot registry from the durable
// repository at daemon startup, logs a one-line summary, and LAUNCHES each depot's
// destination-side coordinators (sp-cftm) — the boot-time reload that makes RULINGS #2 visible
// (persisted depots reload on restart) AND fills the depot warehouse so routing's
// withdrawal-source preference has something to prefer. It is fail-open: a load error is logged
// and swallowed so a transient DB hiccup never blocks startup. It routes through the same
// LoadDepotRegistry seam the contract engine consumes, so the boot log reflects exactly what
// routing will see.
func (s *DaemonServer) reloadDepotRegistryAtBoot(ctx context.Context, playerID int) {
	reg, err := s.LoadDepotRegistry(ctx, playerID)
	if err != nil {
		fmt.Printf("Warning: failed to reload contract depot registry for player %d: %v\n", playerID, err)
		return
	}
	depots := reg.Depots()
	if len(depots) == 0 {
		return
	}
	fmt.Printf("Reloaded %d contract depot(s) for player %d from durable store (restart-safe registry)\n", len(depots), playerID)

	// sp-cftm: launch the destination-side STOCKING half — a warehouse + stocker coordinator per
	// declared, crewed depot element, pointed at the depot's destination warehouse. This is
	// what FILLS the depot warehouse so RouteContract's withdrawal-source preference stops
	// falling through to the byte-identical fresh-source fallback (the sp-u9xa gap). Fail-open +
	// idempotent (the launch path's idle-gap discipline refuses a double-launch), so it is safe
	// to run here after container recovery: a coordinator just re-adopted by recovery is not idle
	// and is left alone; only a genuinely idle depot hull (freshly-applied topology, or a
	// previously-stopped coordinator) is (re)started.
	launchDepotCoordinators(ctx, reg, playerID, s)
}

// toDomain converts a boundary DepotSpec into a validated domain depot (the
// NewContractDepot invariants — non-empty id, at least one warehouse — surface here).
func (cs DepotSpec) toDomain() (*depot.ContractDepot, error) {
	c, err := depot.NewContractDepot(
		cs.ID,
		toElements(cs.Warehouses),
		toElements(cs.Stockers),
		toElements(cs.DeliveryHulls),
		toElements(cs.SourceHubs),
	)
	if err != nil {
		return nil, fmt.Errorf("invalid depot spec %q: %w", cs.ID, err)
	}
	return c, nil
}

// toElements maps a slice of boundary ElementSpecs to domain Elements.
func toElements(specs []ElementSpec) []depot.Element {
	if len(specs) == 0 {
		return nil
	}
	out := make([]depot.Element, len(specs))
	for i, e := range specs {
		out[i] = depot.Element{Waypoint: e.Waypoint, ShipSymbol: e.ShipSymbol}
	}
	return out
}
