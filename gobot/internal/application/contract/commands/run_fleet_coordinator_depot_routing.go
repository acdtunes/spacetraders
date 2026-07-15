package commands

import (
	"context"

	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// depotRoute is the coordinator's per-contract depot routing decision (bead sp-u9xa, extended
// by sp-9j9c): when a configured depot OWNS the contract's delivery geometry, the coordinator
// delivers via the depot's NEAREST delivery hull (the one whose hub is closest to this
// contract's destination) and withdraws the pre-staged good from the depot's co-located
// destination warehouse (withdraw-local + deliver-local), instead of the default distance-based
// pool selection + cheapest-market sourcing.
type depotRoute struct {
	// DepotID is the owning depot's stable id (for logging/observability).
	DepotID string
	// DeliveryHull is the ShipSymbol of the depot delivery hull NEAREST to the routed
	// destination (SelectDeliveryHull, ranked by the same in-system distance the default pool
	// path uses) — the hull the contract is dispatched on instead of the distance-selected pool
	// candidate. A single-hull depot returns that hull unchanged (byte-identical).
	DeliveryHull string
	// Warehouse is the depot's destination-warehouse waypoint that covers the routed
	// destination — the co-located withdraw-local source. The good is pre-staged there
	// by the depot's stockers; the EXISTING inventory-first sourcing path
	// (PlanSourcing + InventorySourceFinder) already prefers this in-system warehouse
	// over the market, so the source preference is emergent — this field records it for
	// the log, not a second sourcing path.
	Warehouse string
}

// contractClaimFleet is the fleet identity the contract coordinator claims a dispatched hull
// under (bead sp-3l64). A depot delivery hull carries the DISTINCT depot.DeliveryHullFleet
// dedication so the coordinator's discovery can never re-grab it for a general contract; it is
// dispatched ONLY via routeContractViaDepot. That depot-routed claim must therefore run under
// the hull's OWN depot-delivery identity, or ClaimShip's dedication guard (DedicatedFleet != ""
// && DedicatedFleet != operation) would REJECT the very hull the depot route selected. Every
// other hull — unpinned or contract-pinned — still claims under the coordinator's "contract"
// identity, so a foreign-pinned hull is rejected, never poached (sp-lprs unchanged). A
// depot-delivery hull only ever reaches the claim via the depot route (excluded from both pools)
// or a mid-delivery readopt, so keying the claim on its dedication cannot widen the poach surface.
func contractClaimFleet(dedicatedFleet string) string {
	if dedicatedFleet == depot.DeliveryHullFleet {
		return dedicatedFleet
	}
	return dedicatedFleetContract
}

// routeContractViaDepot is the sp-u9xa seam (extended by sp-9j9c): the pure decision the
// contract coordinator consults BEFORE its default hull+source selection. It asks the boot-loaded
// depot registry whether a configured depot OWNS this contract's remaining delivery geometry; if
// so — and that depot has a delivery hull — it returns the depotRoute that diverts the contract
// onto the delivery hull NEAREST the routed destination (ranked by the injected distance oracle,
// so a multi-hub delivery fleet serves each cluster locally).
//
// FAIL-SAFE / REGRESSION GUARD (dominant income): it returns ok=false for EVERY shape
// that is not a fully-owning depot — a nil registry (feature unwired), an empty
// registry (the natural off-switch, no config flag), a registry whose depots do not
// cover this contract's destination, or an owning depot with no delivery hull. In all
// those cases the caller runs its pre-existing default path BYTE-IDENTICALLY: empty
// registry == today's behavior. The distance oracle is injected (may be nil — then
// SelectDeliveryHull keeps config order), and the decision itself does no I/O beyond the
// oracle's lazy, memoized graph read, safe to consult every pass.
func routeContractViaDepot(reg *depot.Registry, contract *domainContract.Contract, distance depot.DistanceBetween) (depotRoute, bool) {
	// Feature unwired (tests / daemon predating the wiring): default path, untouched.
	if reg == nil {
		return depotRoute{}, false
	}

	// Route on the contract's REMAINING (unfulfilled) delivery geometry — what the
	// delivery hull will actually service. An empty/non-owning registry yields no
	// owning depot (RouteContract returns nil), so the default path stands.
	destinations := unfulfilledDestinations(contract)
	owning := reg.RouteContract(destinations)
	if owning == nil {
		return depotRoute{}, false
	}

	// The routed destination: the first remaining destination the owning depot
	// actually covers with a warehouse. withdraw-local + deliver-local both happen
	// here. RouteContract only returns a depot covering ≥1 destination, so this is
	// normally non-empty; the guard keeps the decision fail-safe regardless.
	routedDest := firstOwnedDestination(owning, destinations)
	if routedDest == "" {
		return depotRoute{}, false
	}

	// The delivery hull NEAREST the routed destination (SelectDeliveryHull ranks the depot's
	// hubs by the injected in-system distance; a single-hull depot returns that hull unchanged).
	// A depot with warehouses but no pinned delivery hull cannot deliver locally, so it degrades
	// to the default long-haul path.
	hull, ok := owning.SelectDeliveryHull(routedDest, distance)
	if !ok || hull.ShipSymbol == "" {
		return depotRoute{}, false
	}

	return depotRoute{
		DepotID:      owning.ID(),
		DeliveryHull: hull.ShipSymbol,
		Warehouse:    routedDest,
	}, true
}

// unfulfilledDestinations collects the destination waypoints of the contract's deliveries
// that still have units outstanding — the geometry the registry routes on. A fully-
// fulfilled contract yields no destinations, so RouteContract owns nothing and the
// default path stands.
func unfulfilledDestinations(contract *domainContract.Contract) []string {
	var dests []string
	for _, d := range contract.Terms().Deliveries {
		if d.UnitsRequired-d.UnitsFulfilled <= 0 {
			continue
		}
		dests = append(dests, d.DestinationSymbol)
	}
	return dests
}

// firstOwnedDestination returns the first destination in dests that the depot's
// destination warehouse(s) cover — the co-located withdraw+deliver waypoint. "" when
// none is covered.
func firstOwnedDestination(c *depot.ContractDepot, dests []string) string {
	for _, d := range dests {
		if c.Owns(d) {
			return d
		}
	}
	return ""
}

// newDepotDeliveryDistance builds the delivery-hull selection distance oracle (bead sp-9j9c)
// from the system graph — the SAME in-system coordinate separation SelectClosestShip ranks pool
// candidates by (Waypoint.DistanceTo). It memoizes each system's graph inside the returned
// closure so ranking N delivery hulls costs one graph read per system, not one per hull; and the
// read is lazy, so a single-hull depot (which never invokes the oracle) pays nothing. ok=false
// when a graph is unavailable or either waypoint is uncharted, so SelectDeliveryHull falls open
// to config order (regression-safe: an unresolved position never reorders the config pick). A nil
// graph provider yields a nil oracle — SelectDeliveryHull then keeps config order too.
func newDepotDeliveryDistance(ctx context.Context, graphProvider system.ISystemGraphProvider, playerID int) depot.DistanceBetween {
	if graphProvider == nil {
		return nil
	}
	graphs := map[string]*system.NavigationGraph{}
	coordsOf := func(waypoint string) (*shared.Waypoint, bool) {
		systemSymbol := shared.ExtractSystemSymbol(waypoint)
		graph, seen := graphs[systemSymbol]
		if !seen {
			if result, err := graphProvider.GetGraph(ctx, systemSymbol, false, playerID); err == nil && result != nil {
				graph = result.Graph
			}
			graphs[systemSymbol] = graph // cache even a nil (failed) system so it is not re-fetched this pass
		}
		if graph == nil {
			return nil, false
		}
		waypointCoords, ok := graph.Waypoints[waypoint]
		return waypointCoords, ok && waypointCoords != nil
	}
	return func(from, to string) (float64, bool) {
		fromCoords, okFrom := coordsOf(from)
		toCoords, okTo := coordsOf(to)
		if !okFrom || !okTo {
			return 0, false
		}
		return fromCoords.DistanceTo(toCoords), true
	}
}
