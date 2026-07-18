package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/contract/depotstore"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
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
// depot's named role, persists it via Store.AddElement, and then POSITIONS the crewing hull
// (sp-3l64). role is the CLI role name (warehouse | stocker | delivery-hull | source-hub).
//
// The persist alone is the reopened bug: a warehouse/stocker/source-hub hull registered in config
// but LEFT DOCKED where it was, because AddElement never triggered positioning (only the delivery
// hull got the free+exclude+navigate, and only via a config apply / boot reload — never a granular
// add). positionAddedDepotElement closes that: after the durable persist it runs the SAME
// idempotent, fail-open, per-role launch/position path boot runs, so the added hull is freed from
// its prior fleet, excluded from the contract grab, and navigated to its waypoint (or handed to its
// own coordinator to park) — atomically, for EVERY role.
func (s *DaemonServer) AddDepotElement(ctx context.Context, playerID int, depotID, role, waypoint, shipSymbol string) error {
	parsedRole, err := depot.ParseRole(role)
	if err != nil {
		return err
	}
	if err := s.depotStore(playerID).AddElement(ctx, depotID, parsedRole, depot.Element{Waypoint: waypoint, ShipSymbol: shipSymbol}); err != nil {
		return err
	}
	s.positionAddedDepotElement(ctx, playerID, depotID, shipSymbol)
	return nil
}

// positionAddedDepotElement POSITIONS the single hull just added to a depot role (sp-3l64), so a
// granular `element add` actually frees + excludes + navigates the hull instead of only persisting
// config. It reloads the registry and dispatches the added element's launch intent through the
// SAME per-role path boot runs (dispatchDepotLaunch), scoped to exactly the added ship so no
// unrelated element is re-touched. Idempotent + fail-open: an uncrewed slot positions nothing, and
// a registry-reload hiccup is logged and swallowed so it never fails the already-durable persist.
// Routes through s.depotSink() so the wiring is unit-tested against a spy without spawning
// coordinator goroutines.
func (s *DaemonServer) positionAddedDepotElement(ctx context.Context, playerID int, depotID, shipSymbol string) {
	if shipSymbol == "" {
		return // declared-but-uncrewed slot — no hull to position
	}
	reg, err := s.LoadDepotRegistry(ctx, playerID)
	if err != nil {
		fmt.Printf("Warning: depot %q element-add positioning skipped for ship %s (registry reload failed): %v\n", depotID, shipSymbol, err)
		return
	}
	sink := s.depotSink()
	for _, intent := range planDepotLaunches(reg) {
		if intent.depotID != depotID || intent.shipSymbol != shipSymbol {
			continue
		}
		dispatchDepotLaunch(ctx, sink, intent, playerID)
	}
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
	// previously-stopped coordinator) is (re)started. Routes through s.depotSink() (the injectable
	// seam sp-3l64 shares with the element-add positioning) — in production s itself.
	//
	// sp-udgc (re-strander (ii)): but launch ONLY depots whose domain still has LIVE contract
	// demand. A decommissioned contract op leaves stale contract_depots rows behind; without this
	// guard the boot reload re-spawns their stocker/warehouse containers and RE-DEDICATES the
	// crewing hulls off trade EVERY restart, keyed off the stale rows rather than live demand — the
	// confirmed live re-strander (sibling to sp-2jrz's reconciler). The signal is FindActiveContracts
	// (live, accepted-not-fulfilled — NOT the demand miner's contract HISTORY, which a decommissioned
	// domain still shows), matched at the destination-SYSTEM granularity the depot's own receipt
	// solve uses. FAIL-OPEN: on a live-demand lookup error launch every depot exactly as before
	// (byte-identical) rather than withhold a live buffer on a transient hiccup — the guard only ever
	// WITHHOLDS on a positive "no live contract for this system" signal.
	liveSystems, lerr := s.liveContractDestinationSystems(ctx, playerID)
	if lerr != nil {
		fmt.Printf("Warning: depot-launch live-demand lookup failed for player %d (launching all persisted depots, pre-sp-udgc behavior): %v\n", playerID, lerr)
		launchDepotCoordinators(ctx, reg, playerID, s.depotSink())
		return
	}
	if skipped := launchLiveDepotCoordinators(ctx, reg, playerID, s.depotSink(), liveSystems); len(skipped) > 0 {
		fmt.Printf("sp-udgc: withheld depot-launch for %d depot(s) with no live contract demand (player %d): %v — decommissioned/stale topology, crewing hulls left to trade (not re-dedicated to stocker/warehouse)\n",
			len(skipped), playerID, skipped)
	}
}

// liveContractDestinationSystems resolves the set of destination SYSTEMS the player's LIVE
// (accepted, not-yet-fulfilled) contracts deliver to — the demand signal the boot depot-launch guard
// consults so a decommissioned domain (no active contract for its system) is not re-materialized on
// restart (sp-udgc). It reads the SAME FindActiveContracts the bootstrapper trusts (live contracts,
// NOT the contract HISTORY the demand miner ranks — a fulfilled/expired contract still shows in
// history but no longer counts here). A test override drives it without a DB; a nil DB yields
// (nil, nil) so a degraded/test boot fails OPEN to the pre-sp-udgc launch-all behavior.
func (s *DaemonServer) liveContractDestinationSystems(ctx context.Context, playerID int) (map[string]bool, error) {
	if s.depotLiveContractSystemsOverride != nil {
		return s.depotLiveContractSystemsOverride(ctx, playerID)
	}
	if s.db == nil {
		return nil, nil
	}
	contracts, err := persistence.NewGormContractRepository(s.db).FindActiveContracts(ctx, playerID)
	if err != nil {
		return nil, err
	}
	return contractDestinationSystems(contracts), nil
}

// contractDestinationSystems reduces a set of contracts to the destination SYSTEMS their deliveries
// target (sp-udgc) — the granularity a depot's domain is matched at (a depot buffers for a region /
// system, and its receipt solve is scoped by system). Every delivery's destination WAYPOINT collapses
// to its system, so a contract delivering anywhere in a depot's system marks that depot live. Kept
// pure (no I/O) so the extraction is unit-tested without a DB.
func contractDestinationSystems(contracts []*contract.Contract) map[string]bool {
	systems := map[string]bool{}
	for _, c := range contracts {
		for _, d := range c.Terms().Deliveries {
			if system := shared.ExtractSystemSymbol(d.DestinationSymbol); system != "" {
				systems[system] = true
			}
		}
	}
	return systems
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
