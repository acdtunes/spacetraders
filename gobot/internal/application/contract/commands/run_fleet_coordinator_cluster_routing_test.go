package commands

import (
	"testing"

	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/cluster"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// These tests cover the FINAL sp-u9xa seam: the live contract coordinator consuming
// the cluster registry (option a). routeContractViaCluster is the decision the main
// loop consults BEFORE the default distance-based hull selection (SelectClosestShip).
// It is the single point where a configured cluster diverts a contract onto its
// config-assigned, co-located delivery hull; a nil / empty / non-owning registry
// returns ok=false so the coordinator runs its pre-existing default path
// BYTE-IDENTICALLY — the natural off-switch (no config flag; empty registry ==
// today's behavior, which is the dominant-income regression guard).

// newClusterRoutingContract builds a single-delivery PROCUREMENT contract delivering
// to dest — the geometry the registry routes on.
func newClusterRoutingContract(t *testing.T, dest string) *domainContract.Contract {
	t.Helper()
	c, err := domainContract.NewContract("C-CLUSTER", shared.MustNewPlayerID(1), "COSMIC", "PROCUREMENT",
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

// newOwningClusterRegistry builds a registry with ONE cluster that owns dest (a
// destination warehouse parked there — the routing anchor) and pins deliveryHull as
// its config-assigned delivery hull, co-located at the same destination.
func newOwningClusterRegistry(t *testing.T, id, dest, warehouseHull, deliveryHull string) *cluster.Registry {
	t.Helper()
	c, err := cluster.NewContractCluster(id,
		[]cluster.Element{{Waypoint: dest, ShipSymbol: warehouseHull}}, // destination warehouse (routing anchor)
		nil, // stockers
		[]cluster.Element{{Waypoint: dest, ShipSymbol: deliveryHull}}, // config-assigned delivery hull (co-located)
		nil, // source hubs
	)
	if err != nil {
		t.Fatalf("build cluster: %v", err)
	}
	return cluster.NewRegistry([]*cluster.ContractCluster{c})
}

// TestRouteContractViaCluster_NoOwningCluster_FallsThroughToDefault is the REGRESSION
// GUARD for the live contract engine (dominant income): every shape that is NOT an
// owning cluster with a delivery hull returns ok=false, so the coordinator runs its
// unchanged default hull+source selection. This is the natural off-switch — a nil
// registry (feature unwired), an empty registry (no clusters), a cluster that does not
// cover this contract's destination, and an owning cluster with no config-assigned
// delivery hull ALL degrade to the pre-existing default path byte-identically.
func TestRouteContractViaCluster_NoOwningCluster_FallsThroughToDefault(t *testing.T) {
	contract := newClusterRoutingContract(t, "X1-SYS-DEST")

	noDeliveryHull, err := cluster.NewContractCluster("alpha",
		[]cluster.Element{{Waypoint: "X1-SYS-DEST", ShipSymbol: "WH-1"}}, nil, nil, nil)
	if err != nil {
		t.Fatalf("build cluster: %v", err)
	}

	cases := []struct {
		name string
		reg  *cluster.Registry
	}{
		{"nil registry (feature unwired)", nil},
		{"empty registry (off-switch)", cluster.NewRegistry(nil)},
		{"cluster does not own this destination", newOwningClusterRegistry(t, "beta", "X1-SYS-OTHER", "WH-2", "DLV-2")},
		{"owning cluster has no config-assigned delivery hull", cluster.NewRegistry([]*cluster.ContractCluster{noDeliveryHull})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if route, ok := routeContractViaCluster(tc.reg, contract); ok {
				t.Fatalf("expected NO cluster route (default path must run byte-identical), got %+v", route)
			}
		})
	}
}

// TestRouteContractViaCluster_OwningCluster_RoutesToConfigDeliveryHull is the cluster
// integration proper: a contract whose destination IS owned by a configured cluster is
// diverted onto that cluster's config-assigned delivery hull (NOT a distance-selected
// pool hull), and identifies the cluster's destination warehouse as the co-located
// withdraw-local source. The delivery hull symbol and the warehouse waypoint are both
// traced to the cluster CONFIG, never to a distance computation.
func TestRouteContractViaCluster_OwningCluster_RoutesToConfigDeliveryHull(t *testing.T) {
	contract := newClusterRoutingContract(t, "X1-SYS-DEST")
	reg := newOwningClusterRegistry(t, "alpha", "X1-SYS-DEST", "WH-1", "DLV-ALPHA")

	route, ok := routeContractViaCluster(reg, contract)
	if !ok {
		t.Fatal("expected the owning cluster to route this contract to its config-assigned delivery hull")
	}
	if route.DeliveryHull != "DLV-ALPHA" {
		t.Fatalf("expected config-assigned delivery hull DLV-ALPHA, got %q", route.DeliveryHull)
	}
	if route.Warehouse != "X1-SYS-DEST" {
		t.Fatalf("expected withdraw-local source at cluster destination warehouse X1-SYS-DEST, got %q", route.Warehouse)
	}
	if route.ClusterID != "alpha" {
		t.Fatalf("expected owning cluster id alpha, got %q", route.ClusterID)
	}
}
