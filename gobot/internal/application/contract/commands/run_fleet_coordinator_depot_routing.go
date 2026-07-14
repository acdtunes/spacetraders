package commands

import (
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
)

// depotRoute is the coordinator's per-contract depot routing decision (bead
// sp-u9xa, the final seam): when a configured depot OWNS the contract's delivery
// geometry, the coordinator delivers via the depot's config-assigned delivery hull
// and withdraws the pre-staged good from the depot's co-located destination
// warehouse (withdraw-local + deliver-local), instead of the default distance-based
// pool selection + cheapest-market sourcing.
type depotRoute struct {
	// DepotID is the owning depot's stable id (for logging/observability).
	DepotID string
	// DeliveryHull is the ShipSymbol of the depot's config-assigned delivery hull
	// (SelectDeliveryHull) — the hull the contract is dispatched on instead of the
	// distance-selected pool candidate. Pure config output: never a co-location bias.
	DeliveryHull string
	// Warehouse is the depot's destination-warehouse waypoint that covers the routed
	// destination — the co-located withdraw-local source. The good is pre-staged there
	// by the depot's stockers; the EXISTING inventory-first sourcing path
	// (PlanSourcing + InventorySourceFinder) already prefers this in-system warehouse
	// over the market, so the source preference is emergent — this field records it for
	// the log, not a second sourcing path.
	Warehouse string
}

// routeContractViaDepot is the FINAL sp-u9xa seam: the pure decision the contract
// coordinator consults BEFORE its default hull+source selection. It asks the boot-loaded
// depot registry whether a configured depot OWNS this contract's remaining delivery
// geometry; if so — and that depot has a config-assigned delivery hull — it returns
// the depotRoute that diverts the contract onto the pinned, co-located hull.
//
// FAIL-SAFE / REGRESSION GUARD (dominant income): it returns ok=false for EVERY shape
// that is not a fully-owning depot — a nil registry (feature unwired), an empty
// registry (the natural off-switch, no config flag), a registry whose depots do not
// cover this contract's destination, or an owning depot with no config-assigned
// delivery hull. In all those cases the caller runs its pre-existing default path
// BYTE-IDENTICALLY: empty registry == today's behavior. It is a pure query (no I/O),
// safe to consult every pass.
func routeContractViaDepot(reg *depot.Registry, contract *domainContract.Contract) (depotRoute, bool) {
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

	// The config-assigned delivery hull (pure config, first configured — SelectDeliveryHull
	// applies NO co-location preference). A depot with warehouses but no pinned delivery
	// hull cannot deliver locally, so it degrades to the default long-haul path.
	hull, ok := owning.SelectDeliveryHull(routedDest)
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
