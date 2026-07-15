package commands

import (
	"math"
	"testing"

	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// These tests cover the sp-u9xa seam (extended by sp-9j9c): the live contract coordinator
// consuming the depot registry. routeContractViaDepot is the decision the main loop consults
// BEFORE the default distance-based hull selection (SelectClosestShip). It is the single point
// where a configured depot diverts a contract onto its delivery hull NEAREST the destination
// (a single-hull depot routes to that one hull unchanged); a nil / empty / non-owning registry
// returns ok=false so the coordinator runs its pre-existing default path BYTE-IDENTICALLY — the
// natural off-switch (no config flag; empty registry == today's behavior, the dominant-income
// regression guard).

// newDepotRoutingContract builds a single-delivery PROCUREMENT contract delivering
// to dest — the geometry the registry routes on.
func newDepotRoutingContract(t *testing.T, dest string) *domainContract.Contract {
	t.Helper()
	c, err := domainContract.NewContract("C-DEPOT", shared.MustNewPlayerID(1), "COSMIC", "PROCUREMENT",
		domainContract.Terms{
			Payment:    domainContract.Payment{OnAccepted: 50000, OnFulfilled: 150000},
			Deliveries: []domainContract.Delivery{{TradeSymbol: "IRON_ORE", DestinationSymbol: dest, UnitsRequired: 100, UnitsFulfilled: 0}},
			Deadline:   "2999-01-01T00:00:00Z",
		}, nil)
	if err != nil {
		t.Fatalf("build contract: %v", err)
	}
	return c
}

// newOwningDepotRegistry builds a registry with ONE depot that owns dest (a
// destination warehouse parked there — the routing anchor) and pins deliveryHull as
// its config-assigned delivery hull, co-located at the same destination.
func newOwningDepotRegistry(t *testing.T, id, dest, warehouseHull, deliveryHull string) *depot.Registry {
	t.Helper()
	c, err := depot.NewContractDepot(id,
		[]depot.Element{{Waypoint: dest, ShipSymbol: warehouseHull}}, // destination warehouse (routing anchor)
		nil, // stockers
		[]depot.Element{{Waypoint: dest, ShipSymbol: deliveryHull}}, // config-assigned delivery hull (co-located)
		nil, // source hubs
	)
	if err != nil {
		t.Fatalf("build depot: %v", err)
	}
	return depot.NewRegistry([]*depot.ContractDepot{c})
}

// TestRouteContractViaDepot_NoOwningDepot_FallsThroughToDefault is the REGRESSION
// GUARD for the live contract engine (dominant income): every shape that is NOT an
// owning depot with a delivery hull returns ok=false, so the coordinator runs its
// unchanged default hull+source selection. This is the natural off-switch — a nil
// registry (feature unwired), an empty registry (no depots), a depot that does not
// cover this contract's destination, and an owning depot with no config-assigned
// delivery hull ALL degrade to the pre-existing default path byte-identically.
func TestRouteContractViaDepot_NoOwningDepot_FallsThroughToDefault(t *testing.T) {
	contract := newDepotRoutingContract(t, "X1-SYS-DEST")

	noDeliveryHull, err := depot.NewContractDepot("alpha",
		[]depot.Element{{Waypoint: "X1-SYS-DEST", ShipSymbol: "WH-1"}}, nil, nil, nil)
	if err != nil {
		t.Fatalf("build depot: %v", err)
	}

	cases := []struct {
		name string
		reg  *depot.Registry
	}{
		{"nil registry (feature unwired)", nil},
		{"empty registry (off-switch)", depot.NewRegistry(nil)},
		{"depot does not own this destination", newOwningDepotRegistry(t, "beta", "X1-SYS-OTHER", "WH-2", "DLV-2")},
		{"owning depot has no config-assigned delivery hull", depot.NewRegistry([]*depot.ContractDepot{noDeliveryHull})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if route, ok := routeContractViaDepot(tc.reg, contract, nil); ok {
				t.Fatalf("expected NO depot route (default path must run byte-identical), got %+v", route)
			}
		})
	}
}

// TestRouteContractViaDepot_OwningDepot_RoutesToConfigDeliveryHull is the depot
// integration proper: a contract whose destination IS owned by a configured depot is
// diverted onto that depot's config-assigned delivery hull (NOT a distance-selected
// pool hull), and identifies the depot's destination warehouse as the co-located
// withdraw-local source. The delivery hull symbol and the warehouse waypoint are both
// traced to the depot CONFIG, never to a distance computation.
func TestRouteContractViaDepot_OwningDepot_RoutesToConfigDeliveryHull(t *testing.T) {
	contract := newDepotRoutingContract(t, "X1-SYS-DEST")
	reg := newOwningDepotRegistry(t, "alpha", "X1-SYS-DEST", "WH-1", "DLV-ALPHA")

	route, ok := routeContractViaDepot(reg, contract, nil)
	if !ok {
		t.Fatal("expected the owning depot to route this contract to its config-assigned delivery hull")
	}
	if route.DeliveryHull != "DLV-ALPHA" {
		t.Fatalf("expected config-assigned delivery hull DLV-ALPHA, got %q", route.DeliveryHull)
	}
	if route.Warehouse != "X1-SYS-DEST" {
		t.Fatalf("expected withdraw-local source at depot destination warehouse X1-SYS-DEST, got %q", route.Warehouse)
	}
	if route.DepotID != "alpha" {
		t.Fatalf("expected owning depot id alpha, got %q", route.DepotID)
	}
}

// newMultiHullOwningDepotRegistry builds a registry with ONE depot owning dest (a warehouse
// there) and the given delivery hulls placed across hubs — so routeContractViaDepot must pick
// the hull NEAREST to dest via the injected distance oracle, not config order [0].
func newMultiHullOwningDepotRegistry(t *testing.T, id, dest, warehouseHull string, hulls []depot.Element) *depot.Registry {
	t.Helper()
	c, err := depot.NewContractDepot(id,
		[]depot.Element{{Waypoint: dest, ShipSymbol: warehouseHull}}, // destination warehouse (routing anchor)
		nil,   // stockers
		hulls, // delivery hulls placed across hubs
		nil,   // source hubs
	)
	if err != nil {
		t.Fatalf("build depot: %v", err)
	}
	return depot.NewRegistry([]*depot.ContractDepot{c})
}

// TestRouteContractViaDepot_MultiHull_RoutesToNearestDeliveryHull is the sp-9j9c enabler at the
// live seam: with N delivery hulls placed across hubs, the contract is diverted onto the hull
// whose hub is NEAREST the destination (via the injected in-system distance oracle), so each
// cluster's contract delivers locally — NOT shuttled onto the single config-first hull. The
// config-first hull here is the FAR one, so a pass proves distance (not config order) decided.
func TestRouteContractViaDepot_MultiHull_RoutesToNearestDeliveryHull(t *testing.T) {
	const dest = "X1-VB74-J58"
	contract := newDepotRoutingContract(t, dest)
	reg := newMultiHullOwningDepotRegistry(t, "vb74", dest, "WH-1", []depot.Element{
		{Waypoint: "X1-VB74-A1", ShipSymbol: "DLV-FAR"}, // far hub, config-FIRST
		{Waypoint: dest, ShipSymbol: "DLV-LOCAL"},       // co-located with the destination
	})
	coords := map[string][2]float64{dest: {0, 0}, "X1-VB74-A1": {100, 0}}
	distance := func(from, to string) (float64, bool) {
		a, okA := coords[from]
		b, okB := coords[to]
		if !okA || !okB {
			return 0, false
		}
		dx, dy := b[0]-a[0], b[1]-a[1]
		return math.Sqrt(dx*dx + dy*dy), true
	}

	route, ok := routeContractViaDepot(reg, contract, distance)
	if !ok {
		t.Fatal("expected the owning depot to route this contract to a delivery hull")
	}
	if route.DeliveryHull != "DLV-LOCAL" {
		t.Fatalf("expected the NEAREST delivery hull DLV-LOCAL (co-located at %s), got %q (config-first was the far DLV-FAR)", dest, route.DeliveryHull)
	}
}
