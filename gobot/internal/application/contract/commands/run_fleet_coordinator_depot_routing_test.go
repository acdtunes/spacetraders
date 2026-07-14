package commands

import (
	"testing"

	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// These tests cover the FINAL sp-u9xa seam: the live contract coordinator consuming
// the depot registry (option a). routeContractViaDepot is the decision the main
// loop consults BEFORE the default distance-based hull selection (SelectClosestShip).
// It is the single point where a configured depot diverts a contract onto its
// config-assigned, co-located delivery hull; a nil / empty / non-owning registry
// returns ok=false so the coordinator runs its pre-existing default path
// BYTE-IDENTICALLY — the natural off-switch (no config flag; empty registry ==
// today's behavior, which is the dominant-income regression guard).

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
			if route, ok := routeContractViaDepot(tc.reg, contract); ok {
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

	route, ok := routeContractViaDepot(reg, contract)
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
